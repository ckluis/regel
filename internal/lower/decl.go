package lower

import (
	"strings"

	shimast "github.com/microsoft/typescript-go/shim/ast"
	shimscanner "github.com/microsoft/typescript-go/shim/scanner"
	"regel.dev/regel/internal/rast"
)

// lowerer carries the per-module lowering state.
type lowerer struct {
	ctx     ModuleContext
	sf      *shimast.SourceFile
	text    string
	factory *shimast.NodeFactory

	imports  map[string]importBinding
	siblings map[string]*declInfo

	diags []Diagnostic

	// per-declaration state
	cur       *declInfo
	scopes    []*scopeEntry
	typeScope []string
	funcDepth int
	captures  []captureEvent
}

// scopeEntry is one value binder in scope (a De Bruijn slot).
type scopeEntry struct {
	name       string
	isConst    bool
	funcDepth  int
	reassigned bool
	isAsyncFn  bool
}

// captureEvent records a closure capturing a non-const binder; it becomes
// CAPTURE_LET iff the binder is (or later becomes) reassigned (ADR-01 R1:
// "reassigned anywhere in an enclosing function").
type captureEvent struct {
	entry *scopeEntry
	name  string
	line  int
	col   int
}

func (l *lowerer) errorAt(n *shimast.Node, code string, format string, a ...any) {
	line, col := l.posOf(n)
	l.diags = append(l.diags, diagf(code, line, col, format, a...))
	if l.cur != nil {
		i := len(l.diags) - 1
		l.diags[i].Subject = l.cur.name
	}
}

func (l *lowerer) flushCaptureDiags() {
	for _, ev := range l.captures {
		if ev.entry.reassigned {
			l.diags = append(l.diags, diagf(CodeCaptureLet, ev.line, ev.col,
				"closure captures reassigned `let` %q (R1: const-only capture)", ev.name))
		}
	}
}

// --- module-level statements ---

// moduleStatement classifies one top-level statement: nil means it produced no
// declarations (import consumed, or a diagnostic was emitted).
func (l *lowerer) moduleStatement(st *shimast.Node) []*declInfo {
	switch st.Kind {
	case shimast.KindImportDeclaration:
		l.importDecl(st)
		return nil
	case shimast.KindFunctionDeclaration:
		return l.functionDeclInfo(st)
	case shimast.KindVariableStatement:
		return l.varStatementInfo(st)
	case shimast.KindInterfaceDeclaration:
		if di := l.namedDeclInfo(st, rast.DefInterface); di != nil {
			return []*declInfo{di}
		}
		return nil
	case shimast.KindTypeAliasDeclaration:
		if di := l.namedDeclInfo(st, rast.DefType); di != nil {
			return []*declInfo{di}
		}
		return nil
	case shimast.KindClassDeclaration, shimast.KindClassExpression:
		l.checkDecorators(st)
		l.errorAt(st, CodeBanClass, "`class` is banned: data is shapes, behavior is functions")
		return nil
	case shimast.KindEnumDeclaration:
		l.errorAt(st, CodeBanEnum, "`enum` is banned: use a string-literal union")
		return nil
	case shimast.KindModuleDeclaration:
		l.errorAt(st, CodeBanNamespace, "`namespace`/`module` is banned: modules are files, files become rows")
		return nil
	case shimast.KindEmptyStatement:
		return nil
	case shimast.KindExportDeclaration, shimast.KindExportAssignment:
		l.errorAt(st, CodeBadModule, "re-export/`export =`/`export default` not admitted: export declarations directly")
		return nil
	default:
		l.errorAt(st, CodeBadModule,
			"top-level %s not admitted: a module is import/const/function/type/interface declarations", st.Kind.String())
		return nil
	}
}

func (l *lowerer) importDecl(st *shimast.Node) {
	imp := st.AsImportDeclaration()
	spec, ok := moduleSpecifierText(imp.ModuleSpecifier)
	if !ok {
		l.errorAt(st, CodeBadModule, "import specifier must be a string literal")
		return
	}
	if !strings.HasPrefix(spec, "std/") && !strings.HasPrefix(spec, "app/") {
		l.errorAt(st, CodeUnresolvedImport, "import %q outside std/ and app/ (ADR-01 §3: the closed world)", spec)
		return
	}
	if imp.ImportClause == nil {
		l.errorAt(st, CodeBadModule, "bare side-effect import not admitted")
		return
	}
	cl := imp.ImportClause.AsImportClause()
	if cl.Name() != nil {
		l.errorAt(st, CodeBadModule, "default import not admitted: use named imports")
		return
	}
	if cl.NamedBindings == nil || cl.NamedBindings.Kind != shimast.KindNamedImports {
		l.errorAt(st, CodeBadModule, "namespace import not admitted: use named imports")
		return
	}
	for _, el := range cl.NamedBindings.AsNamedImports().Elements.Nodes {
		is := el.AsImportSpecifier()
		local := is.Name().Text()
		imported := local
		if is.PropertyName != nil {
			imported = is.PropertyName.Text()
		}
		if !asciiIdent(local) || !asciiIdent(imported) {
			l.errorAt(el, CodeBanNonASCIIIdent, "import name %q is not an ASCII identifier", local)
			continue
		}
		qualified := spec + "." + imported
		var hash string
		if l.ctx.Resolve != nil {
			if h, ok := l.ctx.Resolve(qualified); ok {
				hash = h
			}
		}
		if hash == "" {
			l.errorAt(el, CodeUnresolvedImport, "import %q does not resolve in the catalog", qualified)
			continue
		}
		l.imports[local] = importBinding{localName: local, module: spec, name: imported, hash: hash}
	}
}

func moduleSpecifierText(n *shimast.Node) (string, bool) {
	if n == nil || n.Kind != shimast.KindStringLiteral {
		return "", false
	}
	return n.Text(), true
}

func (l *lowerer) functionDeclInfo(st *shimast.Node) []*declInfo {
	fd := st.AsFunctionDeclaration()
	l.checkDecorators(st)
	mods := st.ModifierFlags()
	if mods&shimast.ModifierFlagsAmbient != 0 {
		l.errorAt(st, CodeBanNamespace, "`declare` (ambient) is banned: modules are files, files become rows")
		return nil
	}
	if mods&shimast.ModifierFlagsDefault != 0 {
		l.errorAt(st, CodeBadModule, "`export default` not admitted: use a named export")
		return nil
	}
	if fd.Name() == nil {
		l.errorAt(st, CodeBadModule, "top-level function must be named")
		return nil
	}
	name := fd.Name().Text()
	if !asciiIdent(name) {
		l.errorAt(fd.Name(), CodeBanNonASCIIIdent, "identifier %q is not ASCII", name)
		return nil
	}
	if fd.BodyData().AsteriskToken != nil {
		l.errorAt(st, CodeBanGenerator, "generators are banned: one suspension surface (std Iter<T>)")
		return nil
	}
	return []*declInfo{{
		name:     name,
		exported: mods&shimast.ModifierFlagsExport != 0,
		kind:     rast.DefFunc,
		node:     st,
		async:    mods&shimast.ModifierFlagsAsync != 0,
		deps:     map[string]rast.Dep{},
		sibs:     map[string]bool{},
		comments: map[string]string{},
	}}
}

func (l *lowerer) varStatementInfo(st *shimast.Node) []*declInfo {
	vs := st.AsVariableStatement()
	mods := st.ModifierFlags()
	if mods&shimast.ModifierFlagsAmbient != 0 {
		l.errorAt(st, CodeBanNamespace, "`declare` (ambient) is banned: modules are files, files become rows")
		return nil
	}
	lst := vs.DeclarationList.AsVariableDeclarationList()
	flags := vs.DeclarationList.Flags
	if flags&(shimast.NodeFlagsLet|shimast.NodeFlagsConst) == 0 {
		l.errorAt(st, CodeBanVar, "`var` is banned: use `const` (or `let` inside functions)")
		return nil
	}
	if flags&shimast.NodeFlagsConst == 0 {
		l.errorAt(st, CodeBadModule, "top-level `let` not admitted: definitions are immutable, use `const`")
		return nil
	}
	var out []*declInfo
	for _, d := range lst.Declarations.Nodes {
		vd := d.AsVariableDeclaration()
		nameNode := vd.Name()
		if nameNode == nil || nameNode.Kind != shimast.KindIdentifier {
			l.errorAt(d, CodeBadModule, "top-level const must bind a plain identifier (no destructuring)")
			continue
		}
		name := nameNode.Text()
		if !asciiIdent(name) {
			l.errorAt(nameNode, CodeBanNonASCIIIdent, "identifier %q is not ASCII", name)
			continue
		}
		if vd.Initializer == nil {
			l.errorAt(d, CodeBadModule, "top-level const needs an initializer")
			continue
		}
		out = append(out, &declInfo{
			name:     name,
			exported: mods&shimast.ModifierFlagsExport != 0,
			kind:     rast.DefValue,
			node:     d,
			async:    isAsyncFunctionExpr(vd.Initializer),
			deps:     map[string]rast.Dep{},
			sibs:     map[string]bool{},
			comments: map[string]string{},
		})
	}
	return out
}

func (l *lowerer) namedDeclInfo(st *shimast.Node, kind rast.DefKind) *declInfo {
	mods := st.ModifierFlags()
	if mods&shimast.ModifierFlagsAmbient != 0 {
		l.errorAt(st, CodeBanNamespace, "`declare` (ambient) is banned")
		return nil
	}
	nameNode := st.Name()
	if nameNode == nil {
		l.errorAt(st, CodeBadModule, "declaration must be named")
		return nil
	}
	name := nameNode.Text()
	if !asciiIdent(name) {
		l.errorAt(nameNode, CodeBanNonASCIIIdent, "identifier %q is not ASCII", name)
		return nil
	}
	return &declInfo{
		name:     name,
		exported: mods&shimast.ModifierFlagsExport != 0,
		kind:     kind,
		node:     st,
		deps:     map[string]rast.Dep{},
		sibs:     map[string]bool{},
		comments: map[string]string{},
	}
}

func isAsyncFunctionExpr(n *shimast.Node) bool {
	n = unparen(n)
	if n == nil {
		return false
	}
	if n.Kind == shimast.KindArrowFunction || n.Kind == shimast.KindFunctionExpression {
		return n.ModifierFlags()&shimast.ModifierFlagsAsync != 0
	}
	return false
}

func unparen(n *shimast.Node) *shimast.Node {
	for n != nil && n.Kind == shimast.KindParenthesizedExpression {
		n = n.AsParenthesizedExpression().Expression
	}
	return n
}

// checkDecorators emits BAN_DECORATOR for any decorator modifier.
func (l *lowerer) checkDecorators(st *shimast.Node) {
	for _, dec := range st.Decorators() {
		l.errorAt(dec, CodeBanDecorator, "decorators are banned: derivation is explicit AST passes")
	}
}

// --- per-declaration lowering ---

func (l *lowerer) lowerDecl(di *declInfo) {
	l.cur = di
	l.scopes = l.scopes[:0]
	l.typeScope = l.typeScope[:0]
	l.funcDepth = 0
	l.extractDocstring(di)

	switch di.kind {
	case rast.DefFunc:
		fd := di.node.AsFunctionDeclaration()
		di.body = l.lowerFunctionLike(di.node, fd.BodyData().Body, di.async, "")
	case rast.DefValue:
		vd := di.node.AsVariableDeclaration()
		init := vd.Initializer
		// The declared type annotation on a top-level const is part of the
		// definition: `const x: T = e` lowers as `e satisfies T`? No — one form:
		// the annotation, when present, is carried as a KSatisfy wrapper so the
		// type stays IN the hash and the printer round-trips it.
		body := l.expr(init, "")
		if vd.Type != nil {
			t := l.typ(vd.Type)
			body = &rast.Node{Kind: rast.KSatisfy, Kids: []*rast.Node{body, t}}
		}
		di.body = body
	case rast.DefType:
		ta := di.node.AsTypeAliasDeclaration()
		tps := l.typeParams(typeParamNodes(ta.TypeParameters))
		t := l.typ(ta.Type)
		l.popTypeParams(typeParamNodes(ta.TypeParameters))
		di.body = &rast.Node{Kind: rast.KTypeAlias, Kids: []*rast.Node{tps, t}}
	case rast.DefInterface:
		it := di.node.AsInterfaceDeclaration()
		if it.HeritageClauses != nil && len(it.HeritageClauses.Nodes) > 0 {
			l.errorAt(di.node, CodeLowerUnsupported, "interface `extends` has no production (Stage A): inline the members")
		}
		tps := l.typeParams(typeParamNodes(it.TypeParameters))
		members := l.typeMembers(it.Members.Nodes)
		l.popTypeParams(typeParamNodes(it.TypeParameters))
		di.body = &rast.Node{Kind: rast.KInterface, Kids: []*rast.Node{tps, members}}
	}
	if di.body == nil {
		di.body = &rast.Node{Kind: rast.KNone}
	}
	l.cur = nil
}

func typeParamNodes(lst *shimast.NodeList) []*shimast.Node {
	if lst == nil {
		return nil
	}
	return lst.Nodes
}

// lowerFunctionLike lowers a function declaration/expression/arrow to KFunc.
// path is the rast node path of the KFunc node itself ("" = definition root).
func (l *lowerer) lowerFunctionLike(fn *shimast.Node, body *shimast.Node, async bool, path string) *rast.Node {
	l.funcDepth++
	fl := fn.FunctionLikeData()

	tps := l.typeParams(typeParamNodes(fl.TypeParameters))

	var paramNodes []*rast.Node
	var pushed int
	if fl.Parameters != nil {
		for _, prm := range fl.Parameters.Nodes {
			pn, n := l.lowerParam(prm)
			paramNodes = append(paramNodes, pn)
			pushed += n
		}
	}

	var ret *rast.Node = noneNode()
	if fl.Type != nil {
		ret = l.typ(fl.Type)
	}

	var u uint64
	if async {
		u |= 1
	}
	var bodyNode *rast.Node
	if body == nil {
		l.errorAt(fn, CodeLowerUnsupported, "function overload signatures have no production (Stage A): use a union parameter type")
		bodyNode = noneNode()
	} else if body.Kind == shimast.KindBlock {
		bodyNode = l.block(body, childPath(path, 3))
	} else {
		u |= 2 // expression body
		bodyNode = l.expr(body, childPath(path, 3))
	}

	l.popValues(pushed)
	l.popTypeParams(typeParamNodes(fl.TypeParameters))
	l.funcDepth--

	return &rast.Node{Kind: rast.KFunc, U: u, Kids: []*rast.Node{
		listNode(paramNodes), tps, ret, bodyNode,
	}}
}

// lowerParam lowers one parameter; returns the KParam node and how many value
// binders it pushed (they stay in scope; caller pops).
func (l *lowerer) lowerParam(prm *shimast.Node) (*rast.Node, int) {
	pd := prm.AsParameterDeclaration()
	l.checkDecorators(prm)
	if prm.ModifierFlags() != shimast.ModifierFlagsNone {
		l.errorAt(prm, CodeBanClass, "parameter modifiers (parameter properties) are banned with classes")
	}
	var u uint64
	if pd.DotDotDotToken != nil {
		u |= 1
	}
	nameNode := prm.Name()
	if nameNode != nil && nameNode.Kind == shimast.KindIdentifier && nameNode.Text() == "this" {
		l.errorAt(prm, CodeBanThis, "`this` parameter/typing is banned: pass state as an explicit parameter")
	}
	// Pattern lowered first (defaults inside the pattern see the outer scope),
	// then its binders are pushed, then the type, then the default — exactly the
	// printer's discipline so De Bruijn indices round-trip.
	pat, binders := l.pattern(nameNode)
	n := l.pushBinders(binders, false)
	var typ *rast.Node = noneNode()
	if pd.Type != nil {
		typ = l.typ(pd.Type)
	}
	var def *rast.Node = noneNode()
	if pd.Initializer != nil {
		def = l.expr(pd.Initializer, "")
	}
	if pd.QuestionToken != nil {
		// `p?: T` — optionality is a type-level fact; normalize to `p: T |
		// undefined`? No: keep it out of Stage A (one form). Reject for now.
		l.errorAt(prm, CodeLowerUnsupported, "optional parameter `?` has no production (Stage A): use a default or `| undefined`")
	}
	return &rast.Node{Kind: rast.KParam, U: u, Kids: []*rast.Node{pat, typ, def}}, n
}

// binder is a named KBindId occurrence in pattern pre-order.
type binder struct {
	name    string
	node    *rast.Node
	isConst bool
}

// pushBinders pushes pattern binders (pre-order) as scope entries.
func (l *lowerer) pushBinders(bs []binder, isConst bool) int {
	for _, b := range bs {
		l.scopes = append(l.scopes, &scopeEntry{
			name:      b.name,
			isConst:   isConst || b.isConst,
			funcDepth: l.funcDepth,
		})
	}
	return len(bs)
}

func (l *lowerer) popValues(n int) {
	l.scopes = l.scopes[:len(l.scopes)-n]
}

// pattern lowers a binding name/pattern; binders are returned in pre-order and
// NOT yet pushed (defaults inside the pattern are lowered in the outer scope,
// matching the printer).
func (l *lowerer) pattern(n *shimast.Node) (*rast.Node, []binder) {
	if n == nil {
		return noneNode(), nil
	}
	switch n.Kind {
	case shimast.KindIdentifier:
		name := n.Text()
		if !asciiIdent(name) {
			l.errorAt(n, CodeBanNonASCIIIdent, "identifier %q is not ASCII", name)
			name = "_x"
		}
		bn := &rast.Node{Kind: rast.KBindId, Str: name}
		return bn, []binder{{name: name, node: bn}}
	case shimast.KindArrayBindingPattern:
		var elems []*rast.Node
		var binders []binder
		for _, el := range n.AsBindingPattern().Elements.Nodes {
			if el.Kind == shimast.KindOmittedExpression {
				elems = append(elems, noneNode())
				continue
			}
			en, eb := l.bindingElement(el)
			elems = append(elems, en)
			binders = append(binders, eb...)
		}
		return &rast.Node{Kind: rast.KArrayPat, Kids: []*rast.Node{listNode(elems)}}, binders
	case shimast.KindObjectBindingPattern:
		var props []*rast.Node
		var binders []binder
		for _, el := range n.AsBindingPattern().Elements.Nodes {
			en, eb := l.bindingElement(el)
			props = append(props, en)
			binders = append(binders, eb...)
		}
		return &rast.Node{Kind: rast.KObjPat, Kids: []*rast.Node{listNode(props)}}, binders
	default:
		l.errorAt(n, CodeLowerUnsupported, "binding pattern %s has no production", n.Kind.String())
		return noneNode(), nil
	}
}

// bindingElement lowers one element of a binding pattern.
func (l *lowerer) bindingElement(el *shimast.Node) (*rast.Node, []binder) {
	be := el.AsBindingElement()
	inner, binders := l.pattern(el.Name())
	if be.DotDotDotToken != nil {
		if be.Initializer != nil || be.PropertyName != nil {
			l.errorAt(el, CodeLowerUnsupported, "rest element cannot have a default or property name")
		}
		return &rast.Node{Kind: rast.KRestPat, Kids: []*rast.Node{inner}}, binders
	}
	var def *rast.Node = noneNode()
	if be.Initializer != nil {
		def = l.expr(be.Initializer, "")
	}
	if be.PropertyName != nil || parentIsObjectPattern(el) {
		// Object-pattern member: canonical form always carries an explicit key
		// (shorthand `{a}` normalizes to `{a: a}` — one form, one hash).
		keyNode, computed := l.propertyKey(be.PropertyName, el.Name())
		return &rast.Node{Kind: rast.KBindProp, U: boolBit(computed), Kids: []*rast.Node{keyNode, inner, def}}, binders
	}
	// Array-pattern element with default: KBindProp is object-only; wrap via
	// KParam-like shape? Array defaults use KBindProp with KNone key? Keep one
	// form: an array element with a default lowers to KBindProp with key KNone.
	if !def.IsNone() {
		return &rast.Node{Kind: rast.KBindProp, U: 0, Kids: []*rast.Node{noneNode(), inner, def}}, binders
	}
	return inner, binders
}

func parentIsObjectPattern(el *shimast.Node) bool {
	p := el.Parent
	return p != nil && p.Kind == shimast.KindObjectBindingPattern
}

// propertyKey lowers an object key (PropertyName or fallback name node).
// Returns (keyNode, computed).
func (l *lowerer) propertyKey(key, fallback *shimast.Node) (*rast.Node, bool) {
	n := key
	if n == nil {
		n = fallback
	}
	if n == nil {
		return noneNode(), false
	}
	switch n.Kind {
	case shimast.KindIdentifier:
		name := n.Text()
		if !asciiIdent(name) {
			l.errorAt(n, CodeBanNonASCIIIdent, "identifier key %q is not ASCII: use a string key", name)
		}
		return &rast.Node{Kind: rast.KStrPart, Str: name}, false
	case shimast.KindStringLiteral:
		l.checkStringToken(n)
		return &rast.Node{Kind: rast.KStrPart, Str: n.Text()}, false
	case shimast.KindNumericLiteral:
		// `{1: x}` ≡ `{"1": x}` — normalize numeric keys to their string form.
		return &rast.Node{Kind: rast.KStrPart, Str: n.Text()}, false
	case shimast.KindComputedPropertyName:
		inner := n.AsComputedPropertyName().Expression
		return l.expr(inner, ""), true
	case shimast.KindPrivateIdentifier:
		l.errorAt(n, CodeBanClass, "#private names are banned with classes")
		return noneNode(), false
	default:
		l.errorAt(n, CodeLowerUnsupported, "property key %s has no production", n.Kind.String())
		return noneNode(), false
	}
}

// --- docstrings & comments (out of hash, ADR-02 §2) ---

func (l *lowerer) extractDocstring(di *declInfo) {
	var plain []string
	for cr := range shimscanner.GetLeadingCommentRanges(l.factory, l.text, di.node.Pos()) {
		txt := strings.TrimSpace(l.text[cr.Pos():cr.End()])
		if strings.HasPrefix(txt, "/**") {
			di.docstring = txt
		} else {
			plain = append(plain, txt)
		}
	}
	if len(plain) > 0 {
		di.comments[""] = strings.Join(plain, "\n")
	}
}

// noteComments records leading comments of an inner statement at its node path.
func (l *lowerer) noteComments(st *shimast.Node, path string) {
	if l.cur == nil || path == "" {
		return
	}
	var parts []string
	for cr := range shimscanner.GetLeadingCommentRanges(l.factory, l.text, st.Pos()) {
		parts = append(parts, strings.TrimSpace(l.text[cr.Pos():cr.End()]))
	}
	if len(parts) > 0 {
		l.cur.comments[path] = strings.Join(parts, "\n")
	}
}

// --- small node helpers ---

func noneNode() *rast.Node { return &rast.Node{Kind: rast.KNone} }

func listNode(kids []*rast.Node) *rast.Node {
	return &rast.Node{Kind: rast.KList, Kids: kids}
}

func boolBit(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func childPath(parent string, idx int) string {
	if parent == "" {
		return itoa(idx)
	}
	return parent + "/" + itoa(idx)
}

func itoa(i int) string {
	// tiny non-negative int to string
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[p:])
}
