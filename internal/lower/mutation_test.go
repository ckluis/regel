package lower

import "testing"

// fixedResolver resolves any std/app import to a stable per-qualified-name hash,
// so import-bearing modules lower deterministically.
func fixedResolver(q string) (string, bool) { return "r1_" + fakeHash(q), true }

// hashOf lowers src and returns the address of the named definition.
func hashOf(t *testing.T, src, name string) string {
	t.Helper()
	res := Module(src, ModuleContext{ModuleName: "app/mut", Resolve: fixedResolver})
	if !res.OK() {
		t.Fatalf("unexpected rejection for %q: %v\n%s", name, res.Diagnostics, src)
	}
	for _, d := range res.Definitions {
		if d.Name == name {
			return d.Hash
		}
	}
	t.Fatalf("definition %q not found", name)
	return ""
}

type mutCase struct {
	name string
	def  string
	a, b string
}

// ADR-02 §5 mutation matrix — hash-invariant mutations.
func TestMutationSameHash(t *testing.T) {
	cases := []mutCase{
		{"whitespace", "v",
			"export const v = (1 + 2) * 3;\n",
			"export const   v=(1+2)*3  ;\n\n"},
		{"comments", "v",
			"export const v = 1 + 2;\n",
			"// leading\nexport const v = 1 /* mid */ + 2;\n"},
		{"docstring", "v",
			"export const v = 1 + 2;\n",
			"/** does a thing */\nexport const v = 1 + 2;\n"},
		{"local_rename", "f",
			"export const f = (x: number): number => x + 1;\n",
			"export const f = (renamedParam: number): number => renamedParam + 1;\n"},
		{"local_rename_block", "f",
			"export function f(a: number): number { const y = a + 1; return y; }\n",
			"export function f(a: number): number { const zzz = a + 1; return zzz; }\n"},
		{"typeparam_rename", "Id",
			"export type Id<T> = T;\n",
			"export type Id<Element> = Element;\n"},
		{"quote_style", "v",
			"export const v = \"hi\";\n",
			"export const v = 'hi';\n"},
		{"number_spelling_hex", "v",
			"export const v = 255;\n",
			"export const v = 0xff;\n"},
		{"number_spelling_float", "v",
			"export const v = 1;\n",
			"export const v = 1.0;\n"},
		{"number_spelling_sep", "v",
			"export const v = 1000;\n",
			"export const v = 1_000;\n"},
		{"type_member_order", "S",
			"export interface S { a: number; b: string; c: boolean; }\n",
			"export interface S { c: boolean; a: number; b: string; }\n"},
		{"union_member_order", "U",
			"export type U = \"a\" | \"b\" | \"c\";\n",
			"export type U = \"c\" | \"a\" | \"b\";\n"},
		{"import_order", "v",
			"import { one } from \"std/a\";\nimport { two } from \"std/b\";\nexport const v = one + two;\n",
			"import { two } from \"std/b\";\nimport { one } from \"std/a\";\nexport const v = one + two;\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if ha, hb := hashOf(t, c.a, c.def), hashOf(t, c.b, c.def); ha != hb {
				t.Fatalf("%s: hashes differ but should match\n a=%s\n b=%s", c.name, ha, hb)
			}
		})
	}
}

// Hash-sensitive mutations must change the address.
func TestMutationDiffHash(t *testing.T) {
	cases := []mutCase{
		{"literal_value", "v",
			"export const v = 1;\n",
			"export const v = 2;\n"},
		{"string_literal", "v",
			"export const v = \"a\";\n",
			"export const v = \"b\";\n"},
		{"type_annotation", "f",
			"export const f = (x: number): number => x;\n",
			"export const f = (x: string): number => x;\n"},
		{"type_member_type", "S",
			"export interface S { a: number; }\n",
			"export interface S { a: string; }\n"},
		{"operator", "v",
			"export const v = 1 + 2;\n",
			"export const v = 1 - 2;\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if hashOf(t, c.a, c.def) == hashOf(t, c.b, c.def) {
				t.Fatalf("%s: hashes match but should differ", c.name)
			}
		})
	}
}

// A dep-hash change (same source, different referent address) changes the hash.
func TestMutationDepHashDiff(t *testing.T) {
	src := "import { helper } from \"std/x\";\nexport const v = helper(1);\n"
	r1 := Module(src, ModuleContext{ModuleName: "app/mut", Resolve: func(q string) (string, bool) {
		return "r1_" + fakeHash("A"+q), true
	}})
	r2 := Module(src, ModuleContext{ModuleName: "app/mut", Resolve: func(q string) (string, bool) {
		return "r1_" + fakeHash("B"+q), true
	}})
	if !r1.OK() || !r2.OK() {
		t.Fatalf("unexpected rejection: %v %v", r1.Diagnostics, r2.Diagnostics)
	}
	if r1.Definitions[0].Hash == r2.Definitions[0].Hash {
		t.Fatal("changing a dependency's address must change the dependent's hash (Merkle)")
	}
}
