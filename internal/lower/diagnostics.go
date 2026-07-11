package lower

import "fmt"

// Diagnostic is a stable, deterministic lowering/grammar-gate refusal. Code is a
// stable string (see the BAN_*, SWITCH_*, … constants); Message is human text;
// Fix names the std/dialect replacement ("fix in the error", ADR-01 §4). Line/Col
// are 1-based (UTF-16 column, tsgo's unit); 0 when no span is available.
type Diagnostic struct {
	Code     string
	Severity string // always "error" at Stage A
	Subject  string // the definition (or module) the diagnostic is about
	Message  string
	Line     int
	Col      int
	Fix      string
}

// Stable diagnostic codes (STAGE-A-PLAN pin #9 / ADR-01 §2). Every ADR-01 §2 ban
// row has its own BAN_* code; the grammar-gate extras and default-deny lowering
// codes follow. These strings are a stable contract consumed by admission and
// tests — never renumber or rename within Stage A.
const (
	// default-deny lowering
	CodeLowerUnsupported = "LOWER_UNSUPPORTED"

	// ADR-01 §2 bans
	CodeBanClass           = "BAN_CLASS"
	CodeBanThis            = "BAN_THIS"
	CodeBanDecorator       = "BAN_DECORATOR"
	CodeBanGetSet          = "BAN_GETSET"
	CodeBanVar             = "BAN_VAR"
	CodeBanEnum            = "BAN_ENUM"
	CodeBanNamespace       = "BAN_NAMESPACE"
	CodeBanNew             = "BAN_NEW"
	CodeBanInstanceof      = "BAN_INSTANCEOF"
	CodeBanDelete          = "BAN_DELETE"
	CodeBanGenerator       = "BAN_GENERATOR"
	CodeBanSymbol          = "BAN_SYMBOL"
	CodeBanLabel           = "BAN_LABEL"
	CodeBanForIn           = "BAN_FORIN"
	CodeBanWithEval        = "BAN_WITH_EVAL"
	CodeBanTaggedTemplate  = "BAN_TAGGED_TEMPLATE"
	CodeBanComma           = "BAN_COMMA"
	CodeBanVoid            = "BAN_VOID"
	CodeBanDebugger        = "BAN_DEBUGGER"
	CodeBanAny             = "BAN_ANY"
	CodeBanAsCast          = "BAN_AS_CAST"
	CodeBanNonNull         = "BAN_NONNULL"
	CodeBanFunctionType    = "BAN_FUNCTION_TYPE"
	CodeBanObjectType      = "BAN_OBJECT_TYPE"
	CodeBanRegexBacktrack  = "BAN_REGEX_BACKTRACK"
	CodeBanNonFinite       = "BAN_NONFINITE"
	CodeBanLoneSurrogate   = "BAN_LONE_SURROGATE"
	CodeBanNonASCIIIdent   = "BAN_NONASCII_IDENT"

	// grammar-gate extras
	CodeSwitchFallthrough = "SWITCH_FALLTHROUGH"
	CodeFloatingPromise   = "FLOATING_PROMISE"
	CodeDepCycle          = "DEP_CYCLE"
	CodeCaptureLet        = "CAPTURE_LET"
	CodeParseDepth        = "PARSE_DEPTH"

	// module-shape / resolution
	CodeUnresolvedImport = "IMPORT_UNRESOLVED"
	CodeParseError       = "PARSE_ERROR"
	CodeBadModule        = "MODULE_SHAPE"
)

// fixes maps each code to the "fix in the error" std/dialect replacement message
// (ADR-01 §4 requires every rejection to name the replacement). Non-empty for
// every ban and grammar-gate code — asserted by the per-ban rejection fixtures.
var fixes = map[string]string{
	CodeLowerUnsupported:  "construct has no regel-AST production; rewrite using the admitted ADR-01 §3 surface",
	CodeBanClass:          "no classes: data is plain object shapes, behavior is module-level functions and closures",
	CodeBanThis:           "no receiver in the dialect: pass state as an explicit parameter; drop .call/.apply/.bind",
	CodeBanDecorator:      "no decorators: express derivation as explicit AST/std passes, not reflected metadata",
	CodeBanGetSet:         "no getters/setters: use plain properties and explicit functions",
	CodeBanVar:            "use `let` or `const` instead of `var` (function-scope hoisting is banned)",
	CodeBanEnum:           "use a string-literal union or std states(...) instead of `enum`",
	CodeBanNamespace:      "modules are files → rows: drop namespace/module/declare; use import/export",
	CodeBanNew:            "no `new`: call the std factory function for the value you need",
	CodeBanInstanceof:     "no `instanceof` (no prototypes): narrow with `typeof`, `in`, or a discriminant tag",
	CodeBanDelete:         "no `delete`: build a new object without the key (e.g. object rest)",
	CodeBanGenerator:      "no generators/yield: use std Iter<T> / AsyncIter<T> for lazy/serializable iteration",
	CodeBanSymbol:         "no Symbol or symbol keys: use string/number keys",
	CodeBanLabel:          "no labels: use plain break/continue and structured control flow",
	CodeBanForIn:          "no for-in: iterate `for (const k of keys(obj))` (std/iter.keys)",
	CodeBanWithEval:       "no with/eval/Proxy/Reflect: dynamic scope and interception are ungovernable",
	CodeBanTaggedTemplate: "no tagged templates: call an ordinary std builder function",
	CodeBanComma:          "no comma operator: split into separate statements",
	CodeBanVoid:           "no `void` operator: use `undefined` for the value, or a statement for the effect",
	CodeBanDebugger:       "no `debugger`: remove the host-debugger hook",
	CodeBanAny:            "no `any`: use `unknown` and narrow",
	CodeBanAsCast:         "no `as`/`<T>` assertions except `as const`: use `satisfies` or narrow with `unknown`",
	CodeBanNonNull:        "no non-null `!`: narrow the value or handle the null/undefined case",
	CodeBanFunctionType:   "no `Function` type: write the explicit `(args) => ret` signature",
	CodeBanObjectType:     "no `object` type: write an explicit shape or Record<K, V>",
	CodeBanRegexBacktrack: "no regex backreferences/lookaround: the engine is RE2 — rewrite without backtracking",
	CodeBanNonFinite:      "no non-finite numeric literal: only finite f64 literals have a canonical encoding",
	CodeBanLoneSurrogate:  "no lone surrogate in a string: use well-formed Unicode code points",
	CodeBanNonASCIIIdent:  "identifiers must match [A-Za-z_$][A-Za-z0-9_$]*: put human language in string literals",
	CodeSwitchFallthrough: "each non-empty `case` must end in break/return/continue/throw; case labels must be literals",
	CodeFloatingPromise:   "await the promise, return it, or pass it to a std combinator (all/race)",
	CodeDepCycle:          "no mutual recursion across definitions: merge the cycle into one definition or route through a std dispatch table",
	CodeCaptureLet:        "a closure may not capture a reassigned `let`: bind a `const` copy before the closure",
	CodeParseDepth:        "submission nesting exceeds the gate depth ceiling: flatten the expression/statement nesting",
	CodeUnresolvedImport:  "import does not resolve against std/ or app/ in the catalog",
	CodeParseError:        "fix the syntax error reported by the parser",
	CodeBadModule:         "top-level module shape not admitted: use exported/plain function, const, type, or interface declarations",
}

// diag builds a Diagnostic with the registered fix for code.
func diag(code, msg string, line, col int) Diagnostic {
	return Diagnostic{Code: code, Severity: "error", Message: msg, Line: line, Col: col, Fix: fixes[code]}
}

func diagf(code string, line, col int, format string, a ...any) Diagnostic {
	return diag(code, fmt.Sprintf(format, a...), line, col)
}
