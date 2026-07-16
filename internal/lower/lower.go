// Package lower is the tsgo-AST → regel-AST default-deny lowering plus the
// ADR-01 §4 grammar gate (step 2 and step 3 of the admission pipeline). It is
// the only bridge between the vendored parser's tree and the owned identity
// core (internal/rast).
//
// Default-deny (ADR-01 §4 step 2): a tsgo node kind with no production here
// rejects with the stable code LOWER_UNSUPPORTED naming the kind — new syntax
// in a future tsgo fails closed.
//
// The grammar gate (step 3) enforces every §2 ban with its own stable BAN_*
// code (STAGE-A-PLAN pin #9), the switch discipline, the floating-promise
// check, acyclicity across definitions, and capture rule R1. Every rejection
// carries the std replacement in the message ("fix in the error").
//
// # Stage-A approximations (named residues)
//
//   - FLOATING_PROMISE is syntactic: a bare call expression statement whose
//     callee resolves to a local binding or same-module declaration that is
//     syntactically async. The full type-driven Promise-typed-expression check
//     needs checker integration (Stage B).
//   - Capture rules R2/R3 (serializable lattice) are the verifier V5 seam
//     (Stage B); R1 is enforced fully here, R4/R5 hold structurally (this-ban,
//     zero ambient globals).
//   - Comments are captured for statements (node-path-keyed); expression-level
//     comments are dropped (ADR-02 §2 declares comment anchoring best-effort).
package lower

import (
	"regel.dev/regel/internal/rast"
)

// Resolver resolves a qualified import target — "module/path.exportedName",
// e.g. "std/mail.send" — to the referent's content address.
type Resolver func(name string) (hash string, ok bool)

// ModuleContext is the module-level input to lowering.
type ModuleContext struct {
	// ModuleName is the catalog module path, e.g. "app/crm/deal" (no extension).
	ModuleName string
	// Resolve resolves imported names against the catalog. Nil means nothing
	// resolves (every import is IMPORT_UNRESOLVED).
	Resolve Resolver
}

// Definition is one lowered top-level declaration: the unit that becomes an
// immortal catalog row (ADR-03). Body is the normalized regel-AST; Hash its
// ADR-02 address.
type Definition struct {
	Name     string
	Exported bool
	Kind     rast.DefKind
	Body     *rast.Node
	Hash     string
	// Deps are the resolved references (imports + same-module siblings),
	// sorted by (Module, Name); the printer regenerates imports from them.
	Deps []rast.Dep
	// DisplayNames / TypeNames are the out-of-hash binder-name sidecars,
	// pre-order DFS over Body (ADR-02 §2 De Bruijn normalization).
	DisplayNames []string
	TypeNames    []string
	// Docstring is the leading /** … */ JSDoc block (ADR-02 §2).
	Docstring string
	// Comments maps a Body node path ("3/0/2", child indices) to the comment
	// text stripped at that statement. Best-effort metadata.
	Comments map[string]string
}

// Result is the outcome of lowering one module: either Definitions (green) or
// Diagnostics (red). Both non-empty never happens: any error diagnostic means
// zero definitions (fail closed, no partial admit).
type Result struct {
	Definitions []Definition
	Diagnostics []Diagnostic
}

// OK reports whether the module lowered cleanly.
func (r Result) OK() bool { return len(r.Diagnostics) == 0 }

// CanonicalText renders a definition's canonical text projection (ADR-02 §1)
// from its lowered parts.
func CanonicalText(d Definition) string {
	return rast.PrintModule(rast.PrintInput{
		Body:         d.Body,
		Name:         d.Name,
		Exported:     d.Exported,
		Kind:         d.Kind,
		DisplayNames: d.DisplayNames,
		TypeNames:    d.TypeNames,
		Deps:         d.Deps,
	})
}

// Module parses and lowers one module source to its definitions, running the
// full grammar gate. fileName is diagnostic-only ("/app/x.ts" style).
func Module(source string, ctx ModuleContext) Result {
	return lowerModule(source, ctx)
}

// depKey identifies a dependency EDGE by its nominal import identity (module +
// local exported name), NOT by its content hash. Every std TYPE shares the
// opaque `unknown` genesis body (internal/admission/image.go), so distinct std
// types collide on their content hash by design; a deps map keyed by hash would
// collapse two distinct import edges into one and silently DROP the other
// (STAGE-D §13.11 residue #11 — a dropped Vault/Conn edge blinds V2/V5). Keying
// by (module, name) keeps every distinct edge. The NUL separator cannot occur in
// a module path or ASCII identifier, and an import key never begins with
// sibPlaceholder, so import keys and sibling-placeholder keys never collide.
func depKey(module, name string) string { return module + "\x00" + name }
