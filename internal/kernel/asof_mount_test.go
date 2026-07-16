package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
)

// asof_mount_test.go is the BUILD-E scenario (d) red-path: a `?as_of=` session
// mount resolves the derived TEMPLATE artifact as-of that instant, so the first
// paint renders the schema/behavior the world had then — a def change (field-add)
// admitted AFTER the boundary is invisible to an as-of mount, while a live mount
// shows it. This is the mechanism scenario-d-asof-rollback.sh observes end to end;
// the Go anchor guards it in `go test ./...`.
//
// Found by USE: scenario (d) called for observing an as-of rollback THROUGH THE UI,
// but the session mount resolved only the head template (ORDER BY id DESC LIMIT 1)
// — there was no way to render a historical schema. loadViewMeta + mountSession
// now thread an optional asOf; this red path pins that a post-boundary field-add
// does not bleed into the pre-boundary render.

func asofResV1() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Lead = resource({
  fields: { org: "text", name: "text", score: "number" },
  policy: orgScoped,
});
`
}

func asofResV2() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Lead = resource({
  fields: { org: "text", name: "text", score: "number", region: "text" },
  policy: orgScoped,
});
`
}

func TestAsOfMountRendersHistoricalSchema(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()

	// v1: no region field.
	v1 := se.admit(t, asofResV1(), "app/asof", nil)
	if v1.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Lead v1: %q %+v", v1.Outcome, v1.Diagnostics)
	}

	// Boundary: read the DB clock AFTER v1 commits and BEFORE the field-add.
	var boundary time.Time
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(ctx, `SELECT now()`, nil, &boundary); err != nil {
			t.Fatalf("read boundary: %v", err)
		}
	})
	time.Sleep(5 * time.Millisecond) // keep v2's created_at strictly after the boundary

	// Seed a Lead row so the form has an edit target (both schemas share it).
	var id int64
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(ctx,
			`INSERT INTO `+quoteIdent("res_"+tblSlug("app/asof/Lead"))+
				` (org, name, score) VALUES ('acme','Ada',1) RETURNING id`, nil, &id); err != nil {
			t.Fatalf("seed lead: %v", err)
		}
	})

	// v2: adds the region field (a tenant field-add, additive) under optimistic
	// concurrency against the v1 head hash.
	base := map[string]string{"app/asof/Lead": v1.Hashes["app/asof/Lead"]}
	if v := se.admit(t, asofResV2(), "app/asof", base); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Lead v2 (field-add): %q %+v", v.Outcome, v.Diagnostics)
	}

	mountHTML := func(asOf *time.Time) string {
		t.Helper()
		res, err := se.srv.mountSession(ctx, "app/asof/Lead/form/"+fmtID(id), "human:e", "acme", "", asOf)
		if err != nil {
			t.Fatalf("mount (asOf=%v): %v", asOf, err)
		}
		return res.HTML
	}

	// LIVE mount (asOf nil) — the head template renders the new region field.
	live := mountHTML(nil)
	if !strings.Contains(live, ">region</label>") {
		t.Fatalf("live mount must render the region field; HTML=%s", live)
	}

	// AS-OF mount at the pre-field-add boundary — the region field is ABSENT (the
	// world's schema then had no such field), yet the older fields still render.
	past := mountHTML(&boundary)
	if strings.Contains(past, ">region</label>") {
		t.Fatalf("as-of mount BEFORE the field-add must NOT render region (rollback leaked); HTML=%s", past)
	}
	if !strings.Contains(past, ">name</label>") {
		t.Fatalf("as-of mount must still render the v1 fields (name); HTML=%s", past)
	}
}

// TestAsOfMountBeforeAnyAdmissionFails: an as-of instant BEFORE the resource ever
// existed resolves no template and cleanly errors (no panic, no head fallback).
func TestAsOfMountBeforeAnyAdmissionFails(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()

	before := time.Now().Add(-time.Hour)
	if v := se.admit(t, asofResV1(), "app/asof", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Lead v1: %q %+v", v.Outcome, v.Diagnostics)
	}
	_, err := se.srv.mountSession(ctx, "app/asof/Lead/form", "human:e", "acme", "", &before)
	if err == nil {
		t.Fatalf("an as-of mount before the resource existed must error, not fall back to head")
	}
}
