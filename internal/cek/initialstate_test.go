package cek

import (
	"context"
	"testing"
)

// TestInitialStateClosureArm: InitialState over a closure value (clo != nil)
// seeds a fresh state that, resumed, applies the closure to the args — the
// join-child seed path.
func TestInitialStateClosureArm(t *testing.T) {
	src := `export function mk(): (x: number) => number { return (x: number): number => x * 3; }`
	src2, names := build(t, src)
	in := New(src2, nil)
	ctx := context.Background()

	o := in.Run(ctx, RunReq{DefHash: names["mk"], Tier: TierTrusted})
	if o.Kind != OutDone || o.Value.Tag != TagClosure {
		t.Fatalf("mk did not return a closure: kind=%d val=%+v", o.Kind, o.Value)
	}
	clo := o.Value.clo()

	st, err := in.InitialState("", clo, []Value{f64(4)}, TierSandbox, 1<<30, 1<<40)
	if err != nil {
		t.Fatalf("InitialState(closure): %v", err)
	}
	if st.ParkKind != ParkFresh {
		t.Fatalf("ParkKind = %d, want ParkFresh(%d)", st.ParkKind, ParkFresh)
	}
	res := in.Resume(ctx, st, Delivery{}, Principal{IsOperator: true})
	if res.Kind != OutDone || res.Value.Tag != TagF64 || res.Value.N != 12 {
		t.Fatalf("closure-seed resume: kind=%d val=%+v, want 12", res.Kind, res.Value)
	}
}
