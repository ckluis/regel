package rast

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// DefKind names how a definition's top-level declaration is rendered.
type DefKind int

const (
	DefFunc      DefKind = iota // body is KFunc → `export function NAME(...) {...}`
	DefValue                    // body is an expression → `export const NAME = <expr>;`
	DefType                     // body is KTypeAlias → `export type NAME<...> = <type>;`
	DefInterface                // body is KInterface → `export interface NAME<...> {...}`
	DefNative                   // body is KNativeBody → typecheckable stub (no round-trip)
)

// Dep is one resolved dependency edge for import regeneration (ADR-02 §2): the
// referent's address, the module it lives in, and the local name it is imported
// as. The printer regenerates `import { Name } from "Module"` lines, sorted by
// (Module, Name), and prints KRef/TCatRef nodes as Name.
type Dep struct {
	Name   string
	Module string
	Hash   string
}

// PrintInput is everything the printer needs beyond the hashed AST: the display
// names (out of the hash, ADR-02 §3) and the dep/name mapping for imports.
type PrintInput struct {
	Body         *Node
	Name         string
	Exported     bool
	Kind         DefKind
	DisplayNames []string // value binders, pre-order DFS; shorter ⇒ _v{i} fallback
	TypeNames    []string // type binders, pre-order DFS; shorter ⇒ _T{i} fallback
	Deps         []Dep
}

// PrintModule renders a definition to canonical TypeScript module text (ADR-02
// §1). The output re-lowers+re-hashes to the same address (guarantee 2) and is a
// fixed point (guarantee 3). Determinism, not typography, is the contract.
func PrintModule(in PrintInput) string {
	p := &printer{
		vname:   map[*Node]string{},
		tname:   map[*Node]string{},
		hashDep: map[string]string{},
	}
	for _, d := range in.Deps {
		p.hashDep[d.Hash] = d.Name
	}
	p.selfName = in.Name
	p.assignNames(in.Body, in.DisplayNames, in.TypeNames)

	var sb strings.Builder
	p.emitImports(&sb, in.Deps)
	p.emitDecl(&sb, in)
	return sb.String()
}

type printer struct {
	vname    map[*Node]string // KBindId node → value display name
	tname    map[*Node]string // TParam node → type display name
	hashDep  map[string]string
	selfName string

	vbind []*Node // value binders in scope (top = last)
	tbind []*Node // type binders in scope
}

// --- name pre-pass (pre-order DFS assigns binder display names) ---

func (p *printer) assignNames(n *Node, disp, tdisp []string) {
	vc, tc := 0, 0
	var walk func(*Node)
	walk = func(m *Node) {
		if m == nil {
			return
		}
		switch m.Kind {
		case KBindId:
			var nm string
			if vc < len(disp) && disp[vc] != "" {
				nm = disp[vc]
			} else {
				nm = fmt.Sprintf("_v%d", vc)
			}
			p.vname[m] = nm
			vc++
		case TParam:
			var nm string
			if tc < len(tdisp) && tdisp[tc] != "" {
				nm = tdisp[tc]
			} else {
				nm = fmt.Sprintf("_T%d", tc)
			}
			p.tname[m] = nm
			tc++
		}
		for _, c := range m.Kids {
			walk(c)
		}
	}
	walk(n)
}

func (p *printer) nameOfLocal(index uint64) string {
	i := len(p.vbind) - 1 - int(index)
	if i < 0 || i >= len(p.vbind) {
		return fmt.Sprintf("_unbound%d", index)
	}
	return p.vname[p.vbind[i]]
}

func (p *printer) nameOfTLocal(index uint64) string {
	i := len(p.tbind) - 1 - int(index)
	if i < 0 || i >= len(p.tbind) {
		return fmt.Sprintf("_Unbound%d", index)
	}
	return p.tname[p.tbind[i]]
}

// collectBinders appends, in pre-order, every KBindId leaf under a pattern — the
// binder nodes it introduces (used to push/pop value scope for a binding site).
func collectBinders(pat *Node, out *[]*Node) {
	if pat == nil {
		return
	}
	if pat.Kind == KBindId {
		*out = append(*out, pat)
		return
	}
	for _, c := range pat.Kids {
		collectBinders(c, out)
	}
}

// --- imports ---

func (p *printer) emitImports(sb *strings.Builder, deps []Dep) {
	if len(deps) == 0 {
		return
	}
	byMod := map[string][]string{}
	for _, d := range deps {
		byMod[d.Module] = append(byMod[d.Module], d.Name)
	}
	mods := make([]string, 0, len(byMod))
	for m := range byMod {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	for _, m := range mods {
		names := byMod[m]
		sort.Strings(names)
		names = dedupeStrings(names)
		sb.WriteString("import { ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString(" } from ")
		sb.WriteString(strconv.Quote(m))
		sb.WriteString(";\n")
	}
	sb.WriteString("\n")
}

func dedupeStrings(s []string) []string {
	out := s[:0:0]
	var last string
	for i, v := range s {
		if i == 0 || v != last {
			out = append(out, v)
		}
		last = v
	}
	return out
}

// --- top-level declaration ---

func (p *printer) emitDecl(sb *strings.Builder, in PrintInput) {
	exp := ""
	if in.Exported {
		exp = "export "
	}
	switch in.Kind {
	case DefFunc:
		f := in.Body
		async := ""
		if f.U&1 != 0 {
			async = "async "
		}
		sb.WriteString(exp + async + "function " + safeName(in.Name))
		p.emitFuncTail(sb, f, true)
	case DefValue:
		sb.WriteString(exp + "const " + safeName(in.Name) + " = ")
		sb.WriteString(p.expr(in.Body))
		sb.WriteString(";\n")
	case DefType:
		ta := in.Body // KTypeAlias: Kids=[KList typeParams, type]
		p.pushTypeParams(ta.Kids[0])
		sb.WriteString(exp + "type " + safeName(in.Name) + p.typeParams(ta.Kids[0]) + " = ")
		sb.WriteString(p.typ(ta.Kids[1]))
		sb.WriteString(";\n")
		p.popTypeParams(ta.Kids[0])
	case DefInterface:
		it := in.Body // KInterface: Kids=[KList typeParams, KList members]
		p.pushTypeParams(it.Kids[0])
		sb.WriteString(exp + "interface " + safeName(in.Name) + p.typeParams(it.Kids[0]) + " ")
		sb.WriteString(p.typeMembers(it.Kids[1]))
		sb.WriteString("\n")
		p.popTypeParams(it.Kids[0])
	case DefNative:
		nb := in.Body // KNativeBody: Str=symbol, Kids=[type]
		sb.WriteString(exp + "const " + safeName(in.Name) + ": " + p.typ(nb.Kids[0]) +
			" = regelNative(" + strconv.Quote(nb.Str) + ");\n")
	}
}

// emitFuncTail prints `<TP>(params): ret { body }` (decl=true) for a KFunc.
func (p *printer) emitFuncTail(sb *strings.Builder, f *Node, decl bool) {
	params, tparams, ret, body := f.Kids[0], f.Kids[1], f.Kids[2], f.Kids[3]
	p.pushTypeParams(tparams)
	sb.WriteString(p.typeParams(tparams))
	// Push value binders for all params (visible to later defaults and body).
	var pushed []*Node
	sb.WriteString("(")
	for i, prm := range params.Kids {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(p.param(prm, &pushed))
	}
	sb.WriteString(")")
	if !ret.IsNone() {
		sb.WriteString(": " + p.typ(ret))
	}
	sb.WriteString(" ")
	sb.WriteString(p.block(body))
	p.popValue(pushed)
	p.popTypeParams(tparams)
	_ = decl
}

func (p *printer) param(prm *Node, pushed *[]*Node) string {
	// KParam: U bit0 rest; Kids=[pattern, type|None, default|None]
	var sb strings.Builder
	if prm.U&1 != 0 {
		sb.WriteString("...")
	}
	pat, typ, def := prm.Kids[0], prm.Kids[1], prm.Kids[2]
	sb.WriteString(p.pattern(pat))
	var binders []*Node
	collectBinders(pat, &binders)
	p.vbind = append(p.vbind, binders...)
	*pushed = append(*pushed, binders...)
	if !typ.IsNone() {
		sb.WriteString(": " + p.typ(typ))
	}
	if !def.IsNone() {
		sb.WriteString(" = " + p.expr(def))
	}
	return sb.String()
}

func (p *printer) popValue(binders []*Node) {
	p.vbind = p.vbind[:len(p.vbind)-len(binders)]
}

// --- statements ---

func (p *printer) block(n *Node) string {
	// KBlock: Kids=[KList stmts]
	var sb strings.Builder
	sb.WriteString("{\n")
	var introduced []*Node
	for _, st := range n.Kids[0].Kids {
		sb.WriteString(p.stmt(st, &introduced))
	}
	p.popValue(introduced)
	sb.WriteString("}")
	return sb.String()
}

// stmt prints a statement; binders it introduces into the *enclosing block* are
// appended to introduced (so the block pops them at its end).
func (p *printer) stmt(n *Node, introduced *[]*Node) string {
	switch n.Kind {
	case KVarDecl:
		kw := "let"
		if n.U&1 != 0 {
			kw = "const"
		}
		parts := make([]string, 0, len(n.Kids[0].Kids))
		var newBinders []*Node
		for _, d := range n.Kids[0].Kids {
			// KDeclr: Kids=[pattern, type|None, init|None]. init is in the CURRENT
			// scope (before the new binder is pushed).
			pat, typ, init := d.Kids[0], d.Kids[1], d.Kids[2]
			seg := p.pattern(pat)
			if !typ.IsNone() {
				seg += ": " + p.typ(typ)
			}
			if !init.IsNone() {
				seg += " = " + p.expr(init)
			}
			parts = append(parts, seg)
			collectBinders(pat, &newBinders)
		}
		// Push after all initializers are rendered.
		p.vbind = append(p.vbind, newBinders...)
		*introduced = append(*introduced, newBinders...)
		return kw + " " + strings.Join(parts, ", ") + ";\n"
	case KExprStmt:
		return "(" + p.expr(n.Kids[0]) + ");\n"
	case KIf:
		s := "if (" + p.expr(n.Kids[0]) + ") " + p.stmtBlockish(n.Kids[1])
		if !n.Kids[2].IsNone() {
			s += " else " + p.stmtBlockish(n.Kids[2])
		}
		return s + "\n"
	case KFor:
		return p.forStmt(n)
	case KForOf:
		return p.forOfStmt(n)
	case KWhile:
		return "while (" + p.expr(n.Kids[0]) + ") " + p.stmtBlockish(n.Kids[1]) + "\n"
	case KDoWhile:
		return "do " + p.stmtBlockish(n.Kids[0]) + " while (" + p.expr(n.Kids[1]) + ");\n"
	case KSwitch:
		return p.switchStmt(n)
	case KBreak:
		return "break;\n"
	case KContinue:
		return "continue;\n"
	case KReturn:
		if n.Kids[0].IsNone() {
			return "return;\n"
		}
		return "return " + p.expr(n.Kids[0]) + ";\n"
	case KThrow:
		return "throw " + p.expr(n.Kids[0]) + ";\n"
	case KTry:
		return p.tryStmt(n)
	case KBlock:
		return p.block(n) + "\n"
	default:
		return "/* unprintable stmt " + kindName(n.Kind) + " */\n"
	}
}

// stmtBlockish prints a statement as a block or single-statement body.
func (p *printer) stmtBlockish(n *Node) string {
	if n.Kind == KBlock {
		return p.block(n)
	}
	var intro []*Node
	s := p.stmt(n, &intro)
	p.popValue(intro)
	return "{\n" + s + "}"
}

func (p *printer) forStmt(n *Node) string {
	// KFor: Kids=[init|None, cond|None, incr|None, body]; init binders scoped to loop.
	init, cond, incr, body := n.Kids[0], n.Kids[1], n.Kids[2], n.Kids[3]
	var sb strings.Builder
	sb.WriteString("for (")
	var loopBinders []*Node
	if init.IsNone() {
		sb.WriteString(";")
	} else if init.Kind == KVarDecl {
		var intro []*Node
		seg := p.stmt(init, &intro) // pushes binders, ends with ";\n"
		loopBinders = intro
		sb.WriteString(strings.TrimRight(seg, "\n"))
	} else {
		sb.WriteString(p.expr(init) + ";")
	}
	if !cond.IsNone() {
		sb.WriteString(" " + p.expr(cond))
	}
	sb.WriteString(";")
	if !incr.IsNone() {
		sb.WriteString(" " + p.expr(incr))
	}
	sb.WriteString(") ")
	sb.WriteString(p.stmtBlockish(body))
	p.popValue(loopBinders)
	return sb.String() + "\n"
}

func (p *printer) forOfStmt(n *Node) string {
	// KForOf: U bit0 await; Kids=[decl, iterExpr, body]. decl is a KVarDecl with one
	// declarator whose init is None; its binder is scoped to the body.
	decl, iter, body := n.Kids[0], n.Kids[1], n.Kids[2]
	aw := ""
	if n.U&1 != 0 {
		aw = "await "
	}
	kw := "let"
	if decl.U&1 != 0 {
		kw = "const"
	}
	pat := decl.Kids[0].Kids[0]
	// iterExpr is evaluated in the outer scope (binder not yet pushed).
	iterStr := p.expr(iter)
	var binders []*Node
	collectBinders(pat, &binders)
	p.vbind = append(p.vbind, binders...)
	s := "for " + aw + "(" + kw + " " + p.pattern(pat) + " of " + iterStr + ") " + p.stmtBlockish(body) + "\n"
	p.popValue(binders)
	return s
}

func (p *printer) switchStmt(n *Node) string {
	var sb strings.Builder
	sb.WriteString("switch (" + p.expr(n.Kids[0]) + ") {\n")
	for _, cl := range n.Kids[1].Kids {
		// KClause: U bit0 default; Kids=[test|None, KList stmts]
		if cl.U&1 != 0 {
			sb.WriteString("default:\n")
		} else {
			sb.WriteString("case " + p.expr(cl.Kids[0]) + ":\n")
		}
		var intro []*Node
		for _, st := range cl.Kids[1].Kids {
			sb.WriteString(p.stmt(st, &intro))
		}
		p.popValue(intro)
	}
	sb.WriteString("}\n")
	return sb.String()
}

func (p *printer) tryStmt(n *Node) string {
	// KTry: Kids=[tryBlock, catch|None, finallyBlock|None]
	var sb strings.Builder
	sb.WriteString("try " + p.block(n.Kids[0]))
	if !n.Kids[1].IsNone() {
		c := n.Kids[1] // KCatch: U bit0 hasParam; Kids=[pattern|None, block]
		if c.U&1 != 0 {
			pat := c.Kids[0]
			var binders []*Node
			collectBinders(pat, &binders)
			p.vbind = append(p.vbind, binders...)
			sb.WriteString(" catch (" + p.pattern(pat) + ") " + p.block(c.Kids[1]))
			p.popValue(binders)
		} else {
			sb.WriteString(" catch " + p.block(c.Kids[1]))
		}
	}
	if !n.Kids[2].IsNone() {
		sb.WriteString(" finally " + p.block(n.Kids[2]))
	}
	return sb.String() + "\n"
}

// --- patterns ---

func (p *printer) pattern(n *Node) string {
	switch n.Kind {
	case KBindId:
		return safeName(p.vname[n])
	case KArrayPat:
		parts := make([]string, 0, len(n.Kids[0].Kids))
		for _, e := range n.Kids[0].Kids {
			if e.IsNone() {
				parts = append(parts, "")
				continue
			}
			parts = append(parts, p.pattern(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KObjPat:
		parts := make([]string, 0, len(n.Kids[0].Kids))
		for _, e := range n.Kids[0].Kids {
			parts = append(parts, p.pattern(e))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case KBindProp:
		// U bit0 computed; Kids=[keyNode, pattern, default|None]
		key := p.propKey(n.Kids[0], n.U&1 != 0)
		val := p.pattern(n.Kids[1])
		s := key + ": " + val
		if !n.Kids[2].IsNone() {
			s += " = " + p.expr(n.Kids[2])
		}
		return s
	case KRestPat:
		return "..." + p.pattern(n.Kids[0])
	default:
		return "/*pat?*/"
	}
}

func (p *printer) propKey(n *Node, computed bool) string {
	if computed {
		return "[" + p.expr(n) + "]"
	}
	if n.Kind == KStrPart || n.Kind == KStr {
		if isIdent(n.Str) {
			return n.Str
		}
		return strconv.Quote(n.Str)
	}
	return p.expr(n)
}

// --- expressions ---

func (p *printer) expr(n *Node) string {
	switch n.Kind {
	case KNum:
		return numLit(n.U)
	case KBigInt:
		return bigLit(n) + "n"
	case KStr:
		return quoteStr(n.Str)
	case KBool:
		if n.U != 0 {
			return "true"
		}
		return "false"
	case KNull:
		return "null"
	case KUndefined:
		return "undefined"
	case KRegex:
		return "/" + n.Str + "/" + regexFlags(n.U)
	case KTemplate:
		return p.template(n)
	case KLocal:
		return safeName(p.nameOfLocal(n.U))
	case KRef:
		if nm, ok := p.hashDep[n.Str]; ok {
			return safeName(nm)
		}
		return "/*ref:" + n.Str + "*/"
	case KSelfRef:
		return safeName(p.selfName)
	case KName:
		return safeName(n.Str)
	case KArray:
		parts := make([]string, 0, len(n.Kids[0].Kids))
		for _, e := range n.Kids[0].Kids {
			if e.IsNone() {
				parts = append(parts, "")
				continue
			}
			parts = append(parts, p.expr(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KObject:
		parts := make([]string, 0, len(n.Kids[0].Kids))
		for _, pr := range n.Kids[0].Kids {
			parts = append(parts, p.objProp(pr))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case KSpread:
		return "..." + p.expr(n.Kids[0])
	case KCall:
		s := p.expr(n.Kids[0])
		if n.U&1 != 0 {
			s += "?."
		}
		ta := ""
		if len(n.Kids) > 2 && !n.Kids[2].IsNone() {
			ta = p.typeArgs(n.Kids[2])
		}
		args := make([]string, 0, len(n.Kids[1].Kids))
		for _, a := range n.Kids[1].Kids {
			args = append(args, p.expr(a))
		}
		return s + ta + "(" + strings.Join(args, ", ") + ")"
	case KMember:
		s := p.expr(n.Kids[0])
		if n.U&1 != 0 {
			return s + "?." + n.Str
		}
		return s + "." + n.Str
	case KIndex:
		s := p.expr(n.Kids[0])
		if n.U&1 != 0 {
			return s + "?.[" + p.expr(n.Kids[1]) + "]"
		}
		return s + "[" + p.expr(n.Kids[1]) + "]"
	case KBinary:
		return "(" + p.expr(n.Kids[0]) + " " + binOp(OpKind(n.U)) + " " + p.expr(n.Kids[1]) + ")"
	case KUnary:
		return "(" + unOp(OpKind(n.U)) + p.expr(n.Kids[0]) + ")"
	case KUpdate:
		op := "++"
		if OpKind(n.U>>1) == OpDec {
			op = "--"
		}
		if n.U&1 != 0 { // prefix
			return "(" + op + p.expr(n.Kids[0]) + ")"
		}
		return "(" + p.expr(n.Kids[0]) + op + ")"
	case KCond:
		return "(" + p.expr(n.Kids[0]) + " ? " + p.expr(n.Kids[1]) + " : " + p.expr(n.Kids[2]) + ")"
	case KTypeof:
		return "(typeof " + p.expr(n.Kids[0]) + ")"
	case KAwait:
		return "(await " + p.expr(n.Kids[0]) + ")"
	case KAsConst:
		return "(" + p.expr(n.Kids[0]) + " as const)"
	case KSatisfy:
		return "(" + p.expr(n.Kids[0]) + " satisfies " + p.typ(n.Kids[1]) + ")"
	case KFunc:
		return p.arrow(n)
	default:
		return "/*expr?" + kindName(n.Kind) + "*/"
	}
}

func (p *printer) objProp(pr *Node) string {
	if pr.Kind == KSpread {
		return "..." + p.expr(pr.Kids[0])
	}
	// KProp: U bit0 computed; Kids=[keyNode, value]
	key := p.propKey(pr.Kids[0], pr.U&1 != 0)
	return key + ": " + p.expr(pr.Kids[1])
}

func (p *printer) arrow(f *Node) string {
	// KFunc as arrow. U bit0 async, bit1 expr-body.
	var sb strings.Builder
	if f.U&1 != 0 {
		sb.WriteString("async ")
	}
	params, tparams, ret, body := f.Kids[0], f.Kids[1], f.Kids[2], f.Kids[3]
	p.pushTypeParams(tparams)
	sb.WriteString(p.typeParams(tparams))
	var pushed []*Node
	sb.WriteString("(")
	for i, prm := range params.Kids {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(p.param(prm, &pushed))
	}
	sb.WriteString(")")
	if !ret.IsNone() {
		sb.WriteString(": " + p.typ(ret))
	}
	sb.WriteString(" => ")
	if f.U&2 != 0 { // expression body
		e := p.expr(body)
		if strings.HasPrefix(e, "{") {
			e = "(" + e + ")"
		}
		sb.WriteString(e)
	} else {
		sb.WriteString(p.block(body))
	}
	p.popValue(pushed)
	p.popTypeParams(tparams)
	return "(" + sb.String() + ")"
}

func (p *printer) template(n *Node) string {
	var sb strings.Builder
	sb.WriteString("`")
	for _, part := range n.Kids[0].Kids {
		if part.Kind == KStrPart {
			sb.WriteString(escapeTemplate(part.Str))
		} else {
			sb.WriteString("${" + p.expr(part) + "}")
		}
	}
	sb.WriteString("`")
	return sb.String()
}

// --- types ---

func (p *printer) typ(n *Node) string {
	switch n.Kind {
	case TKeyword:
		return n.Str
	case TLiteral:
		return p.expr(n.Kids[0])
	case TArray:
		return "(" + p.typ(n.Kids[0]) + ")[]"
	case TReadonly:
		return "readonly " + p.typ(n.Kids[0])
	case TTuple:
		parts := make([]string, 0, len(n.Kids[0].Kids))
		for _, e := range n.Kids[0].Kids {
			parts = append(parts, p.typ(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case TUnion:
		parts := make([]string, 0, len(n.Kids[0].Kids))
		for _, e := range n.Kids[0].Kids {
			parts = append(parts, p.typ(e))
		}
		return "(" + strings.Join(parts, " | ") + ")"
	case TInter:
		parts := make([]string, 0, len(n.Kids[0].Kids))
		for _, e := range n.Kids[0].Kids {
			parts = append(parts, p.typ(e))
		}
		return "(" + strings.Join(parts, " & ") + ")"
	case TRef:
		return safeName(n.Str) + p.typeArgList(n)
	case TCatRef:
		nm := p.hashDep[n.Str]
		if nm == "" {
			nm = "/*tref:" + n.Str + "*/"
		}
		return safeName(nm) + p.typeArgList(n)
	case TLocal:
		return safeName(p.nameOfTLocal(n.U)) + p.typeArgList(n)
	case TObject:
		return p.typeMembers(n.Kids[0])
	case TFunc:
		// Kids=[KList typeParams, KList params, retType]
		p.pushTypeParams(n.Kids[0])
		tp := p.typeParams(n.Kids[0])
		var pushed []*Node
		ps := make([]string, 0, len(n.Kids[1].Kids))
		for _, prm := range n.Kids[1].Kids {
			ps = append(ps, p.param(prm, &pushed))
		}
		ret := p.typ(n.Kids[2])
		p.popValue(pushed)
		p.popTypeParams(n.Kids[0])
		return "(" + tp + "(" + strings.Join(ps, ", ") + ") => " + ret + ")"
	case TCond:
		return "(" + p.typ(n.Kids[0]) + " extends " + p.typ(n.Kids[1]) + " ? " + p.typ(n.Kids[2]) + " : " + p.typ(n.Kids[3]) + ")"
	case TKeyof:
		return "(keyof " + p.typ(n.Kids[0]) + ")"
	case TIndexed:
		return p.typ(n.Kids[0]) + "[" + p.typ(n.Kids[1]) + "]"
	case TQuery:
		return "(typeof " + p.expr(n.Kids[0]) + ")"
	default:
		return "/*type?" + kindName(n.Kind) + "*/"
	}
}

// typeMembers prints a KList of TPropSig/TIndexSig as an object-type body.
func (p *printer) typeMembers(lst *Node) string {
	var sb strings.Builder
	sb.WriteString("{ ")
	for _, m := range lst.Kids {
		switch m.Kind {
		case TPropSig:
			if m.U&1 != 0 {
				sb.WriteString("readonly ")
			}
			sb.WriteString(propSigKey(m.Str))
			if m.U&2 != 0 {
				sb.WriteString("?")
			}
			sb.WriteString(": " + p.typ(m.Kids[0]) + "; ")
		case TIndexSig:
			if m.U&1 != 0 {
				sb.WriteString("readonly ")
			}
			sb.WriteString("[_k: " + p.typ(m.Kids[0]) + "]: " + p.typ(m.Kids[1]) + "; ")
		}
	}
	sb.WriteString("}")
	return sb.String()
}

func propSigKey(k string) string {
	if isIdent(k) {
		return k
	}
	return strconv.Quote(k)
}

// typeParams / typeArgs

func (p *printer) pushTypeParams(lst *Node) {
	if lst == nil {
		return
	}
	for _, tp := range lst.Kids {
		if tp.Kind == TParam {
			p.tbind = append(p.tbind, tp)
		}
	}
}

func (p *printer) popTypeParams(lst *Node) {
	if lst == nil {
		return
	}
	n := 0
	for _, tp := range lst.Kids {
		if tp.Kind == TParam {
			n++
		}
	}
	p.tbind = p.tbind[:len(p.tbind)-n]
}

func (p *printer) typeParams(lst *Node) string {
	if lst == nil || len(lst.Kids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lst.Kids))
	for _, tp := range lst.Kids {
		// TParam: Kids=[constraint|None, default|None]
		s := safeName(p.tname[tp])
		if !tp.Kids[0].IsNone() {
			s += " extends " + p.typ(tp.Kids[0])
		}
		if !tp.Kids[1].IsNone() {
			s += " = " + p.typ(tp.Kids[1])
		}
		parts = append(parts, s)
	}
	return "<" + strings.Join(parts, ", ") + ">"
}

// typeArgList renders trailing type args carried on a TRef/TCatRef/TLocal (Kids[0]=KList).
func (p *printer) typeArgList(n *Node) string {
	if len(n.Kids) == 0 || n.Kids[0] == nil || len(n.Kids[0].Kids) == 0 {
		return ""
	}
	return p.typeArgs(n.Kids[0])
}

func (p *printer) typeArgs(lst *Node) string {
	parts := make([]string, 0, len(lst.Kids))
	for _, t := range lst.Kids {
		parts = append(parts, p.typ(t))
	}
	return "<" + strings.Join(parts, ", ") + ">"
}

// --- literal formatting ---

// numLit renders an f64 bit pattern deterministically. It uses the shortest
// round-tripping decimal (strconv 'g', -1) which re-parses to the same bits;
// -0, integers, and fractions all round-trip.
func numLit(bits uint64) string {
	f := math.Float64frombits(bits)
	if math.IsInf(f, 0) || math.IsNaN(f) {
		// Non-finite literals are rejected at the gate; never reachable from a
		// stored AST. Emit a form that will fail re-admission loudly if it ever is.
		return "0/*nonfinite*/"
	}
	if math.Signbit(f) && f == 0 {
		return "-0"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	return s
}

func bigLit(n *Node) string {
	z := new(big.Int).SetBytes(n.Mag)
	if n.U&1 != 0 {
		z.Neg(z)
	}
	return z.String()
}

func regexFlags(u uint64) string {
	// Emit in canonical (bit) order — already sorted.
	var sb strings.Builder
	order := []struct {
		bit  uint64
		char byte
	}{
		{RegexFlagD, 'd'}, {RegexFlagG, 'g'}, {RegexFlagI, 'i'}, {RegexFlagM, 'm'},
		{RegexFlagS, 's'}, {RegexFlagU, 'u'}, {RegexFlagV, 'v'}, {RegexFlagY, 'y'},
	}
	for _, o := range order {
		if u&o.bit != 0 {
			sb.WriteByte(o.char)
		}
	}
	return sb.String()
}

func quoteStr(s string) string { return strconv.Quote(s) }

func escapeTemplate(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '`':
			sb.WriteString("\\`")
		case '\\':
			sb.WriteString("\\\\")
		case '$':
			sb.WriteString("\\$")
		case '\r':
			sb.WriteString("\\r")
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func binOp(op OpKind) string {
	switch op {
	case OpAdd:
		return "+"
	case OpSub:
		return "-"
	case OpMul:
		return "*"
	case OpDiv:
		return "/"
	case OpMod:
		return "%"
	case OpExp:
		return "**"
	case OpShl:
		return "<<"
	case OpShr:
		return ">>"
	case OpUShr:
		return ">>>"
	case OpBitAnd:
		return "&"
	case OpBitOr:
		return "|"
	case OpBitXor:
		return "^"
	case OpLt:
		return "<"
	case OpGt:
		return ">"
	case OpLe:
		return "<="
	case OpGe:
		return ">="
	case OpEqEq:
		return "=="
	case OpNeEq:
		return "!="
	case OpEqEqEq:
		return "==="
	case OpNeEqEq:
		return "!=="
	case OpAnd:
		return "&&"
	case OpOr:
		return "||"
	case OpNullish:
		return "??"
	case OpIn:
		return "in"
	case OpAssign:
		return "="
	case OpAddAssign:
		return "+="
	case OpSubAssign:
		return "-="
	case OpMulAssign:
		return "*="
	case OpDivAssign:
		return "/="
	case OpModAssign:
		return "%="
	case OpExpAssign:
		return "**="
	case OpShlAssign:
		return "<<="
	case OpShrAssign:
		return ">>="
	case OpUShrAssign:
		return ">>>="
	case OpBitAndAssign:
		return "&="
	case OpBitOrAssign:
		return "|="
	case OpBitXorAssign:
		return "^="
	case OpAndAssign:
		return "&&="
	case OpOrAssign:
		return "||="
	case OpNullAssign:
		return "??="
	default:
		return "?op?"
	}
}

func unOp(op OpKind) string {
	switch op {
	case OpPos:
		return "+"
	case OpNeg:
		return "-"
	case OpNot:
		return "!"
	case OpBitNot:
		return "~"
	default:
		return "?u?"
	}
}

// --- identifiers ---

var reservedWords = map[string]bool{
	"if": true, "else": true, "return": true, "const": true, "let": true, "for": true,
	"while": true, "do": true, "switch": true, "case": true, "default": true, "break": true,
	"continue": true, "function": true, "typeof": true, "await": true, "async": true,
	"throw": true, "try": true, "catch": true, "finally": true, "in": true, "of": true,
	"new": true, "class": true, "this": true, "void": true, "delete": true, "true": true,
	"false": true, "null": true, "undefined": true, "as": true, "satisfies": true,
	"interface": true, "type": true, "export": true, "import": true, "from": true,
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r == '$' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// safeName returns an emittable identifier; empty/invalid names get a stable
// placeholder so the projection still parses (never expected on real inputs).
func safeName(s string) string {
	if s == "" {
		return "_anon"
	}
	return s
}

func kindName(k Kind) string { return fmt.Sprintf("K%d", int(k)) }
