package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/ui"
)

// e3_component_test.go is the BUILD-E (D3) kernel battery: a hand-authored component
// mounts over a resource row and renders + patches through the SAME session
// machinery as a derived detail (ADR-10 §7, ADR-11 §1/§5/§6).

func contactSrc() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Contact = resource({
  fields: { org: "text", name: "text", stage: "states:lead|won", email: "pii:email" },
  policy: orgScoped,
});
`
}

// accountCardSrc binds two non-pii fields (name@text, stage@badge) and — the
// masking case — a pii field (email) at the text masking leaf.
func accountCardSrc() string {
	return `import { card, stack, text, badge } from "std/ui";
export function AccountCard(props: { name: string; stage: string; email: string }) {
  return card({}, [
    stack({}, [ text({ value: props.name }), text({ value: props.email }) ]),
    badge({ value: props.stage })
  ]);
}
`
}

func (se *sessionEnv) contactTable() string { return "res_" + tblSlug("app/rx/Contact") }

func (se *sessionEnv) seedContact(t *testing.T, org, name, stage string) int64 {
	t.Helper()
	tbl := se.contactTable()
	var id int64
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(context.Background(),
			`INSERT INTO `+quoteIdent(tbl)+` (org, name, stage) VALUES ($1,$2,$3) RETURNING id`,
			[]any{org, name, stage}, &id); err != nil {
			t.Fatalf("seed contact: %v", err)
		}
	})
	return id
}

func compSlotForField(t *testing.T, srv *Server, compName, resource, field string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := srv.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.pool.Release(conn)
	vm, err := loadViewMeta(ctx, conn, resource, nil)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := loadComponentTemplate(ctx, conn, compName, vm)
	if err != nil {
		t.Fatalf("load component %s: %v", compName, err)
	}
	for _, s := range ct.Slots {
		if s.Field == field {
			return s.ID
		}
	}
	t.Fatalf("no component slot for field %q", field)
	return ""
}

// TestComponentMountRenderAndPatch (red-paths a + b): a hand-authored AccountCard
// mounts over a Contact row, renders its fields server-side, and a dependency
// mutation (a name edit) patches the live component session exactly like a derived
// detail — plus (six-leaf masking) the pii email leaf renders masked, never plain.
func TestComponentMountRenderAndPatch(t *testing.T) {
	se := newSessionEnv(t)
	se.admitDealless(t) // admit Contact + AccountCard
	id := se.seedContact(t, "acme", "AcmeCo", "lead")

	view := "app/rx/Contact/detail/" + fmtID(id) + "?component=app/rx/AccountCard"
	comp := se.mount(t, view, "human:a", "acme")

	nameSlot := compSlotForField(t, se.srv, "app/rx/AccountCard", "app/rx/Contact", "name")
	stageSlot := compSlotForField(t, se.srv, "app/rx/AccountCard", "app/rx/Contact", "stage")
	emailSlot := compSlotForField(t, se.srv, "app/rx/AccountCard", "app/rx/Contact", "email")

	// (a) server-side render of the bound fields.
	if got := comp.slots[nameSlot]; got != "AcmeCo" {
		t.Fatalf("component name slot = %q, want AcmeCo", got)
	}
	if got := comp.slots[stageSlot]; got != "lead" {
		t.Fatalf("component stage slot = %q, want lead", got)
	}
	// six-leaf masking: the pii email leaf renders the mask token, never plaintext.
	if got := comp.slots[emailSlot]; !strings.HasPrefix(got, ui.MaskGlyph) {
		t.Fatalf("pii email leaf must render masked, got %q", got)
	}

	// (b) a dependency mutation patches the live component session.
	cc := comp.openSSE(0)
	defer cc.close()
	time.Sleep(150 * time.Millisecond)

	nameForm := slotForField(t, se.srv, "app/rx/Contact", "form", "name")
	ed := se.mount(t, "app/rx/Contact/form/"+fmtID(id), "human:e", "acme")
	ed.postEvent("input", nameForm, "AcmeCorp")
	if r := ed.postEvent("submit", "", ""); r["applied"] != true {
		t.Fatalf("name-edit submit not applied: %+v", r)
	}

	f := cc.nextFrame(t, 4*time.Second)
	op, ok := opFor(f, nameSlot)
	if !ok || op.Payload != "AcmeCorp" {
		t.Fatalf("component session did not receive the name patch: %+v", f)
	}
}

// admitDealless admits the Contact resource and the AccountCard component.
func (se *sessionEnv) admitDealless(t *testing.T) {
	t.Helper()
	if v := se.admit(t, contactSrc(), "app/rx", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Contact: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	if v := se.admit(t, accountCardSrc(), "app/rx", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit AccountCard: %q (%+v)", v.Outcome, v.Diagnostics)
	}
}
