package admission

import (
	"fmt"
	"sort"

	"regel.dev/regel/internal/lower"
)

// verifyV1 runs the Stage-A verifier suite (STAGE-A-PLAN pin #2): V1
// capability-audit, red-path-first (ADR-07 §4).
//
// The set of capabilities a definition can NAME (free references resolving via
// its deps to capability-bearing std bindings) must satisfy
//
//	named ⊆ declared ⊆ grants
//
// with no ambient authority. A violation returns a CAP_UNGRANTED diagnostic
// naming the capability and the subject, which the pipeline turns into a
// rollback (zero trace) plus a durable refusal.
//
// STAGE-A RESIDUE: the named-capability set is computed from a definition's
// resolved dep edges (referenced addresses) rather than a full free-variable
// walk of the rast body — equivalent for Stage A's single capability-bearing
// std binding (std/mail.send). V2–V6 (pii-flow, catalog-parity, contracts,
// capture, derivation-parity) are Stage-B seams.
func verifyV1(defs []loweredDef, patch Patch, grants map[string]bool, im *Image) []Diagnostic {
	var diags []Diagnostic
	for _, ld := range defs {
		named := namedCapabilities(ld.Def, im)
		declared := setOf(patch.declaredFor(ld.CatalogName))

		// named ⊆ declared: a capability the code can name but did not declare.
		for _, cap := range sortedKeys(named) {
			if !declared[cap] {
				diags = append(diags, capUngranted(ld, cap,
					fmt.Sprintf("definition names capability %q but does not declare it", cap)))
			}
		}
		// declared ⊆ grants: a declared capability the principal was not granted.
		for _, cap := range sortedKeys(declared) {
			if !grants[cap] {
				diags = append(diags, capUngranted(ld, cap,
					fmt.Sprintf("capability %q is declared but not granted to the submitting principal", cap)))
			}
		}
	}
	return diags
}

// namedCapabilities is the set of capabilities a definition can name, via its
// resolved dependency edges into capability-bearing std bindings.
func namedCapabilities(d lower.Definition, im *Image) map[string]bool {
	out := map[string]bool{}
	for _, dep := range d.Deps {
		if cap, ok := im.CapabilityByHash[dep.Hash]; ok {
			out[cap] = true
		}
	}
	return out
}

func capUngranted(ld loweredDef, capability, msg string) Diagnostic {
	return Diagnostic{
		StageOrVerifier: "V1",
		Code:            "CAP_UNGRANTED",
		Severity:        "error",
		Subject:         ld.CatalogName,
		Loc:             Loc{DefHash: ld.Def.Hash},
		Message:         msg,
		Fix:             "declare the capability in the patch envelope and hold a matching grant, or remove the capability-bearing call",
	}
}

func setOf(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		if x != "" {
			m[x] = true
		}
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
