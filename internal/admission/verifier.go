package admission

import (
	"fmt"
	"sort"

	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/mutants"
	"regel.dev/regel/internal/rast"
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
		// MUTANT V1_SKIP_DECLARED_CHECK (ADR-07 §5 dir-ii): dropping this loop lets
		// ambient authority through — a def can name a capability it never declared.
		if !mutants.Active("V1_SKIP_DECLARED_CHECK") {
			for _, cap := range sortedKeys(named) {
				if !declared[cap] {
					diags = append(diags, capUngranted(ld, cap,
						fmt.Sprintf("definition names capability %q but does not declare it "+
							"(declare it as %q — the bare token, e.g. --declare %s; a std/ import "+
							"prefix is stripped)", cap, cap, cap)))
				}
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

// verifyV2 is pii-flow (ADR-07 §4 V2): a taint analysis over the typed AST. A
// vault/pii-typed value reaching a boundary sink (the return path of a served
// definition, or a capability-bearing outbound/log call) without passing through
// a masking or reveal-grant combinator ⇒ PII_ESCAPE{field, sink}; a vault-typed
// literal in code ⇒ PII_LITERAL{loc} (the ADR-03 immortality interaction). Taint
// propagates through local bindings and through a helper whose declared return
// type is a vault type (the multi-hop case).
func verifyV2(lowered []loweredDef, _ derivationPlan, im *Image) []Diagnostic {
	piiReturn := piiReturnMap(lowered)
	var diags []Diagnostic
	for _, ld := range lowered {
		d, _ := verifyV2Def(ld, im, piiReturn)
		diags = append(diags, d...)
	}
	return diags
}

// verifyV4 is contracts (ADR-07 §4 V4): pre/post combinator clauses must be PURE.
// A clause that names a capability-bearing binding ⇒ CONTRACT_EFFECTFUL{clause};
// a clause that names a governance / out-of-scope symbol (a policy/resource/sql
// binding, never a pure predicate) ⇒ CONTRACT_MALFORMED{def, clause}.
func verifyV4(lowered []loweredDef, im *Image) []Diagnostic {
	var diags []Diagnostic
	for _, ld := range lowered {
		diags = append(diags, verifyV4Def(ld, im)...)
	}
	return diags
}

// verifyV5 is the ADR-05 §3 capture verifier (ADR-07 §4 V5): for every await in a
// workflow-tier definition, the live-variable set must lie inside the R2
// serializable lattice — the exact set the CFR value codec round-trips
// (cfr.EncodableTags, the shared type table). A live host resource (std/sql.Conn)
// has no encodable value tag, so held across an await ⇒ CAPTURE_UNSERIALIZABLE.
func verifyV5(lowered []loweredDef, patch Patch, im *Image) []Diagnostic {
	var diags []Diagnostic
	for _, ld := range lowered {
		if patch.Tier[ld.CatalogName] != "workflow" {
			continue // capture only matters at workflow tier (ADR-10 §6)
		}
		diags = append(diags, verifyV5Def(ld, im)...)
	}
	return diags
}

// verifyV5Def runs the capture walk over one workflow definition's body.
func verifyV5Def(ld loweredDef, im *Image) []Diagnostic {
	_, _, body, ok := funcParts(ld.Def)
	if !ok {
		return nil
	}
	w := &v5walk{
		im: im, di: newDefInfo(ld.Def),
		defHash: ld.Def.Hash, catName: ld.CatalogName,
		atRisk: map[int]bool{},
	}
	if body != nil && body.Kind == rast.KBlock && len(body.Kids) > 0 && body.Kids[0] != nil {
		w.walkStmts(body.Kids[0].Kids)
	}
	return w.diags
}

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
	// MUTANT V3_SKIP_POLICY_PARITY (ADR-07 §5 dir-ii): skipping the parity sweep
	// lets a declared-but-unwired governance artifact become inert code.
	if mutants.Active("V3_SKIP_POLICY_PARITY") {
		return nil
	}
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
					"has no masking rule / vault route derivable at this epoch (a composite address or "+
					"relation edge is not a single vaultable value)", attr),
				Fix: "pii a scalar value-leaf base (text/longtext/number/money/boolean/date/timestamp/email/phone/url/select/states), or drop the pii modifier",
			})
		}
		// KT-A3 internal arm: every derivable pii field MUST carry a vault route AND a
		// mask rule; a pii field whose route was suppressed ⇒ DERIVE_PARTIAL (the row
		// never exists). Under normal derivation this always holds — the parity red-path
		// suppresses a route to prove the control load-bearing.
		routed := setOf(rp.VaultRoutes)
		for _, f := range sortedFields(rp.NewShape) {
			if f.PII && piiWrappable(f.Base) && !routed[f.Name] {
				diags = append(diags, Diagnostic{
					StageOrVerifier: "V6", Code: "DERIVE_PARTIAL", Severity: "error",
					Subject: rp.Decl.CatalogName, Loc: Loc{DefHash: rp.Decl.DefHash},
					Message: fmt.Sprintf("derivation is partial over pii attribute %q: its form/table derivation "+
						"lacks a vault route (KT-A3) — the value has no sealed home", f.Name),
					Fix: "derive a vault route for every pii field (the value must never reach a base or history column)",
				})
			}
		}
		// Parity: the emitted derived-artifact passes must equal the required ten
		// (ADR-10 §4). A suppressed pass ⇒ the declaration ≢ its derived artifacts.
		emitted := setOf(rp.EmittedPasses)
		for _, want := range requiredPasses {
			if !emitted[want] {
				diags = append(diags, Diagnostic{
					StageOrVerifier: "V6", Code: "DERIVE_PARITY", Severity: "error",
					Subject: rp.Decl.CatalogName, Loc: Loc{DefHash: rp.Decl.DefHash},
					Message: fmt.Sprintf("derivation parity failure: the declared resource emits %d/%d artifacts — "+
						"pass %q is missing, so the declaration is not equal to its derived artifacts",
						len(rp.EmittedPasses), len(requiredPasses), want),
					Fix: "every resource must emit exactly the ten ADR-10 §4 derivation passes",
				})
			}
		}
	}
	// MUTANT V6_ALLOW_DESTRUCTIVE (ADR-07 §5 dir-ii): skipping the destructive-DDL
	// sweep lets an inline DROP/rewrite past the additive-only boundary.
	destructiveOK := mutants.Active("V6_ALLOW_DESTRUCTIVE")
	for _, rp := range plan.Resources {
		if destructiveOK {
			break
		}
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
