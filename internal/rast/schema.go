package rast

// TLV schema — format version r1.
//
// canonEncode emits, for each node, exactly:
//
//	tag(1 byte = Kind)
//	[ if hasStr: varint(len(Str)) ‖ Str-bytes ]
//	[ if hasU:   varint(U) ]
//	[ if hasMag: varint(len(Mag)) ‖ Mag-bytes ]
//	varint(len(Kids)) ‖ encode(each child)
//
// The scalar fields present per kind are declared here and NOWHERE else; encoder
// and decoder both read this table, so they cannot drift. Children are always a
// length-prefixed list, so the encoder needs no per-kind child logic. Numbers
// (KNum.U) are the one exception to varint: an f64 bit pattern is emitted as 8
// big-endian bytes (ADR-02 §2), selected by numAsF64 below.
//
// Adding a kind in a future r2 appends a row; existing rows never change,
// preserving hash immortality (ADR-02 §6).

type fieldSchema struct {
	hasStr bool
	hasU   bool
	hasMag bool
}

var schema = func() [kindMax]fieldSchema {
	var s [kindMax]fieldSchema
	set := func(k Kind, str, u, mag bool) { s[k] = fieldSchema{str, u, mag} }

	set(KNone, false, false, false)
	set(KList, false, false, false)

	set(KNum, false, true, false)
	set(KBigInt, false, true, true)
	set(KStr, true, false, false)
	set(KBool, false, true, false)
	set(KNull, false, false, false)
	set(KUndefined, false, false, false)
	set(KRegex, true, true, false)
	set(KTemplate, false, false, false)
	set(KStrPart, true, false, false)

	set(KLocal, false, true, false)
	set(KRef, true, false, false)
	set(KSelfRef, false, false, false)
	set(KName, true, false, false)

	set(KArray, false, false, false)
	set(KObject, false, false, false)
	set(KProp, false, true, false)
	set(KSpread, false, false, false)
	set(KCall, false, true, false)
	set(KMember, true, true, false)
	set(KIndex, false, true, false)
	set(KBinary, false, true, false)
	set(KUnary, false, true, false)
	set(KUpdate, false, true, false)
	set(KCond, false, false, false)
	set(KTypeof, false, false, false)
	set(KAwait, false, false, false)
	set(KAsConst, false, false, false)
	set(KSatisfy, false, false, false)
	set(KFunc, false, true, false)
	set(KParam, false, true, false)

	set(KBindId, false, false, false)
	set(KArrayPat, false, false, false)
	set(KObjPat, false, false, false)
	set(KBindProp, false, true, false)
	set(KRestPat, false, false, false)

	set(KBlock, false, false, false)
	set(KVarDecl, false, true, false)
	set(KDeclr, false, false, false)
	set(KExprStmt, false, false, false)
	set(KIf, false, false, false)
	set(KFor, false, false, false)
	set(KForOf, false, true, false)
	set(KWhile, false, false, false)
	set(KDoWhile, false, false, false)
	set(KSwitch, false, false, false)
	set(KClause, false, true, false)
	set(KBreak, false, false, false)
	set(KContinue, false, false, false)
	set(KReturn, false, false, false)
	set(KThrow, false, false, false)
	set(KTry, false, false, false)
	set(KCatch, false, true, false)

	set(KTypeAlias, false, false, false)
	set(KInterface, false, false, false)
	set(KNativeBody, true, false, false)

	set(TKeyword, true, false, false)
	set(TLiteral, false, false, false)
	set(TArray, false, false, false)
	set(TTuple, false, false, false)
	set(TUnion, false, false, false)
	set(TInter, false, false, false)
	set(TRef, true, false, false)
	set(TLocal, false, true, false)
	set(TCatRef, true, false, false)
	set(TObject, false, false, false)
	set(TPropSig, true, true, false)
	set(TIndexSig, false, true, false)
	set(TFunc, false, false, false)
	set(TCond, false, false, false)
	set(TKeyof, false, false, false)
	set(TIndexed, false, false, false)
	set(TQuery, false, false, false)
	set(TParam, false, false, false)
	set(TReadonly, false, false, false)
	set(TMapped, false, true, false)
	set(TTemplLit, false, false, false)

	return s
}()

// numAsF64 marks kinds whose U field is an 8-byte IEEE-754 pattern rather than a
// varint. Only KNum qualifies (ADR-02 §2 number encoding).
func numAsF64(k Kind) bool { return k == KNum }

// valid reports whether k is a defined kind in this schema version.
func valid(k Kind) bool {
	return int(k) < kindMax && (k == KNone || k == KList || schemaDefined[k])
}

var schemaDefined = func() map[Kind]bool {
	m := map[Kind]bool{}
	for _, k := range allKinds {
		m[k] = true
	}
	return m
}()

var allKinds = []Kind{
	KNone, KList,
	KNum, KBigInt, KStr, KBool, KNull, KUndefined, KRegex, KTemplate, KStrPart,
	KLocal, KRef, KSelfRef, KName,
	KArray, KObject, KProp, KSpread, KCall, KMember, KIndex, KBinary, KUnary,
	KUpdate, KCond, KTypeof, KAwait, KAsConst, KSatisfy, KFunc, KParam,
	KBindId, KArrayPat, KObjPat, KBindProp, KRestPat,
	KBlock, KVarDecl, KDeclr, KExprStmt, KIf, KFor, KForOf, KWhile, KDoWhile,
	KSwitch, KClause, KBreak, KContinue, KReturn, KThrow, KTry, KCatch,
	KTypeAlias, KInterface, KNativeBody,
	TKeyword, TLiteral, TArray, TTuple, TUnion, TInter, TRef, TLocal, TCatRef,
	TObject, TPropSig, TIndexSig, TFunc, TCond, TKeyof, TIndexed, TQuery,
	TParam, TReadonly, TMapped, TTemplLit,
}
