package lower

import (
	"sort"
	"strings"

	shimast "github.com/microsoft/typescript-go/shim/ast"
	shimscanner "github.com/microsoft/typescript-go/shim/scanner"
	"regel.dev/regel/internal/rast"
	"regel.dev/regel/internal/tsx"
)

// sibPlaceholder marks an unresolved same-module sibling reference inside a
// KRef/TCatRef Str until hashes exist (patched in topological order). The NUL
// prefix cannot occur in a real r1_ address.
const sibPlaceholder = "\x00sib\x00"

// importBinding is one named import in scope: `import { name } from "module"`.
type importBinding struct {
	localName string
	module    string
	name      string // exported name in the source module
	hash      string
}

// declInfo is one top-level declaration being lowered.
type declInfo struct {
	name      string
	exported  bool
	kind      rast.DefKind
	node      *shimast.Node
	async     bool // syntactically async (floating-promise approximation)
	body      *rast.Node
	deps      map[string]rast.Dep // key: hash or sibling placeholder
	sibs      map[string]bool     // sibling decl names referenced
	docstring string
	comments  map[string]string
}

// lowerModule is the whole ADR-01 §4 step 1–3 pipeline for one module.
func lowerModule(source string, ctx ModuleContext) Result {
	fileName := "/" + ctx.ModuleName + ".ts"
	pr, err := tsx.Parse(fileName, source)
	if err != nil || pr.SourceFile == nil {
		return Result{Diagnostics: []Diagnostic{diag(CodeParseError, "parse failed", 0, 0)}}
	}

	l := &lowerer{
		ctx:      ctx,
		sf:       pr.SourceFile,
		text:     pr.SourceFile.Text(),
		factory:  shimast.NewNodeFactory(shimast.NodeFactoryHooks{}),
		imports:  map[string]importBinding{},
		siblings: map[string]*declInfo{},
	}

	// Parse errors reject first (step 1).
	for _, d := range pr.Diagnostics {
		l.diags = append(l.diags, diagf(CodeParseError, d.Line, d.Col, "%s", d.Message))
	}
	if len(l.diags) > 0 {
		return Result{Diagnostics: l.diags}
	}

	// PARSE_DEPTH is the gate's first check (STAGE-A-PLAN pin #9; the tsx seam
	// measures with an explicit heap stack, never recursion).
	if _, exceeded := tsx.CheckParseDepth(pr.SourceFile); exceeded {
		return Result{Diagnostics: []Diagnostic{diagf(CodeParseDepth, 1, 1,
			"syntactic nesting exceeds the gate ceiling of %d", tsx.MaxParseDepth)}}
	}

	// Module walk: imports + declaration roster (pre-pass gives hoisting and the
	// async map for the floating-promise approximation).
	var order []*declInfo
	for _, st := range pr.SourceFile.Statements.Nodes {
		if infos := l.moduleStatement(st); infos != nil {
			for _, di := range infos {
				if prev := l.siblings[di.name]; prev != nil {
					l.errorAt(st, CodeBadModule, "duplicate top-level name %q", di.name)
					continue
				}
				l.siblings[di.name] = di
				order = append(order, di)
			}
		}
	}

	// Lower each declaration body.
	for _, di := range order {
		l.lowerDecl(di)
	}

	// Acyclicity (ADR-01 §3): mutual recursion across definitions is DEP_CYCLE;
	// self-recursion lowered to SelfRef never appears as a sibling edge.
	topo, cyc := topoOrder(order)
	if cyc != nil {
		l.diags = append(l.diags, diagf(CodeDepCycle, 1, 1,
			"dependency cycle across definitions: %s", strings.Join(cyc, " -> ")))
	}

	// R1 capture verdicts (reassignment is known only after the whole module).
	l.flushCaptureDiags()

	if len(l.diags) > 0 {
		sortDiags(l.diags)
		return Result{Diagnostics: l.diags}
	}

	// Hash in topological order, patching sibling placeholders with real
	// addresses (the Merkle substitution), then normalize + finalize sidecars.
	hashes := map[string]string{}
	defs := make([]Definition, 0, len(order))
	byName := map[string]*Definition{}
	for _, di := range topo {
		patchSiblings(di.body, hashes)
		normalized := rast.Normalize(di.body)
		displayNames, typeNames := finalizeSidecars(normalized)
		deps := make([]rast.Dep, 0, len(di.deps))
		for key, d := range di.deps {
			if strings.HasPrefix(key, sibPlaceholder) {
				d.Hash = hashes[strings.TrimPrefix(key, sibPlaceholder)]
			}
			deps = append(deps, d)
		}
		sort.Slice(deps, func(i, j int) bool {
			if deps[i].Module != deps[j].Module {
				return deps[i].Module < deps[j].Module
			}
			return deps[i].Name < deps[j].Name
		})
		addr := rast.Address(normalized)
		hashes[di.name] = addr
		defs = append(defs, Definition{
			Name:         di.name,
			Exported:     di.exported,
			Kind:         di.kind,
			Body:         normalized,
			Hash:         addr,
			Deps:         deps,
			DisplayNames: displayNames,
			TypeNames:    typeNames,
			Docstring:    di.docstring,
			Comments:     di.comments,
		})
		byName[di.name] = &defs[len(defs)-1]
	}

	// Return in source order.
	out := make([]Definition, 0, len(order))
	for _, di := range order {
		out = append(out, *byName[di.name])
	}
	return Result{Definitions: out}
}

// patchSiblings replaces sibling placeholders in KRef/TCatRef nodes with the
// referents' now-known addresses (must run before Normalize/Address).
func patchSiblings(n *rast.Node, hashes map[string]string) {
	if n == nil {
		return
	}
	if (n.Kind == rast.KRef || n.Kind == rast.TCatRef) && strings.HasPrefix(n.Str, sibPlaceholder) {
		if h, ok := hashes[strings.TrimPrefix(n.Str, sibPlaceholder)]; ok {
			n.Str = h
		}
	}
	for _, c := range n.Kids {
		patchSiblings(c, hashes)
	}
}

// finalizeSidecars strips the temporary display names lowering parked in
// KBindId/TParam Str fields into the pre-order sidecars the printer consumes
// (the fields are not encoded for those kinds, but Equal/Print read them).
// MUST run on the NORMALIZED tree so sidecar order matches the printer's
// pre-order walk over the same (sorted) tree.
func finalizeSidecars(n *rast.Node) (displayNames, typeNames []string) {
	var walk func(*rast.Node)
	walk = func(m *rast.Node) {
		if m == nil {
			return
		}
		switch m.Kind {
		case rast.KBindId:
			displayNames = append(displayNames, m.Str)
			m.Str = ""
		case rast.TParam:
			typeNames = append(typeNames, m.Str)
			m.Str = ""
		}
		for _, c := range m.Kids {
			walk(c)
		}
	}
	walk(n)
	return displayNames, typeNames
}

// topoOrder orders decls so every sibling dependency precedes its dependent;
// a cycle returns (nil, cyclePath).
func topoOrder(order []*declInfo) ([]*declInfo, []string) {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	byName := map[string]*declInfo{}
	for _, di := range order {
		byName[di.name] = di
	}
	var out []*declInfo
	var cyc []string
	var visit func(di *declInfo, path []string) bool
	visit = func(di *declInfo, path []string) bool {
		switch color[di.name] {
		case black:
			return true
		case gray:
			// Cycle: slice the path from the first occurrence of this name.
			for i, nm := range path {
				if nm == di.name {
					cyc = append(append([]string{}, path[i:]...), di.name)
					return false
				}
			}
			cyc = append(append([]string{}, path...), di.name)
			return false
		}
		color[di.name] = gray
		sibNames := make([]string, 0, len(di.sibs))
		for nm := range di.sibs {
			sibNames = append(sibNames, nm)
		}
		sort.Strings(sibNames)
		for _, nm := range sibNames {
			dep := byName[nm]
			if dep == nil {
				continue
			}
			if !visit(dep, append(path, di.name)) {
				return false
			}
		}
		color[di.name] = black
		out = append(out, di)
		return true
	}
	for _, di := range order {
		if !visit(di, nil) {
			return nil, cyc
		}
	}
	return out, nil
}

func sortDiags(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Col != b.Col {
			return a.Col < b.Col
		}
		return a.Code < b.Code
	})
}

// posOf converts a node position to a 1-based line/col pair.
func (l *lowerer) posOf(n *shimast.Node) (int, int) {
	if n == nil {
		return 0, 0
	}
	pos := skipTriviaPos(l.text, n.Pos())
	line, col := shimscanner.GetECMALineAndUTF16CharacterOfPosition(l.sf, pos)
	return line + 1, int(col) + 1
}

// skipTriviaPos advances past whitespace so diagnostics point at the token, not
// its leading trivia (Node.Pos is the full start).
func skipTriviaPos(text string, pos int) int {
	for pos < len(text) {
		switch text[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		default:
			return pos
		}
	}
	return pos
}
