package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/mcp"
	"regel.dev/regel/internal/pgwire"
)

// r7_restart_flip_depth_test.go DISCHARGES STAGE-E residue R7 (spec/gates/STAGE-E.md
// §9 #7). Stage-E flipped the agent `condition.restart` authority ON through the
// mechanized ADR-12 §7 gate, but the DEEP end-to-end evidence used SYNTHETIC frames
// (the m5eval harness hand-built a `frames-open` blob, so the post-flip agent call
// hit `INTERNAL` on a continuation the CEK machine could not decode). R7 asked for
// the same flip proven on a REAL parked workflow.
//
// This test drives the whole real path in one place:
//
//	admit real code (taak.signal → a durable condition + restarts, then a channel.send
//	effect, then a result) → run it on the REAL CEK machine via the REAL reactor → it
//	parks as a REAL durable_condition with REAL CFR frames (encode(sha256(frames)) is
//	the expectedHash the operator inbox/condition.list embeds per button) → an AGENT
//	principal calls the REAL MCP `condition.restart` door.
//
// RED (witnessed first, ADR-12 §7 policy): with the authority flip ABSENT (no green
// `restart` m5_gate row), the agent restart is REFUSED `RESTART_DISABLED` and leaves
// a CLEAN trace — the condition stays open, the workflow stays parked, ZERO effects
// fire. Under R7_RED_WITNESS=1 the machinery (the green gate) is withheld for the
// whole test, so the deep path cannot complete and the test is RED — the residue's
// "fails without the machinery" witness.
//
// GREEN: a green restart-decision gate row flips the authority ON; the SAME agent
// restart now runs the ADR-05 §6 fenced pick + ClaimAndResume over the REAL frames,
// the workflow runs to its correct final result, and its effect is delivered
// exactly-once. A second agent restart is idempotently rejected (ALREADY_RESOLVED)
// with no second effect — no double resume.
func TestR7AgentRestartRealParkedWorkflow(t *testing.T) {
	e := newReactorEnv(t)
	ctx := context.Background()

	// (1) A REAL admitted workflow: raise a durable condition with two restarts, then
	// (only reachable AFTER the restart resumes) record a channel.send effect and
	// return a result that names the chosen restart. No side-door Go, no hand-built
	// frames — this is admitted through the real gate and run by the real machine.
	src := `import { signal } from "std/taak";
import { send } from "std/wf";
export function approve(): string {
  const r = signal("app.approval",
    [{ name: "approve", label: "Approve" }, { name: "abort", label: "Abort" }]);
  send("approvals", r.restart);
  return "resolved:" + r.restart;
}`
	v := e.admit(t, src, "app/r7", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/r7/approve"]

	// (2) Start + run on the REAL reactor until it parks on the durable condition,
	// then stop the reactor so nothing races the MCP restart below.
	id := e.start(t, hash, nil, map[string]any{"subject": "op", "operator": true})
	r := e.srv.StartReactor(ctx, ReactorConfig{PollInterval: 15 * time.Millisecond})
	e.waitStatus(t, id, "condition", 5*time.Second)
	r.Stop()

	if n := e.intScalar(t, `SELECT count(*) FROM durable_condition WHERE continuation_id=$1 AND class='app.approval' AND status='open'`, id); n != 1 {
		t.Fatalf("want exactly 1 open durable condition, got %d", n)
	}
	// The REAL frame hash the fence will re-check — read from the parked continuation.
	var condID, frameHash string
	e.withConn(t, func(c *pgwire.Conn) {
		if _, err := c.QueryRow(ctx, `
SELECT dc.id::text, encode(sha256(cont.frames),'hex')
FROM durable_condition dc JOIN continuation cont ON cont.id = dc.continuation_id
WHERE dc.continuation_id = $1 AND dc.status = 'open'`, []any{id}, &condID, &frameHash); err != nil {
			t.Fatalf("load condition + frame hash: %v", err)
		}
	})
	if frameHash == "" {
		t.Fatalf("empty frame hash — the parked continuation has no frames (synthetic-frames regression)")
	}

	// (3) The REAL MCP door over the SAME PG + an AGENT principal.
	srv, err := mcp.New(ctx, e.pool)
	if err != nil {
		t.Fatalf("mcp.New: %v", err)
	}
	const agentKey = "k-r7-agent"
	e.exec(t, `INSERT INTO agent_key (key_hash, actor_kind, actor_id, scope_kind, scope_id)
VALUES ($1,'agent','r7a',2,'r7org')`, mcp.HashKey(agentKey))

	// --- RED PATH (witnessed first): flip ABSENT ⇒ agent restart REFUSED ------------
	// No `restart` m5_gate row exists ⇒ the mechanized ADR-12 §7 flip reads absent ⇒
	// the agent-facing condition.restart is disabled. This is the refusal Stage-E only
	// witnessed on synthetic frames — here it is the SAME REAL parked workflow.
	red := r7CallRestart(t, srv, agentKey, condID, "approve", frameHash)
	if red["code"] != "RESTART_DISABLED" {
		t.Fatalf("RED: agent restart with the authority flip absent must be RESTART_DISABLED, got %+v", red)
	}
	t.Logf("RED: agent condition.restart on the REAL parked workflow (cond=%s, frameHash=%s) refused: status=%v code=%v detail=%v",
		condID, frameHash[:12]+"…", red["status"], red["code"], red["detail"])
	// Clean trace: nothing moved. The condition is still open, the workflow is still
	// parked, and ZERO effects fired (the send is past the un-taken resume point).
	if n := e.intScalar(t, `SELECT count(*) FROM durable_condition WHERE id=$1 AND status='open'`, condID); n != 1 {
		t.Fatalf("RED: condition must stay open after a refused restart, open=%d", n)
	}
	if s := e.status(t, id); s != "condition" {
		t.Fatalf("RED: continuation must stay parked, status=%q", s)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, id); n != 0 {
		t.Fatalf("RED: a refused restart must fire zero effects, outbox=%d", n)
	}

	// R7_RED_WITNESS=1 withholds the flip machinery (the green gate) for the WHOLE
	// test, so the deep end-to-end path cannot complete — the residue's "the test
	// fails without the machinery" red witness, captured under evidence-f/r7/.
	if os.Getenv("R7_RED_WITNESS") == "1" {
		t.Fatalf("R7_RED_WITNESS: green gate withheld — the agent-driven restart of the REAL " +
			"parked workflow stays refused (RESTART_DISABLED); the deep path cannot run to done " +
			"without the flipped authority. This is the red witness R7 requires.")
	}

	// --- GREEN PATH: flip the authority ON via a green restart-decision gate row -----
	// Green + sized + not-partial for the current epoch ⇒ agentRestartAuthorityEnabled
	// returns true (mirrors the Stage-E M5 §7 measured 0.968 / M=31).
	r7SetGreenGate(t, e)

	got := r7CallRestart(t, srv, agentKey, condID, "approve", frameHash)
	if got["status"] != "done" {
		t.Fatalf("GREEN: agent-driven restart of the real parked workflow must run to done, got %+v", got)
	}
	// The workflow completed on REAL frames with its correct final result.
	e.waitStatus(t, id, "done", 5*time.Second)
	if res := e.result(t, id); res.S != "resolved:approve" {
		t.Fatalf("GREEN: final result = %+v, want resolved:approve", res)
	}
	// Exactly-once effects: exactly one channel.send outbox row, zero duplicate keys.
	if n := e.intScalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, id); n != 1 {
		t.Fatalf("GREEN: exactly-once broke — outbox rows=%d, want 1", n)
	}
	dupes := e.intScalar(t, `
SELECT count(*) FROM (
  SELECT continuation_id, step_seq, ordinal FROM outbox
  GROUP BY continuation_id, step_seq, ordinal HAVING count(*) > 1) d`)
	if dupes != 0 {
		t.Fatalf("GREEN: duplicate outbox keys=%d, want 0 (double resume)", dupes)
	}
	// The resolution is attributed to the AGENT principal and the condition is resolved.
	var status, resolvedBy string
	e.withConn(t, func(c *pgwire.Conn) {
		if _, err := c.QueryRow(ctx, `SELECT status, COALESCE(resolved_by,'') FROM durable_condition WHERE id=$1`,
			[]any{condID}, &status, &resolvedBy); err != nil {
			t.Fatalf("load resolution: %v", err)
		}
	})
	if status != "resolved" {
		t.Fatalf("GREEN: condition status=%q, want resolved", status)
	}
	if !strings.Contains(resolvedBy, "r7a") {
		t.Fatalf("GREEN: resolved_by=%q, want the agent principal (agent:r7a)", resolvedBy)
	}

	// A SECOND agent restart is idempotently rejected — no double resume, effect stays
	// once (ADR-12 §7 / simplest-thing idempotence, now on the real resolved condition).
	again := r7CallRestart(t, srv, agentKey, condID, "approve", frameHash)
	if again["code"] != "ALREADY_RESOLVED" {
		t.Fatalf("GREEN: second restart must be idempotently rejected, got %+v", again)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, id); n != 1 {
		t.Fatalf("GREEN: idempotent reject must not add an effect, outbox=%d", n)
	}
}

// r7SetGreenGate writes a GREEN current-epoch `restart` m5_gate row — the mechanized
// ADR-12 §7 flip reads this and only this to enable the agent authority. Numbers
// mirror the Stage-E M5 §7 run (accuracy 0.968 ≥ 0.95, M=31 ≥ 30, not partial).
func r7SetGreenGate(t *testing.T, e *reactorEnv) {
	t.Helper()
	e.exec(t, `
INSERT INTO m5_gate (epoch, gate, corpus_size, floor_size, measured, floor, green, partial)
VALUES ((SELECT n FROM epoch_current WHERE one=true), 'restart', 31, 30, 0.968, 0.95, true, false)
ON CONFLICT (epoch, gate) DO UPDATE SET
  corpus_size=EXCLUDED.corpus_size, floor_size=EXCLUDED.floor_size,
  measured=EXCLUDED.measured, floor=EXCLUDED.floor,
  green=EXCLUDED.green, partial=EXCLUDED.partial, computed_at=now()`)
}

// r7CallRestart drives one condition.restart through the REAL MCP door (ServeStdio,
// one JSON-RPC line) as the given API key and returns the tool's structured payload
// (the object embedded in content[0].text).
func r7CallRestart(t *testing.T, srv *mcp.Server, key, condID, restartName, expectedHash string) map[string]any {
	t.Helper()
	call := map[string]any{
		"name": "condition.restart",
		"arguments": map[string]any{
			"condition_id": condID, "restart_name": restartName, "expectedHash": expectedHash,
		},
	}
	params, _ := json.Marshal(call)
	line := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + string(params) + "}\n"
	var out bytes.Buffer
	if err := srv.ServeStdio(context.Background(), &mcp.Session{APIKey: key}, strings.NewReader(line), &out); err != nil {
		t.Fatalf("ServeStdio: %v (raw=%s)", err, out.String())
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("parse rpc envelope: %v (raw=%s)", err, out.String())
	}
	if resp.Error != nil {
		t.Fatalf("condition.restart rpc error: %s", string(resp.Error))
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("condition.restart returned no content: %s", out.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &m); err != nil {
		t.Fatalf("tool payload not an object: %s (%v)", resp.Result.Content[0].Text, err)
	}
	return m
}
