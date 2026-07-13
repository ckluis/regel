package catalog

import (
	"context"
	"strings"
	"time"
)

// Chain is the principal scope chain a request carries (ADR-03 §3). Absent
// levels are the empty string; the product level (scope_kind 0) is always ”.
type Chain struct {
	UserID    string
	TeamID    string
	OrgID     string
	PackageID string
}

// ResolveReq is one name resolution. CallerModule is the module path of the
// definition issuing the lookup (” for external entry points, which then see
// only exported pointers). AsOf, when set, resolves against history at that
// instant instead of the live pointer.
type ResolveReq struct {
	Name         string
	Chain        Chain
	CallerModule string
	AsOf         *time.Time
}

// Resolved is the winning pointer of a resolution.
type Resolved struct {
	Hash      string
	Kind      string
	ScopeKind int
}

// moduleOf is the module path of a name: every segment but the final
// declaration segment. A single-segment name has module ”. Pure function of
// the name (ADR-03 §3), so it is computed here rather than in SQL.
func moduleOf(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[:i]
	}
	return ""
}

// Resolve is the single name resolver (ADR-03 §3). It walks the scope chain
// most-specific-first (user > team > org > package > product) and returns the
// first hit, enforcing the visibility predicate inline: an exported pointer
// matches always; a private pointer matches only when the caller's module is
// the name's own module. As-of resolution runs the same shape against
// name_pointer_history bounded by valid_from/valid_to; the I4 GiST exclusion
// guarantees at most one history row per (name, scope) at any instant. The
// second return is false when nothing resolves (indistinguishable, by design,
// from a private name the caller cannot see).
func Resolve(ctx context.Context, q Querier, req ResolveReq) (Resolved, bool, error) {
	c := req.Chain
	var r Resolved
	var ok bool
	var err error
	if req.AsOf == nil {
		// module_of(name) is constant across the result set (WHERE name = :name),
		// so it is bound as a parameter rather than recomputed per row.
		ok, err = q.QueryRow(ctx, `
SELECT hash, kind, scope_kind FROM name_pointer
WHERE name = $1
  AND (scope_kind, scope_id) IN (VALUES
        (4::smallint, $2::text), (3::smallint, $3::text), (2::smallint, $4::text),
        (1::smallint, $5::text), (0::smallint, ''::text))
  AND (visibility = 'exported'
       OR (visibility = 'private' AND $6::text = $7::text))
ORDER BY scope_kind DESC
LIMIT 1`,
			[]any{req.Name, c.UserID, c.TeamID, c.OrgID, c.PackageID, moduleOf(req.Name), req.CallerModule},
			&r.Hash, &r.Kind, &r.ScopeKind)
	} else {
		// History carries no visibility/kind columns; kind is recovered by joining
		// the immortal definition row. As-of visibility is not reconstructable
		// (visibility is not versioned), so the historical predicate is scope-only.
		ok, err = q.QueryRow(ctx, `
SELECT h.hash, d.kind, h.scope_kind
FROM name_pointer_history h JOIN definition d ON d.hash = h.hash
WHERE h.name = $1
  AND (h.scope_kind, h.scope_id) IN (VALUES
        (4::smallint, $2::text), (3::smallint, $3::text), (2::smallint, $4::text),
        (1::smallint, $5::text), (0::smallint, ''::text))
  AND h.valid_from <= $6 AND (h.valid_to IS NULL OR h.valid_to > $6)
ORDER BY h.scope_kind DESC
LIMIT 1`,
			[]any{req.Name, c.UserID, c.TeamID, c.OrgID, c.PackageID, *req.AsOf},
			&r.Hash, &r.Kind, &r.ScopeKind)
	}
	if err != nil || !ok {
		return Resolved{}, ok, err
	}
	return r, true, nil
}
