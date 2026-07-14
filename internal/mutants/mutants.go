// Package mutants is the ADR-07 §5 direction-(ii) mutant registry: a set of
// NAMED WEAKENINGS compiled into the production enforcement code across the whole
// trust boundary — the six admission verifiers, the ADR-01 grammar gate, and the
// ADR-01/ADR-02 resolver. Each mutant, when active, flips one comparison, drops
// one sink, or widens one matcher in the REAL production path (never a mock —
// ADR-07 §5 BUILD-C pin: "the mutant must weaken the REAL production code path").
//
// The registry is HARD-OFF by default: production admission never enables a
// mutant. Only the adversarial harness (internal/admission, ADR-07 §5) arms one
// mutant at a time, runs the hostile corpus (gate/redpath), asserts the corpus
// KILLS it (≥1 hostile fixture goes green), then disarms. A mutant no corpus
// fixture kills is a surviving mutant and blocks the release (the harness fails).
//
// Honesty guard: Enable requires the registry to be Armed first, and only the
// harness arms it. A stray Enable in a production path is a no-op, so the six
// verifiers / gate / resolver default hard-off even against accidental calls.
package mutants

import "sync"

var (
	mu      sync.Mutex
	armed   bool
	enabled = map[string]bool{}
)

// All is the closed set of registered direction-(ii) mutants (ADR-07 §5 BUILD-C
// pin). Every entry names the enforcement site it weakens and the component that
// owns it. The harness iterates this list; a name here with no killing corpus
// fixture fails the release.
var All = []Mutant{
	{Name: "V1_SKIP_DECLARED_CHECK", Component: "V1", Weakens: "V1 capability-audit: skips the named ⊆ declared check"},
	{Name: "V2_DROP_LOG_SINK", Component: "V2", Weakens: "V2 pii-flow: drops the capability-bearing (outbound/log) sink from the sink-set"},
	{Name: "V3_SKIP_POLICY_PARITY", Component: "V3", Weakens: "V3 catalog-parity: skips the declared-but-unwired policy check"},
	{Name: "V4_ALLOW_EFFECTFUL", Component: "V4", Weakens: "V4 contracts: allows a capability-bearing (effectful) contract clause"},
	{Name: "V5_ALLOW_ALL_TAGS", Component: "V5", Weakens: "V5 capture: treats the host-resource tag as encodable (admits any capture)"},
	{Name: "V6_ALLOW_DESTRUCTIVE", Component: "V6", Weakens: "V6 derivation-parity: allows inline destructive DDL"},
	{Name: "GATE_ALLOW_BANNED_SYNTAX", Component: "grammar-gate", Weakens: "grammar gate: widens the `as`-cast matcher so a banned cast slips through"},
	{Name: "GATE_SKIP_FLOATING_PROMISE", Component: "grammar-gate", Weakens: "grammar gate: skips the floating-promise check"},
	{Name: "GATE_WEAKEN_CAPTURE_R1", Component: "grammar-gate", Weakens: "grammar gate: weakens the R1 const-only-capture predicate"},
	{Name: "RESOLVER_ADMIT_OUT_OF_WORLD", Component: "resolver", Weakens: "resolver: admits an out-of-world import (falls back past the catalog-world boundary)"},
}

// Mutant is one registered weakening.
type Mutant struct {
	Name      string
	Component string
	Weakens   string
}

// Arm enables the registry so Enable can activate a mutant. The harness arms it
// for the duration of a run and Disarms after; production never arms.
func Arm() {
	mu.Lock()
	defer mu.Unlock()
	armed = true
}

// Disarm turns the registry off and clears every active mutant (harness cleanup).
func Disarm() {
	mu.Lock()
	defer mu.Unlock()
	armed = false
	enabled = map[string]bool{}
}

// Enable activates one named mutant. No-op unless the registry is Armed, so a
// production path can never switch a weakening on.
func Enable(name string) {
	mu.Lock()
	defer mu.Unlock()
	if !armed {
		return
	}
	enabled[name] = true
}

// Disable deactivates one named mutant (harness one-at-a-time discipline).
func Disable(name string) {
	mu.Lock()
	defer mu.Unlock()
	delete(enabled, name)
}

// Active reports whether the named mutant is currently switched on. Production
// enforcement code calls this at each weakenable site; it is false by default
// and false whenever the registry is disarmed.
func Active(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	return armed && enabled[name]
}
