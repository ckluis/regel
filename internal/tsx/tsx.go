// Package tsx is the checker seam (ADR-07 §2): the single package in the kernel
// that talks to the vendored tsgo (TypeScript 7 native compiler) shim. It exposes
// two operations over an in-memory, hermetic module host:
//
//   - Parse: parse ONE file to the tsgo AST (*ast.SourceFile) with parse
//     diagnostics only. This is what the lowering pass (internal/lower) consumes.
//   - Typecheck: construct a FRESH tsgo Program per call over exactly the files in
//     the request (the L0 std / L1 app / L2 patch world), plus the embedded
//     lib.d.ts, and return sorted diagnostics. No disk, no clock, no env, no
//     network, no module resolution outside the supplied map.
//
// # The in-memory world and its import scheme
//
// A CheckRequest carries Files: a map from normalized absolute POSIX path
// (e.g. "/app/crm/deal.ts", "/std/mail.ts") to source text, and RootFiles: the
// affected subset to check. These paths are the keys the tsgo CompilerHost reads
// through a vfs.FS built by vfs.FromMap — the closed world. Nothing else is
// readable except the embedded standard library, overlaid by bundled.WrapFS at
// the bundled:/// scheme and pointed at via the host's DefaultLibraryPath.
//
// Imports inside the sources resolve INSIDE the map only, via two mechanisms:
//
//   1. Path-mapped bare specifiers. The locked tsconfig sets baseUrl "/" and
//      paths { "std/*": ["std/*"], "app/*": ["app/*"] }, so
//      `import { send } from "std/mail"` resolves to "/std/mail.ts" and
//      `import { x } from "app/crm/deal"` resolves to "/app/crm/deal.ts" — the
//      exact map keys, extension stripped. This mirrors the ADR-07 §2 L0/L1/L2
//      layering (std/ and app/ namespaces).
//   2. Relative specifiers (`./b`, `../std/mail`) resolve natively against the
//      importing file's directory within the map.
//
// moduleResolution is `bundler`, so a bare specifier that matches neither the
// std/* nor app/* pattern, and any relative or mapped path with no file in the
// map, produces a module-not-found diagnostic (TS2307) — the closed world is
// enforced by resolution itself, not by a separate check.
//
// # Hermeticity
//
// Every call builds a fresh vfs.FS, CompilerHost, and Program (fresh checker
// state — no tsgo state is carried across calls, per ADR-04 §6.5). Diagnostics
// are a pure function of (Files, RootFiles, locked options, epoch libs): the same
// CheckRequest yields deep-equal results on any kernel (asserted by tests).
package tsx

import (
	"context"
	"sort"

	shimast "github.com/microsoft/typescript-go/shim/ast"
	shimbundled "github.com/microsoft/typescript-go/shim/bundled"
	shimcollections "github.com/microsoft/typescript-go/shim/collections"
	shimcompiler "github.com/microsoft/typescript-go/shim/compiler"
	shimcore "github.com/microsoft/typescript-go/shim/core"
	shimdiagnostics "github.com/microsoft/typescript-go/shim/diagnostics"
	shimlocale "github.com/microsoft/typescript-go/shim/locale"
	shimparser "github.com/microsoft/typescript-go/shim/parser"
	shimscanner "github.com/microsoft/typescript-go/shim/scanner"
	shimtsoptions "github.com/microsoft/typescript-go/shim/tsoptions"
	shimtspath "github.com/microsoft/typescript-go/shim/tspath"
	shimvfs "github.com/microsoft/typescript-go/shim/vfs"
)

const (
	// rootDir is the current directory of the in-memory host. All world paths are
	// absolute under it (POSIX, case-sensitive).
	rootDir = "/"

	// MaxParseDepth is the gate-fixed syntactic nesting-depth ceiling (ADR-07 §3,
	// R1-09). It is checked at the tsx seam as a deterministic guard so a
	// deeply-nested submission is a retrievable PARSE_DEPTH refusal, not a Go-stack
	// crash. The value is a gate-fixed constant for M0/M1; lowering it can only
	// tighten what an accepted submission may nest.
	//
	// NOTE / residue for the lowering agent: this seam measures depth on the
	// already-parsed tsgo tree with an EXPLICIT (heap) stack, so our own check
	// never recurses. The truly hostile case — a submission so deep it exhausts the
	// Go stack inside tsgo's recursive-descent parser, BEFORE any tree exists —
	// cannot be guarded here without editing fork internals (forbidden by the
	// vendoring contract). The canonical at-descent PARSE_DEPTH guard and its
	// stable diagnostic code belong to internal/lower's grammar gate (ADR-01 §4
	// step 3); this constant and CheckParseDepth are the cheap seam-level backstop.
	MaxParseDepth = 4096
)

// Diagnostic is a flattened, deterministic diagnostic. Line/Col are 1-based; Col
// is a UTF-16 code-unit offset (tsgo's native column unit). File is the world
// path (map key) or "" for a global/options diagnostic.
type Diagnostic struct {
	File     string
	Line     int
	Col      int
	Code     int
	Category string
	Message  string
}

// ParseResult is the output of Parse: the tsgo AST root plus parse diagnostics.
// SourceFile is the full tsgo AST the lowering pass traverses.
type ParseResult struct {
	SourceFile  *shimast.SourceFile
	Diagnostics []Diagnostic
	// MaxDepth is the measured maximum syntactic nesting depth of the tree.
	MaxDepth int
	// DepthExceeded is true iff MaxDepth > MaxParseDepth.
	DepthExceeded bool
}

// CheckRequest is the closed world to typecheck. Files maps normalized absolute
// POSIX paths to source; RootFiles is the affected subset to check (must be keys
// of Files).
type CheckRequest struct {
	Files     map[string]string
	RootFiles []string
}

// CheckResult is the sorted, deterministic diagnostic set from a Typecheck.
type CheckResult struct {
	Diagnostics []Diagnostic
}

// Version returns the vendored tsgo/TypeScript checker version (e.g. "7.1.0-dev").
func Version() string { return shimcore.Version() }

// Parse parses a single file to the tsgo AST with parse diagnostics only (no
// typecheck, no module resolution). The returned SourceFile is the full tsgo AST
// for the lowering pass. A syntax error yields parse diagnostics, never a panic.
func Parse(fileName, source string) (*ParseResult, error) {
	opts := shimast.SourceFileParseOptions{
		FileName: fileName,
		Path:     shimtspath.ToPath(fileName, rootDir, true),
		// Force module-ness so a file with no import/export is still parsed as a
		// module (matches the Typecheck host's moduleDetection: force).
		ExternalModuleIndicatorOptions: shimast.ExternalModuleIndicatorOptions{Force: true},
	}
	sf := shimparser.ParseSourceFile(opts, source, shimcore.GetScriptKindFromFileName(fileName))

	res := &ParseResult{SourceFile: sf}
	if sf != nil {
		for _, d := range sf.Diagnostics() {
			res.Diagnostics = append(res.Diagnostics, toDiagnostic(d))
		}
		res.MaxDepth = maxNestingDepth(sf)
		res.DepthExceeded = res.MaxDepth > MaxParseDepth
	}
	sortDiagnostics(res.Diagnostics)
	return res, nil
}

// CheckParseDepth measures the maximum syntactic nesting depth of a parsed tree
// and reports whether it breaches MaxParseDepth. Exposed for the grammar gate.
func CheckParseDepth(sf *shimast.SourceFile) (depth int, exceeded bool) {
	if sf == nil {
		return 0, false
	}
	depth = maxNestingDepth(sf)
	return depth, depth > MaxParseDepth
}

// lockedOptions builds the ADR-01 §4 step 5 locked compiler options. A fresh
// value is returned per call so no option state is shared across admissions.
func lockedOptions() *shimcore.CompilerOptions {
	// TS7 removed `baseUrl`; path-mapping targets must be relative ("./…"),
	// resolved against the host's current directory (rootDir "/"). So "std/mail"
	// maps to "/std/mail" and "app/crm/deal" to "/app/crm/deal", extension added
	// by resolution — the exact world map keys.
	paths := shimcollections.NewOrderedMapFromList([]shimcollections.MapEntry[string, []string]{
		{Key: "std/*", Value: []string{"./std/*"}},
		{Key: "app/*", Value: []string{"./app/*"}},
	})
	return &shimcore.CompilerOptions{
		// Strictness (ADR-01 §4 step 5).
		Strict:                     shimcore.TSTrue,
		NoImplicitAny:              shimcore.TSTrue,
		ExactOptionalPropertyTypes: shimcore.TSTrue,
		NoUncheckedIndexedAccess:   shimcore.TSTrue,
		UseUnknownInCatchVariables: shimcore.TSTrue,
		NoUnusedLocals:             shimcore.TSTrue,
		IsolatedModules:            shimcore.TSTrue,
		VerbatimModuleSyntax:       shimcore.TSTrue,
		NoEmit:                     shimcore.TSTrue,
		// Minimal modern target/lib: esnext. Leaving Lib nil selects the default
		// lib for the target (lib.esnext.full.d.ts), served from the embedded FS.
		Target: shimcore.ScriptTargetESNext,
		Module: shimcore.ModuleKindESNext,
		// Closed resolver over the map only: bundler resolution + baseUrl/paths so
		// "std/*" and "app/*" (and relative specifiers) resolve inside the world.
		ModuleResolution: shimcore.ModuleResolutionKindBundler,
		ModuleDetection:  shimcore.ModuleDetectionKindForce,
		Paths:            paths,
	}
}

// Typecheck constructs a fresh Program over exactly req.Files (+ embedded libs)
// and returns sorted diagnostics for req.RootFiles plus program/global
// diagnostics. Hermetic: no disk, no clock, no env, no resolution outside Files.
func Typecheck(req CheckRequest) (CheckResult, error) {
	// Build the in-memory world. Copy the map so the caller's map is never
	// aliased or mutated, and so the FS is a pure function of the request.
	world := make(map[string]string, len(req.Files))
	for k, v := range req.Files {
		world[k] = v
	}
	fs := shimbundled.WrapFS(shimvfs.FromMap(world, true /*useCaseSensitiveFileNames*/))

	host := shimcompiler.NewCompilerHost(rootDir, fs, shimbundled.LibPath(), nil, nil)

	config := shimtsoptions.NewParsedCommandLine(
		lockedOptions(),
		append([]string(nil), req.RootFiles...),
		shimtspath.ComparePathsOptions{UseCaseSensitiveFileNames: true, CurrentDirectory: rootDir},
	)

	program := shimcompiler.NewProgram(shimcompiler.ProgramOptions{
		Config: config,
		Host:   host,
	})

	ctx := context.Background()

	var raw []*shimast.Diagnostic
	// Program- and global-level diagnostics (options, lib, global scope).
	raw = append(raw, program.GetProgramDiagnostics()...)
	raw = append(raw, program.GetGlobalDiagnostics(ctx)...)

	// Per-root-file diagnostics: syntactic, bind, semantic — the affected set.
	for _, rf := range req.RootFiles {
		sf := program.GetSourceFile(rf)
		if sf == nil {
			continue
		}
		raw = append(raw, program.GetSyntacticDiagnostics(ctx, sf)...)
		raw = append(raw, program.GetBindDiagnostics(ctx, sf)...)
		raw = append(raw, program.GetSemanticDiagnostics(ctx, sf)...)
	}

	out := make([]Diagnostic, 0, len(raw))
	seen := make(map[Diagnostic]struct{}, len(raw))
	for _, d := range raw {
		dg := toDiagnostic(d)
		if _, dup := seen[dg]; dup {
			continue
		}
		seen[dg] = struct{}{}
		out = append(out, dg)
	}
	sortDiagnostics(out)
	return CheckResult{Diagnostics: out}, nil
}

// toDiagnostic flattens a tsgo diagnostic to our deterministic shape, resolving
// its byte position to a 1-based line/column via the source file's line map.
func toDiagnostic(d *shimast.Diagnostic) Diagnostic {
	dg := Diagnostic{
		Code:     int(d.Code()),
		Category: categoryString(d.Category()),
		Message:  d.Localize(shimlocale.Default),
	}
	if f := d.File(); f != nil {
		dg.File = f.FileName()
		line, char := shimscanner.GetECMALineAndUTF16CharacterOfPosition(f, d.Pos())
		dg.Line = line + 1
		dg.Col = int(char) + 1
	}
	return dg
}

// categoryString maps a diagnostic category to a stable, clean severity string.
func categoryString(c shimdiagnostics.Category) string {
	switch c {
	case shimdiagnostics.CategoryError:
		return "Error"
	case shimdiagnostics.CategoryWarning:
		return "Warning"
	case shimdiagnostics.CategorySuggestion:
		return "Suggestion"
	case shimdiagnostics.CategoryMessage:
		return "Message"
	default:
		return "Message"
	}
}

// sortDiagnostics orders diagnostics deterministically by file, line, col, code,
// then message — so identical requests yield byte-identical slices.
func sortDiagnostics(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Col != b.Col {
			return a.Col < b.Col
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
}

// maxNestingDepth returns the maximum node nesting depth of the tree using an
// explicit heap stack (never native recursion), so measuring a pathological
// tree cannot itself overflow the Go stack.
func maxNestingDepth(sf *shimast.SourceFile) int {
	type item struct {
		n     *shimast.Node
		depth int
	}
	stack := []item{{sf.AsNode(), 1}}
	max := 0
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if it.depth > max {
			max = it.depth
		}
		it.n.ForEachChild(func(c *shimast.Node) bool {
			stack = append(stack, item{c, it.depth + 1})
			return false
		})
	}
	return max
}
