package cfr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
)

// isoLayout is the fixed-width UTC ISO-8601 instant (ADR-05 §2 BUILD-A marker):
// lexicographic order equals chronological order, so the sleeping-timer partial
// index range-scans it as text. Timer `due` for sleeping rows is computed in SQL
// from DB now(); this Go layout is the documented fallback for join-child rows,
// whose `due` is cosmetic (they are 'ready', never scanned).
const isoLayout = "2006-01-02T15:04:05.000000Z"

func nowISO() string { return time.Now().UTC().Format(isoLayout) }

// OutcomeReq carries everything ParkOutcome needs to checkpoint one step's result
// into the CURRENT open transaction.
type OutcomeReq struct {
	ContinuationID string
	StepSeq        int64  // the continuation's CURRENT step_seq (post-claim) — the outbox dedup key
	PrincipalJSON  string // parent principal jsonb, inherited by join children
	RootDefHash    string
	Interp         ChildStater // for join-child materialization
	Out            cek.Outcome
}

// ParkOutcome is the ONE generalized checkpoint writer (ADR-05 §5/§6/§7). It runs
// inside the caller's open transaction and writes the terminal or re-parked state
// of a step, plus its effect trace, plus structured-concurrency bookkeeping.
// EVERY continuation UPDATE is guarded WHERE status='running' AND lease_owner=$kernel
// and asserts RowsAffected==1 — the cancellation/zombie fence at the write side.
func ParkOutcome(ctx context.Context, db DB, env StepEnv, req OutcomeReq) error {
	switch req.Out.Kind {
	case cek.OutDone:
		return parkDone(ctx, db, env, req)
	case cek.OutParked:
		if req.Out.Wake != nil {
			return parkWake(ctx, db, env, req)
		}
		return parkCondition(ctx, db, env, req)
	default: // OutFaulted / OutError
		return parkFailed(ctx, db, env, req)
	}
}

func parkDone(ctx context.Context, db DB, env StepEnv, req OutcomeReq) error {
	result, err := EncodeValue(req.Out.Value)
	if err != nil {
		return err
	}
	res, err := db.Exec(ctx, `
UPDATE continuation SET frames=''::bytea, status='done', result=$2::bytea, updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$3::uuid`,
		req.ContinuationID, byteaLiteral(result), env.KernelID)
	if err != nil {
		return err
	}
	if err := guardRunning(res, req.ContinuationID); err != nil {
		return err
	}
	if err := writeEffects(ctx, db, req.ContinuationID, req.StepSeq, req.Out.Effects); err != nil {
		return err
	}
	// JOIN-PARENT bookkeeping: this terminal child may complete a parent's quorum.
	return settleJoinParent(ctx, db, req.ContinuationID)
}

func parkWake(ctx context.Context, db DB, env StepEnv, req OutcomeReq) error {
	blob, err := Encode(req.Out.State)
	if err != nil {
		return err
	}
	// A channel.send before a sleep/receive/join is recorded exactly-once here.
	if err := writeEffects(ctx, db, req.ContinuationID, req.StepSeq, req.Out.Effects); err != nil {
		return err
	}
	w := req.Out.Wake
	switch w.Kind {
	case cek.WakeTimer:
		res, err := db.Exec(ctx, `
UPDATE continuation SET frames=$2::bytea, status='sleeping',
   wake = jsonb_build_object('kind','timer','due',
     to_char((now() + make_interval(secs => $4::float8/1000.0)) AT TIME ZONE 'UTC',
             'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')),
   updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$3::uuid`,
			req.ContinuationID, byteaLiteral(blob), env.KernelID, w.DelayMS)
		if err != nil {
			return err
		}
		return guardRunning(res, req.ContinuationID)

	case cek.WakeMessage:
		matchJSON := wakeMatchJSON(w.Match)
		// If a MATCHING message is already waiting, claim it and mark ready
		// immediately — no sleep (ADR-05 §5: send-before-receive).
		msgID, err := claimOldestMatchingMessage(ctx, db, w.Channel, w.Match, req.ContinuationID)
		if err != nil {
			return err
		}
		if msgID != "" {
			res, err := db.Exec(ctx, `
UPDATE continuation SET frames=$2::bytea, status='ready',
   wake = jsonb_strip_nulls(jsonb_build_object(
     'kind','message','channel',$4::text,'message_id',$5::text,
     'match', $6::jsonb)),
   updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$3::uuid`,
				req.ContinuationID, byteaLiteral(blob), env.KernelID, w.Channel, msgID, nullableJSON(matchJSON))
			if err != nil {
				return err
			}
			if err := guardRunning(res, req.ContinuationID); err != nil {
				return err
			}
			if err := insertResumeTask(ctx, db, req.ContinuationID, req.StepSeq); err != nil {
				return err
			}
			return notifyTask(ctx, db)
		}
		res, err := db.Exec(ctx, `
UPDATE continuation SET frames=$2::bytea, status='sleeping',
   wake = jsonb_strip_nulls(jsonb_build_object(
     'kind','message','channel',$4::text,'match',$5::jsonb)),
   updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$3::uuid`,
			req.ContinuationID, byteaLiteral(blob), env.KernelID, w.Channel, nullableJSON(matchJSON))
		if err != nil {
			return err
		}
		return guardRunning(res, req.ContinuationID)

	case cek.WakeEvent:
		onJSON, _ := json.Marshal(w.On)
		res, err := db.Exec(ctx, `
UPDATE continuation SET frames=$2::bytea, status='sleeping',
   wake = jsonb_build_object('kind','event','stream',$4::text,'on',$5::jsonb),
   updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$3::uuid`,
			req.ContinuationID, byteaLiteral(blob), env.KernelID, w.Stream, string(onJSON))
		if err != nil {
			return err
		}
		return guardRunning(res, req.ContinuationID)

	case cek.WakeJoin:
		return parkJoin(ctx, db, env, req, blob)
	default:
		return fmt.Errorf("cfr: unknown wake kind %d", w.Kind)
	}
}

// parkJoin materializes one child continuation per thunk and parks the parent on
// a join wake (ADR-05 §5 BUILD-B). Children and the parent's park commit together.
func parkJoin(ctx context.Context, db DB, env StepEnv, req OutcomeReq, parentBlob []byte) error {
	w := req.Out.Wake
	if req.Interp == nil {
		return fmt.Errorf("cfr: join park needs an interpreter to build children")
	}
	tier := req.Out.State.Tier
	fuel := req.Out.State.FuelSteps
	alloc := req.Out.State.FuelAlloc
	children := make([]string, 0, len(w.Thunks))
	for i, thunk := range w.Thunks {
		clo, ok := thunk.Ref.(*cek.ClosureObj)
		if !ok {
			return fmt.Errorf("cfr: join thunk %d is not a closure", i)
		}
		childState, err := req.Interp.InitialState("", clo, nil, tier, fuel, alloc)
		if err != nil {
			return err
		}
		childBlob, err := Encode(childState)
		if err != nil {
			return err
		}
		childID := uuid4()
		// PIN CHOICE: the join linkage lives in the child's PRINCIPAL, not its wake.
		// A join child that itself parks (e.g. wf.sleep in a race leg) rewrites its
		// wake, which would drop wake-borne linkage keys; the principal is stable
		// across parks. (The pin suggested wake keys OR principal — principal is the
		// correct one once children can park.)
		childPrincipal := withJoinLinkage(req.PrincipalJSON, req.ContinuationID, i)
		wake := fmt.Sprintf(`{"kind":"timer","due":%q}`, nowISO())
		if _, err := db.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
VALUES ($1,'workflow',$2,$3,$4,$5::bytea,$6::jsonb,'ready',$7::jsonb,0)`,
			childID, clo.DefHash, epochOrOne(env), FormatVersion, byteaLiteral(childBlob), wake,
			childPrincipal); err != nil {
			return err
		}
		if err := insertResumeTask(ctx, db, childID, 0); err != nil {
			return err
		}
		children = append(children, childID)
	}
	mode := "all"
	if w.Race {
		mode = "race"
	}
	childrenJSON, _ := json.Marshal(children)
	parentWake := fmt.Sprintf(`{"kind":"join","children":%s,"quorum":%d,"mode":%q}`,
		string(childrenJSON), w.Quorum, mode)
	res, err := db.Exec(ctx, `
UPDATE continuation SET frames=$2::bytea, status='sleeping', wake=$4::jsonb, updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$3::uuid`,
		req.ContinuationID, byteaLiteral(parentBlob), env.KernelID, parentWake)
	if err != nil {
		return err
	}
	if err := guardRunning(res, req.ContinuationID); err != nil {
		return err
	}
	return notifyTask(ctx, db)
}

func parkCondition(ctx context.Context, db DB, env StepEnv, req OutcomeReq) error {
	blob, err := Encode(req.Out.State)
	if err != nil {
		return err
	}
	condID := uuid4()
	res, err := db.Exec(ctx, `
UPDATE continuation SET frames=$2::bytea, status='condition',
   wake = jsonb_build_object('kind','manual','condition',$4::text), updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$3::uuid`,
		req.ContinuationID, byteaLiteral(blob), env.KernelID, condID)
	if err != nil {
		return err
	}
	if err := guardRunning(res, req.ContinuationID); err != nil {
		return err
	}
	payloadJSON, err := jsonOrEmpty(req.Out.Condition.Payload)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload) VALUES ($1,$2,$3,$4::jsonb)`,
		condID, req.ContinuationID, req.Out.Condition.Class, payloadJSON); err != nil {
		return err
	}
	for _, r := range req.Out.Condition.Restarts {
		if _, err := db.Exec(ctx, `
INSERT INTO restart (id, condition_id, name, label, capability_required) VALUES ($1,$2,$3,$4,$5)`,
			uuid4(), condID, r.Name, r.Label, nullable(r.CapabilityRequired)); err != nil {
			return err
		}
	}
	return nil
}

func parkFailed(ctx context.Context, db DB, env StepEnv, req OutcomeReq) error {
	res, err := db.Exec(ctx, `
UPDATE continuation SET status='failed', updated_at=now()
 WHERE id=$1 AND status='running' AND lease_owner=$2::uuid`, req.ContinuationID, env.KernelID)
	if err != nil {
		return err
	}
	if err := guardRunning(res, req.ContinuationID); err != nil {
		return err
	}
	msg := "step faulted"
	if req.Out.Kind == cek.OutError && req.Out.Err != nil {
		msg = req.Out.Err.Error()
	} else if req.Out.Kind == cek.OutFaulted {
		msg = "fault: " + faultText(req.Out.Fault)
	}
	return insertStepFailedCondition(ctx, db, req.ContinuationID, msg)
}

// writeEffects records one outbox row per effect (ADR-05 §7 dedup key); a
// channel.send additionally lands a channel_message and wakes the oldest matching
// sleeping receiver.
func writeEffects(ctx context.Context, db DB, continuationID string, stepSeq int64, effects []cek.Effect) error {
	for i, ef := range effects {
		payloadJSON, err := jsonOrEmpty(ef.Payload)
		if err != nil {
			return err
		}
		outboxID := uuid4()
		if _, err := db.Exec(ctx, `
INSERT INTO outbox (id, continuation_id, step_seq, ordinal, class, payload)
VALUES ($1,$2,$3,$4,$5,$6::jsonb)`,
			outboxID, continuationID, stepSeq, i, ef.Class, payloadJSON); err != nil {
			return err
		}
		// External effects (mail/http/log) are delivered across the process boundary
		// by the ADR-06 §5 dispatcher — enqueue a 'deliver' task in this same step
		// transaction (ADR-05 §7: outbox row + driving task commit atomically).
		if IsExternalEffectClass(ef.Class) {
			dedup := fmt.Sprintf("%s:%d:%d", continuationID, stepSeq, i)
			if err := EnqueueDeliverTask(ctx, db, outboxID, dedup); err != nil {
				return err
			}
		}
		if ef.Class == "channel.send" {
			channel, _ := ef.Payload["channel"].(string)
			payload, err := EncodeValue(ef.Val)
			if err != nil {
				return err
			}
			msgID := uuid4()
			if _, err := db.Exec(ctx, `
INSERT INTO channel_message (id, channel, payload, sent_by) VALUES ($1,$2,$3::bytea,$4)`,
				msgID, channel, byteaLiteral(payload), continuationID); err != nil {
				return err
			}
			if _, err := deliverToOldestReceiver(ctx, db, channel, msgID, ef.Val); err != nil {
				return err
			}
		}
	}
	return nil
}

// settleJoinParent flips a join parent to ready exactly once when its quorum of
// terminal children is reached (ADR-05 §5 BUILD-B: quorum COMPUTED, not counted).
// The status CAS (WHERE status='sleeping') makes a re-offered child step unable to
// double-flip — test 10's crash leg.
func settleJoinParent(ctx context.Context, db DB, childID string) error {
	var childPrincipalJSON string
	found, err := db.QueryRow(ctx, `SELECT principal::text FROM continuation WHERE id=$1`, []any{childID}, &childPrincipalJSON)
	if err != nil || !found {
		return err
	}
	parentID, _ := parseJoinLinkage(childPrincipalJSON)
	if parentID == "" {
		return nil // not a join child
	}

	var pWakeJSON string
	var pSeq int64
	pfound, err := db.QueryRow(ctx, `SELECT wake::text, step_seq FROM continuation WHERE id=$1`,
		[]any{parentID}, &pWakeJSON, &pSeq)
	if err != nil || !pfound {
		return err
	}
	pw := parseWake(pWakeJSON)
	if pw.Kind != "join" {
		return nil
	}

	var doneCount int
	if _, err := db.QueryRow(ctx, `
SELECT count(*) FROM continuation WHERE id = ANY($1::uuid[]) AND status='done'`,
		[]any{pw.Children}, &doneCount); err != nil {
		return err
	}
	if doneCount < pw.Quorum {
		return nil
	}

	if pw.Mode == "race" {
		r, err := db.Exec(ctx, `
UPDATE continuation SET status='ready',
   wake = jsonb_set(wake, '{winner}', to_jsonb($2::text)), updated_at=now()
 WHERE id=$1 AND status='sleeping'`, parentID, childID)
		if err != nil {
			return err
		}
		if r.RowsAffected != 1 {
			return nil // already flipped — idempotent
		}
	} else {
		r, err := db.Exec(ctx, `
UPDATE continuation SET status='ready', updated_at=now()
 WHERE id=$1 AND status='sleeping'`, parentID)
		if err != nil {
			return err
		}
		if r.RowsAffected != 1 {
			return nil // already flipped — idempotent
		}
	}
	if err := insertResumeTask(ctx, db, parentID, pSeq); err != nil {
		return err
	}
	if err := notifyTask(ctx, db); err != nil {
		return err
	}
	if pw.Mode == "race" {
		losers := make([]string, 0, len(pw.Children))
		for _, id := range pw.Children {
			if id != childID {
				losers = append(losers, id)
			}
		}
		if len(losers) > 0 {
			if _, err := db.Exec(ctx, `
UPDATE continuation SET status='cancelled', updated_at=now()
 WHERE id = ANY($1::uuid[]) AND status IN ('sleeping','ready','running')`, losers); err != nil {
				return err
			}
		}
	}
	return nil
}

// guardRunning is the write-side cancellation/zombie fence (ADR-05 §7): a
// checkpoint that does not update exactly one 'running' row owned by this kernel
// (the row was cancelled, re-claimed, or its lease reaped) fails, rolling the step
// back so nothing torn commits.
func guardRunning(res pgwire.Result, cid string) error {
	if res.RowsAffected != 1 {
		return fmt.Errorf("cfr: checkpoint lost the running fence for %s (rows=%d)", cid, res.RowsAffected)
	}
	return nil
}

func principalOrEmpty(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// withJoinLinkage returns the child's principal jsonb: the parent principal with
// join_parent/join_ordinal merged in (ADR-05 §5 BUILD-B). Stored in the principal
// because it must survive the child's OWN parks (a race leg that sleeps).
func withJoinLinkage(parentPrincipalJSON, parentID string, ordinal int) string {
	m := map[string]any{}
	if parentPrincipalJSON != "" {
		_ = json.Unmarshal([]byte(parentPrincipalJSON), &m)
	}
	m["join_parent"] = parentID
	m["join_ordinal"] = ordinal
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// parseJoinLinkage reads join_parent/join_ordinal from a child's principal jsonb.
func parseJoinLinkage(principalJSON string) (parent string, ordinal int) {
	var m struct {
		JoinParent  string `json:"join_parent"`
		JoinOrdinal int    `json:"join_ordinal"`
	}
	if principalJSON != "" {
		_ = json.Unmarshal([]byte(principalJSON), &m)
	}
	return m.JoinParent, m.JoinOrdinal
}

func faultText(v cek.Value) string {
	if s, ok := v.StrVal(); ok {
		return s
	}
	return "non-string fault"
}
