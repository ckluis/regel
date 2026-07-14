package gitproj

import (
	"context"
	"time"

	"regel.dev/regel/internal/catalog"
)

// query.go loads the immortal catalog + ledger rows the fold replays. Everything
// read here is append-only/immortal (definitions, definition_meta, admission,
// name_pointer_history), so the fold is a pure function of stored data.

// admissionRow is one ledger row's projection-relevant fields, in id order.
type admissionRow struct {
	id          int64
	actorKind   string
	actorID     string
	via         string
	createdUnix int64
}

func loadAdmissions(ctx context.Context, q catalog.Querier) ([]admissionRow, error) {
	rows, err := q.Query(ctx, `
SELECT id, actor_kind, actor_id, via, created_at
FROM admission ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []admissionRow
	for rows.Next() {
		var a admissionRow
		var created time.Time
		if err := rows.Scan(&a.id, &a.actorKind, &a.actorID, &a.via, &created); err != nil {
			return nil, err
		}
		a.createdUnix = created.UTC().Unix()
		out = append(out, a)
	}
	return out, rows.Err()
}

// loadProjectedHistory groups every PROJECTED (product + package scope) name-pointer
// history window by the admission that opened it. Overlays (scope_kind 2/3/4), the
// vault, grants, and all non-definition rows are structurally absent — they are not
// name_pointer rows, so no byte of them can reach the fold (ADR-09 §5 leak safety).
func loadProjectedHistory(ctx context.Context, q catalog.Querier) (map[int64][]ptr, error) {
	rows, err := q.Query(ctx, `
SELECT admission_id, name, hash, visibility
FROM name_pointer_history
WHERE scope_kind IN (0, 1)
  AND (name LIKE 'app/%' OR name LIKE 'std/%')
ORDER BY admission_id ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]ptr{}
	for rows.Next() {
		var admID int64
		var p ptr
		if err := rows.Scan(&admID, &p.name, &p.hash, &p.visibility); err != nil {
			return nil, err
		}
		out[admID] = append(out[admID], p)
	}
	return out, rows.Err()
}

// loadDefs loads the immortal content every projected file body needs, keyed by
// content hash: kind, canonical_text, and the out-of-hash docstring.
func loadDefs(ctx context.Context, q catalog.Querier) (map[string]defRow, error) {
	rows, err := q.Query(ctx, `
SELECT d.hash, d.kind, d.canonical_text, COALESCE(m.docstring, '')
FROM definition d
LEFT JOIN definition_meta m ON m.hash = d.hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]defRow{}
	for rows.Next() {
		var hash string
		var d defRow
		if err := rows.Scan(&hash, &d.kind, &d.canonicalText, &d.docstring); err != nil {
			return nil, err
		}
		out[hash] = d
	}
	return out, rows.Err()
}
