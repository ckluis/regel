package admission

import "testing"

// d2_v2_component_test.go is the BUILD-D increment D2 red-path battery for the V2
// pii-flow extension to component non-leaf bindings (ADR-10 §7 / ADR-11 §8): a
// custom component-kind definition may bind a Vault/pii value ONLY at the six
// masking leaves; a bind at any other component site is an unmasked sink V2 rejects
// at admission. RED-first: the fixture admits before the non-leaf sink control
// exists, then GREEN closes it. Every existing V2 test stays green.

// TestV2ComponentNonLeafRejects: pii bound into a non-leaf component (heading) is
// rejected with PII_NONLEAF_BIND, zero trace.
func TestV2ComponentNonLeafRejects(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import { heading } from "std/ui";
import type { Vault } from "std/pii";
export function badge(owner: Vault<string>) {
  return heading({ title: owner });
}
`
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/nonleaf", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected (pii at a non-leaf component sink); diags=%+v", v.Outcome, v.Diagnostics)
	}
	if !hasDiag(v, "V2", "PII_NONLEAF_BIND") {
		t.Fatalf("want V2 PII_NONLEAF_BIND, got %+v", v.Diagnostics)
	}
	assertZeroTrace(t, w, v, d, a, r, "app/nonleaf/badge")
}

// TestV2ComponentLeafAdmits: the green twin — binding the SAME pii value at a
// masking leaf (text) admits (the leaf is the sanctioned masking site).
func TestV2ComponentLeafAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import { text } from "std/ui";
import type { Vault } from "std/pii";
export function cell(owner: Vault<string>) {
  return text({ value: owner });
}
`
	v, err := admit(ctx, w.conn, src, "app/leafok", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("binding pii at a masking leaf must admit, got %q %+v", v.Outcome, v.Diagnostics)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/leafok/cell'"); got != 1 {
		t.Fatalf("leaf-bound component pointer missing (%d)", got)
	}
}
