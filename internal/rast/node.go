// Package rast is the owned regel-AST: a closed set of node kinds covering
// exactly the ADR-01 §3 admitted surface (statements, expressions, and types —
// types are IN the hash per ADR-02 §3), plus the identity primitives Ref(hash),
// SelfRef, and NativeBody (ADR-10 §1). It owns four artifacts that must stay
// bit-stable per epoch (ADR-02 §6): the node schema, normalize, canonEncode
// (the TLV binary format), and the SHA-256 content address.
//
// # What is hashed (ADR-02 §1)
//
//	hash = SHA-256( domain ‖ canonEncode( normalize( ast ) ) )
//	domain = "regel-ast/1\n"   (schema version r1)
//
// Canonical text is a derived projection (Print), defended by the round-trip
// guarantees (ADR-02 §5), never a hash input. Identity is severed from
// formatting and from tsgo: the encoding is over the owned regel-AST only.
//
// # Node representation
//
// Every node is one uniform struct: a Kind tag plus three optional scalar
// payloads (Str, U, Mag) and an ordered child list Kids. Which scalars a kind
// uses is fixed by the schema table (see schema.go); this is what makes
// canonEncode total and deterministic — one tag byte, the schema-declared
// scalars in fixed order, then a length-prefixed child list, yielding exactly
// one byte sequence per normalized AST.
//
// Variable-arity positions (block statements, call arguments, object members,
// union members, …) are wrapped in a single KList child so every other node is
// fixed-arity; absent optional children are the explicit KNone node. The result
// is that the encoder never needs per-kind child logic.
//
// # De Bruijn locals (ADR-02 §2)
//
// Local bindings and parameters carry no name in the hash: a reference to a
// local is a KLocal node whose U field is its De Bruijn index (0 = nearest
// enclosing binder). Display names live in a sidecar (LowerResult.DisplayNames)
// keyed by binder-introduction order and are used only by the printer. Renaming
// a local never changes a hash; alpha-equivalent definitions dedupe to one row.
package rast

// Kind is the closed regel-AST node-kind tag. Values are the TLV tag bytes and
// are STABLE for schema version r1 (append-only; never renumber within r1).
type Kind uint8

const (
	KNone Kind = 0 // absent optional child
	KList Kind = 1 // ordered child-list wrapper (Kids are the elements)

	// --- literals & atoms ---
	KNum       Kind = 10 // U = IEEE-754 f64 bit pattern (big-endian on the wire)
	KBigInt    Kind = 11 // U bit0 = sign (1=negative); Mag = minimal big-endian magnitude
	KStr       Kind = 12 // Str = code points (well-formed UTF-8, never NFC/NFD-mutated)
	KBool      Kind = 13 // U = 0|1
	KNull      Kind = 14
	KUndefined Kind = 15
	KRegex     Kind = 16 // Str = pattern code points; U = sorted-flags bitmask
	KTemplate  Kind = 17 // Kids = [KList of alternating KStrPart and expression nodes]
	KStrPart   Kind = 18 // Str = a literal template chunk

	// --- references / identity ---
	KLocal   Kind = 20 // U = De Bruijn index (display name in sidecar)
	KRef     Kind = 21 // Str = referent address (r1_…); a catalogued dep edge
	KSelfRef Kind = 22 // the definition's own name (self-recursion)
	KName    Kind = 23 // Str = an unresolved free name (typecheck rejects later)

	// --- expressions ---
	KArray    Kind = 30 // Kids = [KList of elements/spreads]
	KObject   Kind = 31 // Kids = [KList of KProp]
	KProp     Kind = 32 // U bit0 = computed key; Kids = [keyNode, value]
	KSpread   Kind = 33 // Kids = [expr]
	KCall     Kind = 34 // U bit0 = optional (?.()); Kids = [callee, KList args, typeArgs|KNone]
	KMember   Kind = 35 // U bit0 = optional (?.); Str = property; Kids = [obj]
	KIndex    Kind = 36 // U bit0 = optional; Kids = [obj, indexExpr]
	KBinary   Kind = 37 // U = OpKind; Kids = [left, right] (includes assignment & `in`)
	KUnary    Kind = 38 // U = OpKind (prefix); Kids = [operand]
	KUpdate   Kind = 39 // U bit0 = prefix, bits1.. = OpKind; Kids = [operand]
	KCond     Kind = 40 // ternary; Kids = [cond, whenTrue, whenFalse]
	KTypeof   Kind = 41 // Kids = [expr]
	KAwait    Kind = 42 // Kids = [expr]
	KAsConst  Kind = 43 // Kids = [expr]  (`as const`)
	KSatisfy  Kind = 44 // Kids = [expr, type]
	KFunc     Kind = 45 // U bit0 async, bit1 arrow-expr-body; Kids = [KList params, KList typeParams, retType|KNone, body]
	KParam    Kind = 46 // U bit0 rest; Kids = [pattern, type|KNone, default|KNone]

	// --- binding patterns (introduce De Bruijn binders) ---
	KBindId   Kind = 50 // simple binder (display name in sidecar)
	KArrayPat Kind = 51 // Kids = [KList of elem: KBindId|KArrayPat|KObjectPat|KRestPat|KNone(hole)]
	KObjPat   Kind = 52 // Kids = [KList of KBindProp]
	KBindProp Kind = 53 // U bit0 computed key; Kids = [keyNode, pattern, default|KNone]
	KRestPat  Kind = 54 // Kids = [pattern]

	// --- statements ---
	KBlock    Kind = 60 // Kids = [KList stmts]
	KVarDecl  Kind = 61 // U bit0 = const; Kids = [KList declarators]
	KDeclr    Kind = 62 // declarator; Kids = [pattern, type|KNone, init|KNone]
	KExprStmt Kind = 63 // Kids = [expr]
	KIf       Kind = 64 // Kids = [cond, then, else|KNone]
	KFor      Kind = 65 // Kids = [init|KNone, cond|KNone, incr|KNone, body]
	KForOf    Kind = 66 // U bit0 await; Kids = [decl, iterExpr, body]
	KWhile    Kind = 67 // Kids = [cond, body]
	KDoWhile  Kind = 68 // Kids = [body, cond]
	KSwitch   Kind = 69 // Kids = [disc, KList clauses]
	KClause   Kind = 70 // U bit0 default; Kids = [test|KNone, KList stmts]
	KBreak    Kind = 71
	KContinue Kind = 72
	KReturn   Kind = 73 // Kids = [expr|KNone]
	KThrow    Kind = 74 // Kids = [expr]
	KTry      Kind = 75 // Kids = [tryBlock, catch|KNone, finallyBlock|KNone]
	KCatch    Kind = 76 // U bit0 = hasParam; Kids = [pattern|KNone, block]

	// --- definitions ---
	KTypeAlias  Kind = 80 // Kids = [KList typeParams, type]
	KInterface  Kind = 81 // Kids = [KList typeParams, KList members(sorted)]
	KNativeBody Kind = 82 // Str = intrinsic symbol; Kids = [type]  (ADR-10 §1; no lowering production)

	// --- types (IN the hash, ADR-02 §3) ---
	TKeyword   Kind = 100 // Str = keyword (number|string|boolean|bigint|void|null|undefined|unknown|never|true|false)
	TLiteral   Kind = 101 // Kids = [literal expr]
	TArray     Kind = 102 // Kids = [elemType]
	TTuple     Kind = 103 // Kids = [KList elemTypes]
	TUnion     Kind = 104 // Kids = [KList members (sorted by encoding)]
	TInter     Kind = 105 // Kids = [KList members (sorted by encoding)]
	TRef       Kind = 106 // Str = name (lib global / type param display); Kids = [KList typeArgs]
	TLocal     Kind = 107 // U = type-param De Bruijn index; Kids = [KList typeArgs]
	TCatRef    Kind = 108 // Str = catalogued type address; Kids = [KList typeArgs]
	TObject    Kind = 109 // type literal; Kids = [KList members (sorted by key)]
	TPropSig   Kind = 110 // U bit0 readonly, bit1 optional; Str = key; Kids = [type]
	TIndexSig  Kind = 111 // U bit0 readonly; Kids = [keyType, valueType]
	TFunc      Kind = 112 // Kids = [KList typeParams, KList params, retType]
	TCond      Kind = 113 // Kids = [check, extends, true, false]
	TKeyof     Kind = 114 // Kids = [operandType]
	TIndexed   Kind = 115 // Kids = [objType, indexType]
	TQuery     Kind = 116 // typeof-type; Kids = [expr]
	TParam     Kind = 117 // type-parameter binder; Kids = [constraint|KNone, default|KNone]
	TReadonly  Kind = 118 // readonly array/tuple operator; Kids = [type]
	TMapped    Kind = 119 // U bits0-1 readonly none/+/-, bits2-3 question none/+/-; Kids = [TParam, srcType, asType|KNone, valueType]
	TTemplLit  Kind = 120 // template-literal type; Kids = [KList of alternating KStrPart and types]

	kindMax = 200
)

// Node is the single uniform regel-AST node. Interpretation of the scalar fields
// is fixed per Kind by the schema table (schema.go).
type Node struct {
	Kind Kind
	Str  string  // primary string payload (see per-kind docs)
	U    uint64  // primary numeric payload (f64 bits / De Bruijn / flags / OpKind)
	Mag  []byte  // bigint magnitude (minimal big-endian); only KBigInt
	Kids []*Node // ordered children
}

// OpKind enumerates the operators carried in KBinary/KUnary/KUpdate.U. Values
// are stable for r1.
type OpKind uint8

const (
	OpNone OpKind = 0
	// binary arithmetic / bitwise / comparison / logical
	OpAdd        OpKind = 1
	OpSub        OpKind = 2
	OpMul        OpKind = 3
	OpDiv        OpKind = 4
	OpMod        OpKind = 5
	OpExp        OpKind = 6
	OpShl        OpKind = 7
	OpShr        OpKind = 8
	OpUShr       OpKind = 9
	OpBitAnd     OpKind = 10
	OpBitOr      OpKind = 11
	OpBitXor     OpKind = 12
	OpLt         OpKind = 13
	OpGt         OpKind = 14
	OpLe         OpKind = 15
	OpGe         OpKind = 16
	OpEqEq       OpKind = 17
	OpNeEq       OpKind = 18
	OpEqEqEq     OpKind = 19
	OpNeEqEq     OpKind = 20
	OpAnd        OpKind = 21
	OpOr         OpKind = 22
	OpNullish    OpKind = 23
	OpIn         OpKind = 24
	// assignment family
	OpAssign       OpKind = 40
	OpAddAssign    OpKind = 41
	OpSubAssign    OpKind = 42
	OpMulAssign    OpKind = 43
	OpDivAssign    OpKind = 44
	OpModAssign    OpKind = 45
	OpExpAssign    OpKind = 46
	OpShlAssign    OpKind = 47
	OpShrAssign    OpKind = 48
	OpUShrAssign   OpKind = 49
	OpBitAndAssign OpKind = 50
	OpBitOrAssign  OpKind = 51
	OpBitXorAssign OpKind = 52
	OpAndAssign    OpKind = 53
	OpOrAssign     OpKind = 54
	OpNullAssign   OpKind = 55
	// unary
	OpPos    OpKind = 70 // unary +
	OpNeg    OpKind = 71 // unary -
	OpNot    OpKind = 72 // !
	OpBitNot OpKind = 73 // ~
	OpInc    OpKind = 74 // ++ (KUpdate)
	OpDec    OpKind = 75 // -- (KUpdate)
)

// Regex flag bits for KRegex.U (sorted/canonical by construction).
const (
	RegexFlagD uint64 = 1 << iota // d (indices)
	RegexFlagG                    // g (global)
	RegexFlagI                    // i (ignoreCase)
	RegexFlagM                    // m (multiline)
	RegexFlagS                    // s (dotAll)
	RegexFlagU                    // u (unicode)
	RegexFlagV                    // v (unicodeSets)
	RegexFlagY                    // y (sticky)
)

// list builds a KList node from elements.
func list(kids ...*Node) *Node { return &Node{Kind: KList, Kids: kids} }

// none is the shared absent-optional sentinel.
func none() *Node { return &Node{Kind: KNone} }

// IsNone reports the absent-optional sentinel.
func (n *Node) IsNone() bool { return n == nil || n.Kind == KNone }
