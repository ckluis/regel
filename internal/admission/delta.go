package admission

import (
	"context"
	"sort"

	"regel.dev/regel/internal/catalog"
)

// delta.go computes the ADR-07 §6 blast-radius delta and validates the content-
// seeder set (ADR-07 §1 / ADR-12 §6). Both are pure projections over what the
// verifiers already computed vs. the base snapshot — no new analysis.

// validateSeeders binds the content-seeder set from the patch read-log against the
// AUTHENTICATED principal's scope chain (step 2a). A seeder scope outside that
// chain is unrepresentable (rejected). Human/CLI submissions carry an empty set;
// an external-effect source with no principal is recorded 'unattributed'.
func validateSeeders(patch Patch, auth Principal) ([]Seeder, *Diagnostic) {
	if auth.Via != "mcp" {
		return nil, nil // only agent (MCP) submissions carry a read-log
	}
	var out []Seeder
	for _, e := range patch.ReadLog {
		if !seederInChain(e.Scope, auth.Chain) {
			d := Diagnostic{
				StageOrVerifier: "2a", Code: "SEEDER_OUT_OF_CHAIN", Severity: "error",
				Subject: e.SourceRef,
				Message: "a content-seeder's scope lies outside the submitting principal's scope chain; " +
					"a seeder can never be attributed to a scope the principal cannot see",
				Fix: "submit only read-log entries within your own scope chain",
			}
			return nil, &d
		}
		seededBy := e.SeededBy
		if e.SourceKind == "external" || seededBy == "" {
			seededBy = "unattributed"
		}
		out = append(out, Seeder{
			SourceKind: e.SourceKind, SourceRef: e.SourceRef, Scope: e.Scope, SeededBy: seededBy,
		})
	}
	return out, nil
}

// seederInChain reports whether a seeder scope is within the principal's chain.
// Product scope (kind 0) is always in-chain; any other scope's id must match a
// non-empty chain level.
func seederInChain(s Scope, chain catalog.Chain) bool {
	if s.Kind == 0 {
		return true
	}
	for _, id := range []string{chain.UserID, chain.TeamID, chain.OrgID, chain.PackageID} {
		if id != "" && id == s.ID {
			return true
		}
	}
	return false
}

// computeDelta projects the capability/pii/ddl blast radius vs. the base snapshot.
// touched is the set of pii values V2 saw reach a sink; plan is the V6 derivation.
func computeDelta(ctx context.Context, q catalog.Querier, lowered []loweredDef, patch Patch,
	grants map[string]bool, plan derivationPlan, touched map[string]bool, scope Scope, im *Image) (Delta, error) {

	var d Delta

	// --- capabilities (V1) ---
	requested := map[string]bool{}
	for _, ld := range lowered {
		for _, c := range patch.declaredFor(ld.CatalogName) {
			if c != "" {
				requested[c] = true
			}
		}
	}
	added := map[string]bool{}
	for _, ld := range lowered {
		changed, baseHash, err := defChanged(ctx, q, ld, scope)
		if err != nil {
			return Delta{}, err
		}
		if !changed {
			continue
		}
		baseNamed, err := baseNamedCaps(ctx, q, baseHash, im)
		if err != nil {
			return Delta{}, err
		}
		for c := range namedCapabilities(ld.Def, im) {
			if !baseNamed[c] {
				added[c] = true
			}
		}
	}
	d.Capabilities = CapDelta{
		Requested:   sortedKeys(requested),
		Granted:     intersect(requested, grants),
		AddedVsBase: sortedKeys(added),
	}

	// --- pii surface (V2) ---
	addedPii := map[string]bool{}
	// touched is per-patch; a value is "added" if its owning def changed. At
	// Stage-C the touched set names bindings, not defs, so a changed def contributes
	// all its touched names; a no-op contributes none (all defs unchanged).
	anyChanged := false
	for _, ld := range lowered {
		changed, _, err := defChanged(ctx, q, ld, scope)
		if err != nil {
			return Delta{}, err
		}
		anyChanged = anyChanged || changed
	}
	if anyChanged {
		for name := range touched {
			addedPii[name] = true
		}
	}
	d.PIISurface = PIIDelta{Touched: sortedKeys(touched), AddedVsBase: sortedKeys(addedPii)}

	// --- ddl surface (V6) ---
	var stmts, additive []string
	destructive := false
	for _, rp := range plan.Resources {
		stmts = append(stmts, rp.Additive...)
		stmts = append(stmts, rp.Destructive...)
		additive = append(additive, rp.Additive...)
		if len(rp.Destructive) > 0 {
			destructive = true
		}
	}
	sort.Strings(stmts)
	sort.Strings(additive)
	d.DDLSurface = DDLDelta{Statements: stmts, Additive: !destructive, AddedVsBase: additive}

	return d, nil
}

// defChanged reports whether a def is new or moved vs. the base head pointer.
func defChanged(ctx context.Context, q catalog.Querier, ld loweredDef, scope Scope) (bool, string, error) {
	var head string
	found, err := q.QueryRow(ctx,
		`SELECT hash FROM name_pointer WHERE name=$1 AND scope_kind=$2 AND scope_id=$3`,
		[]any{ld.CatalogName, scope.Kind, scope.ID}, &head)
	if err != nil {
		return false, "", err
	}
	if !found {
		return true, "", nil // brand new
	}
	return head != ld.Def.Hash, head, nil
}

// baseNamedCaps returns the capabilities the base head definition could name.
func baseNamedCaps(ctx context.Context, q catalog.Querier, baseHash string, im *Image) (map[string]bool, error) {
	out := map[string]bool{}
	if baseHash == "" {
		return out, nil
	}
	var deps []string
	found, err := q.QueryRow(ctx, `SELECT deps FROM definition WHERE hash=$1`, []any{baseHash}, &deps)
	if err != nil || !found {
		return out, err
	}
	for _, h := range deps {
		if c, ok := im.CapabilityByHash[h]; ok {
			out[c] = true
		}
	}
	return out, nil
}

func intersect(a map[string]bool, b map[string]bool) []string {
	var out []string
	for k := range a {
		if b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
