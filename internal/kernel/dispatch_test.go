package kernel

import (
	"context"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

// newManualReactor builds a Reactor whose loops are NOT started, so a test can
// drive dispatchOnce / reaperOnce deterministically.
func (e *reactorEnv) newManualReactor(cfg ReactorConfig) *Reactor {
	cfg = cfg.withDefaults()
	return &Reactor{srv: e.srv, cfg: cfg, wake: make(chan struct{}, 1), breaker: newReaperBreaker(cfg)}
}

// aContinuation admits and runs a trivial workflow to completion, returning a
// valid continuation id (the FK target for a hand-made outbox row).
func (e *reactorEnv) aContinuation(t *testing.T, prefix string) string {
	t.Helper()
	v := e.admit(t, `export function f(): number { return 1; }`, prefix, nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q", v.Outcome)
	}
	id := e.start(t, v.Hashes[prefix+"/f"], nil, map[string]any{"subject": "op", "operator": true})
	r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	e.waitStatus(t, id, "done", 5*time.Second)
	r.Stop()
	return id
}

// TestOutboxDispatcherEffectivelyOnce (RED-path 5, ADR-06 §5 / ADR-05 §7):
//   - a normal delivery marks the outbox row exactly once;
//   - a crash BETWEEN the sink call and the delivered_at mark redelivers the
//     intent exactly once (never lost) — the honest effectively-once limit;
//   - an already-delivered row is never redelivered;
//   - concurrent dispatchers never double-deliver.
func TestOutboxDispatcherEffectivelyOnce(t *testing.T) {
	e := newReactorEnv(t)
	ctx := context.Background()
	sink := cfr.NewRecordingSink()
	e.srv.SetDeliverySink(sink)
	r := e.newManualReactor(ReactorConfig{})

	contID := e.aContinuation(t, "app/disp")

	// Hand-make an external outbox row + its deliver task (as a step would).
	mkIntent := func(step, ordinal int) (outboxID, dedup string) {
		outboxID = insertOutbox(t, e, contID, step, ordinal, "mail.send")
		dedup = dedupKey(contID, step, ordinal)
		e.exec(t, `INSERT INTO task (id, kind, run_at, payload) VALUES (gen_random_uuid(),'deliver',now(),
		  jsonb_build_object('intent_id',$1::text,'dedup_key',$2::text))`, outboxID, dedup)
		return
	}

	// --- normal delivery: marked exactly once ---
	obA, keyA := mkIntent(1, 0)
	if err := r.dispatchOnce(ctx); err != nil {
		t.Fatalf("dispatchOnce: %v", err)
	}
	if sink.Count(keyA) != 1 {
		t.Fatalf("normal delivery count = %d, want 1", sink.Count(keyA))
	}
	if e.intScalar(t, `SELECT count(*) FROM outbox WHERE id=$1 AND delivered_at IS NOT NULL`, obA) != 1 {
		t.Fatalf("outbox %s not marked delivered", obA)
	}
	// Re-running finds no ready deliver task and never re-delivers.
	_ = r.dispatchOnce(ctx)
	if sink.Count(keyA) != 1 {
		t.Fatalf("already-delivered redelivered: count = %d, want 1", sink.Count(keyA))
	}

	// --- crash between sink call and mark: redelivered exactly once ---
	obB, keyB := mkIntent(2, 0)
	// Arm the sink to FAIL the first delivery attempt (stand-in for a crash before
	// the mark): the task is left for retry, the outbox row stays undelivered.
	sink.FailOnce(keyB)
	if err := r.dispatchOnce(ctx); err != nil {
		t.Fatalf("dispatchOnce(crash leg): %v", err)
	}
	if e.intScalar(t, `SELECT count(*) FROM outbox WHERE id=$1 AND delivered_at IS NULL`, obB) != 1 {
		t.Fatalf("crash leg wrongly marked %s delivered", obB)
	}
	// The task was left 'ready' for retry (never lost).
	if e.intScalar(t, `SELECT count(*) FROM task WHERE kind='deliver' AND payload->>'intent_id'=$1 AND status='ready'`, obB) != 1 {
		t.Fatalf("crashed deliver task not left ready for retry")
	}
	// Retry delivers exactly once more and marks.
	if err := r.dispatchOnce(ctx); err != nil {
		t.Fatalf("dispatchOnce(retry): %v", err)
	}
	if sink.Count(keyB) != 1 {
		t.Fatalf("retry delivery count = %d, want 1 (redelivered once, never lost)", sink.Count(keyB))
	}
	if e.intScalar(t, `SELECT count(*) FROM outbox WHERE id=$1 AND delivered_at IS NOT NULL`, obB) != 1 {
		t.Fatalf("retry did not mark %s delivered", obB)
	}

	// --- concurrent dispatchers never double-deliver ---
	obC, keyC := mkIntent(3, 0)
	_ = obC
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- r.dispatchOnce(ctx) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent dispatchOnce: %v", err)
		}
	}
	if sink.Count(keyC) != 1 {
		t.Fatalf("concurrent delivery count = %d, want 1 (no double-deliver)", sink.Count(keyC))
	}
}

func insertOutbox(t *testing.T, e *reactorEnv, contID string, step, ordinal int, class string) string {
	t.Helper()
	var id string
	e.withConn(t, func(c *pgwire.Conn) {
		if _, err := c.QueryRow(context.Background(), `
INSERT INTO outbox (id, continuation_id, step_seq, ordinal, class, payload)
VALUES (gen_random_uuid(),$1,$2,$3,$4,'{"to":"x"}'::jsonb) RETURNING id::text`,
			[]any{contID, step, ordinal, class}, &id); err != nil {
			t.Fatalf("insert outbox: %v", err)
		}
	})
	return id
}

func dedupKey(contID string, step, ordinal int) string {
	return contID + ":" + itoaT(step) + ":" + itoaT(ordinal)
}

func itoaT(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// dispatch timing sanity: the loop is idempotent under an empty queue.
func TestDispatchEmptyIsNoop(t *testing.T) {
	e := newReactorEnv(t)
	r := e.newManualReactor(ReactorConfig{})
	if err := r.dispatchOnce(context.Background()); err != nil {
		t.Fatalf("empty dispatchOnce: %v", err)
	}
	_ = time.Now
}
