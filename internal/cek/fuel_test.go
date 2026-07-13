package cek

import (
	"context"
	"testing"
)

// TestExactBudgetFuel is the Stage-B exact-fuel gate item: for a deterministic
// program with total transition count T, Fuel=T completes, Fuel=T-1 parks at
// exactly T-1, and grant-fuel resumes accumulate to the identical result. All
// observed numbers are logged for the gate report.
func TestExactBudgetFuel(t *testing.T) {
	src := `export function f(n: number): number {
	  let acc = 0;
	  for (let i = 0; i < n; i++) { acc = acc + i; }
	  return acc;
	}`
	src2, names := build(t, src)
	in := New(src2, nil)
	ctx := context.Background()
	hash := names["f"]
	arg := []Value{f64(50)}
	const bigAlloc = int64(1) << 40

	// Reference: measure T with a huge budget.
	ref := in.Run(ctx, RunReq{DefHash: hash, Args: arg, Tier: TierSandbox, Fuel: 1 << 30, Alloc: bigAlloc})
	if ref.Kind != OutDone {
		t.Fatalf("reference kind=%d err=%v", ref.Kind, ref.Err)
	}
	T := ref.Transitions
	R := ref.Value
	t.Logf("EXACT-FUEL: total transitions T=%d, result=%v", T, R.N)

	// (a) Fuel=T completes.
	oa := in.Run(ctx, RunReq{DefHash: hash, Args: arg, Tier: TierSandbox, Fuel: T, Alloc: bigAlloc})
	if oa.Kind != OutDone || oa.Value.N != R.N {
		t.Fatalf("Fuel=T: kind=%d val=%+v, want Done %v", oa.Kind, oa.Value, R.N)
	}
	t.Logf("EXACT-FUEL: Fuel=T(%d) → Done, transitions=%d", T, oa.Transitions)

	// (b) Fuel=T-1 parks at exactly T-1 with fuel.exhausted.
	ob := in.Run(ctx, RunReq{DefHash: hash, Args: arg, Tier: TierSandbox, Fuel: T - 1, Alloc: bigAlloc})
	if ob.Kind != OutParked {
		t.Fatalf("Fuel=T-1: expected Parked, got kind=%d", ob.Kind)
	}
	if ob.Transitions != T-1 {
		t.Fatalf("Fuel=T-1: Transitions=%d, want %d", ob.Transitions, T-1)
	}
	if ob.Condition == nil || ob.Condition.Class != "fuel.exhausted" {
		t.Fatalf("Fuel=T-1: condition=%+v, want fuel.exhausted", ob.Condition)
	}
	t.Logf("EXACT-FUEL: Fuel=T-1(%d) → Parked at transitions=%d, class=%s", T-1, ob.Transitions, ob.Condition.Class)

	// (c) resuming (b) with grant-fuel 1 completes to the identical result.
	oc := in.Resume(ctx, ob.State,
		Delivery{Restart: &RestartChoice{Name: "grant-fuel", Args: map[string]any{"fuel": int64(1)}}},
		Principal{IsOperator: true})
	if oc.Kind != OutDone || oc.Value.N != R.N {
		t.Fatalf("resume grant-fuel 1: kind=%d val=%+v, want Done %v", oc.Kind, oc.Value, R.N)
	}
	t.Logf("EXACT-FUEL: T-1 park + grant-fuel(1) → Done, resumed transitions=%d", oc.Transitions)

	// (d) Fuel=T-5 parks; grant-fuel 4 re-parks at exactly 4 (its own counter);
	//     grant-fuel 1 completes to the identical result.
	od := in.Run(ctx, RunReq{DefHash: hash, Args: arg, Tier: TierSandbox, Fuel: T - 5, Alloc: bigAlloc})
	if od.Kind != OutParked || od.Transitions != T-5 {
		t.Fatalf("Fuel=T-5: kind=%d transitions=%d, want Parked at %d", od.Kind, od.Transitions, T-5)
	}
	t.Logf("EXACT-FUEL: Fuel=T-5(%d) → Parked at transitions=%d", T-5, od.Transitions)

	od2 := in.Resume(ctx, od.State,
		Delivery{Restart: &RestartChoice{Name: "grant-fuel", Args: map[string]any{"fuel": int64(4)}}},
		Principal{IsOperator: true})
	if od2.Kind != OutParked {
		t.Fatalf("grant-fuel 4: expected re-park, got kind=%d", od2.Kind)
	}
	if od2.Transitions != 4 {
		t.Fatalf("grant-fuel 4: resumed-leg Transitions=%d, want 4", od2.Transitions)
	}
	t.Logf("EXACT-FUEL: +grant-fuel(4) → re-Parked, resumed-leg transitions=%d", od2.Transitions)

	od3 := in.Resume(ctx, od2.State,
		Delivery{Restart: &RestartChoice{Name: "grant-fuel", Args: map[string]any{"fuel": int64(1)}}},
		Principal{IsOperator: true})
	if od3.Kind != OutDone || od3.Value.N != R.N {
		t.Fatalf("grant-fuel 1: kind=%d val=%+v, want Done %v", od3.Kind, od3.Value, R.N)
	}
	t.Logf("EXACT-FUEL: +grant-fuel(1) → Done, resumed-leg transitions=%d, result=%v", od3.Transitions, od3.Value.N)
}
