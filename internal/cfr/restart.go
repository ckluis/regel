package cfr

import (
	"context"
	"errors"

	"regel.dev/regel/internal/cek"
)

// restart.go is the ONE fenced restart-resolution path (ADR-12 §7), shared by the
// kernel /restart door and the MCP condition.restart tool — one implementation,
// two doors. It adds the expectedHash fence on top of ADR-05 §6's resolution and
// composes with the ClaimAndStep seam (via ClaimAndResume), never around it:
//
//	load condition → assert status='open' (already-resolved ⇒ idempotent reject)
//	→ assert current frames hash == expectedHash (moved ⇒ CONDITION_MOVED)
//	→ PickRestart (capability_required check + resolution CAS + resume task)
//	→ ClaimAndResume (step_seq CAS + the ADR-05 §6 resume).
//
// expectedHash = SHA-256 of the condition's continuation frames blob at render
// time (what condition.list and the operator inbox embed per button), computed in
// Postgres so the fence sees exactly the bytes the resume will re-enter.
var (
	// ErrConditionMoved: the continuation's frames no longer hash to the caller's
	// expectedHash — a stale button/inspection; nothing is resumed (ADR-12 §7).
	ErrConditionMoved = errors.New("cfr: condition moved (expectedHash mismatch)")
	// ErrConditionResolved: the condition is already resolved — an idempotent
	// reject, never a double resume (ADR-12 §7 / simplest-thing idempotence test).
	ErrConditionResolved = errors.New("cfr: condition already resolved")
)

// FrameHash returns the SHA-256 hex of a continuation's frames blob — the
// expectedHash a caller must present to restart the condition parked on it.
func FrameHash(ctx context.Context, db DB, continuationID string) (string, bool, error) {
	var h string
	found, err := db.QueryRow(ctx,
		`SELECT encode(sha256(frames), 'hex') FROM continuation WHERE id=$1`,
		[]any{continuationID}, &h)
	return h, found, err
}

// ConditionFrameHash returns (continuationID, status, currentFrameHash) for a
// durable condition — the fence inputs, read in one round trip.
func ConditionFrameHash(ctx context.Context, db DB, conditionID string) (contID, status, frameHash string, found bool, err error) {
	found, err = db.QueryRow(ctx, `
SELECT dc.continuation_id, dc.status, encode(sha256(c.frames), 'hex')
FROM durable_condition dc JOIN continuation c ON c.id = dc.continuation_id
WHERE dc.id = $1`,
		[]any{conditionID}, &contID, &status, &frameHash)
	return contID, status, frameHash, found, err
}

// ResolveConditionFenced runs the full fenced restart path for one durable
// condition. resolvedBy is the principal id for the audit columns; grantedCaps is
// the caller's capability set (ADR-05 capability_required); resume is the
// interpreter's resume closure. A CONDITION_MOVED / already-resolved / missing /
// capability failure is returned as a typed error and resumes nothing.
func ResolveConditionFenced(ctx context.Context, db DB, conditionID, restartName, expectedHash string,
	args map[string]any, resolvedBy string, grantedCaps []string,
	resume func(state *cek.State, choice cek.RestartChoice) cek.Outcome) (cek.Outcome, error) {

	contID, status, frameHash, found, err := ConditionFrameHash(ctx, db, conditionID)
	if err != nil {
		return cek.Outcome{}, err
	}
	if !found {
		return cek.Outcome{}, ErrRestartNotFound
	}
	if status != "open" {
		return cek.Outcome{}, ErrConditionResolved
	}
	if expectedHash != "" && expectedHash != frameHash {
		return cek.Outcome{}, ErrConditionMoved
	}

	// PickRestart asserts open-status again (CAS) + capability_required, records the
	// resolution, and inserts the resume task — all in its own SERIALIZABLE txn.
	if err := PickRestart(ctx, db, conditionID, restartName, args, resolvedBy, grantedCaps); err != nil {
		return cek.Outcome{}, err
	}

	var seenSeq int64
	if _, err := db.QueryRow(ctx,
		`SELECT step_seq FROM continuation WHERE id=$1`, []any{contID}, &seenSeq); err != nil {
		return cek.Outcome{}, err
	}
	out, _, err := ClaimAndResume(ctx, db, contID, seenSeq, kernelIDFor(resolvedBy), resume)
	return out, err
}

// kernelIDFor mints a stable-enough lease owner id for a one-shot fenced resume
// (the restart path pins no epoch; ClaimAndResume carries KernelEpoch=0).
func kernelIDFor(_ string) string { return uuid4() }
