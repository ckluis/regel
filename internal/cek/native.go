package cek

import "context"

// NativeFn is a native (std / AOT) function dispatched by definition hash
// (ADR-04 §5, §7 Value ABI). A non-nil *NativePark return means the call parks:
// the machine returns a Parked outcome, and a later resume re-enters at the call
// point delivering either the chosen restart's value (Condition park) or the
// wake's delivered value (Wake park).
type NativeFn func(h *Host, args []Value) (Value, *NativePark)

// NativePark is how a native signals a park (ADR-05 §5/§6). Exactly one field is
// non-nil: Condition parks on a durable condition (status='condition'; resume
// delivers a restart value); Wake parks on a wake trigger (status='sleeping';
// resume delivers a value at the call point).
type NativePark struct {
	Condition *Condition // park on a durable condition (status='condition')
	Wake      *Wake      // park on a wake (status='sleeping')
}

// Registry maps a definition hash to its native implementation. The kernel
// populates it at genesis (ADR-10 §2).
type Registry struct {
	fns map[string]NativeFn
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{fns: map[string]NativeFn{}} }

// Register binds a hash to a native implementation.
func (r *Registry) Register(hash string, fn NativeFn) { r.fns[hash] = fn }

// lookup returns the native for a hash, if any.
func (r *Registry) lookup(hash string) (NativeFn, bool) {
	fn, ok := r.fns[hash]
	return fn, ok
}

// Effect is one recorded effect intent — the effect-class trace ADR-04 §6.5
// compares for determinism. Stage-A natives (mail.send) record intents rather
// than perform I/O. Val optionally carries a payload Value whose full lattice
// fidelity must survive to the store (e.g. a channel.send body); it is CFR
// value-encoded into the outbox by the checkpoint transaction.
type Effect struct {
	Class   string
	Payload map[string]any
	Val     Value
}

// Host is the capability/effect context threaded to native calls (ADR-04 §5,
// the §7 Host). It carries the principal, the registry, and the effect trace.
type Host struct {
	ctx       context.Context
	reg       *Registry
	Principal Principal
	Effects   []Effect
}

// Ctx exposes the run context to natives.
func (h *Host) Ctx() context.Context { return h.ctx }

// RecordEffect appends an effect intent to the trace.
func (h *Host) RecordEffect(class string, payload map[string]any) {
	h.Effects = append(h.Effects, Effect{Class: class, Payload: payload})
}

// RecordEffectVal appends an effect intent carrying a full-fidelity payload Value
// (ADR-05 §5 BUILD-B channel.send) to the trace.
func (h *Host) RecordEffectVal(class string, payload map[string]any, val Value) {
	h.Effects = append(h.Effects, Effect{Class: class, Payload: payload, Val: val})
}

// SignalCondition is the sentinel a native returns to park on an app-defined
// durable condition (ADR-05 §6 std signal()).
func SignalCondition(class string, restarts []Restart, payload map[string]any) *Condition {
	return &Condition{Class: class, Payload: payload, Restarts: restarts}
}

// --- Stage-A micro-std natives (STAGE-A-PLAN pin #3) --------------------------

// StdMailSend records a mail.send intent (no real I/O in Stage A) and returns a
// record describing the intent.
func StdMailSend(h *Host, args []Value) (Value, *NativePark) {
	to, subject := "", ""
	if len(args) > 0 {
		to = toStr(args[0])
	}
	if len(args) > 1 {
		subject = toStr(args[1])
	}
	h.RecordEffect("mail.send", map[string]any{"to": to, "subject": subject})
	r := newRecord()
	r.set("intent", strVal("mail.send"))
	r.set("to", strVal(to))
	r.set("subject", strVal(subject))
	return recVal(r), nil
}

// StdContractRequires evaluates a precondition predicate value (already reduced
// to a boolean by the caller) and throws-shaped is left to the machine; here it
// returns the boolean verdict.
func StdContractRequires(h *Host, args []Value) (Value, *NativePark) {
	if len(args) == 0 {
		return boolVal(false), nil
	}
	return boolVal(truthy(args[0])), nil
}

// StdContractEnsures mirrors requires for postconditions.
func StdContractEnsures(h *Host, args []Value) (Value, *NativePark) {
	if len(args) == 0 {
		return boolVal(false), nil
	}
	return boolVal(truthy(args[0])), nil
}

// StdKeys returns the own-key list of a record (ADR-01 own-key semantics) or the
// index list of an array.
func StdKeys(h *Host, args []Value) (Value, *NativePark) {
	out := &ArrayObj{}
	if len(args) == 1 {
		switch args[0].Tag {
		case TagRecord:
			for _, k := range args[0].rec().Keys {
				out.Elems = append(out.Elems, strVal(k))
			}
		case TagArray:
			for i := range args[0].arr().Elems {
				out.Elems = append(out.Elems, strVal(itoa(i)))
			}
		}
	}
	return arrVal(out), nil
}

// --- Stage-B micro-std wake natives (ADR-05 §5 BUILD-B) -----------------------
// RED STUBS: these return no park so the wake tests fail; real bodies land GREEN.

// StdWfSleep parks on a timer wake (wf.sleep(ms)).
func StdWfSleep(h *Host, args []Value) (Value, *NativePark) { return undef(), nil }

// StdWfReceive parks on a message wake (wf.receive(channel)).
func StdWfReceive(h *Host, args []Value) (Value, *NativePark) { return undef(), nil }

// StdWfSend records a channel.send effect (wf.send(channel, value)); no park.
func StdWfSend(h *Host, args []Value) (Value, *NativePark) { return undef(), nil }

// StdWfAll parks on a join wake with quorum = len(thunks) (wf.all(thunks)).
func StdWfAll(h *Host, args []Value) (Value, *NativePark) { return undef(), nil }

// StdWfRace parks on a join wake with quorum = 1 (wf.race(thunks)).
func StdWfRace(h *Host, args []Value) (Value, *NativePark) { return undef(), nil }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
