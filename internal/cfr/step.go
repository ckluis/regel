package cfr

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
)

// StepEnv threads the claiming kernel's identity, pinned epoch, and lease length
// through every claim/park path (ADR-05 §7, ADR-06 §6). KernelEpoch == 0 disables
// the epoch fence (the Stage-A restart path, which never pins an epoch); a fenced
// reactor always carries its boot-pinned epoch (≥ 1).
type StepEnv struct {
	KernelID     string
	KernelEpoch  int
	LeaseSeconds int // default 30
}

func (e StepEnv) leaseSecs() int {
	if e.LeaseSeconds <= 0 {
		return 30
	}
	return e.LeaseSeconds
}

// ErrEpochFence is returned when a work transaction observes a live catalog epoch
// different from the kernel's pinned epoch (ADR-06 §6). No claim is consumed; the
// caller (reactor) drains and exits.
type ErrEpochFence struct {
	Observed int
	Required int
}

func (e ErrEpochFence) Error() string {
	return fmt.Sprintf("cfr: epoch fence tripped: observed %d, kernel pinned %d", e.Observed, e.Required)
}

// ChildStater is the slice of the interpreter the store needs to materialize
// join children (ADR-05 §5 BUILD-B: one child continuation per thunk closure).
// *cek.Interp satisfies it.
type ChildStater interface {
	InitialState(defHash string, clo *cek.ClosureObj, args []cek.Value, tier cek.Tier, fuel, alloc int64) (*cek.State, error)
}

// --- metrics (ADR-05 §7 abort budget; ADR-06 §5 recovery) --------------------

var (
	mSerializationAborts int64
	mRetryExhausted      int64
	mCASLosses           int64
	mReoffers            int64
	mTasksDrained        int64
)

// Metrics is a snapshot of the store/reactor golden signals.
type Metrics struct {
	SerializationAborts int64 `json:"serialization_aborts"`
	RetryExhausted      int64 `json:"retry_exhausted"`
	CASLosses           int64 `json:"cas_losses"`
	Reoffers            int64 `json:"reoffers"`
	TasksDrained        int64 `json:"tasks_drained"`
}

// MetricsSnapshot reads the package-level atomic counters.
func MetricsSnapshot() Metrics {
	return Metrics{
		SerializationAborts: atomic.LoadInt64(&mSerializationAborts),
		RetryExhausted:      atomic.LoadInt64(&mRetryExhausted),
		CASLosses:           atomic.LoadInt64(&mCASLosses),
		Reoffers:            atomic.LoadInt64(&mReoffers),
		TasksDrained:        atomic.LoadInt64(&mTasksDrained),
	}
}

// IncCASLoss / IncReoffer / IncTaskDrained let the reactor record its counters
// through the same registry the store publishes.
func IncCASLoss()         { atomic.AddInt64(&mCASLosses, 1) }
func IncReoffer()         { atomic.AddInt64(&mReoffers, 1) }
func IncReoffers(n int64) { atomic.AddInt64(&mReoffers, n) }
func IncTaskDrained()     { atomic.AddInt64(&mTasksDrained, 1) }

// RetrySerializable runs fn under the ADR-05 §7 retry-on-40001 policy: up to 5
// attempts, capped exponential backoff (base 10ms, factor 2, cap 500ms) with full
// jitter, retrying only serialization failures (40001) and deadlocks (40P01).
// Each retry re-runs the WHOLE step from the claim — safe because nothing escaped
// before COMMIT. Aborts and exhaustion feed the abort-rate budget counters.
func RetrySerializable(ctx context.Context, label string, fn func(attempt int) error) error {
	const (
		maxAttempts = 5
		base        = 10 * time.Millisecond
		cap         = 500 * time.Millisecond
	)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := fn(attempt)
		if err == nil {
			return nil
		}
		if !pgwire.IsCode(err, "40001") && !pgwire.IsCode(err, "40P01") {
			return err
		}
		atomic.AddInt64(&mSerializationAborts, 1)
		if attempt == maxAttempts-1 {
			atomic.AddInt64(&mRetryExhausted, 1)
			return err
		}
		// capped exponential backoff, full jitter
		d := base << attempt
		if d > cap {
			d = cap
		}
		sleep := time.Duration(rand.Int63n(int64(d) + 1))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
	return nil
}

// --- LoadResult --------------------------------------------------------------

// LoadResult reads a terminal continuation's produced value (ADR-05 §2 result
// column). It returns (value, true, nil) only when status='done' and the result
// blob decodes; otherwise (_, false, nil).
func LoadResult(ctx context.Context, db DB, id string) (cek.Value, bool, error) {
	var status, resultHex string
	found, err := db.QueryRow(ctx,
		`SELECT status, COALESCE(encode(result,'hex'),'') FROM continuation WHERE id=$1`,
		[]any{id}, &status, &resultHex)
	if err != nil {
		return cek.Value{}, false, err
	}
	if !found || status != "done" || resultHex == "" {
		return cek.Value{}, false, nil
	}
	blob, err := hexDecode(resultHex)
	if err != nil {
		return cek.Value{}, false, err
	}
	v, err := DecodeValue(blob)
	if err != nil {
		return cek.Value{}, false, err
	}
	return v, true, nil
}

// --- StartWorkflow -----------------------------------------------------------

// StartWorkflow inserts a fresh 'ready' workflow continuation and its resume task
// in one SERIALIZABLE transaction (ADR-06 §4), then NOTIFYs the drain. It is the
// single door shared by POST /workflow and the tests. The seed CFR is a
// never-stepped InitialState (ParkKind ParkFresh); the reactor drives its first
// step. wake={"kind":"timer","due":now} satisfies wake_kind_shape without arming
// the sleeping-timer scanner (that scans status='sleeping' only).
func StartWorkflow(ctx context.Context, db DB, env StepEnv, in ChildStater, rootHash string, args []cek.Value, principal map[string]any, tier cek.Tier) (string, error) {
	st, err := in.InitialState(rootHash, nil, args, tier, defaultFuel(tier), defaultAlloc(tier))
	if err != nil {
		return "", err
	}
	frames, err := Encode(st)
	if err != nil {
		return "", err
	}
	principalJSON, err := jsonOrEmpty(principal)
	if err != nil {
		return "", err
	}
	contID := uuid4()
	err = RetrySerializable(ctx, "start-workflow", func(int) error {
		if e := db.BeginSerializable(ctx); e != nil {
			return e
		}
		committed := false
		defer func() {
			if !committed {
				_ = db.Rollback(ctx)
			}
		}()
		if _, e := db.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
VALUES ($1,'workflow',$2,$3,$4,$5::bytea,
  jsonb_build_object('kind','timer','due',
    to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')),
  'ready',$6::jsonb,0)`,
			contID, rootHash, epochOrOne(env), FormatVersion, byteaLiteral(frames), principalJSON); e != nil {
			return e
		}
		if e := insertResumeTask(ctx, db, contID, 0); e != nil {
			return e
		}
		if e := notifyTask(ctx, db); e != nil {
			return e
		}
		if e := db.Commit(ctx); e != nil {
			return e
		}
		committed = true
		return nil
	})
	if err != nil {
		return "", err
	}
	return contID, nil
}

// --- SendChannel -------------------------------------------------------------

// SendChannel lands a channel message and, if a matching receiver is already
// sleeping, claims the message for the oldest one and flips it ready with a
// resume task — all in one transaction (ADR-05 §5 BUILD-B external send). It
// returns the receiver continuation id, or "" when the message was queued for a
// future receiver.
func SendChannel(ctx context.Context, db DB, env StepEnv, channel string, value cek.Value, sentBy string) (string, error) {
	payload, err := EncodeValue(value)
	if err != nil {
		return "", err
	}
	var deliveredTo string
	err = RetrySerializable(ctx, "channel-send", func(int) error {
		deliveredTo = ""
		if e := db.BeginSerializable(ctx); e != nil {
			return e
		}
		committed := false
		defer func() {
			if !committed {
				_ = db.Rollback(ctx)
			}
		}()
		msgID := uuid4()
		if _, e := db.Exec(ctx, `
INSERT INTO channel_message (id, channel, payload, sent_by) VALUES ($1,$2,$3::bytea,$4)`,
			msgID, channel, byteaLiteral(payload), sentBy); e != nil {
			return e
		}
		to, e := deliverToOldestReceiver(ctx, db, channel, msgID, value)
		if e != nil {
			return e
		}
		deliveredTo = to
		if e := db.Commit(ctx); e != nil {
			return e
		}
		committed = true
		return nil
	})
	if err != nil {
		return "", err
	}
	return deliveredTo, nil
}

// deliverToOldestReceiver claims msgID for the oldest sleeping message-receiver on
// channel whose match predicate accepts the message payload (BUILD-D, ADR-05 §5),
// flips it ready with the message id pinned into its wake, and inserts a resume
// task + NOTIFY. A receiver with a disjoint predicate is skipped so the message
// stays queued for a matching one. Returns the receiver id or "".
func deliverToOldestReceiver(ctx context.Context, db DB, channel, msgID string, payload cek.Value) (string, error) {
	rows, err := db.Query(ctx, `
SELECT id::text, step_seq, COALESCE(wake->'match','null'::jsonb)::text FROM continuation
WHERE status='sleeping' AND wake->>'kind'='message' AND wake->>'channel'=$1
ORDER BY updated_at`, channel)
	if err != nil {
		return "", err
	}
	type recvCand struct {
		id      string
		seq     int64
		matchJS string
	}
	var cands []recvCand
	for rows.Next() {
		var c recvCand
		if err := rows.Scan(&c.id, &c.seq, &c.matchJS); err != nil {
			rows.Close()
			return "", err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, c := range cands {
		var m matchShape
		if c.matchJS != "" && c.matchJS != "null" {
			_ = json.Unmarshal([]byte(c.matchJS), &m)
		}
		if !messageMatches(payload, m) {
			continue
		}
		if _, err := db.Exec(ctx, `UPDATE channel_message SET claimed_by=$1 WHERE id=$2`, c.id, msgID); err != nil {
			return "", err
		}
		res, err := db.Exec(ctx, `
UPDATE continuation
   SET status='ready',
       wake = jsonb_set(wake, '{message_id}', to_jsonb($2::text)),
       updated_at=now()
 WHERE id=$1 AND status='sleeping'`, c.id, msgID)
		if err != nil {
			return "", err
		}
		if res.RowsAffected != 1 {
			continue // lost this receiver to a concurrent claim: try the next
		}
		if err := insertResumeTask(ctx, db, c.id, c.seq); err != nil {
			return "", err
		}
		if err := notifyTask(ctx, db); err != nil {
			return "", err
		}
		return c.id, nil
	}
	return "", nil // no matching receiver: message stays queued
}

// --- ClaimAndStep ------------------------------------------------------------

// ClaimAndStep is the generalized ADR-05 §7 step transaction: epoch fence, claim
// CAS, capability re-validation, delivery build by park kind, resume, and the
// generalized ParkOutcome checkpoint — all in one SERIALIZABLE transaction with a
// pre-COMMIT epoch re-check (ADR-06 §6). It returns the outcome and whether this
// call won the claim.
func ClaimAndStep(ctx context.Context, db DB, env StepEnv, in ChildStater, continuationID string, seenSeq int64,
	resume func(state *cek.State, d cek.Delivery, p cek.Principal) cek.Outcome) (out cek.Outcome, claimed bool, err error) {

	if err = db.BeginSerializable(ctx); err != nil {
		return cek.Outcome{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = db.Rollback(ctx)
		}
	}()

	// EPOCH FENCE (ADR-06 §6): guard read as part of the first round trip.
	if fenceErr := checkEpoch(ctx, db, env); fenceErr != nil {
		return cek.Outcome{}, false, fenceErr
	}

	// CLAIM CAS (ADR-05 §7).
	res, cerr := db.Exec(ctx, `
UPDATE continuation
   SET status='running', lease_owner=$2::uuid, lease_until=now()+make_interval(secs=>$4),
       step_seq=step_seq+1, updated_at=now()
 WHERE id=$1 AND status='ready' AND step_seq=$3`, continuationID, env.KernelID, seenSeq, env.leaseSecs())
	if cerr != nil {
		err = cerr
		return cek.Outcome{}, false, err
	}
	if res.RowsAffected != 1 {
		atomic.AddInt64(&mCASLosses, 1)
		if err = db.Commit(ctx); err == nil {
			committed = true
		}
		return cek.Outcome{}, false, err
	}
	claimed = true
	stepSeq := seenSeq + 1

	// Load frames + principal + wake.
	var framesHex, principalJSON, wakeJSON string
	if _, err = db.QueryRow(ctx,
		`SELECT encode(frames,'hex'), principal::text, wake::text FROM continuation WHERE id=$1`,
		[]any{continuationID}, &framesHex, &principalJSON, &wakeJSON); err != nil {
		return cek.Outcome{}, true, err
	}
	principal, err := loadPrincipal(ctx, db, principalJSON)
	if err != nil {
		return cek.Outcome{}, true, err
	}

	frames, derr := hexDecode(framesHex)
	if derr != nil {
		err = derr
		return cek.Outcome{}, true, err
	}
	state, decErr := Decode(frames)
	if decErr != nil {
		if e := recordStepFailedRunning(ctx, db, env, continuationID, decErr); e != nil {
			err = e
			return cek.Outcome{}, true, err
		}
		if e := db.Commit(ctx); e == nil {
			committed = true
		} else {
			err = e
			return cek.Outcome{}, true, err
		}
		return cek.Outcome{}, true, decErr
	}

	// CAPABILITY RE-VALIDATION (ADR-05 §4): a token whose grant is gone is refused
	// BEFORE the machine is re-entered — zero effects run.
	if badCap, ok := firstRevokedCapability(state, principal); ok {
		if e := parkCapabilityRevoked(ctx, db, env, continuationID, badCap); e != nil {
			err = e
			return cek.Outcome{}, true, err
		}
		if e := checkEpoch(ctx, db, env); e != nil {
			err = e
			return cek.Outcome{}, true, err
		}
		if e := db.Commit(ctx); e != nil {
			err = e
			return cek.Outcome{}, true, err
		}
		committed = true
		return cek.Outcome{Kind: cek.OutParked, Condition: &cek.Condition{Class: "capability.revoked"}}, true, nil
	}

	// DELIVERY by park kind.
	delivery, derr2 := buildDelivery(ctx, db, state, wakeJSON, continuationID)
	if derr2 != nil {
		err = derr2
		return cek.Outcome{}, true, err
	}

	out = resume(state, delivery, principal)

	if err = ParkOutcome(ctx, db, env, OutcomeReq{
		ContinuationID: continuationID,
		StepSeq:        stepSeq,
		PrincipalJSON:  principalJSON,
		RootDefHash:    state.DefHash,
		Interp:         in,
		Out:            out,
	}); err != nil {
		return out, true, err
	}

	// Pre-COMMIT epoch re-check (ADR-06 §6).
	if fenceErr := checkEpoch(ctx, db, env); fenceErr != nil {
		return out, true, fenceErr
	}
	if err = db.Commit(ctx); err != nil {
		return out, true, err
	}
	committed = true
	return out, true, nil
}

// checkEpoch reads the one-row epoch_current fence (ADR-06 §6). KernelEpoch==0
// disables fencing. A missing row (pre-genesis DB) also skips the fence.
func checkEpoch(ctx context.Context, db DB, env StepEnv) error {
	if env.KernelEpoch == 0 {
		return nil
	}
	var n int
	found, err := db.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &n)
	if err != nil {
		return err
	}
	if found && n != env.KernelEpoch {
		return ErrEpochFence{Observed: n, Required: env.KernelEpoch}
	}
	return nil
}

// buildDelivery constructs the value/restart a resume delivers (ADR-05 §5/§6).
func buildDelivery(ctx context.Context, db DB, state *cek.State, wakeJSON, continuationID string) (cek.Delivery, error) {
	switch state.ParkKind {
	case cek.ParkSignal, cek.ParkFuel, cek.ParkGovernor:
		name, args, ok, err := loadResolvedRestart(ctx, db, continuationID)
		if err != nil {
			return cek.Delivery{}, err
		}
		if !ok {
			return cek.Delivery{}, ErrNotResolved
		}
		ch := cek.RestartChoice{Name: name, Args: args}
		return cek.Delivery{Restart: &ch}, nil
	case cek.ParkWake:
		w := parseWake(wakeJSON)
		switch w.Kind {
		case "timer":
			u := cek.UndefV()
			return cek.Delivery{Value: &u}, nil
		case "message":
			if w.MessageID == "" {
				u := cek.UndefV()
				return cek.Delivery{Value: &u}, nil
			}
			v, err := loadMessagePayload(ctx, db, w.MessageID)
			if err != nil {
				return cek.Delivery{}, err
			}
			return cek.Delivery{Value: &v}, nil
		case "join":
			v, err := joinResult(ctx, db, w)
			if err != nil {
				return cek.Delivery{}, err
			}
			return cek.Delivery{Value: &v}, nil
		default:
			u := cek.UndefV()
			return cek.Delivery{Value: &u}, nil
		}
	default: // ParkFresh
		return cek.Delivery{}, nil
	}
}

// joinResult builds the delivered value for a join wake: for `all`, an array of
// child results in thunk order; for `race`, the recorded winner's result.
func joinResult(ctx context.Context, db DB, w wakeShape) (cek.Value, error) {
	if w.Mode == "race" {
		if w.Winner == "" {
			return cek.UndefV(), nil
		}
		return loadChildResult(ctx, db, w.Winner)
	}
	elems := make([]cek.Value, 0, len(w.Children))
	for _, id := range w.Children {
		v, err := loadChildResult(ctx, db, id)
		if err != nil {
			return cek.Value{}, err
		}
		elems = append(elems, v)
	}
	return cek.Value{Tag: cek.TagArray, Ref: &cek.ArrayObj{Elems: elems}}, nil
}

func loadChildResult(ctx context.Context, db DB, id string) (cek.Value, error) {
	var resultHex string
	found, err := db.QueryRow(ctx,
		`SELECT COALESCE(encode(result,'hex'),'') FROM continuation WHERE id=$1`, []any{id}, &resultHex)
	if err != nil {
		return cek.Value{}, err
	}
	if !found || resultHex == "" {
		return cek.UndefV(), nil
	}
	blob, err := hexDecode(resultHex)
	if err != nil {
		return cek.Value{}, err
	}
	return DecodeValue(blob)
}

func loadMessagePayload(ctx context.Context, db DB, msgID string) (cek.Value, error) {
	var payloadHex string
	found, err := db.QueryRow(ctx,
		`SELECT encode(payload,'hex') FROM channel_message WHERE id=$1`, []any{msgID}, &payloadHex)
	if err != nil {
		return cek.Value{}, err
	}
	if !found {
		return cek.UndefV(), nil
	}
	blob, err := hexDecode(payloadHex)
	if err != nil {
		return cek.Value{}, err
	}
	return DecodeValue(blob)
}

func loadResolvedRestart(ctx context.Context, db DB, continuationID string) (string, map[string]any, bool, error) {
	var name, argsJSON string
	found, err := db.QueryRow(ctx, `
SELECT r.name, COALESCE(dc.resolved_args::text, '{}')
FROM durable_condition dc JOIN restart r ON r.id = dc.resolved_restart
WHERE dc.continuation_id = $1 AND dc.status = 'resolved'
ORDER BY dc.signaled_at DESC LIMIT 1`, []any{continuationID}, &name, &argsJSON)
	if err != nil {
		return "", nil, false, err
	}
	if !found {
		return "", nil, false, nil
	}
	return name, parseArgs(argsJSON), true, nil
}

// --- principal + capability re-validation ------------------------------------

func loadPrincipal(ctx context.Context, db DB, principalJSON string) (cek.Principal, error) {
	var pr struct {
		Subject  string `json:"subject"`
		Operator bool   `json:"operator"`
	}
	if principalJSON != "" {
		_ = json.Unmarshal([]byte(principalJSON), &pr)
	}
	grants := map[string]bool{}
	if pr.Subject != "" {
		rows, err := db.Query(ctx,
			`SELECT capability FROM grant_row WHERE subject=$1 AND (expires_at IS NULL OR expires_at > now())`,
			pr.Subject)
		if err != nil {
			return cek.Principal{}, err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return cek.Principal{}, err
			}
			grants[c] = true
		}
		if err := rows.Err(); err != nil {
			return cek.Principal{}, err
		}
	}
	return cek.Principal{Subject: pr.Subject, Grants: grants, IsOperator: pr.Operator || pr.Subject == "operator"}, nil
}

// firstRevokedCapability walks the decoded state for capability tokens and returns
// the first whose capability is not among the principal's LIVE grants (ADR-05 §4).
// PIN DEVIATION: grant_row carries no surrogate id (its PK is
// (subject,capability,scope)), so the token handle is treated as the capability
// name and validated against the subject's live grants.
func firstRevokedCapability(state *cek.State, p cek.Principal) (string, bool) {
	bad := ""
	cek.WalkValues(state, func(v cek.Value) {
		if bad != "" {
			return
		}
		if cap, ok := v.CapToken(); ok {
			if !p.Grants[cap] {
				bad = cap
			}
		}
	})
	if bad != "" {
		return bad, true
	}
	return "", false
}

func parkCapabilityRevoked(ctx context.Context, db DB, env StepEnv, continuationID, capName string) error {
	condID := uuid4()
	res, err := db.Exec(ctx, `
UPDATE continuation
   SET status='condition',
       wake = jsonb_build_object('kind','manual','condition',$2::text),
       updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$3::uuid`, continuationID, condID, env.KernelID)
	if err != nil {
		return err
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("cfr: capability-revoked park lost the running fence for %s", continuationID)
	}
	payload := fmt.Sprintf(`{"capability":%q}`, capName)
	if _, err := db.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload) VALUES ($1,$2,'capability.revoked',$3::jsonb)`,
		condID, continuationID, payload); err != nil {
		return err
	}
	for _, r := range []cek.Restart{
		{Name: "re-grant", Label: "Re-grant " + capName, CapabilityRequired: "operator"},
		{Name: "abort", Label: "Abort"},
	} {
		if _, err := db.Exec(ctx, `
INSERT INTO restart (id, condition_id, name, label, capability_required) VALUES ($1,$2,$3,$4,$5)`,
			uuid4(), condID, r.Name, r.Label, nullable(r.CapabilityRequired)); err != nil {
			return err
		}
	}
	return nil
}

// --- small shared helpers ----------------------------------------------------

func insertResumeTask(ctx context.Context, db DB, continuationID string, stepSeq int64) error {
	payload := fmt.Sprintf(`{"continuation_id":%q,"step_seq":%d}`, continuationID, stepSeq)
	_, err := db.Exec(ctx, `
INSERT INTO task (id, kind, run_at, payload) VALUES ($1,'resume',now(),$2::jsonb)`, uuid4(), payload)
	return err
}

func notifyTask(ctx context.Context, db DB) error {
	_, err := db.Exec(ctx, `NOTIFY task`)
	return err
}

func recordStepFailedRunning(ctx context.Context, db DB, env StepEnv, continuationID string, cause error) error {
	res, err := db.Exec(ctx, `
UPDATE continuation SET status='failed', updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$2::uuid`, continuationID, env.KernelID)
	if err != nil {
		return err
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("cfr: step-failed lost the running fence for %s", continuationID)
	}
	return insertStepFailedCondition(ctx, db, continuationID, cause.Error())
}

// RecordStepFailed writes a step.failed durable condition (ADR-05 §6) for a
// continuation whose driving task hit the attempt ceiling (ADR-06 §5) — failure
// surfaces as a restart, never a silent dead-letter.
func RecordStepFailed(ctx context.Context, db DB, continuationID, msg string) error {
	return insertStepFailedCondition(ctx, db, continuationID, msg)
}

// insertStepFailedCondition writes a step.failed durable condition with an [abort]
// restart (ADR-05 §6 shape). Used by decode-fail, the fault checkpoint, and the
// reactor's attempt-ceiling path.
func insertStepFailedCondition(ctx context.Context, db DB, continuationID, msg string) error {
	condID := uuid4()
	payload := fmt.Sprintf(`{"error":%q}`, msg)
	if _, err := db.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload) VALUES ($1,$2,'step.failed',$3::jsonb)`,
		condID, continuationID, payload); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `
INSERT INTO restart (id, condition_id, name, label, capability_required) VALUES ($1,$2,'abort','Abort',NULL)`,
		uuid4(), condID)
	return err
}

func epochOrOne(env StepEnv) int {
	if env.KernelEpoch <= 0 {
		return 1
	}
	return env.KernelEpoch
}

func defaultFuel(tier cek.Tier) int64 {
	if tier == cek.TierTrusted {
		return 0
	}
	return 1 << 30
}
func defaultAlloc(tier cek.Tier) int64 {
	if tier == cek.TierTrusted {
		return 0
	}
	return 1 << 40
}

// --- wake jsonb shape --------------------------------------------------------

type wakeShape struct {
	Kind        string          `json:"kind"`
	Due         string          `json:"due"`
	Channel     string          `json:"channel"`
	Match       json.RawMessage `json:"match"` // BUILD-D: message match predicate
	MessageID   string          `json:"message_id"`
	Children    []string        `json:"children"`
	Quorum      int             `json:"quorum"`
	Mode        string          `json:"mode"`
	Winner      string          `json:"winner"`
	Stream      string          `json:"stream"` // BUILD-D: event wake resource
	On          []string        `json:"on"`     // BUILD-D: event wake watch set
	JoinParent  string          `json:"join_parent"`
	JoinOrdinal int             `json:"join_ordinal"`
}

// matchOf decodes the wake's stored match predicate (BUILD-D). An absent match
// yields the empty predicate (matches anything).
func (w wakeShape) matchOf() matchShape {
	if len(w.Match) == 0 {
		return matchShape{}
	}
	var m matchShape
	_ = json.Unmarshal(w.Match, &m)
	return m
}

func parseWake(s string) wakeShape {
	var w wakeShape
	if s != "" {
		_ = json.Unmarshal([]byte(s), &w)
	}
	return w
}
