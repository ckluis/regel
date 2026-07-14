package cek

import (
	"context"

	"regel.dev/regel/internal/mutants"
)

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
	// MUTANT EVAL_EFFECT_ORDER_TRANSPOSED (ADR-04 §6 R1-02, layer c): transposing
	// the newly recorded effect before the previous one silently reorders the
	// effect-class trace — the regel-native differential oracle must catch it.
	if mutants.Active("EVAL_EFFECT_ORDER_TRANSPOSED") && len(h.Effects) >= 1 {
		last := h.Effects[len(h.Effects)-1]
		h.Effects[len(h.Effects)-1] = Effect{Class: class, Payload: payload}
		h.Effects = append(h.Effects, last)
		return
	}
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
	// Runtime capability gate (defense in depth, ADR-05 §4): the caller's
	// principal must be the operator or hold the mail.send grant. Otherwise the
	// call parks on capability.revoked and records NO effect (fail closed).
	if !h.Principal.IsOperator && !h.Principal.Grants["mail.send"] {
		return undef(), &NativePark{Condition: SignalCondition("capability.revoked",
			[]Restart{
				{Name: "re-grant", Label: "Re-grant mail.send", CapabilityRequired: "operator"},
				{Name: "abort", Label: "Abort"},
			},
			map[string]any{"capability": "mail.send"})}
	}
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

// contractViolationPark builds the typed durable-condition park a violated
// boundary clause raises (BUILD-C runtime discharge of the V4-derived boundary
// validators; ADR-04 §6 layers a/b). The turn is refused at the boundary: the
// park is durable and resumable through the ADR-05 restart discipline, and — the
// pre-violation guarantee — no effect recorded in this turn ever fires (the park
// path carries no effect trace to the checkpoint).
func contractViolationPark(clause string) *NativePark {
	return &NativePark{Condition: SignalCondition("contract."+clause+".violated",
		[]Restart{{Name: "abort", Label: "Abort"}},
		map[string]any{"clause": clause})}
}

// StdContractPre is the ENFORCING precondition boundary validator (std/contract
// .pre): a falsy predicate refuses entry with a typed contract.pre.violated
// park. This is the runtime discharge of the validator artifact the derivation
// seam mirrors from the clause (ADR-07 §4 V4).
func StdContractPre(h *Host, args []Value) (Value, *NativePark) {
	ok := len(args) > 0 && truthy(args[0])
	// MUTANT EVAL_PRE_ALWAYS_SATISFIED (ADR-04 §6 R1-02, layer a): treating a
	// violated precondition as satisfied lets a refused boundary evaluate — the
	// differential oracle must catch the wrong verdict.
	if mutants.Active("EVAL_PRE_ALWAYS_SATISFIED") {
		ok = true
	}
	if !ok {
		return undef(), contractViolationPark("pre")
	}
	return undef(), nil
}

// StdContractPost is the ENFORCING postcondition boundary validator
// (std/contract.post): a falsy predicate refuses exit with a typed
// contract.post.violated park, with the clause as the rejection subject.
func StdContractPost(h *Host, args []Value) (Value, *NativePark) {
	ok := len(args) > 0 && truthy(args[0])
	// MUTANT EVAL_VALIDATOR_ZERO_ACCEPTS (ADR-04 §6 R1-02, layer b): widening the
	// validator's accept set so a numeric-0 predicate passes is the off-by-one
	// class of validator bug — the differential oracle must catch the wrong
	// validator outcome.
	if mutants.Active("EVAL_VALIDATOR_ZERO_ACCEPTS") &&
		len(args) > 0 && args[0].Tag == TagF64 && args[0].N == 0 {
		ok = true
	}
	if !ok {
		return undef(), contractViolationPark("post")
	}
	return undef(), nil
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

// wfFault builds a fail-closed durable-condition park for a wf.* argument fault
// (a non-serializable programming error surfaced as a resumable condition, never
// a crash). Restart set is [abort].
func wfFault(class, msg string) *NativePark {
	return &NativePark{Condition: SignalCondition(class,
		[]Restart{{Name: "abort", Label: "Abort"}}, map[string]any{"error": msg})}
}

// StdWfSleep parks on a timer wake (wf.sleep(ms)); resume delivers undefined.
func StdWfSleep(h *Host, args []Value) (Value, *NativePark) {
	if len(args) < 1 || args[0].Tag != TagF64 {
		return undef(), wfFault("wf.arg", "wf.sleep expects a number of milliseconds")
	}
	return undef(), &NativePark{Wake: &Wake{Kind: WakeTimer, DelayMS: int64(args[0].N)}}
}

// StdWfReceive parks on a message wake (wf.receive(channel)); resume delivers the
// message payload value.
func StdWfReceive(h *Host, args []Value) (Value, *NativePark) {
	if len(args) < 1 || args[0].Tag != TagStr {
		return undef(), wfFault("wf.arg", "wf.receive expects a channel name")
	}
	return undef(), &NativePark{Wake: &Wake{Kind: WakeMessage, Channel: args[0].S}}
}

// StdWfSend records a channel.send effect carrying the full-fidelity payload
// value (wf.send(channel, value)); it does NOT park. The store applies it
// transactionally at checkpoint.
func StdWfSend(h *Host, args []Value) (Value, *NativePark) {
	if len(args) < 2 || args[0].Tag != TagStr {
		return undef(), wfFault("wf.arg", "wf.send expects (channel, value)")
	}
	h.RecordEffectVal("channel.send", map[string]any{"channel": args[0].S}, args[1])
	return undef(), nil
}

// StdWfAll parks on a join wake with quorum = len(thunks) (wf.all(thunks)).
func StdWfAll(h *Host, args []Value) (Value, *NativePark) { return wfJoin(args, false) }

// StdWfRace parks on a join wake with quorum = 1 (wf.race(thunks)).
func StdWfRace(h *Host, args []Value) (Value, *NativePark) { return wfJoin(args, true) }

// wfJoin validates a thunk array and parks a join wake. Every element must be a
// closure (the dialect's only deferred-computation value, ADR-05 §5 BUILD-B); a
// non-closure element fails closed.
func wfJoin(args []Value, race bool) (Value, *NativePark) {
	if len(args) < 1 || args[0].Tag != TagArray {
		return undef(), wfFault("wf.arg", "wf.all/race expects an array of thunks")
	}
	elems := args[0].arr().Elems
	thunks := make([]Value, 0, len(elems))
	for _, el := range elems {
		if el.Tag != TagClosure {
			return undef(), wfFault("wf.thunk", "join thunk is not a closure")
		}
		thunks = append(thunks, el)
	}
	quorum := len(thunks)
	if race {
		quorum = 1
	}
	return undef(), &NativePark{Wake: &Wake{Kind: WakeJoin, Thunks: thunks, Quorum: quorum, Race: race}}
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
