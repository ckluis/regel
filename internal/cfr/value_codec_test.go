package cfr

import (
	"bytes"
	"context"
	"math"
	"testing"

	"regel.dev/regel/internal/cek"
)

// reencode round-trips a value through EncodeValue → DecodeValue → EncodeValue
// and asserts the two encodings are byte-identical (the universal fidelity check
// that needs no internal accessors).
func reencode(t *testing.T, v cek.Value) cek.Value {
	t.Helper()
	b1, err := EncodeValue(v)
	if err != nil {
		t.Fatalf("EncodeValue: %v", err)
	}
	v2, err := DecodeValue(b1)
	if err != nil {
		t.Fatalf("DecodeValue: %v", err)
	}
	b2, err := EncodeValue(v2)
	if err != nil {
		t.Fatalf("re-EncodeValue: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("value not byte-identical across round-trip (%d vs %d bytes)", len(b1), len(b2))
	}
	return v2
}

// TestEncodeValueScalars round-trips scalar values including exact f64 bit
// patterns and bigints.
func TestEncodeValueScalars(t *testing.T) {
	cases := []cek.Value{
		cek.UndefV(), cek.NullV(),
		cek.BoolV(true), cek.BoolV(false),
		cek.NumV(0), cek.NumV(42), cek.NumV(-3.5),
		cek.NumV(0.1 + 0.2), cek.NumV(math.Inf(1)), cek.NumV(math.Inf(-1)),
		cek.StrV(""), cek.StrV("hello, 世界"),
	}
	for _, v := range cases {
		v2 := reencode(t, v)
		if !v.Equal(v2) {
			t.Fatalf("scalar %+v != decoded %+v", v, v2)
		}
	}

	// f64 bit-pattern fidelity: NaN and negative zero survive exactly (Equal
	// cannot see these, so compare raw bits).
	for _, f := range []float64{math.NaN(), math.Copysign(0, -1)} {
		v := cek.NumV(f)
		v2 := reencode(t, v)
		fv, _ := v2.Num()
		if math.Float64bits(fv) != math.Float64bits(f) {
			t.Fatalf("f64 bits not preserved: %x vs %x", math.Float64bits(fv), math.Float64bits(f))
		}
	}
}

// TestEncodeValueCompound round-trips compound values (arrays, records, closures,
// bigint) produced by real programs, checking byte-identical re-encode.
func TestEncodeValueCompound(t *testing.T) {
	ctx := context.Background()
	progs := map[string]string{
		"array":   `export function f(): number[] { return [1, 2, 3, 4]; }`,
		"nested":  `export function f() { return { a: 1, b: [2, 3], c: { d: "x" } }; }`,
		"closure": `export function f() { const g = (x: number): number => x + 1; return g; }`,
		"bigint":  `export function f(): bigint { return 123456789012345678901234567890n; }`,
	}
	for name, src := range progs {
		in, hash := buildInterp(t, src, "f")
		o := in.Run(ctx, cek.RunReq{DefHash: hash, Tier: cek.TierTrusted})
		if o.Kind != cek.OutDone {
			t.Fatalf("%s: run kind=%d", name, o.Kind)
		}
		v2 := reencode(t, o.Value)
		// For non-closure values, structural equality must also hold.
		if name != "closure" && !o.Value.Equal(v2) {
			t.Fatalf("%s: decoded value != original", name)
		}
	}
}

// TestInitialStateSeedRoundTrip: InitialState → Encode → Decode → Resume(fresh)
// yields the same result as a direct Run (the CFR fresh-seed path, ParkFresh).
func TestInitialStateSeedRoundTrip(t *testing.T) {
	src := `export function f(n: number): number {
	  let acc = 0;
	  for (let i = 0; i < n; i++) { acc = acc + i * 2; }
	  return acc;
	}`
	in, hash := buildInterp(t, src, "f")
	ctx := context.Background()
	arg := []cek.Value{cek.NumV(20)}
	const fuel = int64(1) << 30
	const alloc = int64(1) << 40

	ref := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierSandbox, Fuel: fuel, Alloc: alloc})
	if ref.Kind != cek.OutDone {
		t.Fatalf("reference kind=%d", ref.Kind)
	}

	st, err := in.InitialState(hash, nil, arg, cek.TierSandbox, fuel, alloc)
	if err != nil {
		t.Fatalf("InitialState: %v", err)
	}
	blob, err := Encode(st)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	st2, err := Decode(blob)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	res := in.Resume(ctx, st2, cek.Delivery{}, cek.Principal{IsOperator: true})
	if res.Kind != cek.OutDone {
		t.Fatalf("fresh resume kind=%d err=%v", res.Kind, res.Err)
	}
	if !res.Value.Equal(ref.Value) {
		t.Fatalf("fresh-seed resume %+v != reference %+v", res.Value, ref.Value)
	}
}
