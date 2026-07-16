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
	// WakeEvent (BUILD-D, ADR-05 §5): taak.onChange(resource, keys?). The store
	// parks until a derived-resource mutation on `stream` (matching a key in `On`,
	// or ANY row when `On` is empty) commits; that mutation's transaction flips the
	// row ready. Resume delivers undefined (the change is the signal, not a value).
	WakeEvent
)

// WakeMatch is the optional structural predicate on a message receive (BUILD-D,
// ADR-05 §5 `match:<pred>`). Minimal v1 shape: equality of a dotted field Path in
// the message payload record against Equals. Has is false ⇒ the receiver matches
// any message on its channel (FIFO, the Stage-B behavior). Equals is a scalar
// Value (string / number / bool / bigint); the store serializes it into the wake
// jsonb and re-evaluates it against a decoded message payload at delivery.
type WakeMatch struct {
	Path   string
	Equals Value
	Has    bool
}

// Wake is the wake trigger a native attaches to a park (ADR-05 §5 BUILD-B). It
// is transient park metadata carried on the Outcome (like Condition) — the STORE
// reads it to write the wake row / child rows at checkpoint; it is NOT part of
// the serialized State (the parent's CFR is independently re-encodable). Exactly
// one WakeKind is live per Wake.
type Wake struct {
	Kind    WakeKind
	DelayMS int64      // timer: sleep duration in milliseconds; store computes due
	Channel string     // message: the channel name to receive on
	Match   *WakeMatch // message: optional structural match predicate (BUILD-D)
	Thunks  []Value    // join: the TagClosure values the store materializes as children
	Quorum  int        // join: len(Thunks) for all, 1 for race
	Race    bool       // join: true for race (deliver the winner); false for all (deliver every result in thunk order)
	Stream  string     // event: the derived resource whose mutation wakes this park
	On      []string   // event: the row ids to watch (empty ⇒ ANY row on the stream)
}
