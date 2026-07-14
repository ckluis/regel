package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/pgwire"
)

// reads.go is the scope-filtered, timing-floored catalog read surface the tools and
// resources share (ADR-12 §2/§3). The visibility predicate is evaluated FIRST on
// every name-addressed read; a not-visible name and a not-exist name share the one
// fast-fail NOT_FOUND path, byte-identical and padded to ResolutionFloor.

// notFoundResult is the ONE byte-identical NOT_FOUND body (ADR-12 §3): an
// out-of-scope real name and a hallucinated name return exactly these bytes.
func notFoundResult() map[string]any { return map[string]any{"error": "NOT_FOUND"} }

// scopeClause builds the visible-scope predicate over a table alias (ADR-03 §3):
// (alias.scope_kind=K AND alias.scope_id=$n) OR … . Scope KINDS are a fixed 0..4
// set (safe to inline); scope IDS are parameterized. startIdx is the next $ index.
func scopeClause(alias string, chain catalog.Chain, startIdx int) (string, []any) {
	scopes := visibleScopes(chain)
	var parts []string
	var args []any
	idx := startIdx
	for _, sc := range scopes {
		parts = append(parts, fmt.Sprintf("(%s.scope_kind=%d AND %s.scope_id=$%d)", alias, sc.Kind, alias, idx))
		args = append(args, sc.ID)
		idx++
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// searchRow is one catalog.search / resource-list result.
type searchRow struct {
	Hash      string `json:"hash"`
	QName     string `json:"qname"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	Contracts string `json:"contracts,omitempty"`
	Docstring string `json:"docstring,omitempty"`
}

// catalogSearch runs the scope-chain-filtered search (ADR-12 §2): out-of-scope
// names are absent; every result carries its canonical qname. No source, no data.
func catalogSearch(ctx context.Context, conn *pgwire.Conn, chain catalog.Chain, query, kind, scope string) ([]searchRow, error) {
	sc, args := scopeClause("np", chain, 1)
	sqlText := `
SELECT np.hash, np.name, np.kind, np.scope_kind, np.scope_id,
       d.contracts::text, COALESCE(dm.docstring,'')
FROM name_pointer np
JOIN definition d ON d.hash = np.hash
LEFT JOIN definition_meta dm ON dm.hash = np.hash
WHERE ` + sc + ` AND np.visibility='exported'`
	next := len(args) + 1
	if query != "" {
		sqlText += fmt.Sprintf(" AND np.name ILIKE $%d", next)
		args = append(args, "%"+query+"%")
		next++
	}
	if kind != "" {
		sqlText += fmt.Sprintf(" AND np.kind = $%d", next)
		args = append(args, kind)
		next++
	}
	// scope? is a FILTER predicate (narrow within visible), never a second address.
	if scope != "" {
		if k, id, ok := parseScopeToken(scope); ok {
			sqlText += fmt.Sprintf(" AND np.scope_kind=%d AND np.scope_id=$%d", k, next)
			args = append(args, id)
			next++
		}
	}
	sqlText += " ORDER BY np.name, np.scope_kind LIMIT 200"

	rows, err := conn.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	var out []searchRow
	for rows.Next() {
		var hash, name, k, sid, contracts, doc string
		var skind int
		if err := rows.Scan(&hash, &name, &k, &skind, &sid, &contracts, &doc); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, searchRow{
			Hash: hash, QName: makeQName(name, skind, sid), Name: name, Kind: k,
			Scope: scopeToken(skind, sid), Contracts: contracts, Docstring: doc,
		})
	}
	return out, rows.Err()
}

// getResult is a catalog.get response (code, never data).
type getResult struct {
	Hash          string   `json:"hash"`
	QName         string   `json:"qname"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	CanonicalText string   `json:"canonical_text"`
	Contracts     string   `json:"contracts"`
	Deps          []string `json:"deps"`
	Scope         string   `json:"scope"`
	AdmittedBy    string   `json:"admitted_by"`
	AdmittedAt    string   `json:"admitted_at"`
}

// catalogGetByQName resolves a qname to a definition with the visibility predicate
// FIRST. Returns (result, found). A not-visible or not-exist name returns
// found=false down the identical path; the caller floors the NOT_FOUND.
func catalogGetByQName(ctx context.Context, conn *pgwire.Conn, chain catalog.Chain, qname string) (*getResult, bool, error) {
	name, kind, id, ok := parseQName(qname)
	if !ok {
		return nil, false, nil
	}
	// Visibility predicate FIRST — before any row is fetched (ADR-12 §3).
	if !scopeVisible(chain, kind, id) {
		maybeLeak(ctx, conn, name, kind, id)
		return nil, false, nil
	}
	return catalogGetPointer(ctx, conn, name, kind, id)
}

// catalogGetPointer fetches an exported pointer + its definition at an exact scope.
func catalogGetPointer(ctx context.Context, conn *pgwire.Conn, name string, kind int, id string) (*getResult, bool, error) {
	var hash, k, canon, contracts, admittedAt, admActor, admKind string
	var deps []string
	found, err := conn.QueryRow(ctx, `
SELECT np.hash, np.kind, d.canonical_text, d.contracts::text, d.deps,
       to_char(a.created_at, 'YYYY-MM-DD"T"HH24:MI:SSZ'), a.actor_id, a.actor_kind
FROM name_pointer np
JOIN definition d ON d.hash = np.hash
JOIN admission a ON a.id = np.admission_id
WHERE np.name=$1 AND np.scope_kind=$2 AND np.scope_id=$3 AND np.visibility='exported'`,
		[]any{name, kind, id}, &hash, &k, &canon, &contracts, &deps, &admittedAt, &admActor, &admKind)
	if err != nil || !found {
		return nil, false, err
	}
	return &getResult{
		Hash: hash, QName: makeQName(name, kind, id), Name: name, Kind: k,
		CanonicalText: canon, Contracts: contracts, Deps: deps,
		Scope: scopeToken(kind, id), AdmittedBy: admKind + ":" + admActor, AdmittedAt: admittedAt,
	}, true, nil
}

// catalogGetByHash resolves a hash to a definition ONLY if the caller can see it
// through some visible exported pointer (scope filter on a content-addressed read).
func catalogGetByHash(ctx context.Context, conn *pgwire.Conn, chain catalog.Chain, hash string) (*getResult, bool, error) {
	sc, args := scopeClause("np", chain, 2)
	args = append([]any{hash}, args...)
	var name, k, sid string
	var skind int
	found, err := conn.QueryRow(ctx, `
SELECT np.name, np.kind, np.scope_kind, np.scope_id
FROM name_pointer np
WHERE np.hash=$1 AND np.visibility='exported' AND `+sc+`
ORDER BY np.scope_kind DESC LIMIT 1`,
		args, &name, &k, &skind, &sid)
	if err != nil || !found {
		return nil, false, err
	}
	return catalogGetPointer(ctx, conn, name, skind, sid)
}

// catalogDeps returns a definition's dependency edges (dir out) or dependents (dir
// in), each filtered to a visible exported pointer (out-of-scope edges are absent).
func catalogDeps(ctx context.Context, conn *pgwire.Conn, chain catalog.Chain, hash, dir string) ([]map[string]string, bool, error) {
	// The hash itself must be visible first.
	if _, ok, err := catalogGetByHash(ctx, conn, chain, hash); err != nil || !ok {
		return nil, false, err
	}
	sc, args := scopeClause("np", chain, 2)
	var sqlText string
	if dir == "in" {
		// Dependents: definitions whose deps array contains this hash.
		sqlText = `
SELECT DISTINCT np.hash, np.name
FROM name_pointer np JOIN definition d ON d.hash = np.hash
WHERE $1 = ANY(d.deps) AND np.visibility='exported' AND ` + sc + `
ORDER BY np.name LIMIT 500`
	} else {
		// Dependencies: the def's own dep hashes, resolved to a visible name.
		sqlText = `
SELECT DISTINCT np.hash, np.name
FROM definition src
JOIN name_pointer np ON np.hash = ANY(src.deps)
WHERE src.hash=$1 AND np.visibility='exported' AND ` + sc + `
ORDER BY np.name LIMIT 500`
	}
	args = append([]any{hash}, args...)
	rows, err := conn.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, false, err
	}
	out := []map[string]string{}
	for rows.Next() {
		var h, n string
		if err := rows.Scan(&h, &n); err != nil {
			rows.Close()
			return nil, false, err
		}
		out = append(out, map[string]string{"hash": h, "name": n})
	}
	return out, true, rows.Err()
}

// maybeLeak simulates the NAIVE resolver's extra row-fetch work for a not-visible
// name (an existence oracle through the clock). Off by default; the timing red-path
// flips it on with the floor bypassed to prove the two-sample test load-bearing.
func maybeLeak(ctx context.Context, conn *pgwire.Conn, name string, kind int, id string) {
	if !leakOutOfScope {
		return
	}
	var hash string
	_, _ = conn.QueryRow(ctx,
		`SELECT hash FROM name_pointer WHERE name=$1 AND scope_kind=$2 AND scope_id=$3`,
		[]any{name, kind, id}, &hash)
	// A tiny extra, name-existence-correlated pause standing in for the index-hit +
	// row-fetch + dependency-touch a real out-of-scope key would cost.
	if hash != "" {
		time.Sleep(1500 * time.Microsecond)
	}
}
