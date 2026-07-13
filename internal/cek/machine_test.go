package cek

import (
	"context"
	"testing"

	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/rast"
)

// build lowers a module and returns a DefSource plus a name→hash map.
func build(t *testing.T, source string) (MapSource, map[string]string) {
	t.Helper()
	r := lower.Module(source, lower.ModuleContext{ModuleName: "app/test"})
	if !r.OK() {
		t.Fatalf("lower failed: %v", r.Diagnostics)
	}
	src := MapSource{}
	names := map[string]string{}
	for _, d := range r.Definitions {
		src[d.Hash] = d.Body
		names[d.Name] = d.Hash
	}
	return src, names
}

func runFn(t *testing.T, source, name string, args ...Value) Outcome {
	t.Helper()
	src, names := build(t, source)
	in := New(src, nil)
	h, ok := names[name]
	if !ok {
		t.Fatalf("no definition named %q", name)
	}
	return in.Run(context.Background(), RunReq{DefHash: h, Args: args, Tier: TierTrusted})
}

func wantF64(t *testing.T, o Outcome, want float64) {
	t.Helper()
	if o.Kind != OutDone {
		t.Fatalf("outcome kind=%d err=%v fault=%+v", o.Kind, o.Err, o.Fault)
	}
	if o.Value.Tag != TagF64 || o.Value.N != want {
		t.Fatalf("got %+v, want %v", o.Value, want)
	}
}

func TestArithmetic(t *testing.T) {
	o := runFn(t, `export function f(): number { return 40 + 2; }`, "f")
	wantF64(t, o, 42)
}

func TestFloatSemantics(t *testing.T) {
	// JS f64 semantics: 0.1+0.2 == 0.30000000000000004 (runtime, not the exact
	// Go untyped-constant 0.3). Force runtime addition on both sides.
	a, b := 0.1, 0.2
	o := runFn(t, `export function f(): number { return 0.1 + 0.2; }`, "f")
	if o.Kind != OutDone || o.Value.N != a+b {
		t.Fatalf("0.1+0.2 mismatch: %+v", o.Value)
	}
}

func TestFibRecursion(t *testing.T) {
	src := `export function fib(n: number): number { if (n < 2) { return n; } return fib(n-1) + fib(n-2); }`
	o := runFn(t, src, "fib", f64(10))
	wantF64(t, o, 55)
}

func TestForLoopSum(t *testing.T) {
	src := `export function sum(n: number): number { let acc = 0; for (let i = 0; i < n; i++) { acc = acc + i; } return acc; }`
	o := runFn(t, src, "sum", f64(100))
	wantF64(t, o, 4950)
}

func TestWhileLoop(t *testing.T) {
	src := `export function f(n: number): number { let x = 0; let i = 0; while (i < n) { x = x + i; i = i + 1; } return x; }`
	o := runFn(t, src, "f", f64(10))
	wantF64(t, o, 45)
}

func TestClosureCapture(t *testing.T) {
	src := `export function f(): number { const add = (a: number, b: number): number => a + b; return add(3, 4); }`
	o := runFn(t, src, "f")
	wantF64(t, o, 7)
}

func TestArrayForOf(t *testing.T) {
	src := `export function f(): number { const xs = [1, 2, 3, 4]; let s = 0; for (const x of xs) { s = s + x; } return s; }`
	o := runFn(t, src, "f")
	wantF64(t, o, 10)
}

func TestTryCatchFinally(t *testing.T) {
	src := `export function f(): number {
	  let x = 0;
	  try { x = 1; throw 5; } catch (e) { x = x + 10; } finally { x = x + 100; }
	  return x;
	}`
	o := runFn(t, src, "f")
	wantF64(t, o, 111)
}

func TestSwitch(t *testing.T) {
	src := `export function f(n: number): number {
	  switch (n) {
	    case 1: return 10;
	    case 2: return 20;
	    default: return 99;
	  }
	}`
	wantF64(t, runFn(t, src, "f", f64(2)), 20)
	wantF64(t, runFn(t, src, "f", f64(7)), 99)
}

func TestTernaryAndLogical(t *testing.T) {
	src := `export function f(n: number): number { return (n > 0 ? 1 : -1) + (n > 5 && n < 10 ? 100 : 0); }`
	wantF64(t, runFn(t, src, "f", f64(7)), 101)
	wantF64(t, runFn(t, src, "f", f64(-3)), -1)
}

func TestRecordAndMember(t *testing.T) {
	src := `export function f(): number { const o = { a: 3, b: 4 }; return o.a + o.b; }`
	wantF64(t, runFn(t, src, "f"), 7)
}

func TestStringConcat(t *testing.T) {
	src := "export function f(): string { const a = \"foo\"; return `${a}-bar`; }"
	src2, names := build(t, src)
	in := New(src2, nil)
	o := in.Run(context.Background(), RunReq{DefHash: names["f"], Tier: TierTrusted})
	if o.Kind != OutDone || o.Value.Tag != TagStr || o.Value.S != "foo-bar" {
		t.Fatalf("template concat: %+v", o.Value)
	}
}

// TestDeepRecursionBoundedGoStack proves recursion is reified into K (no Go-stack
// growth with program depth): a deep recursion runs without a stack overflow.
func TestDeepRecursionBoundedGoStack(t *testing.T) {
	src := `export function countdown(n: number): number { if (n <= 0) { return 0; } return countdown(n - 1); }`
	o := runFn(t, src, "countdown", f64(200000))
	wantF64(t, o, 0)
}

// TestGovernorRunaway proves a trusted-tier infinite loop parks 'runaway' rather
// than hanging or panicking.
func TestGovernorRunaway(t *testing.T) {
	src := `export function spin(): number { while (true) { } return 0; }`
	src2, names := build(t, src)
	in := New(src2, nil)
	o := in.Run(context.Background(), RunReq{
		DefHash: names["spin"], Tier: TierTrusted, GovCeiling: 100000,
	})
	if o.Kind != OutParked {
		t.Fatalf("expected Parked, got kind=%d", o.Kind)
	}
	if o.Condition.Class != "runaway" {
		t.Fatalf("expected runaway, got %q", o.Condition.Class)
	}
}

// TestFuelExhaustionParks proves a sandbox run parks 'fuel.exhausted' with the
// grant-fuel/abort restarts.
func TestFuelExhaustionParks(t *testing.T) {
	src := `export function burn(n: number): number { let acc = 0; for (let i = 0; i < n; i++) { acc = acc + i; } return acc; }`
	src2, names := build(t, src)
	in := New(src2, nil)
	o := in.Run(context.Background(), RunReq{
		DefHash: names["burn"], Args: []Value{f64(1000000)}, Tier: TierSandbox, Fuel: 500,
	})
	if o.Kind != OutParked {
		t.Fatalf("expected Parked, got kind=%d value=%+v", o.Kind, o.Value)
	}
	if o.Condition.Class != "fuel.exhausted" {
		t.Fatalf("expected fuel.exhausted, got %q", o.Condition.Class)
	}
	names2 := []string{o.Condition.Restarts[0].Name, o.Condition.Restarts[1].Name}
	if names2[0] != "grant-fuel" || names2[1] != "abort" {
		t.Fatalf("unexpected restarts: %v", names2)
	}
}

var _ = rast.KNum
