package cek

// WakeKind is the closed set of wake triggers a parked continuation can await
// (ADR-05 §5 BUILD-B). A wake park is distinct from a durable-condition park:
// the machine suspends waiting for the STORE to deliver a value (a fired timer,
// a channel message, or the joined results of child continuations), not for an
// operator/agent to pick a restart. Values are STABLE and append-only.
type WakeKind uint8

const (
	// WakeTimer: wf.sleep(ms). The store computes due = now()+DelayMS at commit
	// and flips the row to 'ready' when the timer fires; resume delivers undefined.
	WakeTimer WakeKind = iota + 1
	// WakeMessage: wf.receive(channel). The store parks until a channel_message
	// lands; resume delivers the message payload value.
	WakeMessage
	// WakeJoin: wf.all / wf.race over thunk closures. The store materializes one
	// child continuation per thunk; quorum flips the parent; resume delivers an
	// Array of child results (all) or the winner's result (race).
	WakeJoin
)

// Wake is the wake trigger a native attaches to a park (ADR-05 §5 BUILD-B). It
// is transient park metadata carried on the Outcome (like Condition) — the STORE
// reads it to write the wake row / child rows at checkpoint; it is NOT part of
// the serialized State (the parent's CFR is independently re-encodable). Exactly
// one WakeKind is live per Wake.
type Wake struct {
	Kind    WakeKind
	DelayMS int64   // timer: sleep duration in milliseconds; store computes due
	Channel string  // message: the channel name to receive on
	Thunks  []Value // join: the TagClosure values the store materializes as children
	Quorum  int     // join: len(Thunks) for all, 1 for race
	Race    bool    // join: true for race (deliver the winner); false for all (deliver every result in thunk order)
}
