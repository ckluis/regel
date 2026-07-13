package lower

import "testing"

func hasCode(res Result, code string) bool {
	_, ok := findDiag(res.Diagnostics, code)
	return ok
}

// R1: a closure capturing a reassigned `let` is CAPTURE_LET; capturing a const,
// or a let that is never reassigned, is admitted.
func TestCaptureRuleR1(t *testing.T) {
	reassigned := `export function outer(): () => number {
  let count = 0;
  const inc = (): number => { count = count + 1; return count; };
  return inc;
}
`
	res := Module(reassigned, ModuleContext{ModuleName: "app/b"})
	if !hasCode(res, CodeCaptureLet) {
		t.Fatalf("expected CAPTURE_LET, got %v", codes(res.Diagnostics))
	}

	ok := `export function outer(base: number): () => number {
  const snapshot = base;
  return (): number => snapshot + 1;
}
`
	if r := Module(ok, ModuleContext{ModuleName: "app/b"}); !r.OK() {
		t.Fatalf("const capture wrongly rejected: %v", r.Diagnostics)
	}
}

// Mutual recursion across definitions is DEP_CYCLE; self-recursion is fine.
func TestDepCycleVsSelfRecursion(t *testing.T) {
	mutual := `export function ping(n: number): number { return pong(n - 1); }
export function pong(n: number): number { return ping(n - 1); }
`
	if r := Module(mutual, ModuleContext{ModuleName: "app/b"}); !hasCode(r, CodeDepCycle) {
		t.Fatalf("expected DEP_CYCLE, got %v", codes(r.Diagnostics))
	}
	selfRec := `export function fact(n: number): number { return n <= 1 ? 1 : n * fact(n - 1); }
`
	if r := Module(selfRec, ModuleContext{ModuleName: "app/b"}); !r.OK() {
		t.Fatalf("self-recursion wrongly rejected: %v", r.Diagnostics)
	}
}

// A bare call to a syntactically-async binding is a FLOATING_PROMISE (Stage-A
// syntactic approximation).
func TestFloatingPromise(t *testing.T) {
	src := `export async function work(): Promise<number> { return 1; }
export async function run(): Promise<void> { work(); }
`
	if r := Module(src, ModuleContext{ModuleName: "app/b"}); !hasCode(r, CodeFloatingPromise) {
		t.Fatalf("expected FLOATING_PROMISE, got %v", codes(r.Diagnostics))
	}
	awaited := `export async function work(): Promise<number> { return 1; }
export async function run(): Promise<number> { return await work(); }
`
	if r := Module(awaited, ModuleContext{ModuleName: "app/b"}); !r.OK() {
		t.Fatalf("awaited call wrongly rejected: %v", r.Diagnostics)
	}
}

// Negative bigint literals fold and round-trip.
func TestNegativeBigIntRoundTrip(t *testing.T) {
	res := lowerModuleOK(t, "export const v = -100n;\nexport const w = 0n;\n")
	for _, d := range res.Definitions {
		text := CanonicalText(d)
		re := Module(text, ModuleContext{ModuleName: "app/corpus", Resolve: resolverFor(d)})
		if !re.OK() || re.Definitions[0].Hash != d.Hash {
			t.Fatalf("def %q negative-bigint round-trip failed: %v", d.Name, re.Diagnostics)
		}
	}
}
