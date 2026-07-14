package lower

import (
	shimast "github.com/microsoft/typescript-go/shim/ast"
	"regel.dev/regel/internal/mutants"
	"regel.dev/regel/internal/rast"
)

// block lowers a tsgo Block to KBlock. path is the KBlock node's path.
func (l *lowerer) block(b *shimast.Node, path string) *rast.Node {
	stmts := b.AsBlock().Statements.Nodes
	var out []*rast.Node
	intro := 0
	listPath := childPath(path, 0)
	for _, st := range stmts {
		spath := childPath(listPath, len(out))
		l.noteComments(st, spath)
		n, pushed := l.stmt(st, spath)
		intro += pushed
		if n != nil {
			out = append(out, n)
		}
	}
	l.popValues(intro)
	return &rast.Node{Kind: rast.KBlock, Kids: []*rast.Node{listNode(out)}}
}

// blockish normalizes a statement in body position to a block (the canonical
// printer always prints braces — one form, one hash).
func (l *lowerer) blockish(st *shimast.Node, path string) *rast.Node {
	if st.Kind == shimast.KindBlock {
		return l.block(st, path)
	}
	spath := childPath(childPath(path, 0), 0)
	l.noteComments(st, spath)
	n, pushed := l.stmt(st, spath)
	l.popValues(pushed)
	var kids []*rast.Node
	if n != nil {
		kids = append(kids, n)
	}
	return &rast.Node{Kind: rast.KBlock, Kids: []*rast.Node{listNode(kids)}}
}

// stmt lowers one statement. It returns the node (nil for statements that
// vanish, e.g. `;`) and how many binders it pushed into the enclosing block
// scope (the block pops them at its end — the printer's exact discipline).
func (l *lowerer) stmt(st *shimast.Node, path string) (*rast.Node, int) {
	switch st.Kind {
	case shimast.KindVariableStatement:
		return l.varStmt(st, path)
	case shimast.KindFunctionDeclaration:
		return l.nestedFuncDecl(st, path)
	case shimast.KindExpressionStatement:
		l.checkFloatingPromise(st)
		e := l.expr(st.AsExpressionStatement().Expression, childPath(path, 0))
		return &rast.Node{Kind: rast.KExprStmt, Kids: []*rast.Node{e}}, 0
	case shimast.KindIfStatement:
		s := st.AsIfStatement()
		cond := l.expr(s.Expression, childPath(path, 0))
		then := l.blockish(s.ThenStatement, childPath(path, 1))
		els := noneNode()
		if s.ElseStatement != nil {
			els = l.blockish(s.ElseStatement, childPath(path, 2))
		}
		return &rast.Node{Kind: rast.KIf, Kids: []*rast.Node{cond, then, els}}, 0
	case shimast.KindWhileStatement:
		s := st.AsWhileStatement()
		cond := l.expr(s.Expression, childPath(path, 0))
		body := l.blockish(s.Statement, childPath(path, 1))
		return &rast.Node{Kind: rast.KWhile, Kids: []*rast.Node{cond, body}}, 0
	case shimast.KindDoStatement:
		s := st.AsDoStatement()
		body := l.blockish(s.Statement, childPath(path, 0))
		cond := l.expr(s.Expression, childPath(path, 1))
		return &rast.Node{Kind: rast.KDoWhile, Kids: []*rast.Node{body, cond}}, 0
	case shimast.KindForStatement:
		return l.forStmt(st, path)
	case shimast.KindForOfStatement:
		return l.forOfStmt(st, path)
	case shimast.KindForInStatement:
		l.errorAt(st, CodeBanForIn, "`for-in` is banned (prototype enumeration): use `for (const k of keys(obj))`")
		return nil, 0
	case shimast.KindSwitchStatement:
		return l.switchStmt(st, path)
	case shimast.KindBreakStatement:
		if st.AsBreakStatement().Label != nil {
			l.errorAt(st, CodeBanLabel, "labeled `break` is banned: structured control only")
		}
		return &rast.Node{Kind: rast.KBreak}, 0
	case shimast.KindContinueStatement:
		if st.AsContinueStatement().Label != nil {
			l.errorAt(st, CodeBanLabel, "labeled `continue` is banned: structured control only")
		}
		return &rast.Node{Kind: rast.KContinue}, 0
	case shimast.KindReturnStatement:
		s := st.AsReturnStatement()
		e := noneNode()
		if s.Expression != nil {
			e = l.expr(s.Expression, childPath(path, 0))
		}
		return &rast.Node{Kind: rast.KReturn, Kids: []*rast.Node{e}}, 0
	case shimast.KindThrowStatement:
		e := l.expr(st.AsThrowStatement().Expression, childPath(path, 0))
		return &rast.Node{Kind: rast.KThrow, Kids: []*rast.Node{e}}, 0
	case shimast.KindTryStatement:
		return l.tryStmt(st, path)
	case shimast.KindBlock:
		return l.block(st, path), 0
	case shimast.KindEmptyStatement:
		return nil, 0
	case shimast.KindLabeledStatement:
		l.errorAt(st, CodeBanLabel, "labeled statements are banned: structured control only")
		return nil, 0
	case shimast.KindDebuggerStatement:
		l.errorAt(st, CodeBanDebugger, "`debugger` is banned: remove the host-debugger hook")
		return nil, 0
	case shimast.KindClassDeclaration:
		l.checkDecorators(st)
		l.errorAt(st, CodeBanClass, "`class` is banned: data is shapes, behavior is functions")
		return nil, 0
	case shimast.KindEnumDeclaration:
		l.errorAt(st, CodeBanEnum, "`enum` is banned: use a string-literal union")
		return nil, 0
	case shimast.KindModuleDeclaration:
		l.errorAt(st, CodeBanNamespace, "`namespace` is banned: modules are files, files become rows")
		return nil, 0
	case shimast.KindInterfaceDeclaration, shimast.KindTypeAliasDeclaration:
		l.errorAt(st, CodeLowerUnsupported, "nested type declarations have no production: declare types at module top level")
		return nil, 0
	case shimast.KindWithStatement:
		l.errorAt(st, CodeBanWithEval, "`with` is banned: dynamic scope is ungovernable")
		return nil, 0
	default:
		l.errorAt(st, CodeLowerUnsupported, "statement %s has no regel-AST production (default-deny)", st.Kind.String())
		return nil, 0
	}
}

// varStmt lowers a let/const statement. All declarators are lowered in the
// current scope, THEN the new binders are pushed (printer discipline).
func (l *lowerer) varStmt(st *shimast.Node, path string) (*rast.Node, int) {
	vs := st.AsVariableStatement()
	flags := vs.DeclarationList.Flags
	if flags&(shimast.NodeFlagsLet|shimast.NodeFlagsConst) == 0 {
		l.errorAt(st, CodeBanVar, "`var` is banned (function-scope hoisting): use `let` or `const`")
		return nil, 0
	}
	if st.ModifierFlags()&shimast.ModifierFlagsAmbient != 0 {
		l.errorAt(st, CodeBanNamespace, "`declare` (ambient) is banned: modules are files, files become rows")
		return nil, 0
	}
	isConst := flags&shimast.NodeFlagsConst != 0
	return l.varDeclList(vs.DeclarationList, isConst, path)
}

func (l *lowerer) varDeclList(declList *shimast.Node, isConst bool, path string) (*rast.Node, int) {
	lst := declList.AsVariableDeclarationList()
	var declrs []*rast.Node
	var allBinders []binder
	var asyncFlags []bool
	for _, d := range lst.Declarations.Nodes {
		vd := d.AsVariableDeclaration()
		pat, binders := l.pattern(d.Name())
		typ := noneNode()
		if vd.Type != nil {
			typ = l.typ(vd.Type)
		}
		init := noneNode()
		if vd.Initializer != nil {
			init = l.expr(vd.Initializer, "")
		}
		declrs = append(declrs, &rast.Node{Kind: rast.KDeclr, Kids: []*rast.Node{pat, typ, init}})
		async := isAsyncFunctionExpr(vd.Initializer)
		for range binders {
			asyncFlags = append(asyncFlags, async && len(binders) == 1)
		}
		allBinders = append(allBinders, binders...)
	}
	n := l.pushBinders(allBinders, isConst)
	// Mark async-closure binders for the floating-promise approximation.
	for i := 0; i < n; i++ {
		if asyncFlags[i] {
			l.scopes[len(l.scopes)-n+i].isAsyncFn = true
		}
	}
	var u uint64
	if isConst {
		u = 1
	}
	return &rast.Node{Kind: rast.KVarDecl, U: u, Kids: []*rast.Node{listNode(declrs)}}, n
}

// nestedFuncDecl normalizes a nested `function f() {}` statement to
// `const f = (…) => {…}` (one form in a this-free dialect; hoisting is lost —
// deliberate stiffness, use-before-decl fails typecheck).
func (l *lowerer) nestedFuncDecl(st *shimast.Node, path string) (*rast.Node, int) {
	fd := st.AsFunctionDeclaration()
	l.checkDecorators(st)
	if fd.BodyData().AsteriskToken != nil {
		l.errorAt(st, CodeBanGenerator, "generators are banned: one suspension surface (std Iter<T>)")
		return nil, 0
	}
	if fd.Name() == nil {
		l.errorAt(st, CodeBadModule, "function declaration must be named")
		return nil, 0
	}
	name := fd.Name().Text()
	if !asciiIdent(name) {
		l.errorAt(fd.Name(), CodeBanNonASCIIIdent, "identifier %q is not ASCII", name)
		name = "_x"
	}
	async := st.ModifierFlags()&shimast.ModifierFlagsAsync != 0
	fnNode := l.lowerFunctionLike(st, fd.BodyData().Body, async, "")
	bn := &rast.Node{Kind: rast.KBindId, Str: name}
	l.scopes = append(l.scopes, &scopeEntry{name: name, isConst: true, funcDepth: l.funcDepth, isAsyncFn: async})
	declr := &rast.Node{Kind: rast.KDeclr, Kids: []*rast.Node{bn, noneNode(), fnNode}}
	return &rast.Node{Kind: rast.KVarDecl, U: 1, Kids: []*rast.Node{listNode([]*rast.Node{declr})}}, 1
}

func (l *lowerer) forStmt(st *shimast.Node, path string) (*rast.Node, int) {
	s := st.AsForStatement()
	init := noneNode()
	pushed := 0
	if s.Initializer != nil {
		if s.Initializer.Kind == shimast.KindVariableDeclarationList {
			flags := s.Initializer.Flags
			if flags&(shimast.NodeFlagsLet|shimast.NodeFlagsConst) == 0 {
				l.errorAt(st, CodeBanVar, "`var` is banned: use `let`")
				return nil, 0
			}
			init, pushed = l.varDeclList(s.Initializer, flags&shimast.NodeFlagsConst != 0, childPath(path, 0))
		} else {
			init = l.expr(s.Initializer, childPath(path, 0))
		}
	}
	cond := noneNode()
	if s.Condition != nil {
		cond = l.expr(s.Condition, childPath(path, 1))
	}
	incr := noneNode()
	if s.Incrementor != nil {
		incr = l.expr(s.Incrementor, childPath(path, 2))
	}
	body := l.blockish(s.Statement, childPath(path, 3))
	l.popValues(pushed)
	return &rast.Node{Kind: rast.KFor, Kids: []*rast.Node{init, cond, incr, body}}, 0
}

func (l *lowerer) forOfStmt(st *shimast.Node, path string) (*rast.Node, int) {
	s := st.AsForInOrOfStatement()
	var u uint64
	if s.AwaitModifier != nil {
		u |= 1
	}
	if s.Initializer == nil || s.Initializer.Kind != shimast.KindVariableDeclarationList {
		l.errorAt(st, CodeLowerUnsupported, "for-of over an assignment target has no production: declare the loop variable (`for (const x of …)`)")
		return nil, 0
	}
	flags := s.Initializer.Flags
	if flags&(shimast.NodeFlagsLet|shimast.NodeFlagsConst) == 0 {
		l.errorAt(st, CodeBanVar, "`var` is banned: use `const`")
		return nil, 0
	}
	isConst := flags&shimast.NodeFlagsConst != 0
	decls := s.Initializer.AsVariableDeclarationList().Declarations.Nodes
	if len(decls) != 1 {
		l.errorAt(st, CodeLowerUnsupported, "for-of declares exactly one binding")
		return nil, 0
	}
	// Pattern first, iterable in the OUTER scope, then the binders for the body
	// (printer discipline).
	pat, binders := l.pattern(decls[0].Name())
	iter := l.expr(s.Expression, childPath(path, 1))
	n := l.pushBinders(binders, isConst)
	body := l.blockish(s.Statement, childPath(path, 2))
	l.popValues(n)
	var cu uint64
	if isConst {
		cu = 1
	}
	declr := &rast.Node{Kind: rast.KDeclr, Kids: []*rast.Node{pat, noneNode(), noneNode()}}
	decl := &rast.Node{Kind: rast.KVarDecl, U: cu, Kids: []*rast.Node{listNode([]*rast.Node{declr})}}
	return &rast.Node{Kind: rast.KForOf, U: u, Kids: []*rast.Node{decl, iter, body}}, 0
}

func (l *lowerer) switchStmt(st *shimast.Node, path string) (*rast.Node, int) {
	s := st.AsSwitchStatement()
	disc := l.expr(s.Expression, childPath(path, 0))
	var clauses []*rast.Node
	clausesPath := childPath(path, 1)
	for ci, cl := range s.CaseBlock.AsCaseBlock().Clauses.Nodes {
		cc := cl.AsCaseOrDefaultClause()
		isDefault := cl.Kind == shimast.KindDefaultClause
		test := noneNode()
		if !isDefault {
			if !isLiteralCaseLabel(cc.Expression) {
				l.errorAt(cl, CodeSwitchFallthrough, "switch case label must be a literal (literal-union discriminants only)")
			}
			test = l.expr(cc.Expression, "")
		}
		cpath := childPath(clausesPath, ci)
		stmtsPath := childPath(cpath, 1)
		var stmts []*rast.Node
		intro := 0
		for _, cs := range cc.Statements.Nodes {
			spath := childPath(stmtsPath, len(stmts))
			l.noteComments(cs, spath)
			n, pushed := l.stmt(cs, spath)
			intro += pushed
			if n != nil {
				stmts = append(stmts, n)
			}
		}
		l.popValues(intro)
		// Switch discipline: a non-empty clause must end in
		// break/return/continue/throw (no fallthrough).
		if len(stmts) > 0 {
			switch stmts[len(stmts)-1].Kind {
			case rast.KBreak, rast.KContinue, rast.KReturn, rast.KThrow:
			default:
				l.errorAt(cl, CodeSwitchFallthrough, "non-empty switch case must end in break/return/continue/throw")
			}
		}
		clauses = append(clauses, &rast.Node{Kind: rast.KClause, U: boolBit(isDefault),
			Kids: []*rast.Node{test, listNode(stmts)}})
	}
	return &rast.Node{Kind: rast.KSwitch, Kids: []*rast.Node{disc, listNode(clauses)}}, 0
}

// isLiteralCaseLabel accepts literal case labels only (ADR-01 §3 switch over
// literal-union discriminants).
func isLiteralCaseLabel(e *shimast.Node) bool {
	e = unparen(e)
	if e == nil {
		return false
	}
	switch e.Kind {
	case shimast.KindStringLiteral, shimast.KindNumericLiteral, shimast.KindBigIntLiteral,
		shimast.KindTrueKeyword, shimast.KindFalseKeyword, shimast.KindNullKeyword,
		shimast.KindNoSubstitutionTemplateLiteral:
		return true
	case shimast.KindIdentifier:
		return e.Text() == "undefined"
	case shimast.KindPrefixUnaryExpression:
		pu := e.AsPrefixUnaryExpression()
		return pu.Operator == shimast.KindMinusToken &&
			(pu.Operand.Kind == shimast.KindNumericLiteral || pu.Operand.Kind == shimast.KindBigIntLiteral)
	}
	return false
}

func (l *lowerer) tryStmt(st *shimast.Node, path string) (*rast.Node, int) {
	s := st.AsTryStatement()
	tryB := l.block(s.TryBlock, childPath(path, 0))
	catch := noneNode()
	if s.CatchClause != nil {
		cc := s.CatchClause.AsCatchClause()
		cpath := childPath(path, 1)
		if cc.VariableDeclaration != nil {
			vd := cc.VariableDeclaration.AsVariableDeclaration()
			if vd.Type != nil {
				// catch variable is implicitly unknown (useUnknownInCatchVariables);
				// an explicit annotation is trivia-ish but has two spellings — reject
				// to keep one form.
				l.errorAt(cc.VariableDeclaration, CodeLowerUnsupported, "catch variable annotation has no production: the catch variable is `unknown`")
			}
			pat, binders := l.pattern(cc.VariableDeclaration.Name())
			n := l.pushBinders(binders, false)
			blk := l.block(cc.Block, childPath(cpath, 1))
			l.popValues(n)
			catch = &rast.Node{Kind: rast.KCatch, U: 1, Kids: []*rast.Node{pat, blk}}
		} else {
			blk := l.block(cc.Block, childPath(cpath, 1))
			catch = &rast.Node{Kind: rast.KCatch, U: 0, Kids: []*rast.Node{noneNode(), blk}}
		}
	}
	finally := noneNode()
	if s.FinallyBlock != nil {
		finally = l.block(s.FinallyBlock, childPath(path, 2))
	}
	return &rast.Node{Kind: rast.KTry, Kids: []*rast.Node{tryB, catch, finally}}, 0
}

// checkFloatingPromise is the Stage-A syntactic approximation (documented
// residue): a bare call statement whose callee resolves to a syntactically
// async local binding, sibling declaration, or the definition itself is a
// floating promise. The full type-driven check (any Promise-typed expression
// statement) requires checker integration and is a Stage-B verifier concern.
func (l *lowerer) checkFloatingPromise(st *shimast.Node) {
	// MUTANT GATE_SKIP_FLOATING_PROMISE (ADR-07 §5 dir-ii, R1-10): skipping this
	// check lets an un-awaited (un-checkpointed) async effect through the gate.
	if mutants.Active("GATE_SKIP_FLOATING_PROMISE") {
		return
	}
	call := unparen(st.AsExpressionStatement().Expression)
	if call == nil || call.Kind != shimast.KindCallExpression {
		return
	}
	callee := unparen(call.AsCallExpression().Expression)
	if callee == nil || callee.Kind != shimast.KindIdentifier {
		return
	}
	name := callee.Text()
	// Innermost scope entry wins (mirrors valueIdent).
	for i := len(l.scopes) - 1; i >= 0; i-- {
		if l.scopes[i].name == name {
			if l.scopes[i].isAsyncFn {
				l.errorAt(st, CodeFloatingPromise, "un-awaited call of async %q: an un-awaited effect is un-checkpointed", name)
			}
			return
		}
	}
	if l.cur != nil && name == l.cur.name && l.cur.async {
		l.errorAt(st, CodeFloatingPromise, "un-awaited self-call of async %q", name)
		return
	}
	if sib, ok := l.siblings[name]; ok && sib.async {
		l.errorAt(st, CodeFloatingPromise, "un-awaited call of async %q: await it, return it, or pass to all/race", name)
	}
}
