package cek

import "context"

// NativeFn is a native (std / AOT) function dispatched by definition hash
// (ADR-04 §5, §7 Value ABI). A non-nil *Condition return means the call parks:
// the machine returns a Parked outcome carrying that durable condition, and a
// later resume delivers the chosen restart's args as the call's value.
type NativeFn func(h *Host, args []Value) (Value, *Condition)

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
// than perform I/O.
type Effect struct {
	Class   string
	Payload map[string]any
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

// SignalCondition is the sentinel a native returns to park on an app-defined
// durable condition (ADR-05 §6 std signal()).
func SignalCondition(class string, restarts []Restart, payload map[string]any) *Condition {
	return &Condition{Class: class, Payload: payload, Restarts: restarts}
}

// --- Stage-A micro-std natives (STAGE-A-PLAN pin #3) --------------------------

// StdMailSend records a mail.send intent (no real I/O in Stage A) and returns a
// record describing the intent.
func StdMailSend(h *Host, args []Value) (Value, *Condition) {
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
func StdContractRequires(h *Host, args []Value) (Value, *Condition) {
	if len(args) == 0 {
		return boolVal(false), nil
	}
	return boolVal(truthy(args[0])), nil
}

// StdContractEnsures mirrors requires for postconditions.
func StdContractEnsures(h *Host, args []Value) (Value, *Condition) {
	if len(args) == 0 {
		return boolVal(false), nil
	}
	return boolVal(truthy(args[0])), nil
}

// StdKeys returns the own-key list of a record (ADR-01 own-key semantics) or the
// index list of an array.
func StdKeys(h *Host, args []Value) (Value, *Condition) {
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
