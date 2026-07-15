package admission

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/ui"
)

// d2_render_test.go is the BUILD-D increment D2 end-to-end battery: the render
// template pass (ADR-11 §1), server-side first paint over the ADR-10 §7 tier-1
// vocabulary, the per-slot diff, the incremental snapshot digest (§4), and the
// runtime PII masking invariants (§8) — all driven through REAL admission + Postgres.

// loadTemplateBundle reads the 'template' derived_artifact and returns the three
// lowered render templates (detail/form/table).
func loadTemplateBundle(t *testing.T, w *world, resource string) (detail, form, table *ui.Template) {
	t.Helper()
	var raw string
	ok, err := w.conn.QueryRow(ctxT(t),
		`SELECT detail::text FROM derived_artifact WHERE resource_name=$1 AND pass='template'`,
		[]any{resource}, &raw)
	if err != nil || !ok {
		t.Fatalf("load template artifact for %s: ok=%v err=%v", resource, ok, err)
	}
	var bundle struct {
		Version int             `json:"version"`
		Detail  json.RawMessage `json:"detail"`
		Form    json.RawMessage `json:"form"`
		Table   json.RawMessage `json:"table"`
	}
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		t.Fatalf("unmarshal template bundle: %v", err)
	}
	if bundle.Version != ui.TemplateVersion {
		t.Fatalf("template version %d, want %d", bundle.Version, ui.TemplateVersion)
	}
	dt, e1 := ui.DecodeTemplate(bundle.Detail)
	fm, e2 := ui.DecodeTemplate(bundle.Form)
	tb, e3 := ui.DecodeTemplate(bundle.Table)
	if e1 != nil || e2 != nil || e3 != nil {
		t.Fatalf("decode templates: %v %v %v", e1, e2, e3)
	}
	return dt, fm, tb
}

func snapshotMap(state map[string]ui.Materialized) map[string]string {
	out := make(map[string]string, len(state))
	for id, m := range state {
		out[id] = m.Snapshot
	}
	return out
}

// --- the template pass exists for form/table/detail; first paint is real HTML --

func TestD2TemplatePassAndFirstPaint(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	v, err := admit(ctx, w.conn, contactSrc("app/crm"), "app/crm", engineer("dev"), nil)
	if err != nil || v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit Contact: %v / %q %+v", err, v.Outcome, v.Diagnostics)
	}
	// The template pass emitted exactly one artifact, and V6 parity stayed green.
	if got := w.count("SELECT count(*) FROM derived_artifact WHERE resource_name='app/crm/Contact' AND pass='template'"); got != 1 {
		t.Fatalf("template artifact rows = %d, want 1", got)
	}
	detail, form, table := loadTemplateBundle(t, w, "app/crm/Contact")

	const nFields = 14
	if len(detail.Slots) != nFields {
		t.Fatalf("detail slots = %d, want %d (one per field)", len(detail.Slots), nFields)
	}
	if len(form.Slots) != nFields {
		t.Fatalf("form slots = %d, want %d", len(form.Slots), nFields)
	}
	// table: slot 0 is the keyed-list body (spliceList), then one column per field.
	if len(table.Slots) != nFields+1 || table.Slots[0].Kind != "spliceList" {
		t.Fatalf("table slots = %d (want %d) / slot0 kind %q", len(table.Slots), nFields+1, table.Slots[0].Kind)
	}
	// The two pii fields are masked at their §7 leaf in every surface.
	for _, s := range detail.Slots {
		if s.Field == "email" || s.Field == "phone" {
			if !s.Masked || s.MaskLeaf == "" {
				t.Fatalf("pii field %q must be a masked slot: %+v", s.Field, s)
			}
		}
	}

	// First paint of the detail view: real HTML, semantic landmarks, addressable slots.
	data := ui.RenderData{Resource: "app/crm/Contact", Subject: "1", Fields: map[string]string{
		"name": "Acme <Corp>", "score": "42", "site": "https://a.example",
	}}
	html, state := ui.RenderFirstPaint(detail, data, nil)
	if !strings.Contains(html, `<article class="rg-card"`) {
		t.Fatalf("detail root should be a card element:\n%s", html)
	}
	if strings.Contains(html, "<Corp>") {
		t.Fatalf("text not HTML-escaped:\n%s", html)
	}
	if !strings.Contains(html, "Acme &lt;Corp&gt;") {
		t.Fatalf("expected escaped name in paint:\n%s", html)
	}
	if !strings.Contains(html, `data-slot="detail.`) {
		t.Fatalf("slots must be addressable:\n%s", html)
	}
	if len(state) != nFields {
		t.Fatalf("first paint state has %d slots, want %d", len(state), nFields)
	}
}

// --- one field change ⇒ exactly the affected slot; digest incremental == full --

func TestD2DiffAndDigest(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	if v, err := admit(ctx, w.conn, contactSrc("app/crm"), "app/crm", engineer("dev"), nil); err != nil || v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit: %v / %q", err, v.Outcome)
	}
	detail, _, _ := loadTemplateBundle(t, w, "app/crm/Contact")

	base := map[string]string{"name": "Acme", "score": "10", "site": "x"}
	d1 := ui.RenderData{Resource: "app/crm/Contact", Subject: "1", Fields: base}
	_, s1 := ui.RenderFirstPaint(detail, d1, nil)

	// Change exactly one field.
	next := map[string]string{"name": "Acme", "score": "20", "site": "x"}
	d2 := ui.RenderData{Resource: "app/crm/Contact", Subject: "1", Fields: next}
	_, s2 := ui.RenderFirstPaint(detail, d2, nil)

	ops := ui.Diff(detail, s1, s2)
	if len(ops) != 1 {
		t.Fatalf("one field change ⇒ %d ops, want 1: %+v", len(ops), ops)
	}
	if ops[0].Kind != ui.OpSetText || ops[0].Payload != "20" {
		t.Fatalf("wrong op: %+v", ops[0])
	}
	changed := ops[0].SlotID

	// Digest: incremental (subtract old term, add new) == full recompute.
	dig := ui.FullDigest(snapshotMap(s1))
	dig = dig.Set(changed, s1[changed].Snapshot, s2[changed].Snapshot)
	if want := ui.FullDigest(snapshotMap(s2)); dig != want {
		t.Fatalf("incremental digest %d != full %d", dig, want)
	}
}

// --- masking (ADR-11 §8): no grant ⇒ token; grant ⇒ frame plaintext, snapshot
//     masked, audit row; expiry ⇒ re-mask ----------------------------------------

func TestD2MaskingLifecycle(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	v, err := admit(ctx, w.conn, dealSrc(`title: "text", email: "pii:email"`), "app/rmask", engineer("dev"), nil)
	if err != nil || v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit: %v / %q %+v", err, v.Outcome, v.Diagnostics)
	}
	tb := tableSlug("app/rmask/Deal")
	var id int64
	if _, err := w.conn.QueryRow(ctx,
		`INSERT INTO `+quoteIdent(tb)+` (title) VALUES ('acme') RETURNING id`, nil, &id); err != nil {
		t.Fatal(err)
	}
	subj := fmt.Sprintf("%d", id)
	const secret = "founder@acme.example"
	if err := VaultPut(ctx, w.conn, tb, subj, "email", secret); err != nil {
		t.Fatal(err)
	}

	detail, _, _ := loadTemplateBundle(t, w, "app/rmask/Deal")
	// Runtime mask key resource is the physical table (the vault's resource key).
	data := ui.RenderData{Resource: tb, Subject: subj, Fields: map[string]string{"title": "acme"}}
	principal := "human:dpo"

	// (1) No grant ⇒ mask token in snapshot AND html; plaintext absent everywhere.
	mcNone, err := BuildMaskCtx(ctx, w.conn, principal)
	if err != nil {
		t.Fatal(err)
	}
	html, state := ui.RenderFirstPaint(detail, data, mcNone)
	if strings.Contains(html, secret) {
		t.Fatalf("plaintext in html with no grant:\n%s", html)
	}
	emailSlot := maskedSlotID(t, detail, "email")
	if !strings.HasPrefix(state[emailSlot].Snapshot, ui.MaskGlyph) {
		t.Fatalf("email slot not masked: %+v", state[emailSlot])
	}
	// Grep the ENTIRE snapshot for the seeded plaintext (the §8 kill-test).
	for id, m := range state {
		if strings.Contains(m.Snapshot, secret) {
			t.Fatalf("plaintext leaked into snapshot slot %s", id)
		}
	}

	// (2) Mint a grant ⇒ frame carries plaintext, snapshot STILL masked, audit row.
	if err := MintRevealGrant(ctx, w.conn, principal, tb, subj, "email", time.Time{}, "operator:dpo"); err != nil {
		t.Fatal(err)
	}
	mcLive, err := BuildMaskCtx(ctx, w.conn, principal)
	if err != nil {
		t.Fatal(err)
	}
	html2, state2 := ui.RenderFirstPaint(detail, data, mcLive)
	if !strings.Contains(html2, secret) {
		t.Fatalf("granted plaintext missing from frame html:\n%s", html2)
	}
	if strings.Contains(state2[emailSlot].Snapshot, secret) {
		t.Fatal("INVARIANT VIOLATED: plaintext entered the slot snapshot under a grant")
	}
	if !strings.HasPrefix(state2[emailSlot].Snapshot, ui.MaskGlyph) {
		t.Fatalf("revealed snapshot must still be masked (token|scope): %q", state2[emailSlot].Snapshot)
	}
	if got := w.count("SELECT count(*) FROM reveal_audit WHERE resource=$1 AND subject_id=$2 AND field='email' AND principal=$3", tb, subj, principal); got != 1 {
		t.Fatalf("reveal_audit rows = %d, want 1", got)
	}
	// The snapshot digest is UNCHANGED across a reveal that only altered Display —
	// except the grant scope suffix, which deliberately shifts it so a later expiry
	// diffs. Assert the re-mask relationship instead:
	if ui.Diff(detail, state, state2) == nil {
		t.Fatal("grant flip should produce a diff (snapshot suffix carries the grant)")
	}

	// (3) Expire the grant ⇒ next render re-masks; plaintext gone; diff observed.
	if err := MintRevealGrant(ctx, w.conn, principal, tb, subj, "email",
		time.Now().Add(-time.Hour), "operator:dpo"); err != nil {
		t.Fatal(err)
	}
	mcExp, err := BuildMaskCtx(ctx, w.conn, principal)
	if err != nil {
		t.Fatal(err)
	}
	html3, state3 := ui.RenderFirstPaint(detail, data, mcExp)
	if strings.Contains(html3, secret) {
		t.Fatalf("expired grant still reveals plaintext:\n%s", html3)
	}
	if state3[emailSlot].Display == secret {
		t.Fatal("post-expiry display still plaintext")
	}
	if ui.Diff(detail, state2, state3) == nil {
		t.Fatal("expiry must yield a re-mask diff")
	}
}

// maskedSlotID returns the detail slot id bound to the named pii field.
func maskedSlotID(t *testing.T, tpl *ui.Template, field string) string {
	t.Helper()
	for _, s := range tpl.Slots {
		if s.Field == field {
			return s.ID
		}
	}
	t.Fatalf("no slot for field %q", field)
	return ""
}
