package kernel

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
)

// e2_operatorplane_test.go is the BUILD-E (D2) red-path for the minimal
// operatorPlane (ADR-12 §7): the operator desk server-renders REAL rows from the
// live substrate tables — the open durable_condition inbox (+ restart buttons) and
// the gate_refusal ledger.

// TestOperatorPlaneListsConditionsAndRefusal (red-path d): a genuinely-produced
// refusal AND a genuinely-signaled durable condition both appear on the plane.
func TestOperatorPlaneListsConditionsAndRefusal(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()

	// (1) A REAL refusal: a rejected admission (pii bound at a non-leaf component)
	// writes a durable gate_refusal ledger row.
	badSrc := `import { heading } from "std/ui";
import type { Vault } from "std/pii";
export function bad(owner: Vault<string>) { return heading({ title: owner }); }`
	v := se.admit(t, badSrc, "app/op", nil)
	if v.Outcome != admission.OutcomeRejected {
		t.Fatalf("want rejection (writes a refusal), got %q (%+v)", v.Outcome, v.Diagnostics)
	}
	var refPrincipal string
	se.withConn(t, func(c *pgConn) {
		var ok bool
		ok, _ = c.QueryRow(ctx, `SELECT principal FROM gate_refusal WHERE refusal_id=$1`,
			[]any{v.RefusalID}, &refPrincipal)
		if !ok {
			t.Fatalf("no gate_refusal row for %s", v.RefusalID)
		}
	})

	// (2) A REAL durable condition: taak.signal parks a workflow on an open
	// durable_condition with two restart rows (approve, abort).
	sigSrc := `import { signal } from "std/taak";
export function approve(): string {
  const r = signal("app.approval",
    [{ name: "approve", label: "Approve", capability: "operator" }, { name: "abort", label: "Abort" }]);
  return "resolved:" + r.restart;
}`
	sv := se.admit(t, sigSrc, "app/sig", nil)
	if sv.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit signal: %q (%+v)", sv.Outcome, sv.Diagnostics)
	}
	id := se.start(t, sv.Hashes["app/sig/approve"], nil, map[string]any{"subject": "op", "operator": true})
	r := se.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()
	se.waitStatus(t, id, "condition", 5*time.Second)

	// (3) GET the operatorPlane and assert both panels render the real rows.
	req, _ := http.NewRequest("GET", se.ts.URL+"/ui/operatorPlane", nil)
	req.Header.Set("X-Regel-Actor", "human:op")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("operatorPlane mount: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("operatorPlane status = %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	// Both panels render.
	for _, want := range []string{"condition inbox", "refusal ledger"} {
		if !contains(body, want) {
			t.Fatalf("operatorPlane missing the %q panel:\n%s", want, body)
		}
	}
	// The refusal row: outcome + principal.
	if !contains(body, "rejected") {
		t.Fatalf("refusal ledger did not list the rejected outcome:\n%s", body)
	}
	if refPrincipal != "" && !contains(body, refPrincipal) {
		t.Fatalf("refusal ledger did not list the principal %q:\n%s", refPrincipal, body)
	}
	// The condition inbox row: the class + a restart button target.
	if !contains(body, "app.approval") {
		t.Fatalf("condition inbox did not list the open condition:\n%s", body)
	}
	if !contains(body, "approve") {
		t.Fatalf("condition inbox did not render the restart button:\n%s", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
