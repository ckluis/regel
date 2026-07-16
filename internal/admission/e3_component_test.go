package admission

import (
	"testing"

	"regel.dev/regel/internal/ui"
)

// e3_component_test.go is the BUILD-E (D3) hand-authored component lowering battery
// (STAGE-D §13.3 residue): a def composing the tier-1 vocabulary lowers to a
// `component_template` derived_artifact — the same static/dynamic split as derived
// surfaces (ADR-11 §1). Driven through REAL admission.

func loadComponentTemplateArtifact(t *testing.T, w *world, name string) (*ui.Template, bool) {
	t.Helper()
	var raw string
	ok, err := w.conn.QueryRow(ctxT(t),
		`SELECT detail::text FROM derived_artifact WHERE resource_name=$1 AND pass='component_template'`,
		[]any{name}, &raw)
	if err != nil {
		t.Fatalf("query component_template for %s: %v", name, err)
	}
	if !ok {
		return nil, false
	}
	ct, derr := ui.DecodeTemplate([]byte(raw))
	if derr != nil {
		t.Fatalf("decode component template: %v", derr)
	}
	return ct, true
}

// TestD3ComponentLowered: a hand-authored AccountCard (card wrapping text+badge over
// props fields) lowers to a component template with a dynamic leaf slot per bound
// field, at the right tier-1 leaf.
func TestD3ComponentLowered(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { card, stack, text, badge } from "std/ui";
export function AccountCard(props: { name: string; stage: string }) {
  return card({}, [
    stack({}, [ text({ value: props.name }) ]),
    badge({ value: props.stage })
  ]);
}`
	v, err := admit(ctx, w.conn, src, "app/crm", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("AccountCard must admit: %q %+v", v.Outcome, v.Diagnostics)
	}
	ct, ok := loadComponentTemplateArtifact(t, w, "app/crm/AccountCard")
	if !ok {
		t.Fatalf("no component_template artifact emitted for AccountCard")
	}
	if ct.Kind != "component" {
		t.Fatalf("template kind = %q, want component", ct.Kind)
	}
	byField := map[string]ui.Slot{}
	for _, s := range ct.Slots {
		byField[s.Field] = s
	}
	if s, ok := byField["name"]; !ok || s.Leaf != "text" || s.Kind != "setText" {
		t.Fatalf("name must bind at a text leaf: %+v (slots=%+v)", s, ct.Slots)
	}
	if s, ok := byField["stage"]; !ok || s.Leaf != "badge" {
		t.Fatalf("stage must bind at a badge leaf: %+v", s)
	}
	// The static skeleton must carry the card + stack structure (not flattened).
	if ct.Root == nil || ct.Root.Component != "card" {
		t.Fatalf("root skeleton must be the card: %+v", ct.Root)
	}
}

// TestD3NonComponentNotLowered: an ordinary (non-component) def emits NO
// component_template — the pass recognizes only tier-1 compositions.
func TestD3NonComponentNotLowered(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `export function add(a: number, b: number): number { return a + b; }`
	if v, err := admit(ctx, w.conn, src, "app/util", engineer("dev"), nil); err != nil || v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit add: %v %q %+v", err, v.Outcome, v.Diagnostics)
	}
	if _, ok := loadComponentTemplateArtifact(t, w, "app/util/add"); ok {
		t.Fatalf("a non-component def must not emit a component_template")
	}
}

// TestD3ComponentOutside25Rejected (red-path d): a component referencing a
// vocabulary element OUTSIDE the closed 25-roster is rejected at admission — there
// is no raw-HTML escape hatch (ADR-10 §7 law). std/ui exports exactly the 25, so a
// non-roster name does not resolve and the def never admits.
func TestD3ComponentOutside25Rejected(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { unsafeHtml } from "std/ui";
export function Escape(props: { body: string }) {
  return unsafeHtml({ value: props.body });
}`
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/esc", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome == OutcomeAdmitted {
		t.Fatalf("a component using a non-roster element must NOT admit (no raw-HTML hatch)")
	}
	// No component_template leaked for a rejected escape-hatch def.
	if _, ok := loadComponentTemplateArtifact(t, w, "app/esc/Escape"); ok {
		t.Fatalf("rejected escape-hatch def must emit no component_template")
	}
	assertZeroTrace(t, w, v, d, a, r, "app/esc/Escape")
}
