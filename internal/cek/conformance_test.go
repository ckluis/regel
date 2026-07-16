package cek

import (
	"context"
	"strings"
	"testing"

	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/rast"
)

// buildNativesClassed mirrors buildNatives but attaches a declared effect class
// to each native, so the ADR-10 §6 std-conformance gate can be exercised.
func buildNativesClassed(t *testing.T, source string, natives map[string]NativeFn, classes map[string]string) (*Interp, map[string]string) {
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
		reg.SetEffectClass(h, classes[intrinsic])
		hashOf[intrinsic] = h
	}
	resolve := func(name string) (string, bool) { h, ok := hashOf[name]; return h, ok }
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

// evilReadWrites is a native DECLARED `read` (inline, no checkpoint) whose body
// LIES: it records an external effect. The §6 conformance gate must catch it.
func evilReadWrites(h *Host, _ []Value) (Value, *NativePark) {
	h.RecordEffect("mail.send", map[string]any{"to": "leak@evil"})
	return undef(), nil
}

// honestRead is the control: a `read`-declared native that records nothing.
func honestRead(_ *Host, _ []Value) (Value, *NativePark) { return NumV(7), nil }

// TestEffectClassConformanceRedPath (ADR-10 §6 std-conformance gate, RED-path 2):
// a read-declared native that records an effect is detected and failed closed;
// an honest read-declared native runs inline and completes.
func TestEffectClassConformanceRedPath(t *testing.T) {
	natives := map[string]NativeFn{
		"std/evil.peek": evilReadWrites,
		"std/good.peek": honestRead,
	}
	classes := map[string]string{"std/evil.peek": "read", "std/good.peek": "read"}

	t.Run("liar_fails_closed", func(t *testing.T) {
		src := `import { peek } from "std/evil";
export function f(): number { peek(); return 1; }`
		in, names := buildNativesClassed(t, src, natives, classes)
		o := in.Run(context.Background(), RunReq{DefHash: names["f"], Tier: TierTrusted})
		// Fails closed: OutError (internal eval error) — ParkOutcome maps it to
		// status='failed' + a step.failed durable condition, never a committed effect.
		if o.Kind != OutError || o.Err == nil {
			t.Fatalf("expected OutError (conformance catch, fail closed), got kind=%d err=%v", o.Kind, o.Err)
		}
		msg := o.Err.Error()
		if !strings.Contains(msg, "conformance") || !strings.Contains(msg, "effect-class") {
			t.Fatalf("error = %q, want a conformance effect-class violation", msg)
		}
	})

	t.Run("honest_read_runs_inline", func(t *testing.T) {
		src := `import { peek } from "std/good";
export function f(): number { return peek() + 1; }`
		in, names := buildNativesClassed(t, src, natives, classes)
		o := in.Run(context.Background(), RunReq{DefHash: names["f"], Tier: TierTrusted})
		if o.Kind != OutDone || o.Value.N != 8 {
			t.Fatalf("honest read: kind=%d val=%+v, want Done 8", o.Kind, o.Value)
		}
		if len(o.Effects) != 0 {
			t.Fatalf("read recorded %d effects, want 0", len(o.Effects))
		}
	})
}
