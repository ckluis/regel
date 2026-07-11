package lower

// Per-ban rejection fixtures — one per ADR-01 §2 ban row (kill-test family 4,
// grammar half). Each fixture asserts (a) its stable diagnostic code appears
// and (b) the message/fix is non-empty and names the std replacement.
// WRITTEN RED-FIRST per the STAGE-A-PLAN red-path-first rule.

import (
	"strings"
	"testing"
)

type banFixture struct {
	name string
	code string
	src  string
	// fixMention: a fragment that must appear in Message or Fix (the
	// "fix in the error" std replacement).
	fixMention string
}

var banFixtures = []banFixture{
	{"class", "BAN_CLASS",
		"export class A {}\n",
		"function"},
	{"this", "BAN_THIS",
		"export function f(): unknown { return this; }\n",
		"parameter"},
	{"this_call_apply_bind", "BAN_THIS",
		"export function f(g: () => number): number { return g.call(); }\n",
		"direct call"},
	{"decorator", "BAN_DECORATOR",
		"@dec\nexport class A {}\n",
		"AST pass"},
	{"getset", "BAN_GETSET",
		"export const o = { get x() { return 1; } };\n",
		"function"},
	{"var", "BAN_VAR",
		"export function f(): number { var x = 1; return x; }\n",
		"const"},
	{"enum", "BAN_ENUM",
		"export enum E { A, B }\n",
		"string-literal union"},
	{"namespace", "BAN_NAMESPACE",
		"namespace N { export const x = 1; }\n",
		"module"},
	{"declare_ambient", "BAN_NAMESPACE",
		"declare const g: number;\nexport const x = 1;\n",
		"module"},
	{"new", "BAN_NEW",
		"export const d = new Thing();\n",
		"factory"},
	{"instanceof", "BAN_INSTANCEOF",
		"export function f(x: unknown): boolean { return x instanceof f; }\n",
		"typeof"},
	{"delete", "BAN_DELETE",
		"export function f(o: { a?: number }): void { delete o.a; }\n",
		"spread"},
	{"generator", "BAN_GENERATOR",
		"export function* g(): unknown { yield 1; }\n",
		"Iter"},
	{"symbol", "BAN_SYMBOL",
		"export const s = Symbol(\"x\");\n",
		"string"},
	{"label", "BAN_LABEL",
		"export function f(): void { outer: for (;;) { break outer; } }\n",
		"structured"},
	{"forin", "BAN_FORIN",
		"export function f(o: { a: number }): void { for (const k in o) { } }\n",
		"keys"},
	{"eval", "BAN_WITH_EVAL",
		"export const e = eval(\"1\");\n",
		"std"},
	{"proxy", "BAN_WITH_EVAL",
		"export const p = Proxy;\n",
		"std"},
	{"reflect", "BAN_WITH_EVAL",
		"export const r = Reflect;\n",
		"std"},
	{"tagged_template", "BAN_TAGGED_TEMPLATE",
		"export function f(tag: unknown): unknown { return tag`x`; }\n",
		"function"},
	{"comma", "BAN_COMMA",
		"export const c = (1, 2);\n",
		"statement"},
	{"void", "BAN_VOID",
		"export const v = void 0;\n",
		"undefined"},
	{"debugger", "BAN_DEBUGGER",
		"export function f(): void { debugger; }\n",
		"remove"},
	{"any", "BAN_ANY",
		"export const a: any = 1;\n",
		"unknown"},
	{"as_cast", "BAN_AS_CAST",
		"export const x = 1 as unknown;\n",
		"narrow"},
	{"angle_assertion", "BAN_AS_CAST",
		"export const x = <unknown>1;\n",
		"narrow"},
	{"nonnull", "BAN_NONNULL",
		"export function f(x?: number): number { return x!; }\n",
		"narrow"},
	{"function_type", "BAN_FUNCTION_TYPE",
		"export const g: Function = f;\n",
		"function type"},
	{"object_type", "BAN_OBJECT_TYPE",
		"export const o: object = {};\n",
		"shape"},
	{"regex_backreference", "BAN_REGEX_BACKTRACK",
		"export const r = /(a)\\1/;\n",
		"RE2"},
	{"regex_lookahead", "BAN_REGEX_BACKTRACK",
		"export const r = /a(?=b)/;\n",
		"RE2"},
	{"regex_lookbehind", "BAN_REGEX_BACKTRACK",
		"export const r = /(?<=a)b/;\n",
		"RE2"},
	{"nonfinite", "BAN_NONFINITE",
		"export const n = 1e400;\n",
		"finite"},
	{"lone_surrogate", "BAN_LONE_SURROGATE",
		"export const s = \"\\uD800\";\n",
		"code point"},
	{"nonascii_ident", "BAN_NONASCII_IDENT",
		"export const café = 1;\n",
		"string literal"},
}

func codes(ds []Diagnostic) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Code
	}
	return out
}

func findDiag(ds []Diagnostic, code string) (Diagnostic, bool) {
	for _, d := range ds {
		if d.Code == code {
			return d, true
		}
	}
	return Diagnostic{}, false
}

func TestBanFixtures(t *testing.T) {
	for _, fx := range banFixtures {
		t.Run(fx.name, func(t *testing.T) {
			res := Module(fx.src, ModuleContext{ModuleName: "app/fixture"})
			d, ok := findDiag(res.Diagnostics, fx.code)
			if !ok {
				t.Fatalf("expected %s, got diagnostics %v", fx.code, codes(res.Diagnostics))
			}
			if d.Message == "" {
				t.Errorf("%s: empty message", fx.code)
			}
			if d.Fix == "" {
				t.Errorf("%s: empty fix (every ban carries the std replacement)", fx.code)
			}
			blob := strings.ToLower(d.Message + " " + d.Fix)
			if !strings.Contains(blob, strings.ToLower(fx.fixMention)) {
				t.Errorf("%s: message/fix %q does not mention replacement %q", fx.code, blob, fx.fixMention)
			}
			if len(res.Definitions) != 0 {
				t.Errorf("%s: rejection must admit zero definitions (no partial admit)", fx.code)
			}
		})
	}
}

// Positive control: `as const` is NOT BAN_AS_CAST; `in` is admitted; typeof,
// satisfies, optional chaining admitted.
func TestBanFixtures_AdmittedTwins(t *testing.T) {
	src := "export const x = [1, 2] as const;\n" +
		"export function has(o: { a?: number }): boolean { return \"a\" in o; }\n" +
		"export const t = typeof x;\n" +
		"export const s = { a: 1 } satisfies { a: number };\n"
	res := Module(src, ModuleContext{ModuleName: "app/fixture"})
	if !res.OK() {
		t.Fatalf("admitted twins rejected: %v", res.Diagnostics)
	}
	if len(res.Definitions) != 4 {
		t.Fatalf("want 4 definitions, got %d", len(res.Definitions))
	}
}
