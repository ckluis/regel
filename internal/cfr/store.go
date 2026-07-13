package cfr

import (
	"context"
	"errors"
	"fmt"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
)

// DB is the transactional Postgres surface the store needs: the catalog Querier
// methods plus SERIALIZABLE transaction control (ADR-05 §7). *pgwire.Conn
// satisfies it.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgwire.Result, error)
	Query(ctx context.Context, sql string, args ...any) (*pgwire.Rows, error)
	QueryRow(ctx context.Context, sql string, args []any, dest ...any) (bool, error)
	BeginSerializable(ctx context.Context) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Errors returned by the store.
var (
	ErrCapabilityRefused = errors.New("cfr: restart capability refused")
	ErrRestartNotFound   = errors.New("cfr: restart not found")
	ErrNotResolved       = errors.New("cfr: continuation has no resolved restart")
)

// ParkReq is the input to Park: the machine snapshot plus the durable condition
// to raise (ADR-05 §6).
type ParkReq struct {
	State       *cek.State
	Kind        string // 'workflow' | 'request'
	RootDefHash string
	Class       string
	Payload     map[string]any
	Restarts    []cek.Restart
	Principal   map[string]any // resume scope chain (jsonb); nil → {}
}

// Park writes a parked continuation in one SERIALIZABLE transaction (ADR-05
// §6/§7): the continuation row (status='condition', wake={kind:manual}), the
// durable_condition row, and its restart rows. It returns the new continuation
// and condition ids.
func Park(ctx context.Context, db DB, req ParkReq) (continuationID, conditionID string, err error) {
	frames, err := Encode(req.State)
	if err != nil {
		return "", "", err
	}
	continuationID = uuid4()
	conditionID = uuid4()

	payloadJSON, err := jsonOrEmpty(req.Payload)
	if err != nil {
		return "", "", err
	}
	principalJSON, err := jsonOrEmpty(req.Principal)
	if err != nil {
		return "", "", err
	}
	wake := fmt.Sprintf(`{"kind":"manual","condition":%q}`, conditionID)

	if err = db.BeginSerializable(ctx); err != nil {
		return "", "", err
	}
	defer func() {
		if err != nil {
			_ = db.Rollback(ctx)
		}
	}()

	if _, err = db.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
VALUES ($1, $2, $3, 1, $4, $5::bytea, $6::jsonb, 'condition', $7::jsonb, 0)`,
		continuationID, req.Kind, req.RootDefHash, FormatVersion,
		byteaLiteral(frames), wake, principalJSON); err != nil {
		return "", "", err
	}

	if _, err = db.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload)
VALUES ($1, $2, $3, $4::jsonb)`, conditionID, continuationID, req.Class, payloadJSON); err != nil {
		return "", "", err
	}

	for _, r := range req.Restarts {
		if _, err = db.Exec(ctx, `
INSERT INTO restart (id, condition_id, name, label, capability_required)
VALUES ($1, $2, $3, $4, $5)`, uuid4(), conditionID, r.Name, r.Label, nullable(r.CapabilityRequired)); err != nil {
			return "", "", err
		}
	}

	if err = db.Commit(ctx); err != nil {
		return "", "", err
	}
	return continuationID, conditionID, nil
}

// PickRestart resolves a durable condition by choosing a restart (ADR-05 §6): it
// checks the restart's required capability against grantedCaps, records the
// resolution, flips the continuation to 'ready', and inserts a resume task — all
// in one transaction. resolvedBy is the principal id for the audit columns.
func PickRestart(ctx context.Context, db DB, conditionID, restartName string, args map[string]any, resolvedBy string, grantedCaps []string) (err error) {
	argsJSON, err := jsonOrEmpty(args)
	if err != nil {
		return err
	}
	if err = db.BeginSerializable(ctx); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = db.Rollback(ctx)
		}
	}()

	var restartID, capReq, continuationID string
	var capNull, contFound bool
	// restart row + capability
	found, err := db.QueryRow(ctx, `
SELECT r.id, COALESCE(r.capability_required, ''), (r.capability_required IS NULL)
FROM restart r WHERE r.condition_id = $1 AND r.name = $2`,
		[]any{conditionID, restartName}, &restartID, &capReq, &capNull)
	if err != nil {
		return err
	}
	if !found {
		err = ErrRestartNotFound
		return err
	}
	if !capNull && capReq != "" && !contains(grantedCaps, capReq) {
		err = fmt.Errorf("%w: %q", ErrCapabilityRefused, capReq)
		return err
	}

	contFound, err = db.QueryRow(ctx, `SELECT continuation_id FROM durable_condition WHERE id = $1`,
		[]any{conditionID}, &continuationID)
	if err != nil {
		return err
	}
	if !contFound {
		err = ErrRestartNotFound
		return err
	}

	res, err := db.Exec(ctx, `
UPDATE durable_condition
   SET status='resolved', resolved_restart=$2, resolved_args=$3::jsonb, resolved_by=$4, resolved_at=now()
 WHERE id=$1 AND status='open'`, conditionID, restartID, argsJSON, resolvedBy)
	if err != nil {
		return err
	}
	if res.RowsAffected != 1 {
		err = fmt.Errorf("cfr: condition %s not open", conditionID)
		return err
	}

	var stepSeq int64
	if _, err = db.QueryRow(ctx, `
UPDATE continuation SET status='ready', updated_at=now() WHERE id=$1 RETURNING step_seq`,
		[]any{continuationID}, &stepSeq); err != nil {
		return err
	}

	taskPayload := fmt.Sprintf(`{"continuation_id":%q,"step_seq":%d}`, continuationID, stepSeq)
	if _, err = db.Exec(ctx, `
INSERT INTO task (id, kind, run_at, payload) VALUES ($1, 'resume', now(), $2::jsonb)`,
		uuid4(), taskPayload); err != nil {
		return err
	}

	err = db.Commit(ctx)
	return err
}

// ClaimAndResume performs the ADR-05 §7 claim CAS + step transaction: it claims a
// 'ready' continuation (status='ready' AND step_seq=seenSeq ⇒ 'running',
// step_seq+1, lease), decodes the CFR blob, re-enters the machine via resume
// delivering the resolved restart, then checkpoints the outcome. It returns the
// outcome and whether this call won the claim. A corrupt CFR blob fails closed
// into a 'step.failed' condition (ADR-05 §6 test 4b).
func ClaimAndResume(ctx context.Context, db DB, continuationID string, seenSeq int64, kernelID string,
	resume func(state *cek.State, choice cek.RestartChoice) cek.Outcome) (out cek.Outcome, claimed bool, err error) {

	if err = db.BeginSerializable(ctx); err != nil {
		return cek.Outcome{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = db.Rollback(ctx)
		}
	}()

	// The CLAIM (CAS).
	res, cerr := db.Exec(ctx, `
UPDATE continuation
   SET status='running', lease_owner=$2::uuid, lease_until=now()+interval '30 seconds',
       step_seq=step_seq+1, updated_at=now()
 WHERE id=$1 AND status='ready' AND step_seq=$3`, continuationID, kernelID, seenSeq)
	if cerr != nil {
		if pgwire.IsCode(cerr, "40001") { // serialization failure ⇒ lost the race
			return cek.Outcome{}, false, nil
		}
		err = cerr
		return cek.Outcome{}, false, err
	}
	if res.RowsAffected != 1 {
		// Another kernel won, or state moved: no work done.
		if err = db.Commit(ctx); err == nil {
			committed = true
		}
		return cek.Outcome{}, false, err
	}
	claimed = true

	// Load frames + resolved restart.
	var framesHex string
	var restartName, argsJSON string
	if _, err = db.QueryRow(ctx, `SELECT frames FROM continuation WHERE id=$1`,
		[]any{continuationID}, &framesHex); err != nil {
		return cek.Outcome{}, true, err
	}
	rfound, rerr := db.QueryRow(ctx, `
SELECT r.name, COALESCE(dc.resolved_args::text, '{}')
FROM durable_condition dc JOIN restart r ON r.id = dc.resolved_restart
WHERE dc.continuation_id = $1 AND dc.status = 'resolved'
ORDER BY dc.signaled_at DESC LIMIT 1`, []any{continuationID}, &restartName, &argsJSON)
	if rerr != nil {
		err = rerr
		return cek.Outcome{}, true, err
	}
	if !rfound {
		err = ErrNotResolved
		return cek.Outcome{}, true, err
	}

	frames, derr := decodeBytea(framesHex)
	if derr != nil {
		err = derr
		return cek.Outcome{}, true, err
	}
	state, decErr := Decode(frames)
	if decErr != nil {
		// Fail closed: mark the continuation failed and record a step.failed
		// condition; never a partial resume (ADR-05 §6 test 4b).
		if e := recordStepFailed(ctx, db, continuationID, decErr); e != nil {
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

	choice := cek.RestartChoice{Name: restartName, Args: parseArgs(argsJSON)}
	out = resume(state, choice)

	if err = checkpoint(ctx, db, continuationID, out); err != nil {
		return out, true, err
	}
	if err = db.Commit(ctx); err != nil {
		return out, true, err
	}
	committed = true
	return out, true, nil
}

// checkpoint writes the terminal (or re-parked) state of a resumed step.
func checkpoint(ctx context.Context, db DB, continuationID string, out cek.Outcome) error {
	switch out.Kind {
	case cek.OutDone:
		_, err := db.Exec(ctx, `
UPDATE continuation SET frames=$2::bytea, status='done', updated_at=now() WHERE id=$1`,
			continuationID, byteaLiteral(nil))
		return err
	case cek.OutParked:
		// Re-park: rewrite frames + open a fresh condition. (Rare in Stage A.)
		blob, err := Encode(out.State)
		if err != nil {
			return err
		}
		newCond := uuid4()
		wake := fmt.Sprintf(`{"kind":"manual","condition":%q}`, newCond)
		if _, err := db.Exec(ctx, `
UPDATE continuation SET frames=$2::bytea, wake=$3::jsonb, status='condition', updated_at=now() WHERE id=$1`,
			continuationID, byteaLiteral(blob), wake); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload) VALUES ($1, $2, $3, '{}'::jsonb)`,
			newCond, continuationID, out.Condition.Class); err != nil {
			return err
		}
		for _, r := range out.Condition.Restarts {
			if _, err := db.Exec(ctx, `
INSERT INTO restart (id, condition_id, name, label, capability_required) VALUES ($1,$2,$3,$4,$5)`,
				uuid4(), newCond, r.Name, r.Label, nullable(r.CapabilityRequired)); err != nil {
				return err
			}
		}
		return nil
	default: // OutFaulted / OutError
		_, err := db.Exec(ctx, `
UPDATE continuation SET status='failed', updated_at=now() WHERE id=$1`, continuationID)
		return err
	}
}

func recordStepFailed(ctx context.Context, db DB, continuationID string, cause error) error {
	if _, err := db.Exec(ctx, `UPDATE continuation SET status='failed', updated_at=now() WHERE id=$1`,
		continuationID); err != nil {
		return err
	}
	payload := fmt.Sprintf(`{"error":%q}`, cause.Error())
	_, err := db.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload) VALUES ($1, $2, 'step.failed', $3::jsonb)`,
		uuid4(), continuationID, payload)
	return err
}
