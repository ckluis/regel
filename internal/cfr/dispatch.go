package cfr

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Intent is one external effect the dispatcher pushes across the process boundary
// (ADR-06 §5 deliver task; ADR-05 §7 outbox). It is the audit surface of a
// write/external effect recorded by a step's checkpoint.
type Intent struct {
	ID             string         // outbox row id (the deliver task's intent_id)
	ContinuationID string         // owning continuation
	StepSeq        int64          // the step that recorded it
	Ordinal        int            // position within the step's effect trace
	Class          string         // effect class ('mail.send', 'http.get', 'log.write', …)
	Payload        map[string]any // the recorded intent payload
	DedupKey       string         // continuation:step:ordinal — the effectively-once key
}

// DeliverySink is the pluggable process-boundary the dispatcher pushes intents
// across (ADR-06 §5). The default kernel sink discards (real mail/http sinks are
// Stage-E); tests inject a RecordingSink. A sink MUST be idempotent under
// Intent.DedupKey: a crash between the sink call and the delivered_at mark can
// redeliver an intent (the honest effectively-once limit, ADR-05 §7).
type DeliverySink interface {
	Deliver(ctx context.Context, in Intent) error
}

// DiscardSink accepts every intent and does nothing — the default kernel sink at
// M2 (no external sinks exist yet). It makes delivery a no-op that still marks the
// outbox row delivered so the queue drains.
type DiscardSink struct{}

// Deliver implements DeliverySink.
func (DiscardSink) Deliver(context.Context, Intent) error { return nil }

// RecordingSink records every delivered intent (for tests). It counts per
// DedupKey so a red-path can assert effectively-once (never lost, at-most-once
// under no-crash, exactly one redelivery under a mid-delivery crash).
type RecordingSink struct {
	mu       sync.Mutex
	byKey    map[string]int
	failNext map[string]int // DedupKey → remaining forced failures (crash simulation)
	total    int
}

// NewRecordingSink builds an empty recording sink.
func NewRecordingSink() *RecordingSink {
	return &RecordingSink{byKey: map[string]int{}, failNext: map[string]int{}}
}

// FailOnce arms the sink to return an error the next time it sees dedupKey (a
// stand-in for a crash between the sink call and the delivered_at mark).
func (s *RecordingSink) FailOnce(dedupKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[dedupKey]++
}

// Deliver implements DeliverySink.
func (s *RecordingSink) Deliver(_ context.Context, in Intent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext[in.DedupKey] > 0 {
		s.failNext[in.DedupKey]--
		return fmt.Errorf("recording sink: forced failure for %s", in.DedupKey)
	}
	s.byKey[in.DedupKey]++
	s.total++
	return nil
}

// Count returns how many times dedupKey was delivered.
func (s *RecordingSink) Count(dedupKey string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byKey[dedupKey]
}

// Total returns the total delivered count across all keys.
func (s *RecordingSink) Total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// Keys returns the distinct dedup keys delivered.
func (s *RecordingSink) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.byKey))
	for k := range s.byKey {
		out = append(out, k)
	}
	return out
}

// IsExternalEffectClass reports whether an outbox row's effect class is delivered
// ACROSS the process boundary (ADR-05 §7 external / ADR-10 §6). channel.send is
// the sole INTERNAL effect — it is delivered transactionally in-process (a
// channel_message + receiver flip), never by the dispatcher. Residue: when
// SQL-writing `write`-class effects gain their own outbox classes they must also
// be excluded here. Internal classes: channel.send (in-process message) and
// cron.schedule (BUILD-E D10 — materialized into a durable cron task row in the
// step transaction, never delivered across the process boundary).
func IsExternalEffectClass(class string) bool {
	return class != "channel.send" && class != "cron.schedule"
}

// EnqueueDeliverTask inserts a 'deliver' task for an external outbox row inside
// the caller's OPEN step transaction (ADR-06 §5: outbox delivery rides the one
// task table). It commits atomically with the outbox row, so a delivered intent
// always has a driving task and vice versa.
func EnqueueDeliverTask(ctx context.Context, db DB, outboxID, dedupKey string) error {
	payload := fmt.Sprintf(`{"intent_id":%q,"dedup_key":%q}`, outboxID, dedupKey)
	_, err := db.Exec(ctx, `
INSERT INTO task (id, kind, run_at, payload) VALUES ($1,'deliver',now(),$2::jsonb)`,
		uuid4(), payload)
	return err
}

// LoadIntent reads an outbox row as an Intent for delivery, plus whether it is
// already delivered. found=false ⇒ the outbox row is gone (a no-op deliver task).
func LoadIntent(ctx context.Context, db DB, intentID string) (in Intent, delivered bool, found bool, err error) {
	var contID, class, payloadJSON string
	var stepSeq int64
	var ordinal int
	var isDelivered bool
	found, err = db.QueryRow(ctx, `
SELECT continuation_id::text, step_seq, ordinal, class, payload::text,
       (delivered_at IS NOT NULL)
FROM outbox WHERE id=$1`, []any{intentID},
		&contID, &stepSeq, &ordinal, &class, &payloadJSON, &isDelivered)
	if err != nil || !found {
		return Intent{}, false, found, err
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(payloadJSON), &payload)
	in = Intent{
		ID:             intentID,
		ContinuationID: contID,
		StepSeq:        stepSeq,
		Ordinal:        ordinal,
		Class:          class,
		Payload:        payload,
		DedupKey:       fmt.Sprintf("%s:%d:%d", contID, stepSeq, ordinal),
	}
	return in, isDelivered, true, nil
}

// MarkDelivered stamps delivered_at exactly once under the dedup key (the outbox
// UNIQUE (continuation_id, step_seq, ordinal)). The `delivered_at IS NULL` guard
// makes concurrent dispatchers and a re-offered redelivery converge on one mark:
// the second UPDATE affects zero rows. Returns whether THIS call did the mark.
func MarkDelivered(ctx context.Context, db DB, intentID string) (bool, error) {
	res, err := db.Exec(ctx, `
UPDATE outbox SET delivered_at=now() WHERE id=$1 AND delivered_at IS NULL`, intentID)
	if err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}
