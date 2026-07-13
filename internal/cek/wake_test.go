package cek

import (
	"context"
	"testing"

	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/rast"
)

// buildNatives lowers a module that imports the given std natives, wiring a
// resolver + registry + MapSource carrying the KNativeBody defs (mirrors the
// cfr store test's native-dispatch harness, but over MapSource).
func buildNatives(t *testing.T, source string, natives map[string]NativeFn) (*Interp, map[string]string) {
	t.Helper()
	src := MapSource{}
	reg := NewRegistry()
	hashOf := map[string]string{}
	for intrinsic, fn := range natives {
		nb := rast.Normalize(&rast.Node{Kind: rast.KNativeBody, Str: intrinsic,
			Kids: []*rast.Node{{Kind: rast.TKeyword, Str: "unknown"}}})
		h := rast.Address(nb)
		src[h] = nb
		reg.Register(h, fn)
		hashOf[intrinsic] = h
	}
	resolve := func(name string) (string, bool) {
		h, ok := hashOf[name]
		return h, ok
	}
	r := lower.Module(source, lower.ModuleContext{ModuleName: "app/test", Resolve: resolve})
	if !r.OK() {
		t.Fatalf("lower: %v", r.Diagnostics)
	}
	names := map[string]string{}
	for _, d := range r.Definitions {
		src[d.Hash] = d.Body
		names[d.Name] = d.Hash
	}
	return New(src, reg), names
}

func wfNatives() map[string]NativeFn {
	return map[string]NativeFn{
		"std/wf.sleep":   StdWfSleep,
		"std/wf.receive": StdWfReceive,
		"std/wf.send":    StdWfSend,
		"std/wf.all":     StdWfAll,
		"std/wf.race":    StdWfRace,
	}
}

// TestWfSleepParksTimer: wf.sleep parks a WakeTimer with the right DelayMS and
// ParkKind=ParkWake; resume (undefined delivered) completes with the program's
// own result.
func TestWfSleepParksTimer(t *testing.T) {
	src := `import { sleep } from "std/wf";
export function f(): number { sleep(500); return 42; }`
	in, names := buildNatives(t, src, wfNatives())
	ctx := context.Background()

	o := in.Run(ctx, RunReq{DefHash: names["f"], Tier: TierTrusted})
	if o.Kind != OutParked {
		t.Fatalf("expected Parked, got kind=%d err=%v", o.Kind, o.Err)
	}
	if o.Wake == nil || o.Wake.Kind != WakeTimer {
		t.Fatalf("expected WakeTimer, got wake=%+v", o.Wake)
	}
	if o.Wake.DelayMS != 500 {
		t.Fatalf("DelayMS = %d, want 500", o.Wake.DelayMS)
	}
	if o.State.ParkKind != ParkWake {
		t.Fatalf("ParkKind = %d, want ParkWake(%d)", o.State.ParkKind, ParkWake)
	}
	if o.Condition != nil {
		t.Fatalf("wake park must not carry a Condition")
	}

	res := in.Resume(ctx, o.State, Delivery{Value: nil}, Principal{})
	if res.Kind != OutDone || res.Value.Tag != TagF64 || res.Value.N != 42 {
		t.Fatalf("resume: kind=%d val=%+v", res.Kind, res.Value)
	}
}

// TestWfReceiveDeliversPayload: wf.receive parks a WakeMessage; the resumed value
// flows into the subsequent computation.
func TestWfReceiveDeliversPayload(t *testing.T) {
	src := `import { receive } from "std/wf";
export function f(): number { const x = receive("orders"); return x + 1; }`
	in, names := buildNatives(t, src, wfNatives())
	ctx := context.Background()

	o := in.Run(ctx, RunReq{DefHash: names["f"], Tier: TierTrusted})
	if o.Kind != OutParked {
		t.Fatalf("expected Parked, got kind=%d", o.Kind)
	}
	if o.Wake == nil || o.Wake.Kind != WakeMessage || o.Wake.Channel != "orders" {
		t.Fatalf("expected WakeMessage on 'orders', got %+v", o.Wake)
	}

	payload := NumV(41)
	res := in.Resume(ctx, o.State, Delivery{Value: &payload}, Principal{})
	if res.Kind != OutDone || res.Value.N != 42 {
		t.Fatalf("delivered payload did not flow: kind=%d val=%+v", res.Kind, res.Value)
	}
}

// TestWfAllParksJoin: wf.all parks a WakeJoin capturing the thunk closures with
// quorum = len; resume delivers an Array of results in thunk order.
func TestWfAllParksJoin(t *testing.T) {
	src := `import { all } from "std/wf";
export function f(): number { const rs = all([() => 1, () => 2, () => 3]); return rs[0] + rs[1] + rs[2]; }`
	in, names := buildNatives(t, src, wfNatives())
	ctx := context.Background()

	o := in.Run(ctx, RunReq{DefHash: names["f"], Tier: TierTrusted})
	if o.Kind != OutParked {
		t.Fatalf("expected Parked, got kind=%d err=%v", o.Kind, o.Err)
	}
	if o.Wake == nil || o.Wake.Kind != WakeJoin {
		t.Fatalf("expected WakeJoin, got %+v", o.Wake)
	}
	if len(o.Wake.Thunks) != 3 || o.Wake.Quorum != 3 {
		t.Fatalf("thunks=%d quorum=%d, want 3/3", len(o.Wake.Thunks), o.Wake.Quorum)
	}
	for i, tk := range o.Wake.Thunks {
		if tk.Tag != TagClosure {
			t.Fatalf("thunk %d is not a closure (tag %d)", i, tk.Tag)
		}
	}

	results := arrVal(&ArrayObj{Elems: []Value{NumV(10), NumV(20), NumV(30)}})
	res := in.Resume(ctx, o.State, Delivery{Value: &results}, Principal{})
	if res.Kind != OutDone || res.Value.N != 60 {
		t.Fatalf("all resume: kind=%d val=%+v", res.Kind, res.Value)
	}
}

// TestWfRaceParksJoinQuorum1: wf.race parks a WakeJoin with quorum 1; resume
// delivers the winner's result value.
func TestWfRaceParksJoinQuorum1(t *testing.T) {
	src := `import { race } from "std/wf";
export function f(): number { const w = race([() => 1, () => 2]); return w; }`
	in, names := buildNatives(t, src, wfNatives())
	ctx := context.Background()

	o := in.Run(ctx, RunReq{DefHash: names["f"], Tier: TierTrusted})
	if o.Kind != OutParked {
		t.Fatalf("expected Parked, got kind=%d", o.Kind)
	}
	if o.Wake == nil || o.Wake.Kind != WakeJoin || o.Wake.Quorum != 1 {
		t.Fatalf("expected WakeJoin quorum 1, got %+v", o.Wake)
	}
	if len(o.Wake.Thunks) != 2 {
		t.Fatalf("thunks=%d, want 2", len(o.Wake.Thunks))
	}

	winner := NumV(99)
	res := in.Resume(ctx, o.State, Delivery{Value: &winner}, Principal{})
	if res.Kind != OutDone || res.Value.N != 99 {
		t.Fatalf("race resume: kind=%d val=%+v", res.Kind, res.Value)
	}
}

// TestWfAllNonClosureFailsClosed: a non-closure thunk element fails closed (never
// a WakeJoin park, never a crash).
func TestWfAllNonClosureFailsClosed(t *testing.T) {
	src := `import { all } from "std/wf";
export function f(): number { const rs = all([1, 2]); return 0; }`
	in, names := buildNatives(t, src, wfNatives())
	ctx := context.Background()

	o := in.Run(ctx, RunReq{DefHash: names["f"], Tier: TierTrusted})
	if o.Kind == OutDone {
		t.Fatalf("non-closure thunk must not complete normally")
	}
	if o.Kind == OutParked && o.Wake != nil {
		t.Fatalf("non-closure thunk must not park on a join wake: %+v", o.Wake)
	}
	// Fail-closed: a durable condition park (fault-style), not a WakeJoin.
	if o.Kind == OutParked && (o.Condition == nil || o.Condition.Class != "wf.thunk") {
		t.Fatalf("expected wf.thunk condition, got %+v", o.Condition)
	}
}

// TestWfSendRecordsEffect: wf.send records a channel.send effect carrying the
// full-fidelity payload Value and does NOT park.
func TestWfSendRecordsEffect(t *testing.T) {
	h := &Host{}
	v, park := StdWfSend(h, []Value{StrV("orders"), NumV(7)})
	if park != nil {
		t.Fatalf("wf.send must not park, got %+v", park)
	}
	if v.Tag != TagUndefined {
		t.Fatalf("wf.send returns undefined, got %+v", v)
	}
	if len(h.Effects) != 1 {
		t.Fatalf("wf.send recorded %d effects, want 1", len(h.Effects))
	}
	e := h.Effects[0]
	if e.Class != "channel.send" {
		t.Fatalf("effect class = %q, want channel.send", e.Class)
	}
	if e.Payload["channel"] != "orders" {
		t.Fatalf("effect channel = %v, want orders", e.Payload["channel"])
	}
	if e.Val.Tag != TagF64 || e.Val.N != 7 {
		t.Fatalf("effect Val = %+v, want number 7", e.Val)
	}
}

// TestMailSendCapabilityGate: an ungranted principal parks capability.revoked and
// records ZERO effects; an operator or a mail.send-granted principal records one.
func TestMailSendCapabilityGate(t *testing.T) {
	// Ungranted: park capability.revoked, no effect.
	h := &Host{Principal: Principal{Subject: "tenant"}}
	_, park := StdMailSend(h, []Value{StrV("a@b.com"), StrV("hi")})
	if park == nil || park.Condition == nil || park.Condition.Class != "capability.revoked" {
		t.Fatalf("ungranted mail.send must park capability.revoked, got %+v", park)
	}
	if len(h.Effects) != 0 {
		t.Fatalf("ungranted mail.send recorded %d effects, want 0", len(h.Effects))
	}
	// The re-grant restart requires the operator capability.
	var names []string
	for _, r := range park.Condition.Restarts {
		names = append(names, r.Name)
	}
	if len(names) < 2 || names[0] != "re-grant" {
		t.Fatalf("expected re-grant/abort restarts, got %v", names)
	}

	// Operator: effect recorded once, no park.
	hOp := &Host{Principal: Principal{IsOperator: true}}
	if _, p := StdMailSend(hOp, []Value{StrV("a@b.com"), StrV("hi")}); p != nil {
		t.Fatalf("operator mail.send parked: %+v", p)
	}
	if len(hOp.Effects) != 1 {
		t.Fatalf("operator mail.send effects = %d, want 1", len(hOp.Effects))
	}

	// Explicit grant: effect recorded once.
	hGr := &Host{Principal: Principal{Grants: map[string]bool{"mail.send": true}}}
	if _, p := StdMailSend(hGr, []Value{StrV("a@b.com"), StrV("hi")}); p != nil {
		t.Fatalf("granted mail.send parked: %+v", p)
	}
	if len(hGr.Effects) != 1 {
		t.Fatalf("granted mail.send effects = %d, want 1", len(hGr.Effects))
	}
}
