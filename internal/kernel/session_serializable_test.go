package kernel

import (
	"context"
	"testing"

	"regel.dev/regel/internal/admission"
)

// session_serializable_test.go is the L7 (Kleppmann R1 P2.7) anchor: a SERVE read
// PHASE issues several reads (loadViewMeta's resource + template artifact, the
// component template, the data rows). At READ COMMITTED, a name_pointer /
// derived-artifact flip a concurrent admission commits BETWEEN two of those reads
// splits dispatch — the render binds a template from one epoch and rows from
// another. serveReadSnapshot raises the serve read phase to REPEATABLE READ, READ
// ONLY so every read sees ONE instant; mountSession/resyncSession render inside it.
// Writes/admission stay SERIALIZABLE (untouched).

func l7LeadV1() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Lead = resource({
  fields: { org: "text", name: "text" },
  policy: orgScoped,
});
`
}

func l7LeadV2() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Lead = resource({
  fields: { org: "text", name: "text", region: "text" },
  policy: orgScoped,
});
`
}

func l7FieldCount(t *testing.T, conn *pgConn, resource string) int {
	t.Helper()
	vm, err := loadViewMeta(context.Background(), conn, resource, nil)
	if err != nil {
		t.Fatalf("loadViewMeta %s: %v", resource, err)
	}
	return len(vm.Fields)
}

// RED baseline: without the snapshot, two reads on one serve connection straddling a
// concurrent field-add admission observe DIFFERENT schemas — the split the L7 fix
// closes. (This is the hazard, asserted to be real; the GREEN twin closes it.)
func TestL7ReadCommittedSplitsDispatch(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()

	v1 := se.admit(t, l7LeadV1(), "app/l7a", nil)
	if v1.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Lead v1: %q %+v", v1.Outcome, v1.Diagnostics)
	}

	se.withConn(t, func(c *pgConn) {
		// Read 1 (autocommit, READ COMMITTED): 2 fields.
		f1 := l7FieldCount(t, c, "app/l7a/Lead")
		// A concurrent admission commits the field-add flip on its own connection.
		base := map[string]string{"app/l7a/Lead": v1.Hashes["app/l7a/Lead"]}
		if v := se.admit(t, l7LeadV2(), "app/l7a", base); v.Outcome != admission.OutcomeAdmitted {
			t.Fatalf("admit Lead v2 (field-add): %q %+v", v.Outcome, v.Diagnostics)
		}
		// Read 2 (autocommit, READ COMMITTED): now 3 fields — the two reads DISAGREE.
		f2 := l7FieldCount(t, c, "app/l7a/Lead")
		if f1 == f2 {
			t.Fatalf("READ COMMITTED baseline expected to split across the flip (f1=%d f2=%d); "+
				"if equal the hazard is not reproduced and the GREEN twin proves nothing", f1, f2)
		}
	})
	_ = ctx
}

// GREEN: the same two reads inside serveReadSnapshot (REPEATABLE READ, READ ONLY)
// observe the SAME schema across the concurrent flip — dispatch is pinned to one
// instant. A fresh read AFTER the snapshot confirms the flip really committed (so the
// pin is doing real work, not hiding a no-op).
func TestL7RepeatableReadPinsSnapshot(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()

	v1 := se.admit(t, l7LeadV1(), "app/l7b", nil)
	if v1.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Lead v1: %q %+v", v1.Outcome, v1.Diagnostics)
	}

	se.withConn(t, func(c *pgConn) {
		var f1, f2 int
		if err := serveReadSnapshot(ctx, c, func() error {
			f1 = l7FieldCount(t, c, "app/l7b/Lead") // 2, pins the snapshot
			base := map[string]string{"app/l7b/Lead": v1.Hashes["app/l7b/Lead"]}
			if v := se.admit(t, l7LeadV2(), "app/l7b", base); v.Outcome != admission.OutcomeAdmitted {
				t.Fatalf("admit Lead v2 (field-add): %q %+v", v.Outcome, v.Diagnostics)
			}
			f2 = l7FieldCount(t, c, "app/l7b/Lead") // still 2 — the flip is invisible in-snapshot
			return nil
		}); err != nil {
			t.Fatalf("serveReadSnapshot: %v", err)
		}
		if f1 != f2 {
			t.Fatalf("REPEATABLE READ snapshot must pin the view across the flip: f1=%d f2=%d", f1, f2)
		}
		// The connection is back to autocommit; the committed flip is now visible.
		if f3 := l7FieldCount(t, c, "app/l7b/Lead"); f3 == f2 {
			t.Fatalf("the field-add did not actually commit (f2=%d f3=%d) — the test would be vacuous", f2, f3)
		}
	})
}
