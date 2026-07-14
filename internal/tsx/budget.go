package tsx

import (
	"context"
	"time"

	shimast "github.com/microsoft/typescript-go/shim/ast"
	shimscanner "github.com/microsoft/typescript-go/shim/scanner"
)

// budget.go realizes the ADR-07 §3 typecheck budget at the owned tsx seam.
//
// # The deterministic type-graph node ceiling (primary control)
//
// ADR-07 §3 asks for "a deterministic node ceiling on the instantiated type
// graph" whose breach is byte-identical on any machine. The vendored tsgo
// checker instantiates that graph internally and caps it with its own
// instantiation-depth / instantiation-count limits (surfacing TS2589), but those
// limits are NOT reachable through the shim without editing fork internals, which
// the vendoring contract forbids (zero edits). Per the ADR-07 §3 BUILD-C escape
// hatch, the ceiling is realized at the nearest owned seam: a DETERMINISTIC
// pre-check on the type-level syntactic weight of the submitted patch, run over
// the parsed tsgo tree BEFORE the checker constructs the instantiated graph.
//
// The submitted type syntax is the pre-image of the instantiated graph: a nest of
// N conditional/mapped/indexed type nodes instantiates a graph at least N deep, so
// bounding the submitted type-node nesting depth (and total type-node count)
// bounds the graph the checker would build. The measurement is a pure function of
// the parsed tree — the same submission yields the same breach on any kernel,
// independent of load, clock, or checker state — so it satisfies the ADR's
// determinism requirement where the checker's own load-sensitive cutoff would not.
//
// A conditional-type bomb (a deeply-nested `T extends U ? … : …` chain) is thus a
// rejected patch with a `TYPECHECK_BUDGET` diagnostic naming the offending site,
// never a stalled gate or a checker blow-up — the same input, the same verdict.
//
// # The wall-clock deadline (secondary backstop)
//
// TypecheckWithDeadline runs the full checker under a wall-clock deadline. This is
// a liveness backstop only (the deterministic ceiling above is the real defense);
// a breach aborts the attempt cleanly and the caller rejects with
// `TYPECHECK_TIMEOUT`. Serving traffic is untouched — the checker holds no locks.

const (
	// TypeGraphDepthCeiling bounds the nesting depth of consecutive type-syntax
	// nodes in a submission (ADR-07 §3, BUILD-C owned-seam realization). A
	// 200-deep conditional-type bomb (depth 200) breaches it; ordinary generic
	// annotations sit in the low single/double digits, far below. Gate-fixed for
	// M0/M1 — lowering it can only tighten what an accepted submission may nest.
	TypeGraphDepthCeiling = 64

	// TypeGraphNodeCeiling bounds the TOTAL type-syntax node count of a
	// submission — the breadth companion to the depth ceiling, so a wide (rather
	// than deep) type-graph bomb is refused deterministically too.
	TypeGraphNodeCeiling = 4096

	// DefaultTypecheckWall is the liveness backstop deadline (ADR-07 §3 secondary
	// control). Generous: the deterministic ceiling refuses a bomb long before a
	// well-formed submission approaches this.
	DefaultTypecheckWall = 5 * time.Second
)

// BudgetBreach describes a deterministic type-graph budget breach. Site is the
// world path + 1-based line:col of the offending type node; Kind is "depth" or
// "count"; Measured/Ceiling are the breached quantity and its bound.
type BudgetBreach struct {
	Kind     string // "depth" | "count"
	Site     string // "<file>:<line>:<col>"
	Measured int
	Ceiling  int
}

// isTypeSyntaxKind reports whether a node kind is a type-level construct whose
// nesting contributes to the instantiated type graph. Conditional, mapped, and
// indexed-access types are the recursion-bearing forms a bomb is built from;
// union/intersection/reference/array/tuple/operator round out the type surface.
func isTypeSyntaxKind(k shimast.Kind) bool {
	switch k {
	case shimast.KindConditionalType,
		shimast.KindMappedType,
		shimast.KindIndexedAccessType,
		shimast.KindUnionType,
		shimast.KindIntersectionType,
		shimast.KindTypeReference,
		shimast.KindArrayType,
		shimast.KindTupleType,
		shimast.KindTypeOperator,
		shimast.KindTypeLiteral,
		shimast.KindFunctionType,
		shimast.KindConstructorType,
		shimast.KindInferType,
		shimast.KindTemplateLiteralType,
		shimast.KindImportType,
		shimast.KindTypeQuery,
		shimast.KindOptionalType,
		shimast.KindRestType,
		shimast.KindNamedTupleMember:
		return true
	default:
		return false
	}
}

// CheckTypeGraphBudget walks the parsed tree with an EXPLICIT heap stack (never
// native recursion, so measuring a pathological tree cannot itself overflow the
// Go stack) and returns the first budget breach, if any. The walk is a pure
// function of the tree: same tree ⇒ same breach.
//
// Depth is the length of the longest chain of consecutive type-syntax nodes
// (a bomb's conditional nest); count is the total number of type-syntax nodes.
// The offending site for a depth breach is the deepest type node on the chain;
// for a count breach it is the last type node counted.
func CheckTypeGraphBudget(sf *shimast.SourceFile) *BudgetBreach {
	if sf == nil {
		return nil
	}
	type item struct {
		n         *shimast.Node
		typeDepth int
	}
	stack := []item{{sf.AsNode(), 0}}
	total := 0
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		childDepth := 0
		if isTypeSyntaxKind(it.n.Kind) {
			total++
			childDepth = it.typeDepth + 1
			if childDepth > TypeGraphDepthCeiling {
				return &BudgetBreach{
					Kind: "depth", Site: nodeSite(sf, it.n),
					Measured: childDepth, Ceiling: TypeGraphDepthCeiling,
				}
			}
			if total > TypeGraphNodeCeiling {
				return &BudgetBreach{
					Kind: "count", Site: nodeSite(sf, it.n),
					Measured: total, Ceiling: TypeGraphNodeCeiling,
				}
			}
		}
		it.n.ForEachChild(func(c *shimast.Node) bool {
			stack = append(stack, item{c, childDepth})
			return false
		})
	}
	return nil
}

// nodeSite formats a node's world path + 1-based line:col from the source file's
// line map (the same coordinate system as diagnostics).
func nodeSite(sf *shimast.SourceFile, n *shimast.Node) string {
	line, char := shimscanner.GetECMALineAndUTF16CharacterOfPosition(sf, n.Pos())
	return sf.FileName() + ":" + itoa(line+1) + ":" + itoa(int(char)+1)
}

// TypecheckWithDeadline runs Typecheck under a wall-clock deadline (ADR-07 §3
// secondary backstop). It returns (result, timedOut, err). On timeout the
// checker goroutine is abandoned (a liveness backstop, not the primary control:
// the deterministic ceiling refuses a bomb before this ever fires) and the
// caller rejects with TYPECHECK_TIMEOUT. wall <= 0 uses DefaultTypecheckWall.
func TypecheckWithDeadline(req CheckRequest, wall time.Duration) (CheckResult, bool, error) {
	if wall <= 0 {
		wall = DefaultTypecheckWall
	}
	type res struct {
		out CheckResult
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := Typecheck(req)
		ch <- res{out, err}
	}()
	timer := time.NewTimer(wall)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.out, false, r.err
	case <-timer.C:
		return CheckResult{}, true, context.DeadlineExceeded
	}
}

// itoa is a tiny local int→string (avoids importing strconv into the seam).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
