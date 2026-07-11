package tsx

import (
	"reflect"
	"strings"
	"testing"

	shimast "github.com/microsoft/typescript-go/shim/ast"
)

// hasCode reports whether any diagnostic has the given TS code.
func hasCode(ds []Diagnostic, code int) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}

// findCode returns the first diagnostic with the given code, or false.
func findCode(ds []Diagnostic, code int) (Diagnostic, bool) {
	for _, d := range ds {
		if d.Code == code {
			return d, true
		}
	}
	return Diagnostic{}, false
}

// --- Test (c): RED PATH FIRST (written first per the red-path-first rule) ---

// TestTypecheckRed_TypeErrorAndClosedWorld proves two things at once:
//  1. a type error (string assigned to number) yields a diagnostic at the right
//     file/line (TS2322), and
//  2. an import of a path NOT in the world map yields a module-not-found
//     diagnostic (TS2307) — the closed world is enforced by resolution.
func TestTypecheckRed_TypeErrorAndClosedWorld(t *testing.T) {
	req := CheckRequest{
		Files: map[string]string{
			"/app/a.ts": "" +
				"import { missing } from \"app/nope\";\n" + // line 1: not in the map
				"export const n: number = \"not a number\";\n" + // line 2: type error
				"export const m = missing;\n",
		},
		RootFiles: []string{"/app/a.ts"},
	}
	res, err := Typecheck(req)
	if err != nil {
		t.Fatalf("Typecheck error: %v", err)
	}

	// Type error TS2322 on line 2.
	d2322, ok := findCode(res.Diagnostics, 2322)
	if !ok {
		t.Fatalf("expected TS2322 (type not assignable); got %+v", res.Diagnostics)
	}
	if d2322.File != "/app/a.ts" {
		t.Errorf("TS2322 file = %q, want /app/a.ts", d2322.File)
	}
	if d2322.Line != 2 {
		t.Errorf("TS2322 line = %d, want 2", d2322.Line)
	}
	if d2322.Category != "Error" {
		t.Errorf("TS2322 category = %q, want Error", d2322.Category)
	}

	// Module-not-found TS2307 for the out-of-world import (closed world).
	d2307, ok := findCode(res.Diagnostics, 2307)
	if !ok {
		t.Fatalf("expected TS2307 (cannot find module) for out-of-world import; got %+v", res.Diagnostics)
	}
	if d2307.Line != 1 {
		t.Errorf("TS2307 line = %d, want 1", d2307.Line)
	}
	if !strings.Contains(d2307.Message, "app/nope") {
		t.Errorf("TS2307 message should name the specifier, got %q", d2307.Message)
	}
}

// --- Test (a): Parse ---

func TestParse_FunctionAndSyntaxError(t *testing.T) {
	// A small function parses; AST root kind is SourceFile, no parse diagnostics.
	res, err := Parse("/app/greet.ts", "export function greet(name: string): string { return name; }\n")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if res.SourceFile == nil {
		t.Fatal("SourceFile is nil")
	}
	if res.SourceFile.Kind != shimast.KindSourceFile {
		t.Errorf("root kind = %v, want KindSourceFile", res.SourceFile.Kind)
	}
	if len(res.Diagnostics) != 0 {
		t.Errorf("expected no parse diagnostics, got %+v", res.Diagnostics)
	}

	// A syntax error yields a parse diagnostic, not a panic.
	bad, err := Parse("/app/bad.ts", "export function () { \n")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if bad.SourceFile == nil {
		t.Fatal("SourceFile is nil for bad input")
	}
	if len(bad.Diagnostics) == 0 {
		t.Errorf("expected parse diagnostics for a syntax error, got none")
	}
}

// --- Test (b): Typecheck green path (cross-file import) ---

func TestTypecheckGreen_CrossFileImport(t *testing.T) {
	req := CheckRequest{
		Files: map[string]string{
			"/app/b.ts": "export const answer: number = 42;\n",
			"/app/a.ts": "" +
				"import { answer } from \"app/b\";\n" +
				"export const doubled: number = answer * 2;\n",
			"/std/mail.ts": "export function send(to: string): void {}\n",
		},
		RootFiles: []string{"/app/a.ts", "/app/b.ts"},
	}
	res, err := Typecheck(req)
	if err != nil {
		t.Fatalf("Typecheck error: %v", err)
	}
	if len(res.Diagnostics) != 0 {
		t.Fatalf("expected no diagnostics on green path, got %+v", res.Diagnostics)
	}
}

func TestTypecheckGreen_StdImport(t *testing.T) {
	req := CheckRequest{
		Files: map[string]string{
			"/std/mail.ts": "export function send(to: string): void {}\n",
			"/app/a.ts": "" +
				"import { send } from \"std/mail\";\n" +
				"export function run(): void { send(\"x\"); }\n",
		},
		RootFiles: []string{"/app/a.ts"},
	}
	res, err := Typecheck(req)
	if err != nil {
		t.Fatalf("Typecheck error: %v", err)
	}
	if len(res.Diagnostics) != 0 {
		t.Fatalf("expected no diagnostics importing std/mail, got %+v", res.Diagnostics)
	}
}

// --- Test (d): Strictness ---

func TestStrict_ImplicitAnyRejected(t *testing.T) {
	req := CheckRequest{
		Files: map[string]string{
			// Parameter p has no type annotation -> implicit any (TS7006) under
			// noImplicitAny.
			"/app/a.ts": "export function f(p) { return p; }\n",
		},
		RootFiles: []string{"/app/a.ts"},
	}
	res, err := Typecheck(req)
	if err != nil {
		t.Fatalf("Typecheck error: %v", err)
	}
	if !hasCode(res.Diagnostics, 7006) {
		t.Fatalf("expected TS7006 (implicit any) under noImplicitAny, got %+v", res.Diagnostics)
	}
}

func TestStrict_CatchVariableIsUnknown(t *testing.T) {
	req := CheckRequest{
		Files: map[string]string{
			// useUnknownInCatchVariables: e is unknown; assigning it to string errors.
			"/app/a.ts": "" +
				"export function f(): void {\n" +
				"  try { throw new Error(); } catch (e) {\n" +
				"    const s: string = e;\n" + // TS2322: unknown not assignable to string
				"    void s;\n" +
				"  }\n" +
				"}\n",
		},
		RootFiles: []string{"/app/a.ts"},
	}
	res, err := Typecheck(req)
	if err != nil {
		t.Fatalf("Typecheck error: %v", err)
	}
	if !hasCode(res.Diagnostics, 2322) {
		t.Fatalf("expected TS2322 assigning unknown catch var to string, got %+v", res.Diagnostics)
	}
}

// --- Test (e): Hermeticity ---

func TestHermeticity_SameRequestDeepEqual(t *testing.T) {
	newReq := func() CheckRequest {
		return CheckRequest{
			Files: map[string]string{
				"/std/mail.ts": "export function send(to: string): void {}\n",
				"/app/b.ts":    "export const answer: number = 42;\n",
				"/app/a.ts": "" +
					"import { answer } from \"app/b\";\n" +
					"import { send } from \"std/mail\";\n" +
					"export const bad: number = \"nope\";\n" + // deliberate error
					"export const ok = answer;\n" +
					"export function r(): void { send(\"x\"); }\n",
			},
			RootFiles: []string{"/app/a.ts", "/app/b.ts"},
		}
	}
	r1, err := Typecheck(newReq())
	if err != nil {
		t.Fatalf("Typecheck error: %v", err)
	}
	r2, err := Typecheck(newReq())
	if err != nil {
		t.Fatalf("Typecheck error: %v", err)
	}
	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("hermeticity violated:\n r1=%+v\n r2=%+v", r1.Diagnostics, r2.Diagnostics)
	}
	if len(r1.Diagnostics) == 0 {
		t.Fatal("expected the deliberate error to surface (non-empty diagnostics)")
	}
}

// --- Test (f): TS7 version sanity ---

func TestVersion_IsSeven(t *testing.T) {
	v := Version()
	t.Logf("tsgo checker version: %s", v)
	if !strings.HasPrefix(v, "7.") {
		t.Fatalf("expected TypeScript 7.x checker, got %q", v)
	}
}

// --- Parse-depth seam guard ---

func TestParseDepth_DeepNestingFlagged(t *testing.T) {
	// Build a deeply parenthesized expression that exceeds MaxParseDepth.
	var b strings.Builder
	b.WriteString("export const x = ")
	depth := MaxParseDepth + 50
	b.WriteString(strings.Repeat("(", depth))
	b.WriteString("1")
	b.WriteString(strings.Repeat(")", depth))
	b.WriteString(";\n")

	res, err := Parse("/app/deep.ts", b.String())
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !res.DepthExceeded {
		t.Fatalf("expected DepthExceeded for a %d-deep expression (measured %d, limit %d)", depth, res.MaxDepth, MaxParseDepth)
	}
	// A shallow file is not flagged.
	shallow, err := Parse("/app/shallow.ts", "export const y = (((1)));\n")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if shallow.DepthExceeded {
		t.Fatalf("shallow file wrongly flagged (measured %d)", shallow.MaxDepth)
	}
}
