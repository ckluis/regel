package mcp

import (
	"context"
	"testing"
)

// restart_flip_test.go is the DETERMINISTIC (no-LLM) red-path suite for the
// ADR-12 §7 mechanized restart-authority flip (BUILD-E). The real-LLM eval that
// WRITES the m5_gate row lives in gate/m5eval; here we prove the KERNEL door that
// READS it flips exactly on the ADR-12 §7 floors — the flip is refused while the
// gate reads red, under-sized, or partial, and enabled only when green + sized.
// This is the "flip is REFUSED while the gate reads red" guarantee, decoupled
// from the LLM so it runs in ordinary CI.

// setGateRow writes/overwrites the current-epoch `restart` m5_gate row.
func (w *mworld) setGateRow(measured, floor float64, corpus, floorSize int, green, partial bool) {
	w.t.Helper()
	ctx := context.Background()
	var epoch int
	if _, err := w.conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &epoch); err != nil {
		w.t.Fatalf("epoch: %v", err)
	}
	if _, err := w.conn.Exec(ctx, `
INSERT INTO m5_gate (epoch, gate, corpus_size, floor_size, measured, floor, green, partial)
VALUES ($1,'restart',$2,$3,$4,$5,$6,$7)
ON CONFLICT (epoch, gate) DO UPDATE SET
  corpus_size=EXCLUDED.corpus_size, floor_size=EXCLUDED.floor_size,
  measured=EXCLUDED.measured, floor=EXCLUDED.floor,
  green=EXCLUDED.green, partial=EXCLUDED.partial, computed_at=now()`,
		epoch, corpus, floorSize, measured, floor, green, partial); err != nil {
		w.t.Fatalf("set gate row: %v", err)
	}
}

func (w *mworld) clearGateRow() {
	if _, err := w.conn.Exec(context.Background(), `DELETE FROM m5_gate WHERE gate='restart'`); err != nil {
		w.t.Fatalf("clear gate: %v", err)
	}
}

// restartRefused drives an agent condition.restart and asserts RESTART_DISABLED.
func (w *mworld) restartRefused(condID, frameHash string) map[string]any {
	w.t.Helper()
	return w.tool(agentKey, "condition.restart", map[string]any{
		"condition_id": condID, "restart_name": "retry", "expectedHash": frameHash})
}

func TestAgentRestartFlipGate(t *testing.T) {
	w := setupMCP(t)
	condOpen, _, frameHash := w.seedCondition("open")

	// (1) No gate row at all ⇒ authority DISABLED (the DEFAULT: unrun ⇒ off).
	w.clearGateRow()
	if r := w.restartRefused(condOpen, frameHash); r["code"] != "RESTART_DISABLED" {
		t.Fatalf("absent gate row must disable: %+v", r)
	}

	// (2) A RED row (measured below floor) ⇒ DISABLED. This is the core red-path:
	// the flip is refused while the gate reads red, even though the row exists.
	w.setGateRow(0.80, 0.95, 40, 30, false, false)
	if r := w.restartRefused(condOpen, frameHash); r["code"] != "RESTART_DISABLED" {
		t.Fatalf("red gate row must disable: %+v", r)
	}

	// (3) Accuracy at floor but corpus UNDER the ADR-12 §7 size floor (M<30) ⇒
	// DISABLED. A green-looking accuracy cannot flip on an under-sized corpus.
	w.setGateRow(1.0, 0.95, 20, 30, true, false)
	if r := w.restartRefused(condOpen, frameHash); r["code"] != "RESTART_DISABLED" {
		t.Fatalf("under-sized corpus must disable: %+v", r)
	}

	// (4) A PARTIAL row (LLM died mid-run) ⇒ DISABLED even if the partial numbers
	// look green — an open gate is never silently flipped.
	w.setGateRow(1.0, 0.95, 40, 30, true, true)
	if r := w.restartRefused(condOpen, frameHash); r["code"] != "RESTART_DISABLED" {
		t.Fatalf("partial gate row must disable: %+v", r)
	}

	// (5) GREEN + sized + not partial ⇒ authority ENABLED. Now the agent's restart
	// runs the real fenced pick and returns a real status (here CONDITION_MOVED is
	// impossible since the hash is correct; the point is it is NOT RESTART_DISABLED).
	w.setGateRow(0.97, 0.95, 32, 30, true, false)
	r := w.restartRefused(condOpen, frameHash)
	if r["code"] == "RESTART_DISABLED" {
		t.Fatalf("green + sized gate must ENABLE the agent authority, got refused: %+v", r)
	}

	// (6) Flip REVERTS when the metric goes red again (re-computed each epoch):
	// writing a red row over the green one disables the authority immediately.
	condOpen2, _, frameHash2 := w.seedCondition("open")
	w.setGateRow(0.50, 0.95, 32, 30, false, false)
	if r := w.restartRefused(condOpen2, frameHash2); r["code"] != "RESTART_DISABLED" {
		t.Fatalf("reverting to red must re-disable: %+v", r)
	}

	// Operators keep the authority unconditionally regardless of the gate row.
	w.clearGateRow()
	op := w.tool(operatorKey, "condition.restart", map[string]any{
		"condition_id": condOpen2, "restart_name": "retry", "expectedHash": frameHash2})
	if op["code"] == "RESTART_DISABLED" {
		t.Fatalf("operator authority must be unaffected by the eval gate: %+v", op)
	}
}
