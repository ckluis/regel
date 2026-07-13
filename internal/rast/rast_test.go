package rast

import (
	"bytes"
	"math"
	"math/big"
	"math/rand"
	"testing"
)

// --- canonEncode / canonDecode byte round-trip ---

func TestEncodeDecodeByteRoundTrip(t *testing.T) {
	seed := int64(1)
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < 400; i++ {
		n := Normalize(genNode(rng, 5))
		enc := Encode(n)
		dec, err := Decode(enc)
		if err != nil {
			t.Fatalf("seed %d iter %d: decode error: %v", seed, i, err)
		}
		if !bytes.Equal(enc, Encode(dec)) {
			t.Fatalf("seed %d iter %d: re-encode mismatch", seed, i)
		}
		if !Equal(n, dec) {
			t.Fatalf("seed %d iter %d: decoded tree not structurally equal", seed, i)
		}
	}
}

func TestDecodeRejectsTrailingAndTruncated(t *testing.T) {
	n := &Node{Kind: KNum, U: math.Float64bits(1)}
	enc := Encode(n)
	if _, err := Decode(append(enc, 0x00)); err == nil {
		t.Fatal("expected trailing-bytes error")
	}
	if _, err := Decode(enc[:len(enc)-1]); err == nil {
		t.Fatal("expected truncation error")
	}
}

// --- normalize idempotence ---

func TestNormalizeIdempotent(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 300; i++ {
		n := genNode(rng, 5)
		a := Normalize(n)
		b := Normalize(a)
		if !bytes.Equal(Encode(a), Encode(b)) {
			t.Fatalf("iter %d: Normalize not idempotent", i)
		}
	}
}

// --- adversarial identity ---

func TestNegativeZeroDistinct(t *testing.T) {
	pos := Address(Normalize(&Node{Kind: KNum, U: math.Float64bits(0)}))
	neg := Address(Normalize(&Node{Kind: KNum, U: math.Float64bits(math.Copysign(0, -1))}))
	if pos == neg {
		t.Fatal("-0 and 0 must hash differently")
	}
}

func TestNFCvsNFDDistinct(t *testing.T) {
	nfc := "café"  // é as one code point
	nfd := "café" // e + combining acute
	a := Address(Normalize(&Node{Kind: KStr, Str: nfc}))
	b := Address(Normalize(&Node{Kind: KStr, Str: nfd}))
	if a == b {
		t.Fatal("NFC and NFD strings must hash differently (code points preserved)")
	}
}

func TestBigIntEdges(t *testing.T) {
	mk := func(neg bool, mag []byte) *Node {
		var u uint64
		if neg {
			u = 1
		}
		return Normalize(&Node{Kind: KBigInt, U: u, Mag: mag})
	}
	// 0 and "negative 0" (empty mag, sign clear) are the same value.
	if Address(mk(false, nil)) != Address(mk(false, []byte{})) {
		t.Fatal("bigint zero must be stable regardless of nil vs empty mag")
	}
	// 255 (1 byte) vs 256 (2 bytes) distinct.
	if Address(mk(false, big.NewInt(255).Bytes())) == Address(mk(false, big.NewInt(256).Bytes())) {
		t.Fatal("bigint 255 and 256 must differ")
	}
	// +5 vs -5 distinct.
	if Address(mk(false, big.NewInt(5).Bytes())) == Address(mk(true, big.NewInt(5).Bytes())) {
		t.Fatal("bigint +5 and -5 must differ")
	}
	// byte round-trip on a large magnitude.
	big1 := new(big.Int).Lsh(big.NewInt(1), 300).Bytes()
	n := mk(true, big1)
	dec, err := Decode(Encode(n))
	if err != nil || !Equal(n, dec) {
		t.Fatalf("large bigint round-trip failed: %v", err)
	}
}

func TestDeepNestingRoundTrip(t *testing.T) {
	// Deeply nested unary chain — canonEncode/decode must survive it.
	n := &Node{Kind: KNum, U: math.Float64bits(1)}
	for i := 0; i < 1200; i++ {
		n = &Node{Kind: KUnary, U: uint64(OpNeg), Kids: []*Node{n}}
	}
	nn := Normalize(n)
	dec, err := Decode(Encode(nn))
	if err != nil {
		t.Fatalf("deep decode error: %v", err)
	}
	if !bytes.Equal(Encode(nn), Encode(dec)) {
		t.Fatal("deep nesting re-encode mismatch")
	}
}

// --- alpha-equivalence: renaming binders never changes a hash ---

func TestAlphaEquivalence(t *testing.T) {
	// KFunc with a KBindId param whose body references it via KLocal; a rename of
	// the parked display name must not change the address (KBindId.Str is unhashed).
	mk := func(bindName, tparamName string) *Node {
		body := &Node{Kind: KFunc, U: 0, Kids: []*Node{
			list(&Node{Kind: KParam, Kids: []*Node{
				{Kind: KBindId, Str: bindName}, none(), none(),
			}}),
			list(&Node{Kind: TParam, Str: tparamName, Kids: []*Node{none(), none()}}),
			none(),
			{Kind: KBlock, Kids: []*Node{list(
				&Node{Kind: KReturn, Kids: []*Node{{Kind: KLocal, U: 0}}},
			)}},
		}}
		return Normalize(body)
	}
	if Address(mk("x", "T")) != Address(mk("yy", "Uvw")) {
		t.Fatal("renaming a local/type-param binder changed the hash (alpha-equivalence broken)")
	}
	// And member-order in a union with binders nested inside stays stable under rename.
	if !bytes.Equal(Encode(mk("x", "T")), Encode(mk("banana", "Zebra"))) {
		t.Fatal("binder rename perturbed the canonical encoding")
	}
}

// --- generator: random but structurally valid rast nodes ---

func genNode(rng *rand.Rand, depth int) *Node {
	if depth <= 0 {
		return genLeaf(rng)
	}
	switch rng.Intn(12) {
	case 0:
		return genLeaf(rng)
	case 1:
		return &Node{Kind: KArray, Kids: []*Node{genList(rng, depth-1, rng.Intn(4))}}
	case 2:
		return &Node{Kind: KBinary, U: uint64(1 + rng.Intn(24)), Kids: []*Node{
			genNode(rng, depth-1), genNode(rng, depth-1)}}
	case 3:
		return &Node{Kind: KUnary, U: uint64(OpNeg), Kids: []*Node{genNode(rng, depth-1)}}
	case 4:
		return &Node{Kind: KCond, Kids: []*Node{
			genNode(rng, depth-1), genNode(rng, depth-1), genNode(rng, depth-1)}}
	case 5:
		var kids []*Node
		for i := 0; i < rng.Intn(3); i++ {
			kids = append(kids, &Node{Kind: KProp, Kids: []*Node{
				{Kind: KStrPart, Str: randKey(rng)}, genNode(rng, depth-1)}})
		}
		return &Node{Kind: KObject, Kids: []*Node{{Kind: KList, Kids: kids}}}
	case 6: // union type (exercises member sorting)
		return &Node{Kind: TUnion, Kids: []*Node{genTypeList(rng, depth-1, 2+rng.Intn(3))}}
	case 7:
		return &Node{Kind: TArray, Kids: []*Node{genType(rng, depth-1)}}
	case 8:
		return &Node{Kind: KTemplate, Kids: []*Node{{Kind: KList, Kids: []*Node{
			{Kind: KStrPart, Str: randStr(rng)}, genNode(rng, depth-1), {Kind: KStrPart, Str: randStr(rng)}}}}}
	case 9:
		return &Node{Kind: KTypeof, Kids: []*Node{genNode(rng, depth-1)}}
	case 10:
		return &Node{Kind: TObject, Kids: []*Node{genTMembers(rng, depth-1)}}
	default:
		return genType(rng, depth-1)
	}
}

func genList(rng *rand.Rand, depth, n int) *Node {
	var kids []*Node
	for i := 0; i < n; i++ {
		kids = append(kids, genNode(rng, depth))
	}
	return &Node{Kind: KList, Kids: kids}
}

func genTypeList(rng *rand.Rand, depth, n int) *Node {
	var kids []*Node
	for i := 0; i < n; i++ {
		kids = append(kids, genType(rng, depth))
	}
	return &Node{Kind: KList, Kids: kids}
}

func genTMembers(rng *rand.Rand, depth int) *Node {
	var kids []*Node
	for i := 0; i < 1+rng.Intn(3); i++ {
		kids = append(kids, &Node{Kind: TPropSig, Str: randKey(rng), U: uint64(rng.Intn(4)),
			Kids: []*Node{genType(rng, depth)}})
	}
	return &Node{Kind: KList, Kids: kids}
}

func genType(rng *rand.Rand, depth int) *Node {
	if depth <= 0 {
		kws := []string{"number", "string", "boolean", "bigint", "unknown", "never"}
		return &Node{Kind: TKeyword, Str: kws[rng.Intn(len(kws))]}
	}
	switch rng.Intn(5) {
	case 0:
		return &Node{Kind: TKeyword, Str: "number"}
	case 1:
		return &Node{Kind: TArray, Kids: []*Node{genType(rng, depth-1)}}
	case 2:
		return &Node{Kind: TUnion, Kids: []*Node{genTypeList(rng, depth-1, 2+rng.Intn(2))}}
	case 3:
		return &Node{Kind: TRef, Str: "Ref" + randKey(rng), Kids: []*Node{{Kind: KList}}}
	default:
		return &Node{Kind: TLiteral, Kids: []*Node{genLeaf(rng)}}
	}
}

func genLeaf(rng *rand.Rand) *Node {
	switch rng.Intn(6) {
	case 0:
		return &Node{Kind: KNum, U: math.Float64bits(float64(rng.Intn(1000)) - 500)}
	case 1:
		return &Node{Kind: KStr, Str: randStr(rng)}
	case 2:
		return &Node{Kind: KBool, U: uint64(rng.Intn(2))}
	case 3:
		return &Node{Kind: KNull}
	case 4:
		return &Node{Kind: KBigInt, U: uint64(rng.Intn(2)), Mag: big.NewInt(int64(rng.Intn(1 << 20))).Bytes()}
	default:
		return &Node{Kind: KName, Str: "g" + randKey(rng)}
	}
}

func randKey(rng *rand.Rand) string {
	const a = "abcdefghijklmnop"
	n := 1 + rng.Intn(4)
	b := make([]byte, n)
	for i := range b {
		b[i] = a[rng.Intn(len(a))]
	}
	return string(b)
}

func randStr(rng *rand.Rand) string {
	runes := []rune{'a', 'z', ' ', '\n', '$', '`', '\\', 'é', '中', '🙂', '"'}
	n := rng.Intn(6)
	out := make([]rune, n)
	for i := range out {
		out[i] = runes[rng.Intn(len(runes))]
	}
	return string(out)
}
