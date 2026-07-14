package lower

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	shimast "github.com/microsoft/typescript-go/shim/ast"
	"regel.dev/regel/internal/mutants"
	"regel.dev/regel/internal/rast"
)

// expr lowers one tsgo expression node to a regel-AST expression. Default-deny:
// any kind with no production rejects with LOWER_UNSUPPORTED. path is the rast
// node path (expression-level comments are dropped per ADR-02 §2, so it is
// threaded only for symmetry with statement lowering).
func (l *lowerer) expr(n *shimast.Node, path string) *rast.Node {
	_ = path
	if n == nil {
		return noneNode()
	}
	switch n.Kind {
	case shimast.KindParenthesizedExpression:
		return l.expr(n.AsParenthesizedExpression().Expression, "")

	// --- literals & atoms ---
	case shimast.KindNumericLiteral:
		return l.numLiteral(n, n.AsNumericLiteral().Text, false)
	case shimast.KindBigIntLiteral:
		return l.bigLiteral(n, n.AsBigIntLiteral().Text, false)
	case shimast.KindStringLiteral:
		s := n.AsStringLiteral().Text
		l.checkStringToken(n)
		return &rast.Node{Kind: rast.KStr, Str: s}
	case shimast.KindTrueKeyword:
		return &rast.Node{Kind: rast.KBool, U: 1}
	case shimast.KindFalseKeyword:
		return &rast.Node{Kind: rast.KBool, U: 0}
	case shimast.KindNullKeyword:
		return &rast.Node{Kind: rast.KNull}
	case shimast.KindRegularExpressionLiteral:
		return l.regexLiteral(n, n.AsRegularExpressionLiteral().Text)
	case shimast.KindNoSubstitutionTemplateLiteral:
		part := &rast.Node{Kind: rast.KStrPart, Str: n.Text()}
		return &rast.Node{Kind: rast.KTemplate, Kids: []*rast.Node{listNode([]*rast.Node{part})}}
	case shimast.KindTemplateExpression:
		return l.template(n)

	// --- identifiers ---
	case shimast.KindIdentifier:
		return l.valueIdent(n, n.Text())
	case shimast.KindThisKeyword:
		l.errorAt(n, CodeBanThis, "`this` is banned: pass state as an explicit parameter")
		return noneNode()
	case shimast.KindSuperKeyword:
		l.errorAt(n, CodeBanClass, "`super` is banned: no classes")
		return noneNode()

	// --- containers ---
	case shimast.KindArrayLiteralExpression:
		return l.arrayLiteral(n)
	case shimast.KindObjectLiteralExpression:
		return l.objectLiteral(n)
	case shimast.KindSpreadElement:
		return &rast.Node{Kind: rast.KSpread, Kids: []*rast.Node{l.expr(n.AsSpreadElement().Expression, "")}}

	// --- access / call ---
	case shimast.KindPropertyAccessExpression:
		return l.propertyAccess(n)
	case shimast.KindElementAccessExpression:
		return l.elementAccess(n)
	case shimast.KindCallExpression:
		return l.callExpr(n)
	case shimast.KindNewExpression:
		l.errorAt(n, CodeBanNew, "`new` is banned: call the std factory function for the value you need")
		return noneNode()
	case shimast.KindTaggedTemplateExpression:
		l.errorAt(n, CodeBanTaggedTemplate, "tagged templates are banned: call an ordinary std builder function")
		return noneNode()

	// --- operators ---
	case shimast.KindBinaryExpression:
		return l.binaryExpr(n)
	case shimast.KindPrefixUnaryExpression:
		return l.prefixUnary(n)
	case shimast.KindPostfixUnaryExpression:
		return l.postfixUnary(n)
	case shimast.KindConditionalExpression:
		c := n.AsConditionalExpression()
		return &rast.Node{Kind: rast.KCond, Kids: []*rast.Node{
			l.expr(c.Condition, ""), l.expr(c.WhenTrue, ""), l.expr(c.WhenFalse, ""),
		}}
	case shimast.KindTypeOfExpression:
		return &rast.Node{Kind: rast.KTypeof, Kids: []*rast.Node{l.expr(n.AsTypeOfExpression().Expression, "")}}
	case shimast.KindAwaitExpression:
		return &rast.Node{Kind: rast.KAwait, Kids: []*rast.Node{l.expr(n.AsAwaitExpression().Expression, "")}}
	case shimast.KindVoidExpression:
		l.errorAt(n, CodeBanVoid, "`void` operator is banned: use `undefined` for the value, or a statement for the effect")
		return noneNode()
	case shimast.KindDeleteExpression:
		l.errorAt(n, CodeBanDelete, "`delete` is banned: build a new object without the key (object spread)")
		return noneNode()
	case shimast.KindNonNullExpression:
		l.errorAt(n, CodeBanNonNull, "non-null `!` is banned: narrow the value or handle the null/undefined case")
		return l.expr(n.AsNonNullExpression().Expression, "")
	case shimast.KindAsExpression:
		return l.asExpr(n)
	case shimast.KindSatisfiesExpression:
		s := n.AsSatisfiesExpression()
		return &rast.Node{Kind: rast.KSatisfy, Kids: []*rast.Node{l.expr(s.Expression, ""), l.typ(s.Type)}}
	case shimast.KindTypeAssertionExpression:
		l.errorAt(n, CodeBanAsCast, "`<T>` type assertion is banned: use `satisfies` or narrow with `unknown`")
		return l.expr(n.AsTypeAssertion().Expression, "")

	// --- functions ---
	case shimast.KindArrowFunction:
		async := n.ModifierFlags()&shimast.ModifierFlagsAsync != 0
		return l.lowerFunctionLike(n, n.BodyData().Body, async, "")
	case shimast.KindFunctionExpression:
		// STAGE-A RESIDUE: a named function expression's self-name is dropped
		// (normalized to an anonymous arrow); a body that self-references by that
		// name becomes a free KName. Rewrite as `const f = (…) => …` for recursion.
		if n.BodyData().AsteriskToken != nil {
			l.errorAt(n, CodeBanGenerator, "generators are banned: use std Iter<T>")
			return noneNode()
		}
		async := n.ModifierFlags()&shimast.ModifierFlagsAsync != 0
		return l.lowerFunctionLike(n, n.BodyData().Body, async, "")
	case shimast.KindYieldExpression:
		l.errorAt(n, CodeBanGenerator, "`yield` is banned: use std Iter<T>/AsyncIter<T>")
		return noneNode()
	case shimast.KindOmittedExpression:
		return noneNode()

	default:
		l.errorAt(n, CodeLowerUnsupported, "expression %s has no regel-AST production (default-deny)", n.Kind.String())
		return noneNode()
	}
}

// valueIdent resolves an identifier reference to a De Bruijn local, self/sibling/
// import reference, keyword, or free name — applying capture rule R1 bookkeeping.
func (l *lowerer) valueIdent(n *shimast.Node, name string) *rast.Node {
	// 1. innermost value binder in scope (shadows everything else).
	for i := len(l.scopes) - 1; i >= 0; i-- {
		if l.scopes[i].name == name {
			e := l.scopes[i]
			if !e.isConst && e.funcDepth < l.funcDepth {
				line, col := l.posOf(n)
				l.captures = append(l.captures, captureEvent{entry: e, name: name, line: line, col: col})
			}
			return &rast.Node{Kind: rast.KLocal, U: uint64(len(l.scopes) - 1 - i)}
		}
	}
	// 2. self-reference (self-recursion; never a sibling edge / cycle).
	if l.cur != nil && name == l.cur.name {
		return &rast.Node{Kind: rast.KSelfRef}
	}
	// 3. same-module sibling → Merkle placeholder ref (patched to its address).
	if sib, ok := l.siblings[name]; ok && (l.cur == nil || sib != l.cur) {
		if l.cur != nil {
			l.cur.sibs[name] = true
			key := sibPlaceholder + name
			l.cur.deps[key] = rast.Dep{Name: name, Module: l.ctx.ModuleName}
			return &rast.Node{Kind: rast.KRef, Str: key}
		}
	}
	// 4. named import → catalogued dep edge (address known).
	if imp, ok := l.imports[name]; ok {
		if l.cur != nil {
			l.cur.deps[imp.hash] = rast.Dep{Name: imp.name, Module: imp.module, Hash: imp.hash}
		}
		return &rast.Node{Kind: rast.KRef, Str: imp.hash}
	}
	// 5. keyword-ish and banned globals.
	switch name {
	case "undefined":
		return &rast.Node{Kind: rast.KUndefined}
	case "Symbol":
		l.errorAt(n, CodeBanSymbol, "`Symbol` is banned: use string/number keys")
		return noneNode()
	case "eval", "Proxy", "Reflect":
		l.errorAt(n, CodeBanWithEval, "`%s` is banned: dynamic scope/interception is ungovernable — use std", name)
		return noneNode()
	}
	// 6. free name (a lib global; the checker resolves or rejects it later).
	return &rast.Node{Kind: rast.KName, Str: name}
}

// markReassign flags the innermost binder named name as reassigned (R1 input).
func (l *lowerer) markReassign(target *shimast.Node) {
	target = unparen(target)
	if target == nil || target.Kind != shimast.KindIdentifier {
		return
	}
	name := target.Text()
	for i := len(l.scopes) - 1; i >= 0; i-- {
		if l.scopes[i].name == name {
			l.scopes[i].reassigned = true
			return
		}
	}
}

func (l *lowerer) arrayLiteral(n *shimast.Node) *rast.Node {
	var elems []*rast.Node
	for _, e := range n.AsArrayLiteralExpression().Elements.Nodes {
		if e.Kind == shimast.KindOmittedExpression {
			elems = append(elems, noneNode())
			continue
		}
		elems = append(elems, l.expr(e, ""))
	}
	return &rast.Node{Kind: rast.KArray, Kids: []*rast.Node{listNode(elems)}}
}

func (l *lowerer) objectLiteral(n *shimast.Node) *rast.Node {
	var props []*rast.Node
	for _, pr := range n.AsObjectLiteralExpression().Properties.Nodes {
		switch pr.Kind {
		case shimast.KindPropertyAssignment:
			pa := pr.AsPropertyAssignment()
			key, computed := l.propertyKey(pa.Name(), nil)
			props = append(props, &rast.Node{Kind: rast.KProp, U: boolBit(computed),
				Kids: []*rast.Node{key, l.expr(pa.Initializer, "")}})
		case shimast.KindShorthandPropertyAssignment:
			sp := pr.AsShorthandPropertyAssignment()
			nameNode := sp.Name()
			nm := nameNode.Text()
			if !asciiIdent(nm) {
				l.errorAt(nameNode, CodeBanNonASCIIIdent, "shorthand property %q is not ASCII", nm)
			}
			key := &rast.Node{Kind: rast.KStrPart, Str: nm}
			props = append(props, &rast.Node{Kind: rast.KProp, U: 0,
				Kids: []*rast.Node{key, l.valueIdent(nameNode, nm)}})
		case shimast.KindSpreadAssignment:
			props = append(props, &rast.Node{Kind: rast.KSpread,
				Kids: []*rast.Node{l.expr(pr.AsSpreadAssignment().Expression, "")}})
		case shimast.KindMethodDeclaration:
			// ADR-02 §2: method shorthand normalizes to an arrow property (one form).
			md := pr.AsMethodDeclaration()
			if md.BodyData() != nil && md.BodyData().AsteriskToken != nil {
				l.errorAt(pr, CodeBanGenerator, "generator methods are banned: use std Iter<T>")
				continue
			}
			key, computed := l.propertyKey(pr.Name(), nil)
			async := pr.ModifierFlags()&shimast.ModifierFlagsAsync != 0
			fn := l.lowerFunctionLike(pr, pr.BodyData().Body, async, "")
			props = append(props, &rast.Node{Kind: rast.KProp, U: boolBit(computed),
				Kids: []*rast.Node{key, fn}})
		case shimast.KindGetAccessor, shimast.KindSetAccessor:
			l.errorAt(pr, CodeBanGetSet, "getters/setters are banned: use plain properties and explicit functions")
		default:
			l.errorAt(pr, CodeLowerUnsupported, "object member %s has no production", pr.Kind.String())
		}
	}
	return &rast.Node{Kind: rast.KObject, Kids: []*rast.Node{listNode(props)}}
}

func (l *lowerer) propertyAccess(n *shimast.Node) *rast.Node {
	pa := n.AsPropertyAccessExpression()
	obj := l.expr(pa.Expression, "")
	nameNode := pa.Name()
	if nameNode.Kind == shimast.KindPrivateIdentifier {
		l.errorAt(nameNode, CodeBanClass, "#private names are banned with classes")
		return obj
	}
	prop := nameNode.Text()
	if prop == "call" || prop == "apply" || prop == "bind" {
		l.errorAt(n, CodeBanThis, "`.call`/`.apply`/`.bind` are banned: invoke the function with a direct call")
	}
	var u uint64
	if pa.QuestionDotToken != nil {
		u |= 1
	}
	return &rast.Node{Kind: rast.KMember, Str: prop, U: u, Kids: []*rast.Node{obj}}
}

func (l *lowerer) elementAccess(n *shimast.Node) *rast.Node {
	ea := n.AsElementAccessExpression()
	var u uint64
	if ea.QuestionDotToken != nil {
		u |= 1
	}
	return &rast.Node{Kind: rast.KIndex, U: u, Kids: []*rast.Node{
		l.expr(ea.Expression, ""), l.expr(ea.ArgumentExpression, ""),
	}}
}

func (l *lowerer) callExpr(n *shimast.Node) *rast.Node {
	ce := n.AsCallExpression()
	callee := l.expr(ce.Expression, "")
	var args []*rast.Node
	if ce.Arguments != nil {
		for _, a := range ce.Arguments.Nodes {
			args = append(args, l.expr(a, ""))
		}
	}
	typeArgs := noneNode()
	if ce.TypeArguments != nil && len(ce.TypeArguments.Nodes) > 0 {
		var ta []*rast.Node
		for _, t := range ce.TypeArguments.Nodes {
			ta = append(ta, l.typ(t))
		}
		typeArgs = listNode(ta)
	}
	var u uint64
	if ce.QuestionDotToken != nil {
		u |= 1
	}
	return &rast.Node{Kind: rast.KCall, U: u, Kids: []*rast.Node{callee, listNode(args), typeArgs}}
}

func (l *lowerer) asExpr(n *shimast.Node) *rast.Node {
	ae := n.AsAsExpression()
	if isConstTypeRef(ae.Type) {
		return &rast.Node{Kind: rast.KAsConst, Kids: []*rast.Node{l.expr(ae.Expression, "")}}
	}
	// MUTANT GATE_ALLOW_BANNED_SYNTAX (ADR-07 §5 dir-ii, R1-10): widening the
	// `as`-cast matcher to accept ANY cast (not just `as const`) lets a banned
	// type-assertion form slip past the relocated ADR-01 §2 subset ban.
	if mutants.Active("GATE_ALLOW_BANNED_SYNTAX") {
		return l.expr(ae.Expression, "")
	}
	l.errorAt(n, CodeBanAsCast, "`as` cast is banned (except `as const`): use `satisfies` or narrow with `unknown`")
	return l.expr(ae.Expression, "")
}

// isConstTypeRef reports whether a type node is the bare `const` reference of an
// `as const` assertion (a TypeReference named "const" with no type arguments).
func isConstTypeRef(t *shimast.Node) bool {
	if t == nil || t.Kind != shimast.KindTypeReference {
		return false
	}
	tr := t.AsTypeReferenceNode()
	if tr.TypeArguments != nil && len(tr.TypeArguments.Nodes) > 0 {
		return false
	}
	return tr.TypeName != nil && tr.TypeName.Kind == shimast.KindIdentifier && tr.TypeName.Text() == "const"
}

func (l *lowerer) binaryExpr(n *shimast.Node) *rast.Node {
	be := n.AsBinaryExpression()
	opTok := be.OperatorToken.Kind
	if opTok == shimast.KindCommaToken {
		l.errorAt(n, CodeBanComma, "comma operator is banned: split into separate statements")
		return noneNode()
	}
	if opTok == shimast.KindInstanceOfKeyword {
		l.errorAt(n, CodeBanInstanceof, "`instanceof` is banned (no prototypes): narrow with `typeof`, `in`, or a discriminant tag")
		return noneNode()
	}
	op, ok := binaryOp(opTok)
	if !ok {
		l.errorAt(n, CodeLowerUnsupported, "binary operator %s has no production", opTok.String())
		return noneNode()
	}
	if isAssignOp(op) {
		l.markReassign(be.Left)
	}
	return &rast.Node{Kind: rast.KBinary, U: uint64(op), Kids: []*rast.Node{
		l.expr(be.Left, ""), l.expr(be.Right, ""),
	}}
}

func (l *lowerer) prefixUnary(n *shimast.Node) *rast.Node {
	pu := n.AsPrefixUnaryExpression()
	operand := unparen(pu.Operand)
	switch pu.Operator {
	case shimast.KindMinusToken:
		if operand != nil && operand.Kind == shimast.KindNumericLiteral {
			return l.numLiteral(n, operand.AsNumericLiteral().Text, true)
		}
		if operand != nil && operand.Kind == shimast.KindBigIntLiteral {
			return l.bigLiteral(n, operand.AsBigIntLiteral().Text, true)
		}
		return &rast.Node{Kind: rast.KUnary, U: uint64(rast.OpNeg), Kids: []*rast.Node{l.expr(pu.Operand, "")}}
	case shimast.KindPlusToken:
		if operand != nil && operand.Kind == shimast.KindNumericLiteral {
			return l.numLiteral(n, operand.AsNumericLiteral().Text, false)
		}
		return &rast.Node{Kind: rast.KUnary, U: uint64(rast.OpPos), Kids: []*rast.Node{l.expr(pu.Operand, "")}}
	case shimast.KindExclamationToken:
		return &rast.Node{Kind: rast.KUnary, U: uint64(rast.OpNot), Kids: []*rast.Node{l.expr(pu.Operand, "")}}
	case shimast.KindTildeToken:
		return &rast.Node{Kind: rast.KUnary, U: uint64(rast.OpBitNot), Kids: []*rast.Node{l.expr(pu.Operand, "")}}
	case shimast.KindPlusPlusToken, shimast.KindMinusMinusToken:
		l.markReassign(pu.Operand)
		op := rast.OpInc
		if pu.Operator == shimast.KindMinusMinusToken {
			op = rast.OpDec
		}
		return &rast.Node{Kind: rast.KUpdate, U: uint64(op)<<1 | 1, Kids: []*rast.Node{l.expr(pu.Operand, "")}}
	default:
		l.errorAt(n, CodeLowerUnsupported, "prefix operator %s has no production", pu.Operator.String())
		return noneNode()
	}
}

func (l *lowerer) postfixUnary(n *shimast.Node) *rast.Node {
	pu := n.AsPostfixUnaryExpression()
	l.markReassign(pu.Operand)
	op := rast.OpInc
	if pu.Operator == shimast.KindMinusMinusToken {
		op = rast.OpDec
	}
	return &rast.Node{Kind: rast.KUpdate, U: uint64(op) << 1, Kids: []*rast.Node{l.expr(pu.Operand, "")}}
}

func (l *lowerer) template(n *shimast.Node) *rast.Node {
	te := n.AsTemplateExpression()
	parts := []*rast.Node{{Kind: rast.KStrPart, Str: te.Head.Text()}}
	for _, sp := range te.TemplateSpans.Nodes {
		ts := sp.AsTemplateSpan()
		parts = append(parts, l.expr(ts.Expression, ""))
		parts = append(parts, &rast.Node{Kind: rast.KStrPart, Str: ts.Literal.Text()})
	}
	return &rast.Node{Kind: rast.KTemplate, Kids: []*rast.Node{listNode(parts)}}
}

// --- literal parsing ---

func (l *lowerer) numLiteral(n *shimast.Node, text string, negate bool) *rast.Node {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		l.errorAt(n, CodeBanNonFinite, "numeric literal %q has no canonical f64 encoding", text)
		return noneNode()
	}
	if negate {
		f = -f
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		l.errorAt(n, CodeBanNonFinite, "non-finite numeric literal: only finite f64 literals have a canonical encoding")
		return noneNode()
	}
	return &rast.Node{Kind: rast.KNum, U: math.Float64bits(f)}
}

func (l *lowerer) bigLiteral(n *shimast.Node, text string, negate bool) *rast.Node {
	s := strings.TrimSuffix(text, "n")
	z, ok := new(big.Int).SetString(s, 0)
	if !ok {
		l.errorAt(n, CodeLowerUnsupported, "bigint literal %q could not be parsed", text)
		return noneNode()
	}
	if negate {
		z.Neg(z)
	}
	var u uint64
	if z.Sign() < 0 {
		u = 1
	}
	mag := new(big.Int).Abs(z).Bytes() // minimal big-endian, empty for zero
	return &rast.Node{Kind: rast.KBigInt, U: u, Mag: mag}
}

func (l *lowerer) regexLiteral(n *shimast.Node, text string) *rast.Node {
	// text is the full literal "/pattern/flags".
	last := strings.LastIndexByte(text, '/')
	if len(text) < 2 || text[0] != '/' || last <= 0 {
		l.errorAt(n, CodeLowerUnsupported, "malformed regex literal")
		return noneNode()
	}
	pattern := text[1:last]
	flagStr := text[last+1:]
	if hasRegexBacktracking(pattern) {
		l.errorAt(n, CodeBanRegexBacktrack,
			"regex backreference/lookaround is banned: the engine is RE2 — rewrite without backtracking")
		return noneNode()
	}
	var flags uint64
	for _, c := range flagStr {
		switch c {
		case 'd':
			flags |= rast.RegexFlagD
		case 'g':
			flags |= rast.RegexFlagG
		case 'i':
			flags |= rast.RegexFlagI
		case 'm':
			flags |= rast.RegexFlagM
		case 's':
			flags |= rast.RegexFlagS
		case 'u':
			flags |= rast.RegexFlagU
		case 'v':
			flags |= rast.RegexFlagV
		case 'y':
			flags |= rast.RegexFlagY
		default:
			l.errorAt(n, CodeLowerUnsupported, "unknown regex flag %q", string(c))
		}
	}
	return &rast.Node{Kind: rast.KRegex, Str: pattern, U: flags}
}

// hasRegexBacktracking reports lookaround or backreferences, which RE2 forbids.
// Syntactic scan: honors backslash-escapes and character classes so `\\(` or a
// `[(?=]` class member is not mistaken for a group.
func hasRegexBacktracking(p string) bool {
	inClass := false
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			if i+1 < len(p) {
				next := p[i+1]
				if !inClass && next >= '1' && next <= '9' {
					return true // numeric backreference
				}
				if !inClass && next == 'k' {
					return true // named backreference \k<name>
				}
			}
			i++ // skip escaped char
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			if !inClass && i+2 < len(p) && p[i+1] == '?' {
				switch p[i+2] {
				case '=', '!':
					return true // lookahead
				case '<':
					if i+3 < len(p) && (p[i+3] == '=' || p[i+3] == '!') {
						return true // lookbehind
					}
				}
			}
		}
	}
	return false
}

// checkStringToken rejects a string literal carrying a lone surrogate (tsgo
// decodes it to WTF-8, which is invalid UTF-8).
func (l *lowerer) checkStringToken(n *shimast.Node) {
	if !utf8.ValidString(n.Text()) {
		l.errorAt(n, CodeBanLoneSurrogate, "lone surrogate in string: use well-formed Unicode code points")
	}
}

// --- operator tables ---

func binaryOp(k shimast.Kind) (rast.OpKind, bool) {
	switch k {
	case shimast.KindPlusToken:
		return rast.OpAdd, true
	case shimast.KindMinusToken:
		return rast.OpSub, true
	case shimast.KindAsteriskToken:
		return rast.OpMul, true
	case shimast.KindSlashToken:
		return rast.OpDiv, true
	case shimast.KindPercentToken:
		return rast.OpMod, true
	case shimast.KindAsteriskAsteriskToken:
		return rast.OpExp, true
	case shimast.KindLessThanLessThanToken:
		return rast.OpShl, true
	case shimast.KindGreaterThanGreaterThanToken:
		return rast.OpShr, true
	case shimast.KindGreaterThanGreaterThanGreaterThanToken:
		return rast.OpUShr, true
	case shimast.KindAmpersandToken:
		return rast.OpBitAnd, true
	case shimast.KindBarToken:
		return rast.OpBitOr, true
	case shimast.KindCaretToken:
		return rast.OpBitXor, true
	case shimast.KindLessThanToken:
		return rast.OpLt, true
	case shimast.KindGreaterThanToken:
		return rast.OpGt, true
	case shimast.KindLessThanEqualsToken:
		return rast.OpLe, true
	case shimast.KindGreaterThanEqualsToken:
		return rast.OpGe, true
	case shimast.KindEqualsEqualsToken:
		return rast.OpEqEq, true
	case shimast.KindExclamationEqualsToken:
		return rast.OpNeEq, true
	case shimast.KindEqualsEqualsEqualsToken:
		return rast.OpEqEqEq, true
	case shimast.KindExclamationEqualsEqualsToken:
		return rast.OpNeEqEq, true
	case shimast.KindAmpersandAmpersandToken:
		return rast.OpAnd, true
	case shimast.KindBarBarToken:
		return rast.OpOr, true
	case shimast.KindQuestionQuestionToken:
		return rast.OpNullish, true
	case shimast.KindInKeyword:
		return rast.OpIn, true
	case shimast.KindEqualsToken:
		return rast.OpAssign, true
	case shimast.KindPlusEqualsToken:
		return rast.OpAddAssign, true
	case shimast.KindMinusEqualsToken:
		return rast.OpSubAssign, true
	case shimast.KindAsteriskEqualsToken:
		return rast.OpMulAssign, true
	case shimast.KindSlashEqualsToken:
		return rast.OpDivAssign, true
	case shimast.KindPercentEqualsToken:
		return rast.OpModAssign, true
	case shimast.KindAsteriskAsteriskEqualsToken:
		return rast.OpExpAssign, true
	case shimast.KindLessThanLessThanEqualsToken:
		return rast.OpShlAssign, true
	case shimast.KindGreaterThanGreaterThanEqualsToken:
		return rast.OpShrAssign, true
	case shimast.KindGreaterThanGreaterThanGreaterThanEqualsToken:
		return rast.OpUShrAssign, true
	case shimast.KindAmpersandEqualsToken:
		return rast.OpBitAndAssign, true
	case shimast.KindBarEqualsToken:
		return rast.OpBitOrAssign, true
	case shimast.KindCaretEqualsToken:
		return rast.OpBitXorAssign, true
	case shimast.KindAmpersandAmpersandEqualsToken:
		return rast.OpAndAssign, true
	case shimast.KindBarBarEqualsToken:
		return rast.OpOrAssign, true
	case shimast.KindQuestionQuestionEqualsToken:
		return rast.OpNullAssign, true
	default:
		return rast.OpNone, false
	}
}

func isAssignOp(op rast.OpKind) bool { return op >= rast.OpAssign && op <= rast.OpNullAssign }
