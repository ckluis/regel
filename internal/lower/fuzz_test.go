package lower

import (
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"strings"
	"testing"

	"regel.dev/regel/internal/rast"
)

// TestGuarantee1Deterministic — the same source lowers to the same hash.
func TestGuarantee1Deterministic(t *testing.T) {
	for _, d := range lowerModuleOK(t, smokeSrc).Definitions {
		h2 := hashOf(t, smokeSrc, d.Name)
		if h2 != d.Hash {
			t.Fatalf("def %q non-deterministic: %s vs %s", d.Name, d.Hash, h2)
		}
	}
}

// TestPropertyFixedPoint generates random subset-valid ASTs over the rast schema
// and asserts the print → lower → hash fixed point (guarantees 2 & 3).
func TestPropertyFixedPoint(t *testing.T) {
	for iter := 0; iter < 260; iter++ {
		seed := int64(iter*2654435761 + 12345)
		rng := rand.New(rand.NewSource(seed))
		var body *rast.Node
		var kind rast.DefKind
		if rng.Intn(2) == 0 {
			body = genExpr(rng, 4)
			kind = rast.DefValue
		} else {
			body = &rast.Node{Kind: rast.KTypeAlias, Kids: []*rast.Node{
				{Kind: rast.KList}, genFType(rng, 3)}}
			kind = rast.DefType
		}
		norm := rast.Normalize(body)
		d := Definition{Name: "v", Exported: true, Kind: kind, Body: norm, Hash: rast.Address(norm)}
		// strip parked binder names into sidecars the way lowering does.
		text := CanonicalText(d)

		re := Module(text, ModuleContext{ModuleName: "app/fuzz", Resolve: fixedResolver})
		if !re.OK() {
			t.Fatalf("seed %d: printed AST re-lower rejected: %v\nprinted:\n%s", seed, re.Diagnostics, text)
		}
		if len(re.Definitions) != 1 {
			t.Fatalf("seed %d: want 1 def, got %d\n%s", seed, len(re.Definitions), text)
		}
		if re.Definitions[0].Hash != d.Hash {
			t.Fatalf("seed %d: hash not a fixed point\n want %s\n got  %s\nprinted:\n%s",
				seed, d.Hash, re.Definitions[0].Hash, text)
		}
		if t2 := CanonicalText(re.Definitions[0]); t2 != text {
			t.Fatalf("seed %d: print not a fixed point\n--- a ---\n%s\n--- b ---\n%s", seed, text, t2)
		}
	}
}

// TestTokenFuzzNoPanic mutates valid sources at the byte level and asserts the
// lowerer never panics — it either admits or emits diagnostics.
func TestTokenFuzzNoPanic(t *testing.T) {
	seeds := []string{smokeSrc,
		"export const v = 1 + 2;\n",
		"export function f(x: number): number { return x; }\n",
		"export type T<A> = A | number;\n",
		"export interface S { a: number; b?: string; }\n",
	}
	runOne := func(seed int64, src string) (panicked bool, where any) {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				where = r
			}
		}()
		_ = Module(src, ModuleContext{ModuleName: "app/fuzz", Resolve: fixedResolver})
		return false, nil
	}
	for i := 0; i < 360; i++ {
		seed := int64(i*40503 + 7)
		rng := rand.New(rand.NewSource(seed))
		base := seeds[rng.Intn(len(seeds))]
		b := []byte(base)
		nmut := 1 + rng.Intn(4)
		for m := 0; m < nmut && len(b) > 0; m++ {
			switch rng.Intn(3) {
			case 0: // flip a byte to a random printable
				b[rng.Intn(len(b))] = byte(32 + rng.Intn(94))
			case 1: // delete a byte
				j := rng.Intn(len(b))
				b = append(b[:j], b[j+1:]...)
			case 2: // insert a byte
				j := rng.Intn(len(b))
				b = append(b[:j], append([]byte{byte(32 + rng.Intn(94))}, b[j:]...)...)
			}
		}
		if p, w := runOne(seed, string(b)); p {
			t.Fatalf("seed %d panicked: %v\nmutated src:\n%s", seed, w, string(b))
		}
	}
}

// --- safe rast generators (only constructs that print to admissible source) ---

func genExpr(rng *rand.Rand, depth int) *rast.Node {
	if depth <= 0 {
		return genAtom(rng)
	}
	switch rng.Intn(10) {
	case 0:
		return genAtom(rng)
	case 1:
		var kids []*rast.Node
		for i := 0; i < rng.Intn(4); i++ {
			kids = append(kids, genExpr(rng, depth-1))
		}
		return &rast.Node{Kind: rast.KArray, Kids: []*rast.Node{{Kind: rast.KList, Kids: kids}}}
	case 2:
		var kids []*rast.Node
		for i := 0; i < rng.Intn(3); i++ {
			kids = append(kids, &rast.Node{Kind: rast.KProp, Kids: []*rast.Node{
				{Kind: rast.KStrPart, Str: randIdent(rng)}, genExpr(rng, depth-1)}})
		}
		return &rast.Node{Kind: rast.KObject, Kids: []*rast.Node{{Kind: rast.KList, Kids: kids}}}
	case 3:
		op := nonAssignOps[rng.Intn(len(nonAssignOps))]
		return &rast.Node{Kind: rast.KBinary, U: uint64(op), Kids: []*rast.Node{
			genExpr(rng, depth-1), genExpr(rng, depth-1)}}
	case 4:
		op := []rast.OpKind{rast.OpNeg, rast.OpNot, rast.OpBitNot, rast.OpPos}[rng.Intn(4)]
		return mkUnary(op, genExpr(rng, depth-1))
	case 5:
		return &rast.Node{Kind: rast.KCond, Kids: []*rast.Node{
			genExpr(rng, depth-1), genExpr(rng, depth-1), genExpr(rng, depth-1)}}
	case 6:
		return &rast.Node{Kind: rast.KTypeof, Kids: []*rast.Node{genExpr(rng, depth-1)}}
	case 7:
		return &rast.Node{Kind: rast.KAsConst, Kids: []*rast.Node{genExpr(rng, depth-1)}}
	case 8:
		parts := []*rast.Node{{Kind: rast.KStrPart, Str: randText(rng)}}
		for i := 0; i < 1+rng.Intn(2); i++ {
			parts = append(parts, genExpr(rng, depth-1))
			parts = append(parts, &rast.Node{Kind: rast.KStrPart, Str: randText(rng)})
		}
		return &rast.Node{Kind: rast.KTemplate, Kids: []*rast.Node{{Kind: rast.KList, Kids: parts}}}
	default:
		return &rast.Node{Kind: rast.KSatisfy, Kids: []*rast.Node{genExpr(rng, depth-1), genFType(rng, 2)}}
	}
}

// mkUnary mirrors lowering's fold of unary +/- applied to a numeric/bigint
// literal, so the generator only ever emits canonical forms (the printer renders
// `-5` and `KNum(-5)` identically, so `KUnary(neg, KNum(5))` is not canonical).
func mkUnary(op rast.OpKind, operand *rast.Node) *rast.Node {
	if op == rast.OpNeg {
		if operand.Kind == rast.KNum {
			return &rast.Node{Kind: rast.KNum, U: math.Float64bits(-math.Float64frombits(operand.U))}
		}
		if operand.Kind == rast.KBigInt && len(operand.Mag) > 0 {
			// (zero bigint has no signed form; leave it alone)
			return &rast.Node{Kind: rast.KBigInt, U: operand.U ^ 1, Mag: operand.Mag}
		}
	}
	if op == rast.OpPos && (operand.Kind == rast.KNum || operand.Kind == rast.KBigInt) {
		return operand
	}
	return &rast.Node{Kind: rast.KUnary, U: uint64(op), Kids: []*rast.Node{operand}}
}

var nonAssignOps = []rast.OpKind{
	rast.OpAdd, rast.OpSub, rast.OpMul, rast.OpDiv, rast.OpMod, rast.OpExp,
	rast.OpShl, rast.OpShr, rast.OpUShr, rast.OpBitAnd, rast.OpBitOr, rast.OpBitXor,
	rast.OpLt, rast.OpGt, rast.OpLe, rast.OpGe, rast.OpEqEq, rast.OpNeEq,
	rast.OpEqEqEq, rast.OpNeEqEq, rast.OpAnd, rast.OpOr, rast.OpNullish,
}

func genAtom(rng *rand.Rand) *rast.Node {
	switch rng.Intn(7) {
	case 0:
		return &rast.Node{Kind: rast.KNum, U: math.Float64bits(float64(rng.Intn(2000) - 1000))}
	case 1:
		return &rast.Node{Kind: rast.KNum, U: math.Float64bits(rng.Float64()*1e6 - 5e5)}
	case 2:
		return &rast.Node{Kind: rast.KStr, Str: randText(rng)}
	case 3:
		return &rast.Node{Kind: rast.KBool, U: uint64(rng.Intn(2))}
	case 4:
		return &rast.Node{Kind: rast.KNull}
	case 5:
		return &rast.Node{Kind: rast.KBigInt, U: uint64(rng.Intn(2)),
			Mag: big.NewInt(int64(rng.Intn(1 << 30))).Bytes()}
	default:
		return &rast.Node{Kind: rast.KName, Str: "g" + randIdent(rng)}
	}
}

func genFType(rng *rand.Rand, depth int) *rast.Node {
	if depth <= 0 {
		kws := []string{"number", "string", "boolean", "bigint", "unknown", "never"}
		return &rast.Node{Kind: rast.TKeyword, Str: kws[rng.Intn(len(kws))]}
	}
	switch rng.Intn(6) {
	case 0:
		return &rast.Node{Kind: rast.TKeyword, Str: "number"}
	case 1:
		return &rast.Node{Kind: rast.TArray, Kids: []*rast.Node{genFType(rng, depth-1)}}
	case 2:
		var kids []*rast.Node
		for i := 0; i < 2+rng.Intn(2); i++ {
			kids = append(kids, genFType(rng, depth-1))
		}
		return &rast.Node{Kind: rast.TUnion, Kids: []*rast.Node{{Kind: rast.KList, Kids: kids}}}
	case 3:
		return &rast.Node{Kind: rast.TLiteral, Kids: []*rast.Node{genAtom2(rng)}}
	case 4:
		var kids []*rast.Node
		for i := 0; i < 1+rng.Intn(3); i++ {
			kids = append(kids, &rast.Node{Kind: rast.TPropSig, Str: randIdent(rng),
				U: uint64(rng.Intn(4)), Kids: []*rast.Node{genFType(rng, depth-1)}})
		}
		return &rast.Node{Kind: rast.TObject, Kids: []*rast.Node{{Kind: rast.KList, Kids: kids}}}
	default:
		return &rast.Node{Kind: rast.TRef, Str: "Ref" + strings.Title(randIdent(rng)), Kids: []*rast.Node{{Kind: rast.KList}}}
	}
}

// genAtom2 is genAtom restricted to literal-type-admissible atoms.
func genAtom2(rng *rand.Rand) *rast.Node {
	switch rng.Intn(4) {
	case 0:
		return &rast.Node{Kind: rast.KNum, U: math.Float64bits(float64(rng.Intn(1000)))}
	case 1:
		return &rast.Node{Kind: rast.KStr, Str: randText(rng)}
	case 2:
		return &rast.Node{Kind: rast.KBool, U: uint64(rng.Intn(2))}
	default:
		return &rast.Node{Kind: rast.KBigInt, U: 0, Mag: big.NewInt(int64(rng.Intn(1000))).Bytes()}
	}
}

func randIdent(rng *rand.Rand) string {
	const a = "abcdefghijklmnopqrstuvwxyz"
	n := 1 + rng.Intn(5)
	b := make([]byte, n)
	for i := range b {
		b[i] = a[rng.Intn(len(a))]
	}
	return string(b)
}

func randText(rng *rand.Rand) string {
	runes := []rune{'a', 'Z', ' ', '\n', '\t', '$', '`', '\\', 'é', '中', '🙂', '"', '\'', '{', '}'}
	n := rng.Intn(6)
	out := make([]rune, n)
	for i := range out {
		out[i] = runes[rng.Intn(len(runes))]
	}
	return string(out)
}

var _ = fmt.Sprintf
