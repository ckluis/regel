package cfr

import (
	"bytes"
	"context"
	"testing"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/lower"
)

func buildInterp(t *testing.T, source, name string) (*cek.Interp, string) {
	t.Helper()
	r := lower.Module(source, lower.ModuleContext{ModuleName: "app/test"})
	if !r.OK() {
		t.Fatalf("lower: %v", r.Diagnostics)
	}
	src := cek.MapSource{}
	var hash string
	for _, d := range r.Definitions {
		src[d.Hash] = d.Body
		if d.Name == name {
			hash = d.Hash
		}
	}
	if hash == "" {
		t.Fatalf("no def %q", name)
	}
	return cek.New(src, nil), hash
}

const fuzzProgram = `export function sum(n: number): number {
  let acc = 0;
  const bump = (x: number): number => x + 1;
  for (let i = 0; i < n; i++) { acc = acc + bump(i); }
  return acc;
}`

// TestPauseAnywhere is the flagship CFR property (ADR-05 §2, §6 test 6): parking
// at EVERY transition index, encoding→decoding→resuming, yields the identical
// result as an uninterrupted run — pause-anywhere is structural.
func TestPauseAnywhere(t *testing.T) {
	in, hash := buildInterp(t, fuzzProgram, "sum")
	ctx := context.Background()
	arg := []cek.Value{cek.NumV(15)}

	// Reference: uninterrupted trusted run.
	ref := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierTrusted})
	if ref.Kind != cek.OutDone {
		t.Fatalf("reference run kind=%d", ref.Kind)
	}
	total := ref.Transitions

	for k := int64(1); k < total; k++ {
		o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierSandbox, Fuel: k})
		if o.Kind == cek.OutDone {
			continue // parked exactly at the end
		}
		if o.Kind != cek.OutParked {
			t.Fatalf("k=%d: expected Parked, got kind=%d err=%v", k, o.Kind, o.Err)
		}
		// Encode → decode → encode must be byte-identical.
		b1, err := Encode(o.State)
		if err != nil {
			t.Fatalf("k=%d encode: %v", k, err)
		}
		st, err := Decode(b1)
		if err != nil {
			t.Fatalf("k=%d decode: %v", k, err)
		}
		b2, err := Encode(st)
		if err != nil {
			t.Fatalf("k=%d re-encode: %v", k, err)
		}
		if !bytes.Equal(b1, b2) {
			t.Fatalf("k=%d: CFR not byte-identical across round-trip (%d vs %d bytes)", k, len(b1), len(b2))
		}
		// Resume the decoded state to completion; result must match reference.
		res := in.Resume(ctx, st, cek.RestartChoice{Name: "grant-fuel", Args: map[string]any{"fuel": 1 << 30}})
		if res.Kind != cek.OutDone {
			t.Fatalf("k=%d resume kind=%d err=%v", k, res.Kind, res.Err)
		}
		if res.Value.Tag != ref.Value.Tag || res.Value.N != ref.Value.N {
			t.Fatalf("k=%d: resumed %+v != reference %+v", k, res.Value, ref.Value)
		}
	}
}

// TestCorruptCFRFailsClosed is ADR-05 §6 test 4b: a truncated or bit-flipped CFR
// blob fails deserialization closed with a typed error, never a panic.
func TestCorruptCFRFailsClosed(t *testing.T) {
	in, hash := buildInterp(t, fuzzProgram, "sum")
	ctx := context.Background()
	o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: []cek.Value{cek.NumV(10)}, Tier: cek.TierSandbox, Fuel: 60})
	if o.Kind != cek.OutParked {
		t.Fatalf("expected Parked, got %d", o.Kind)
	}
	blob, err := Encode(o.State)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Truncation at every prefix length must fail closed.
	for n := 0; n < len(blob); n++ {
		if _, err := Decode(blob[:n]); err == nil {
			t.Fatalf("truncated blob len %d decoded without error", n)
		}
	}
	// Bit flips must fail closed OR decode to a well-formed (if wrong) state —
	// never panic. We assert no panic and that errors are typed.
	for i := 0; i < len(blob); i++ {
		for _, bit := range []byte{0x01, 0x80} {
			corrupt := append([]byte(nil), blob...)
			corrupt[i] ^= bit
			st, derr := Decode(corrupt)
			if derr != nil {
				if !isCFRErr(derr) {
					t.Fatalf("bit flip @%d: non-typed error %v", i, derr)
				}
				continue
			}
			_ = st // decoded to some state; acceptable as long as no panic
		}
	}
}

func isCFRErr(err error) bool {
	return err != nil && bytesContains(err.Error(), "cfr:")
}

func bytesContains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
