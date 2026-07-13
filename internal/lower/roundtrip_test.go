package lower

import (
	"testing"

	"regel.dev/regel/internal/rast"
)

// resolverFor builds a Resolver over a definition's deps so the printed,
// import-bearing projection re-lowers in isolation (siblings become imports).
func resolverFor(d Definition) Resolver {
	m := map[string]string{}
	for _, dep := range d.Deps {
		m[dep.Module+"."+dep.Name] = dep.Hash
	}
	return func(q string) (string, bool) { h, ok := m[q]; return h, ok }
}

// lowerModuleOK lowers src and fails the test on any diagnostic.
func lowerModuleOK(t *testing.T, src string) Result {
	t.Helper()
	res := Module(src, ModuleContext{ModuleName: "app/corpus", Resolve: func(q string) (string, bool) {
		// A permissive resolver: every std/app import resolves to a stable fake hash.
		return "r1_" + fakeHash(q), true
	}})
	if !res.OK() {
		t.Fatalf("lowering rejected: %v\nsrc:\n%s", res.Diagnostics, src)
	}
	return res
}

func fakeHash(q string) string {
	// deterministic 52-char crockford body from q
	const alpha = "0123456789abcdefghjkmnpqrstvwxyz"
	var b [52]byte
	h := uint64(1469598103934665603)
	for i := 0; i < 52; i++ {
		for j := 0; j < len(q); j++ {
			h ^= uint64(q[j])
			h *= 1099511628211
		}
		h ^= uint64(i) * 2654435761
		h *= 1099511628211
		b[i] = alpha[h&0x1f]
	}
	return string(b[:])
}

func TestRoundTripSmoke(t *testing.T) {
	res := lowerModuleOK(t, smokeSrc)
	for _, d := range res.Definitions {
		text := CanonicalText(d)
		// guarantee 2: print → re-lower → same hash.
		re := Module(text, ModuleContext{ModuleName: "app/corpus", Resolve: resolverFor(d)})
		if !re.OK() {
			t.Fatalf("def %q re-lower rejected: %v\nprinted:\n%s", d.Name, re.Diagnostics, text)
		}
		var got *Definition
		for i := range re.Definitions {
			if re.Definitions[i].Name == d.Name {
				got = &re.Definitions[i]
			}
		}
		if got == nil {
			t.Fatalf("def %q vanished on re-lower\nprinted:\n%s", d.Name, text)
		}
		if got.Hash != d.Hash {
			t.Fatalf("def %q hash changed on re-lower\n got %s\nwant %s\nprinted:\n%s",
				d.Name, got.Hash, d.Hash, text)
		}
		// guarantee 3: printing is a fixed point.
		text2 := CanonicalText(*got)
		if text2 != text {
			t.Fatalf("def %q print not a fixed point\n--- first ---\n%s\n--- second ---\n%s", d.Name, text, text2)
		}
	}
	_ = rast.SchemaVersion
}

const smokeSrc = `
export const n = 42;
export const neg = -3.5;
export const s = "hello\nworld";
export const b = true;
export const nul = null;
export const u = undefined;
export const big = 100n;
export const arr = [1, 2, 3];
export const obj = { a: 1, "b c": 2, [n]: 3, ...arr };
export const tmpl = ` + "`x${n}y${s}z`" + `;
export const re = /ab+c/gi;
export const tern = n > 0 ? "pos" : "neg";
export const chain = obj?.a ?? 0;
export const add = (x: number, y: number): number => x + y;
export const nested = (x: number) => (y: number) => x + y;
export function fact(k: number): number { return k <= 1 ? 1 : k * fact(k - 1); }
export async function fetchIt(url: string): Promise<string> { const r = await add(1, 2); return s; }
export function loops(xs: number[]): number {
  let total = 0;
  for (const x of xs) { total += x; }
  for (let i = 0; i < xs.length; i = i + 1) { total = total + i; }
  while (total > 100) { total = total - 1; }
  do { total = total + 1; } while (total < 5);
  return total;
}
export function ctrl(x: string): number {
  switch (x) {
    case "a": return 1;
    case "b": return 2;
    default: return 0;
  }
}
export function trycatch(): number {
  try { return 1; } catch (e) { return 2; } finally { const z = 3; }
}
export function destr(o: { a: number; b: number }): number { const { a, b } = o; const [p, q] = arr; return a + b + p + q; }
export type Id<T> = T;
export type Pair<A, B> = { first: A; second: B };
export type Union = "x" | "y" | "z";
export type Keys = keyof Pair<number, string>;
export type Cond<T> = T extends string ? number : boolean;
export type Mapped<T> = { readonly [K in keyof T]?: T[K] };
export type Fn = (a: number, b: string) => boolean;
export interface Shape<T> { readonly id: number; label: string; opt?: T; }
export const asc = [1, 2] as const;
export const sat = { a: 1 } satisfies { a: number };
`
