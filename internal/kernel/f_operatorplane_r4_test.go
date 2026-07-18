package kernel

// f_operatorplane_r4_test.go is the STAGE-F R4 red/green battery: the operatorPlane
// promoted to v1.1 — SSE live updates, an approval-delta panel, and a WRITE action
// (the restart button) that walks the EXISTING restart door. It drives the whole
// thing HTTP/SSE-level against a live kernel + real Postgres (no browser, no mocks):
//
//   * a real taak.signal workflow parks on an open durable_condition;
//   * GET /ui/operatorPlane creates a REAL reactive session (3 panels) and returns a
//     session id (v1 returned none — it was read-only);
//   * RED, at the DOOR (not UI logic): a stale-hash restart is refused CONDITION_MOVED
//     (optimistic concurrency) and an unknown restart is refused NOT_FOUND, and NO
//     operator frame is pushed (a refused write changes no state);
//   * GREEN: the operator restart "approve" resolves the condition through the door;
//   * the operatorPlane SSE stream receives a LIVE splice frame — the resolved
//     condition leaves the inbox and its pending→approve transition lands in the
//     approval-delta panel — riding the SAME ADR-11 §6 invalidation machinery every
//     resource view uses.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/ui"
)

const r4SignalSrc = `import { signal } from "std/taak";
export function approve(): string {
  const r = signal("app.approval",
    [{ name: "approve", label: "Approve", capability: "operator" }, { name: "abort", label: "Abort" }]);
  return "resolved:" + r.restart;
}`

// r4RestartDoor POSTs the EXISTING restart door and returns (status, body).
func r4RestartDoor(t *testing.T, base, contID, restart, expectedHash string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"restart": restart, "expected_hash": expectedHash})
	resp, err := http.Post(base+"/continuation/"+contID+"/restart", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("restart door POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestR4OperatorPlaneReactiveWritesAndDelta(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()

	// (1) A real workflow parked on an open durable_condition (two restarts;
	// "approve" requires the operator capability the door grants).
	sv := se.admit(t, r4SignalSrc, "app/r4sig", nil)
	if sv.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit signal: %q (%+v)", sv.Outcome, sv.Diagnostics)
	}
	contID := se.start(t, sv.Hashes["app/r4sig/approve"], nil, map[string]any{"subject": "op", "operator": true})
	r := se.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()
	se.waitStatus(t, contID, "condition", 5*time.Second)

	var condID string
	se.withConn(t, func(c *pgConn) {
		ok, err := c.QueryRow(ctx,
			`SELECT id::text FROM durable_condition WHERE continuation_id=$1 AND status='open'`,
			[]any{contID}, &condID)
		if err != nil || !ok {
			t.Fatalf("no open condition for %s: ok=%v err=%v", contID, ok, err)
		}
	})

	// (2) Mount the operatorPlane — now a REAL reactive session (X-Regel-Session set).
	req, _ := http.NewRequest("GET", se.ts.URL+"/ui/operatorPlane", nil)
	req.Header.Set("X-Regel-Actor", "human:op")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("operatorPlane mount: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	sid := resp.Header.Get("X-Regel-Session")
	if sid == "" {
		t.Fatalf("operatorPlane v1.1 must be a reactive session (no X-Regel-Session header):\n%s", body)
	}
	for _, want := range []string{"condition inbox", "refusal ledger", "approval delta", "app.approval", "approve", contID} {
		if !contains(body, want) {
			t.Fatalf("operatorPlane first paint missing %q:\n%s", want, body)
		}
	}

	// (3) Open the operator SSE stream (the reactive down-channel).
	h := &harness{t: t, base: se.ts.URL, sid: sid, slots: map[string]string{}, pending: map[string]string{}}
	sse := h.openSSE(0)
	defer sse.close()

	// (4) RED — door-level refusals (the DOOR refuses, not UI logic), and no frame.
	if st, b := r4RestartDoor(t, se.ts.URL, contID, "approve", "0000000000000000000000000000000000000000000000000000000000000000"); st != 409 || !contains(b, "moved") {
		t.Fatalf("stale-hash restart: want 409 CONDITION_MOVED, got %d %s", st, b)
	}
	if st, _ := r4RestartDoor(t, se.ts.URL, contID, "nope", ""); st != 404 {
		t.Fatalf("unknown restart: want 404 NOT_FOUND, got %d", st)
	}
	// The condition is still open (a refused write changed nothing) — and no frame.
	sse.assertNoFrame(t, 500*time.Millisecond)
	if s := condStatus(t, se, condID); s != "open" {
		t.Fatalf("condition status after refused writes = %q, want open", s)
	}

	// (5) GREEN — the operator restart "approve" resolves the condition through the
	// SAME door (empty expected_hash ⇒ no stale fence; operator cap granted by door).
	if st, b := r4RestartDoor(t, se.ts.URL, contID, "approve", ""); st != 200 {
		t.Fatalf("authorized restart: want 200, got %d %s", st, b)
	}
	if s := condStatus(t, se, condID); s != "resolved" {
		t.Fatalf("condition status after approve = %q, want resolved", s)
	}

	// (6) The live SSE frame: the resolved condition leaves the inbox (splice remove
	// on opcond.0) and its approve transition is added to the approval-delta panel.
	f := sse.nextFrame(t, 3*time.Second)
	sawInboxRemove, sawDeltaAdd := false, false
	for _, op := range f.Ops {
		if op.Kind != ui.OpSpliceList {
			continue
		}
		for _, sp := range op.Splices {
			if op.SlotID == "opcond.0" && sp.Kind == ui.SpliceRemove && sp.Key == condID {
				sawInboxRemove = true
			}
			if op.SlotID == "opdelta.0" && sp.Kind == ui.SpliceAdd && sp.Key == condID {
				sawDeltaAdd = true
			}
		}
	}
	if !sawInboxRemove {
		t.Fatalf("live SSE frame did not splice-remove the resolved condition from the inbox: %+v", f.Ops)
	}
	if !sawDeltaAdd {
		t.Fatalf("live SSE frame did not splice-add the approve transition to the approval-delta panel: %+v", f.Ops)
	}

	// (7) A fresh mount now shows the approve transition in the approval-delta panel.
	req2, _ := http.NewRequest("GET", se.ts.URL+"/ui/operatorPlane", nil)
	req2.Header.Set("X-Regel-Actor", "human:op")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("operatorPlane re-mount: %v", err)
	}
	raw2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !contains(string(raw2), "approval delta") || !contains(string(raw2), "approve") {
		t.Fatalf("approval-delta panel does not render the resolved approve transition:\n%s", raw2)
	}
}

func condStatus(t *testing.T, se *sessionEnv, condID string) string {
	t.Helper()
	var s string
	se.withConn(t, func(c *pgConn) {
		ok, err := c.QueryRow(context.Background(),
			`SELECT status FROM durable_condition WHERE id=$1`, []any{condID}, &s)
		if err != nil || !ok {
			t.Fatalf("cond status: ok=%v err=%v", ok, err)
		}
	})
	return s
}
