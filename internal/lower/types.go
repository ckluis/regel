package lower

import (
	shimast "github.com/microsoft/typescript-go/shim/ast"
	"regel.dev/regel/internal/rast"
)

// typ lowers one tsgo type node to a regel-AST type (types are IN the hash,
// ADR-02 §3). Default-deny: an unhandled kind rejects with LOWER_UNSUPPORTED.
func (l *lowerer) typ(n *shimast.Node) *rast.Node {
	if n == nil {
		return noneNode()
	}
	switch n.Kind {
	case shimast.KindParenthesizedType:
		return l.typ(n.AsParenthesizedTypeNode().Type)

	// --- keyword primitives ---
	case shimast.KindNumberKeyword:
		return keywordType("number")
	case shimast.KindStringKeyword:
		return keywordType("string")
	case shimast.KindBooleanKeyword:
		return keywordType("boolean")
	case shimast.KindBigIntKeyword:
		return keywordType("bigint")
	case shimast.KindVoidKeyword:
		return keywordType("void")
	case shimast.KindUndefinedKeyword:
		return keywordType("undefined")
	case shimast.KindNeverKeyword:
		return keywordType("never")
	case shimast.KindUnknownKeyword:
		return keywordType("unknown")

	// --- banned type keywords ---
	case shimast.KindAnyKeyword:
		l.errorAt(n, CodeBanAny, "`any` is banned: use `unknown` and narrow")
		return keywordType("unknown")
	case shimast.KindObjectKeyword:
		l.errorAt(n, CodeBanObjectType, "`object` type is banned: write an explicit shape or Record<K, V>")
		return keywordType("unknown")
	case shimast.KindSymbolKeyword:
		l.errorAt(n, CodeBanSymbol, "`symbol` type is banned: use string/number keys")
		return keywordType("unknown")
	case shimast.KindThisType:
		l.errorAt(n, CodeBanThis, "`this` type is banned: pass state as an explicit parameter")
		return keywordType("unknown")

	// --- literal types ---
	case shimast.KindLiteralType:
		return &rast.Node{Kind: rast.TLiteral, Kids: []*rast.Node{l.expr(n.AsLiteralTypeNode().Literal, "")}}
	case shimast.KindNullKeyword, shimast.KindTrueKeyword, shimast.KindFalseKeyword:
		return &rast.Node{Kind: rast.TLiteral, Kids: []*rast.Node{l.expr(n, "")}}

	// --- composite ---
	case shimast.KindArrayType:
		return &rast.Node{Kind: rast.TArray, Kids: []*rast.Node{l.typ(n.AsArrayTypeNode().ElementType)}}
	case shimast.KindTupleType:
		return l.tupleType(n)
	case shimast.KindUnionType:
		return &rast.Node{Kind: rast.TUnion, Kids: []*rast.Node{l.typeList(n.AsUnionTypeNode().Types)}}
	case shimast.KindIntersectionType:
		return &rast.Node{Kind: rast.TInter, Kids: []*rast.Node{l.typeList(n.AsIntersectionTypeNode().Types)}}
	case shimast.KindTypeReference:
		return l.typeRef(n)
	case shimast.KindTypeLiteral:
		return &rast.Node{Kind: rast.TObject, Kids: []*rast.Node{l.typeMembers(n.AsTypeLiteralNode().Members.Nodes)}}
	case shimast.KindFunctionType:
		return l.functionType(n)
	case shimast.KindConditionalType:
		c := n.AsConditionalTypeNode()
		return &rast.Node{Kind: rast.TCond, Kids: []*rast.Node{
			l.typ(c.CheckType), l.typ(c.ExtendsType), l.typ(c.TrueType), l.typ(c.FalseType),
		}}
	case shimast.KindTypeOperator:
		return l.typeOperator(n)
	case shimast.KindIndexedAccessType:
		ia := n.AsIndexedAccessTypeNode()
		return &rast.Node{Kind: rast.TIndexed, Kids: []*rast.Node{l.typ(ia.ObjectType), l.typ(ia.IndexType)}}
	case shimast.KindTypeQuery:
		return &rast.Node{Kind: rast.TQuery, Kids: []*rast.Node{l.entityToExpr(n.AsTypeQueryNode().ExprName)}}
	case shimast.KindMappedType:
		return l.mappedType(n)
	case shimast.KindTemplateLiteralType:
		return l.templateLiteralType(n)

	// --- named residues (Stage A) ---
	case shimast.KindFunctionKeyword:
		l.errorAt(n, CodeBanFunctionType, "`Function` type is banned: write the explicit function type `(args) => ret`")
		return keywordType("unknown")
	case shimast.KindInferType:
		// STAGE-A RESIDUE: `infer` in conditional types has no production yet.
		l.errorAt(n, CodeLowerUnsupported, "`infer` in a conditional type has no production (Stage A)")
		return keywordType("unknown")
	default:
		l.errorAt(n, CodeLowerUnsupported, "type %s has no regel-AST production (default-deny)", n.Kind.String())
		return keywordType("unknown")
	}
}

func keywordType(kw string) *rast.Node { return &rast.Node{Kind: rast.TKeyword, Str: kw} }

func (l *lowerer) typeList(lst *shimast.NodeList) *rast.Node {
	if lst == nil {
		return listNode(nil)
	}
	out := make([]*rast.Node, 0, len(lst.Nodes))
	for _, t := range lst.Nodes {
		out = append(out, l.typ(t))
	}
	return listNode(out)
}

func (l *lowerer) tupleType(n *shimast.Node) *rast.Node {
	var out []*rast.Node
	for _, el := range n.AsTupleTypeNode().Elements.Nodes {
		switch el.Kind {
		case shimast.KindNamedTupleMember:
			// Labels are trivia for identity; keep only the element type.
			out = append(out, l.typ(el.AsNamedTupleMember().Type))
		case shimast.KindRestType, shimast.KindOptionalType:
			l.errorAt(el, CodeLowerUnsupported, "variadic/optional tuple element has no production (Stage A)")
			out = append(out, keywordType("unknown"))
		default:
			out = append(out, l.typ(el))
		}
	}
	return &rast.Node{Kind: rast.TTuple, Kids: []*rast.Node{listNode(out)}}
}

func (l *lowerer) typeRef(n *shimast.Node) *rast.Node {
	tr := n.AsTypeReferenceNode()
	if tr.TypeName == nil || tr.TypeName.Kind != shimast.KindIdentifier {
		l.errorAt(n, CodeLowerUnsupported, "qualified type name has no production (namespaces are banned)")
		return keywordType("unknown")
	}
	name := tr.TypeName.Text()
	args := l.typeArgsNode(tr.TypeArguments)
	if name == "Function" {
		l.errorAt(n, CodeBanFunctionType, "`Function` type is banned: write the explicit function type `(args) => ret`")
		return keywordType("unknown")
	}
	// 1. type parameter in scope → De Bruijn TLocal.
	for i := len(l.typeScope) - 1; i >= 0; i-- {
		if l.typeScope[i] == name {
			return &rast.Node{Kind: rast.TLocal, U: uint64(len(l.typeScope) - 1 - i), Kids: []*rast.Node{args}}
		}
	}
	// 2. self-reference (recursive type/interface).
	if l.cur != nil && name == l.cur.name && (l.cur.kind == rast.DefType || l.cur.kind == rast.DefInterface) {
		return &rast.Node{Kind: rast.KSelfRef, Kids: []*rast.Node{args}}
	}
	// 3. same-module sibling type/interface → catalogued placeholder ref.
	if sib, ok := l.siblings[name]; ok && sib != l.cur && (sib.kind == rast.DefType || sib.kind == rast.DefInterface) {
		if l.cur != nil {
			l.cur.sibs[name] = true
			key := sibPlaceholder + name
			l.cur.deps[key] = rast.Dep{Name: name, Module: l.ctx.ModuleName}
			return &rast.Node{Kind: rast.TCatRef, Str: key, Kids: []*rast.Node{args}}
		}
	}
	// 4. imported type → catalogued ref (address known).
	if imp, ok := l.imports[name]; ok {
		if l.cur != nil {
			l.cur.deps[imp.hash] = rast.Dep{Name: imp.name, Module: imp.module, Hash: imp.hash}
		}
		return &rast.Node{Kind: rast.TCatRef, Str: imp.hash, Kids: []*rast.Node{args}}
	}
	// 5. lib global (Array, Record, Promise, …); the checker resolves it.
	return &rast.Node{Kind: rast.TRef, Str: name, Kids: []*rast.Node{args}}
}

func (l *lowerer) typeArgsNode(lst *shimast.NodeList) *rast.Node {
	if lst == nil {
		return listNode(nil)
	}
	out := make([]*rast.Node, 0, len(lst.Nodes))
	for _, t := range lst.Nodes {
		out = append(out, l.typ(t))
	}
	return listNode(out)
}

func (l *lowerer) functionType(n *shimast.Node) *rast.Node {
	fl := n.FunctionLikeData()
	tps := l.typeParams(typeParamNodes(fl.TypeParameters))
	var params []*rast.Node
	var pushed int
	if fl.Parameters != nil {
		for _, prm := range fl.Parameters.Nodes {
			pn, cnt := l.lowerParam(prm)
			params = append(params, pn)
			pushed += cnt
		}
	}
	ret := keywordType("unknown")
	if fl.Type != nil {
		ret = l.typ(fl.Type)
	}
	l.popValues(pushed)
	l.popTypeParams(typeParamNodes(fl.TypeParameters))
	return &rast.Node{Kind: rast.TFunc, Kids: []*rast.Node{tps, listNode(params), ret}}
}

func (l *lowerer) typeOperator(n *shimast.Node) *rast.Node {
	to := n.AsTypeOperatorNode()
	switch to.Operator {
	case shimast.KindKeyOfKeyword:
		return &rast.Node{Kind: rast.TKeyof, Kids: []*rast.Node{l.typ(to.Type)}}
	case shimast.KindReadonlyKeyword:
		return &rast.Node{Kind: rast.TReadonly, Kids: []*rast.Node{l.typ(to.Type)}}
	case shimast.KindUniqueKeyword:
		l.errorAt(n, CodeBanSymbol, "`unique symbol` is banned: use string/number keys")
		return keywordType("unknown")
	default:
		l.errorAt(n, CodeLowerUnsupported, "type operator %s has no production", to.Operator.String())
		return keywordType("unknown")
	}
}

func (l *lowerer) mappedType(n *shimast.Node) *rast.Node {
	mt := n.AsMappedTypeNode()
	tpDecl := mt.TypeParameter.AsTypeParameterDeclaration()
	kName := "K"
	if tpDecl.Name() != nil {
		kName = tpDecl.Name().Text()
		if !asciiIdent(kName) {
			l.errorAt(tpDecl.Name(), CodeBanNonASCIIIdent, "mapped-type key %q is not ASCII", kName)
			kName = "K"
		}
	}
	kNode := &rast.Node{Kind: rast.TParam, Str: kName, Kids: []*rast.Node{noneNode(), noneNode()}}
	// Push K before lowering the source constraint so De Bruijn indices match the
	// printer, which brings the mapped key into scope before rendering the domain.
	l.typeScope = append(l.typeScope, kName)
	src := keywordType("unknown")
	if tpDecl.Constraint != nil {
		src = l.typ(tpDecl.Constraint)
	}
	as := noneNode()
	if mt.NameType != nil {
		as = l.typ(mt.NameType)
	}
	val := keywordType("unknown")
	if mt.Type != nil {
		val = l.typ(mt.Type)
	}
	l.typeScope = l.typeScope[:len(l.typeScope)-1]

	var u uint64
	u |= plusMinus(mt.ReadonlyToken)      // bits 0-1
	u |= plusMinus(mt.QuestionToken) << 2 // bits 2-3
	return &rast.Node{Kind: rast.TMapped, U: u, Kids: []*rast.Node{kNode, src, as, val}}
}

// plusMinus maps a mapped-type +/- modifier token to 0 (absent), 1 (+/plain), or
// 2 (-).
func plusMinus(tok *shimast.Node) uint64 {
	if tok == nil {
		return 0
	}
	if tok.Kind == shimast.KindMinusToken {
		return 2
	}
	return 1
}

func (l *lowerer) templateLiteralType(n *shimast.Node) *rast.Node {
	tl := n.AsTemplateLiteralTypeNode()
	parts := []*rast.Node{{Kind: rast.KStrPart, Str: tl.Head.Text()}}
	for _, sp := range tl.TemplateSpans.Nodes {
		s := sp.AsTemplateLiteralTypeSpan()
		parts = append(parts, l.typ(s.Type))
		parts = append(parts, &rast.Node{Kind: rast.KStrPart, Str: s.Literal.Text()})
	}
	return &rast.Node{Kind: rast.TTemplLit, Kids: []*rast.Node{listNode(parts)}}
}

// entityToExpr lowers a typeof-type entity name (Identifier or QualifiedName) to
// the value expression the printer re-emits.
func (l *lowerer) entityToExpr(n *shimast.Node) *rast.Node {
	if n == nil {
		return noneNode()
	}
	switch n.Kind {
	case shimast.KindIdentifier:
		return l.valueIdent(n, n.Text())
	case shimast.KindQualifiedName:
		qn := n.AsQualifiedName()
		return &rast.Node{Kind: rast.KMember, Str: qn.Right.Text(), Kids: []*rast.Node{l.entityToExpr(qn.Left)}}
	default:
		l.errorAt(n, CodeLowerUnsupported, "typeof operand %s has no production", n.Kind.String())
		return noneNode()
	}
}

// --- type parameters ---

// typeParams lowers a type-parameter list to a KList of TParam and pushes every
// name onto the type scope. All names are pushed BEFORE constraints/defaults are
// lowered, so a constraint may reference a sibling parameter — exactly the
// printer's discipline (it brings the whole list into scope before rendering).
// Caller must balance with popTypeParams.
func (l *lowerer) typeParams(nodes []*shimast.Node) *rast.Node {
	if len(nodes) == 0 {
		return listNode(nil)
	}
	names := make([]string, len(nodes))
	for i, tp := range nodes {
		nm := "T"
		d := tp.AsTypeParameterDeclaration()
		if d.Name() != nil {
			nm = d.Name().Text()
			if !asciiIdent(nm) {
				l.errorAt(d.Name(), CodeBanNonASCIIIdent, "type parameter %q is not ASCII", nm)
				nm = "T"
			}
		}
		names[i] = nm
		l.typeScope = append(l.typeScope, nm)
	}
	out := make([]*rast.Node, len(nodes))
	for i, tp := range nodes {
		d := tp.AsTypeParameterDeclaration()
		constraint := noneNode()
		if d.Constraint != nil {
			constraint = l.typ(d.Constraint)
		}
		def := noneNode()
		if d.DefaultType != nil {
			def = l.typ(d.DefaultType)
		}
		out[i] = &rast.Node{Kind: rast.TParam, Str: names[i], Kids: []*rast.Node{constraint, def}}
	}
	return listNode(out)
}

func (l *lowerer) popTypeParams(nodes []*shimast.Node) {
	if len(nodes) == 0 {
		return
	}
	l.typeScope = l.typeScope[:len(l.typeScope)-len(nodes)]
}

// typeMembers lowers interface / type-literal members to a KList of TPropSig /
// TIndexSig (Normalize sorts them). Method signatures normalize to a property of
// function type (ADR-02 §2 one-form).
func (l *lowerer) typeMembers(members []*shimast.Node) *rast.Node {
	var out []*rast.Node
	for _, m := range members {
		switch m.Kind {
		case shimast.KindPropertySignature:
			ps := m.AsPropertySignatureDeclaration()
			key, ok := l.propSigKey(m.Name())
			if !ok {
				continue
			}
			var u uint64
			if m.ModifierFlags()&shimast.ModifierFlagsReadonly != 0 {
				u |= 1
			}
			if ps.PostfixToken != nil && ps.PostfixToken.Kind == shimast.KindQuestionToken {
				u |= 2
			}
			t := keywordType("unknown")
			if ps.Type != nil {
				t = l.typ(ps.Type)
			}
			out = append(out, &rast.Node{Kind: rast.TPropSig, Str: key, U: u, Kids: []*rast.Node{t}})
		case shimast.KindIndexSignature:
			is := m.AsIndexSignatureDeclaration()
			fl := m.FunctionLikeData()
			keyType := keywordType("string")
			if fl.Parameters != nil && len(fl.Parameters.Nodes) == 1 {
				p := fl.Parameters.Nodes[0].AsParameterDeclaration()
				if p.Type != nil {
					keyType = l.typ(p.Type)
				}
			}
			valType := keywordType("unknown")
			if is.Type != nil {
				valType = l.typ(is.Type)
			}
			var u uint64
			if m.ModifierFlags()&shimast.ModifierFlagsReadonly != 0 {
				u |= 1
			}
			out = append(out, &rast.Node{Kind: rast.TIndexSig, U: u, Kids: []*rast.Node{keyType, valType}})
		case shimast.KindMethodSignature:
			key, ok := l.propSigKey(m.Name())
			if !ok {
				continue
			}
			ft := l.methodSignatureType(m)
			var u uint64
			if pf := postfixQuestion(m); pf {
				u |= 2
			}
			out = append(out, &rast.Node{Kind: rast.TPropSig, Str: key, U: u, Kids: []*rast.Node{ft}})
		case shimast.KindGetAccessor, shimast.KindSetAccessor:
			l.errorAt(m, CodeBanGetSet, "getters/setters are banned: use plain properties and explicit functions")
		case shimast.KindCallSignature, shimast.KindConstructSignature:
			// STAGE-A RESIDUE: call/construct signatures have no production yet.
			l.errorAt(m, CodeLowerUnsupported, "call/construct signatures have no production (Stage A): use a function-type property")
		default:
			l.errorAt(m, CodeLowerUnsupported, "type member %s has no production", m.Kind.String())
		}
	}
	return listNode(out)
}

// methodSignatureType renders a method signature's call shape as a TFunc, so the
// member normalizes to `name: (args) => ret`.
func (l *lowerer) methodSignatureType(m *shimast.Node) *rast.Node {
	fl := m.FunctionLikeData()
	tps := l.typeParams(typeParamNodes(fl.TypeParameters))
	var params []*rast.Node
	var pushed int
	if fl.Parameters != nil {
		for _, prm := range fl.Parameters.Nodes {
			pn, cnt := l.lowerParam(prm)
			params = append(params, pn)
			pushed += cnt
		}
	}
	ret := keywordType("unknown")
	if fl.Type != nil {
		ret = l.typ(fl.Type)
	}
	l.popValues(pushed)
	l.popTypeParams(typeParamNodes(fl.TypeParameters))
	return &rast.Node{Kind: rast.TFunc, Kids: []*rast.Node{tps, listNode(params), ret}}
}

func postfixQuestion(m *shimast.Node) bool {
	if m.Kind == shimast.KindMethodSignature {
		ms := m.AsMethodSignatureDeclaration()
		return ms.PostfixToken != nil && ms.PostfixToken.Kind == shimast.KindQuestionToken
	}
	return false
}

// propSigKey extracts a string key for a type-member signature; computed and
// non-ASCII cases are rejected.
func (l *lowerer) propSigKey(name *shimast.Node) (string, bool) {
	if name == nil {
		return "", false
	}
	switch name.Kind {
	case shimast.KindIdentifier:
		return name.Text(), true
	case shimast.KindStringLiteral:
		l.checkStringToken(name)
		return name.Text(), true
	case shimast.KindNumericLiteral:
		return name.Text(), true
	case shimast.KindPrivateIdentifier:
		l.errorAt(name, CodeBanClass, "#private names are banned with classes")
		return "", false
	case shimast.KindComputedPropertyName:
		l.errorAt(name, CodeLowerUnsupported, "computed property signature has no production: use an index signature")
		return "", false
	default:
		l.errorAt(name, CodeLowerUnsupported, "property-signature key %s has no production", name.Kind.String())
		return "", false
	}
}

// asciiIdent reports whether s matches [A-Za-z_$][A-Za-z0-9_$]* (ADR-01 §2
// BAN_NONASCII_IDENT: identifiers are ASCII; human language lives in strings).
func asciiIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || c == '$':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}
