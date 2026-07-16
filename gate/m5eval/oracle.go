package m5eval

import (
	"fmt"
	"strings"

	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/oracle"
	"regel.dev/regel/internal/rast"
)

// oracle.go is the per-task BEHAVIORAL oracle (ADR-12 §3a "behaviorally correct
// per a per-task oracle check"). It is INDEPENDENT of admission: it lowers a
// candidate's source and evaluates it through the regel-native reference reducer
// (internal/oracle) — a second, disagreeing witness that shares no code path with
// the production CEK machine — and compares its output on every task Input to the
// known-good Reference's output. Admission proves the patch is ADMISSIBLE; this
// proves it is CORRECT. A known-bad-but-admissible solution passes admission but
// FAILS here, so the harness cannot be gamed by admission alone.

// refValues converts task inputs (float64|string|bool) to reference values.
func refValues(in []any) []oracle.Value {
	out := make([]oracle.Value, 0, len(in))
	for _, a := range in {
		switch v := a.(type) {
		case float64:
			out = append(out, oracle.Value{Kind: oracle.VNum, Num: v})
		case string:
			out = append(out, oracle.Value{Kind: oracle.VStr, Str: v})
		case bool:
			out = append(out, oracle.Value{Kind: oracle.VBool, Bool: v})
		default:
			panic(fmt.Sprintf("m5eval: input kind not covered: %T", a))
		}
	}
	return out
}

// evalOne lowers source (pure compute, no imports) and evaluates Entry against
// one input vector, returning the reference reducer's rendered observable
// ("value:<render>" | "violation:<clause>" | "throw" | "error:<msg>").
func evalOne(source, module, entry string, in []any) string {
	res := lower.Module(source, lower.ModuleContext{ModuleName: module})
	if !res.OK() {
		var msgs []string
		for _, d := range res.Diagnostics {
			msgs = append(msgs, d.Message)
		}
		return "error:lower:" + strings.Join(msgs, ";")
	}
	defs := map[string]*rast.Node{}
	entryHash := ""
	for _, d := range res.Definitions {
		defs[d.Hash] = d.Body
		if d.Name == entry {
			entryHash = d.Hash
		}
	}
	if entryHash == "" {
		return "error:no-entry:" + entry
	}
	m := &oracle.Machine{Defs: defs, Intrinsics: map[string]string{}}
	r := m.Run(entryHash, refValues(in))
	switch r.Kind {
	case "value":
		return "value:" + oracle.Render(r.Value)
	case "violation":
		return "violation:" + r.Clause
	case "throw":
		return "throw"
	default:
		return "error:" + r.Err
	}
}

// referenceOutputs returns the known-good Reference's observable per input — the
// ground truth a candidate must match.
func (task AuthoringTask) referenceOutputs() []string {
	out := make([]string, len(task.Inputs))
	for i, in := range task.Inputs {
		out[i] = evalOne(task.Reference, task.Module, task.Entry, in)
	}
	return out
}

// BehaviorOK reports whether candidate source is behaviorally correct for the
// task: it lowers, exposes Entry, and matches the Reference's observable on every
// input. mismatch names the first divergence (for the transcript). A candidate
// that does not even lower is not behaviorally OK (but it also would not have
// admitted — admission is scored separately).
func (task AuthoringTask) BehaviorOK(candidate string) (ok bool, detail string) {
	want := task.referenceOutputs()
	for i, in := range task.Inputs {
		got := evalOne(candidate, task.Module, task.Entry, in)
		if got != want[i] {
			return false, fmt.Sprintf("input %v: want %q got %q", in, want[i], got)
		}
	}
	return true, "all inputs matched reference"
}
