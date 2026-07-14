package admission

import (
	"encoding/json"

	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/mutants"
	"regel.dev/regel/internal/rast"
)

// contracts.go extracts pre/post contract combinators from a definition body and
// backs V4 (purity check) + the derivation seam's boundary-validator artifact and
// the definition.contracts mirror column (ADR-02 §3: contracts are subset code in
// the body, additionally mirrored for the verifier).

// contractClause is one pre/post call site found in a definition body.
type contractClause struct {
	Kind   string     `json:"kind"` // "pre" | "post"
	clause *rast.Node // the predicate argument (not serialized)
}

// findContractClauses returns every std/contract.pre / .post call in a function
// body, in source order, with its predicate argument.
func findContractClauses(d lower.Definition, im *Image) []contractClause {
	_, _, body, ok := funcParts(d)
	if !ok || body == nil {
		return nil
	}
	var out []contractClause
	var walk func(*rast.Node)
	walk = func(n *rast.Node) {
		if n == nil {
			return
		}
		if n.Kind == rast.KCall && len(n.Kids) >= 2 {
			switch stdIntrinsicOf(n.Kids[0], im) {
			case "std/contract.pre":
				out = append(out, contractClause{Kind: "pre", clause: firstArg(n)})
			case "std/contract.post":
				out = append(out, contractClause{Kind: "post", clause: firstArg(n)})
			}
		}
		for _, k := range n.Kids {
			walk(k)
		}
	}
	walk(body)
	return out
}

func firstArg(call *rast.Node) *rast.Node {
	if len(call.Kids) >= 2 && call.Kids[1] != nil && len(call.Kids[1].Kids) > 0 {
		return call.Kids[1].Kids[0]
	}
	return nil
}

// contractsMirror renders the contract clauses for the definition.contracts jsonb
// column ([] when a definition declares none).
func contractsMirror(d lower.Definition, im *Image) string {
	clauses := findContractClauses(d, im)
	if len(clauses) == 0 {
		return "[]"
	}
	b, err := json.Marshal(clauses)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// verifyV4Def checks every contract clause in one definition is pure and
// references only in-scope symbols (ADR-07 §4 V4). Effectfulness (a named
// capability) takes precedence over malformation (a governance / out-of-scope
// symbol), so a clause that is both reports the deeper failure.
func verifyV4Def(ld loweredDef, im *Image) []Diagnostic {
	var diags []Diagnostic
	for _, c := range findContractClauses(ld.Def, im) {
		effectful, malformed, sym := classifyClause(c.clause, im)
		// MUTANT V4_ALLOW_EFFECTFUL (ADR-07 §5 dir-ii): allowing an effectful clause
		// lets a capability run inside what must be a pure boundary predicate.
		if effectful && mutants.Active("V4_ALLOW_EFFECTFUL") {
			effectful = false
		}
		switch {
		case effectful:
			diags = append(diags, Diagnostic{
				StageOrVerifier: "V4", Code: "CONTRACT_EFFECTFUL", Severity: "error",
				Subject: ld.CatalogName, Loc: Loc{DefHash: ld.Def.Hash},
				Message: "a " + c.Kind + "condition clause names the capability " + sym +
					"; contract clauses must be pure (no effect, no capability)",
				Fix: "remove the capability call from the contract clause — a contract is a pure predicate over the definition's values",
			})
		case malformed:
			diags = append(diags, Diagnostic{
				StageOrVerifier: "V4", Code: "CONTRACT_MALFORMED", Severity: "error",
				Subject: ld.CatalogName, Loc: Loc{DefHash: ld.Def.Hash},
				Message: "a " + c.Kind + "condition clause references the out-of-scope symbol " + sym +
					"; a contract may name only its parameters, locals, literals, and pure functions",
				Fix: "reference only in-scope pure symbols in the contract clause",
			})
		}
	}
	return diags
}

// classifyClause walks a contract predicate for the first capability-bearing
// reference (effectful) or governance / out-of-scope reference (malformed).
func classifyClause(clause *rast.Node, im *Image) (effectful, malformed bool, symbol string) {
	var effSym, malSym string
	var walk func(*rast.Node)
	walk = func(n *rast.Node) {
		if n == nil {
			return
		}
		if n.Kind == rast.KRef {
			if cap, ok := im.CapabilityByHash[n.Str]; ok && effSym == "" {
				effSym = cap
			} else if e := im.ByHash[n.Str]; e != nil && malSym == "" {
				if isGovernanceIntrinsic(e.Intrinsic) {
					malSym = e.Export
				}
			}
		}
		for _, k := range n.Kids {
			walk(k)
		}
	}
	walk(clause)
	if effSym != "" {
		return true, false, effSym
	}
	if malSym != "" {
		return false, true, malSym
	}
	return false, false, ""
}

// isGovernanceIntrinsic reports whether an intrinsic names a governance / effect
// binding that must never appear inside a pure contract clause.
func isGovernanceIntrinsic(intrinsic string) bool {
	for _, p := range []string{"std/policy.", "std/resource.", "std/sql."} {
		if len(intrinsic) >= len(p) && intrinsic[:len(p)] == p {
			return true
		}
	}
	return false
}
