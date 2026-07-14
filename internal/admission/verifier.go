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

// --- V2/V4/V5 seam stubs (increment C2) --------------------------------------
//
// disableV2/V4/V5 are test-only kill switches (default false = enabled). The
// adversarial harness (ADR-07 §5) flips one at a time to prove each verifier is
// load-bearing: with it disabled, its red fixture ADMITS — demonstrating the
// verifier, not some other stage, is what catches the mutant — then restores.
// Package tests run sequentially, so a defer-restored global is safe.
var (
	disableV2 bool
	disableV4 bool
	disableV5 bool
)

// The suite runs V1..V6 in order at step 5b. V2 (pii-flow), V4 (contracts), and
// V5 (capture) are increment-C2 seams: they pass trivially now, mount into the
// same pipeline slot when built, and are clearly marked so no verdict is faked.
func verifyV2(_ []loweredDef, _ derivationPlan, _ *Image) []Diagnostic { return nil }
func verifyV4(_ []loweredDef, _ *Image) []Diagnostic                  { return nil }
func verifyV5(_ []loweredDef, _ Patch, _ *Image) []Diagnostic         { return nil }

// verifyV3 is catalog-parity (ADR-07 §4 V3): every declared governance artifact
// reachable from ≥1 admitted-or-derived execution path in the proposed reference
// graph; nothing declared stays inert. At Stage-C scope the artifact is a policy
// declaration: it is wired iff some resource in base ⊕ patch consults it (the
// policy-wiring pass records the referencing resource's policy hash). A declared-
// but-unconsulted policy ⇒ PARITY_UNWIRED{artifact, definition}. (Contract-
// requirement parity arrives with std/contract in C2.)
func verifyV3(plan derivationPlan) []Diagnostic {
	policies := append([]policyArtifact(nil), plan.Policies...)
	sort.Slice(policies, func(i, j int) bool { return policies[i].CatalogName < policies[j].CatalogName })
	var diags []Diagnostic
	for _, p := range policies {
		if plan.WiredPolicy[p.DefHash] {
			continue
		}
		diags = append(diags, Diagnostic{
			StageOrVerifier: "V3",
			Code:            "PARITY_UNWIRED",
			Severity:        "error",
			Subject:         p.CatalogName,
			Loc:             Loc{DefHash: p.DefHash},
			Message: fmt.Sprintf("policy %q is declared but no resource read/query path consults it; "+
				"a declared governance artifact must be wired to at least one execution path", p.CatalogName),
			Fix: "wire the policy into a resource (resource({..., policy: <name>})) or remove the declaration",
		})
	}
	return diags
}

// verifyV6 is derivation-parity (ADR-07 §4 V6): every derivation pass is total
// over the declared resource (a pii-kind field with no derivable masking rule ⇒
// DERIVE_PARTIAL{attr}), and emitted DDL is additive-only (a derived DROP/rewrite
// without retire intent ⇒ DDL_DESTRUCTIVE{stmt}). Partials are surfaced before
// destructive statements so a resource that is both partial and destructive names
// the deeper (totality) failure first.
func verifyV6(plan derivationPlan) []Diagnostic {
	var diags []Diagnostic
	for _, rp := range plan.Resources {
		for _, attr := range rp.Partials {
			diags = append(diags, Diagnostic{
				StageOrVerifier: "V6",
				Code:            "DERIVE_PARTIAL",
				Severity:        "error",
				Subject:         rp.Decl.CatalogName,
				Loc:             Loc{DefHash: rp.Decl.DefHash},
				Message: fmt.Sprintf("derivation is partial over attribute %q: a pii field of this base kind "+
					"has no masking rule derivable at this epoch", attr),
				Fix: "use a maskable pii base (text/email/phone) or drop the pii modifier",
			})
		}
	}
	for _, rp := range plan.Resources {
		for _, stmt := range rp.Destructive {
			diags = append(diags, Diagnostic{
				StageOrVerifier: "V6",
				Code:            "DDL_DESTRUCTIVE",
				Severity:        "error",
				Subject:         rp.Decl.CatalogName,
				Loc:             Loc{DefHash: rp.Decl.DefHash},
				Message: fmt.Sprintf("derived DDL is destructive: %s — additive-only admission requires an "+
					"explicit intent=retire envelope routing this to the staged maintenance lane", stmt),
				Fix: "resubmit with intent=retire to stage the change, or keep the field",
			})
		}
	}
	return diags
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
