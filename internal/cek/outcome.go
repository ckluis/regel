package cek

// OutcomeKind classifies how a Run (or resume) terminated.
type OutcomeKind uint8

const (
	// OutDone: the machine reduced to a final value.
	OutDone OutcomeKind = iota
	// OutParked: the machine hit a suspension surface (fuel exhaustion,
	// governor breach, or a std signal). State carries a complete serializable
	// snapshot; Condition names the durable condition + restarts.
	OutParked
	// OutFaulted: an uncaught throw. The turn rolls back; Stage A surfaces the
	// thrown value in Fault (the kernel records a fault row).
	OutFaulted
	// OutError: an internal evaluation error (malformed AST, unresolved
	// reference, unsupported construct). Fails closed.
	OutError
)

// Outcome is the result of running the machine to a stopping point.
type Outcome struct {
	Kind OutcomeKind

	// OutDone.
	Value Value

	// OutParked. A parked outcome carries EITHER Condition (a durable-condition
	// park, status='condition') OR Wake (a wake park, status='sleeping'), never
	// both (ADR-05 §5/§6).
	State     *State     // full serializable machine snapshot (park point)
	Condition *Condition // durable condition + restarts
	Wake      *Wake      // wake trigger (timer / message / join); nil for condition parks

	// OutFaulted.
	Fault Value

	// OutError.
	Err error

	// Transitions is the number of CEK transitions this run consumed — the
	// counter ADR-04 §8 exposes for the ≤50k-per-request ceiling assertion.
	Transitions int64
}

// Condition is a durable condition raised at a park point (ADR-05 §6). Class is
// a namespaced tag ('fuel.exhausted' | 'runaway' | app-defined); Restarts are
// the named choices rendered as operator buttons / agent choices.
type Condition struct {
	Class    string
	Payload  map[string]any
	Restarts []Restart
}

// Restart is one named recovery choice (ADR-05 §6 restart row).
type Restart struct {
	Name               string
	Label              string
	CapabilityRequired string // e.g. only operators may 'grant-fuel'
}

// standard restart sets ---------------------------------------------------------

func fuelRestarts() []Restart {
	return []Restart{
		{Name: "grant-fuel", Label: "Grant more fuel", CapabilityRequired: "operator"},
		{Name: "abort", Label: "Abort"},
	}
}

func runawayRestarts() []Restart {
	return []Restart{
		{Name: "abort", Label: "Abort"},
		{Name: "grant-time", Label: "Grant more time", CapabilityRequired: "operator"},
	}
}
