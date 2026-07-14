package admission

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"regel.dev/regel/internal/catalog"
)

// FetchVerdict resolves a verdict id for verdict.get / catalog://verdict/{id}
// (ADR-12 §5, caller-scoped BUILD-C pin). id is a patch_id (bigint admission id)
// or a refusal_id (uuid). It returns the stored Verdict ONLY when the recorded
// principal is the authenticated caller; a foreign id, or one that never existed,
// returns (Verdict{}, false, nil) — the IDENTICAL not-found path (same bytes), so
// a guessed/leaked id is not a cross-principal disclosure oracle. The §3 latency
// floor is applied by the calling door, not here (this stays pure/fast).
func FetchVerdict(ctx context.Context, q catalog.Querier, id, callerSubject string) (Verdict, bool, error) {
	if looksLikeUUID(id) {
		return fetchRefusalVerdict(ctx, q, id, callerSubject)
	}
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		return fetchAdmissionVerdict(ctx, q, n, callerSubject)
	}
	// Not a shape any ledger uses: identical not-found.
	return Verdict{}, false, nil
}

// fetchRefusalVerdict serves a gate_refusal row's Verdict, caller-scoped.
func fetchRefusalVerdict(ctx context.Context, q catalog.Querier, refusalID, caller string) (Verdict, bool, error) {
	var blob string
	found, err := q.QueryRow(ctx,
		`SELECT verdict::text FROM gate_refusal WHERE refusal_id=$1 AND principal=$2`,
		[]any{refusalID, caller}, &blob)
	if err != nil || !found {
		return Verdict{}, false, err
	}
	var v Verdict
	if err := json.Unmarshal([]byte(blob), &v); err != nil {
		return Verdict{}, false, err
	}
	return v, true, nil
}

// fetchAdmissionVerdict serves a committed admission's verifier_report Verdict,
// caller-scoped ((actor_kind||':'||actor_id) must equal the caller).
func fetchAdmissionVerdict(ctx context.Context, q catalog.Querier, admissionID int64, caller string) (Verdict, bool, error) {
	var blob string
	found, err := q.QueryRow(ctx, `
SELECT verifier_report::text FROM admission
WHERE id=$1 AND (actor_kind || ':' || actor_id) = $2`,
		[]any{admissionID, caller}, &blob)
	if err != nil || !found {
		return Verdict{}, false, err
	}
	var v Verdict
	if err := json.Unmarshal([]byte(blob), &v); err != nil {
		return Verdict{}, false, err
	}
	return v, true, nil
}

// looksLikeUUID reports whether s has the 8-4-4-4-12 hex UUID shape.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
