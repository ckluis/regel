package m5eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"regel.dev/regel/internal/pgwire"
)

// m5llm_test.go is the REAL-LLM orchestrator for the three OPEN M5 gates. It is
// gated on REGEL_M5_LLM=1 (+ the `claude` CLI present) so a CI-without-LLM run
// SKIPS cleanly and stays honest. It is RESUMABLE: every scored (task,attempt) is
// persisted as it completes, so a re-run fills only the gaps (infrastructure
// failures — LLM timeout/rate-limit — are left as gaps, never faked). It drives
// the REAL MCP agent plane (a `regel mcp` subprocess) with the REAL model in the
// loop, computes each gate ONLY from persisted rows, derives the §5 fuel capacity
// from the measured P95, and demonstrates the mechanized §7 flip end to end.
//
// Run it via scripts/m5-eval.sh (which prepares the DB + binary + agent key).

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func TestM5EvalRealLLM(t *testing.T) {
	if os.Getenv("REGEL_M5_LLM") != "1" {
		t.Skip("REGEL_M5_LLM!=1: real-LLM M5 eval is opt-in (CI stays green without an LLM)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH: cannot run the real-LLM eval")
	}
	dsn := os.Getenv("REGEL_PG_DSN")
	if dsn == "" {
		t.Fatal("REGEL_PG_DSN required (the prepared eval DB)")
	}
	bin := os.Getenv("REGEL_M5_BIN")
	if bin == "" {
		t.Fatal("REGEL_M5_BIN required (the built regel binary)")
	}
	evDir := env("REGEL_M5_EVIDENCE", "spec/gates/evidence-e/m5")
	timeout := time.Duration(envInt("REGEL_M5_TIMEOUT_S", 120)) * time.Second
	maxIters := envInt("REGEL_M5_MAXITERS", 4)
	maxInfra := envInt("REGEL_M5_MAX_INFRA", 6)
	k := envInt("REGEL_M5_K", PinnedK)
	authN := envInt("REGEL_M5_AUTHORING_N", len(AuthoringCorpus))
	restM := envInt("REGEL_M5_RESTART_M", len(RestartCorpus))

	if err := os.MkdirAll(evDir, 0o755); err != nil {
		t.Fatalf("evidence dir: %v", err)
	}

	cfg, err := pgwire.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	ctx := context.Background()
	conn, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect eval DB: %v", err)
	}
	defer conn.Close()
	epoch, err := CurrentEpoch(ctx, conn)
	if err != nil {
		t.Fatalf("epoch: %v", err)
	}
	scope := "org.org1"
	agentSubject := "agent:a1"
	agentKey := env("REGEL_M5_KEY", "m5-agent")

	// Pin k + corpus hashes PER EPOCH (L2 fix). A stale/tampered pin aborts.
	if err := EnsurePin(ctx, conn, epoch, "authoring", k, AuthoringCorpusHash(), len(AuthoringCorpus)); err != nil {
		t.Fatalf("authoring pin: %v", err)
	}
	if err := EnsurePin(ctx, conn, epoch, "restart", 1, RestartCorpusHash(), len(RestartCorpus)); err != nil {
		t.Fatalf("restart pin: %v", err)
	}

	trPath := filepath.Join(evDir, "transcript.jsonl")
	trf, err := os.OpenFile(trPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	defer trf.Close()
	emit := func(rec map[string]any) {
		rec["at"] = time.Now().UTC().Format(time.RFC3339)
		b, _ := json.Marshal(rec)
		trf.Write(append(b, '\n'))
	}

	callCount := 0
	start := time.Now()

	// ---------------- §3a authoring pass@k ----------------
	authDone, err := DoneAttempts(ctx, conn, epoch, "authoring")
	if err != nil {
		t.Fatalf("done authoring: %v", err)
	}
	sess := mustSession(t, ctx, bin, agentKey, dsn)
	infra := 0
authoring:
	for ti := 0; ti < authN && ti < len(AuthoringCorpus); ti++ {
		task := AuthoringCorpus[ti]
		for attempt := 1; attempt <= k; attempt++ {
			if authDone[task.ID][attempt] {
				continue
			}
			t.Logf("[authoring] task=%s attempt=%d/%d", task.ID, attempt, k)
			r, tr := RunAuthoringAttempt(sess, task, scope, attempt, maxIters, timeout)
			callCount++ // at least one LLM call per attempt (usually more with the fix loop)
			tr["kind"] = "authoring"
			emit(tr)
			if r.Err != "" {
				infra++
				t.Logf("  infra error (gap left): %s (%d/%d)", r.Err, infra, maxInfra)
				// MCP transport may be dead — restart the session.
				sess.Close()
				sess = mustSession(t, ctx, bin, agentKey, dsn)
				if infra >= maxInfra {
					t.Logf("  too many infra errors; leaving authoring partial and moving on")
					break authoring
				}
				continue
			}
			infra = 0
			if err := SaveResult(ctx, conn, epoch, "authoring", r); err != nil {
				t.Fatalf("save authoring result: %v", err)
			}
			t.Logf("  passed=%v admitted=%v iters=%d fuel=%.0f", r.Passed, r.Admitted, r.Iterations, r.FuelUsed)
		}
	}
	sess.Close()

	authResults, _ := LoadResults(ctx, conn, epoch, "authoring")
	am := ComputeAuthoring(authResults, k, authN)
	authDetail := map[string]any{
		"pass_at_1": am.PassAt1, "pass_at_k": am.PassAtK, "k": k,
		"p95_iterations_to_green": am.P95Iter, "completed_tasks": am.N, "corpus_run_n": authN,
		"full_corpus_n": len(AuthoringCorpus), "floor_pass1": FloorPassAt1, "floor_passk": FloorPassAtK,
		"per_task": am.PerTask, "iter_samples": am.IterSamples,
	}
	if err := WriteGate(ctx, conn, epoch, "authoring", am.N, FloorAuthoringN, am.PassAtK, FloorPassAtK, am.Green, am.Partial, authDetail); err != nil {
		t.Fatalf("write authoring gate: %v", err)
	}
	t.Logf("AUTHORING: pass@1=%.3f pass@k=%.3f (N=%d, k=%d) green=%v partial=%v", am.PassAt1, am.PassAtK, am.N, k, am.Green, am.Partial)

	// ---------------- §5 eval-derived fuel capacity ----------------
	capacity, covers, err := DeriveFuelCapacity(ctx, conn, epoch, am, authResults)
	if err != nil {
		t.Fatalf("derive fuel: %v", err)
	}
	t.Logf("FUEL: capacity=%.0f (ceil(P95=%.1f * %.0f * %.1f)) covers_corpus=%v", capacity, am.P95Iter, CostFullPipeline, FuelMargin, covers)

	// ---------------- §7 restart-decision accuracy ----------------
	restDone, err := DoneAttempts(ctx, conn, epoch, "restart")
	if err != nil {
		t.Fatalf("done restart: %v", err)
	}
	sess = mustSession(t, ctx, bin, agentKey, dsn)
	infra = 0
restart:
	for si := 0; si < restM && si < len(RestartCorpus); si++ {
		sc := RestartCorpus[si]
		if restDone[sc.ID][1] {
			continue
		}
		t.Logf("[restart] scenario=%s class=%s", sc.ID, sc.Class)
		r, tr := RunRestartScenario(ctx, sess, conn, sc, agentSubject, timeout)
		callCount++
		tr["kind"] = "restart"
		emit(tr)
		if r.Err != "" {
			infra++
			t.Logf("  infra error (gap left): %s (%d/%d)", r.Err, infra, maxInfra)
			sess.Close()
			sess = mustSession(t, ctx, bin, agentKey, dsn)
			if infra >= maxInfra {
				t.Logf("  too many infra errors; leaving restart partial")
				break restart
			}
			continue
		}
		infra = 0
		if err := SaveResult(ctx, conn, epoch, "restart", r); err != nil {
			t.Fatalf("save restart result: %v", err)
		}
		t.Logf("  decision=%v correct=%v", r.Detail["decision"], r.Passed)
	}
	sess.Close()

	restResults, _ := LoadResults(ctx, conn, epoch, "restart")
	rm := ComputeRestart(restResults, restM)
	restDetail := map[string]any{
		"accuracy": rm.Accuracy, "correct": rm.Correct, "M": rm.M, "corpus_run_m": restM,
		"full_corpus_m": len(RestartCorpus), "floor_accuracy": FloorRestartAcc, "floor_M": FloorRestartM,
		"per_case": rm.PerCase,
	}
	if err := WriteGate(ctx, conn, epoch, "restart", rm.M, FloorRestartM, rm.Accuracy, FloorRestartAcc, rm.Green, rm.Partial, restDetail); err != nil {
		t.Fatalf("write restart gate: %v", err)
	}
	t.Logf("RESTART: accuracy=%.3f (correct=%d/%d) green=%v partial=%v", rm.Accuracy, rm.Correct, rm.M, rm.Green, rm.Partial)

	// ---------------- the mechanized flip, verified end to end ----------------
	// Writing the restart m5_gate row IS the flip (the kernel door reads it). Prove
	// it on REAL numbers: drive the agent condition.restart over the real MCP door
	// and assert it is ENABLED iff the restart gate is green — and REFUSED while red.
	flip := verifyFlip(t, ctx, conn, bin, agentKey, dsn, agentSubject, rm.Green)
	t.Logf("FLIP: restart_gate_green=%v agent_authority_enabled=%v (%s)", rm.Green, flip.enabled, flip.detail)

	// ---------------- write machine + human evidence ----------------
	summary := map[string]any{
		"epoch": epoch, "run_at": time.Now().UTC().Format(time.RFC3339),
		"llm_calls_at_least": callCount, "wall_seconds": int(time.Since(start).Seconds()),
		"gates": map[string]any{
			"authoring_pass_at_k": authDetail,
			"restart_accuracy":    restDetail,
			"fuel_capacity": map[string]any{
				"capacity": capacity, "covers_corpus": covers, "p95_iterations_to_green": am.P95Iter,
				"formula": fmt.Sprintf("ceil(%.2f * %.0f * %.1f)", am.P95Iter, CostFullPipeline, FuelMargin),
			},
		},
		"flip": map[string]any{
			"restart_gate_green":      rm.Green,
			"agent_authority_enabled": flip.enabled,
			"consistent":              flip.enabled == rm.Green,
			"detail":                  flip.detail,
		},
		"pins": map[string]any{
			"authoring": map[string]any{"k": k, "corpus_hash": AuthoringCorpusHash(), "corpus_size": len(AuthoringCorpus)},
			"restart":   map[string]any{"k": 1, "corpus_hash": RestartCorpusHash(), "corpus_size": len(RestartCorpus)},
		},
	}
	writeJSON(t, filepath.Join(evDir, "summary.json"), summary)
	writeHuman(t, filepath.Join(evDir, "CAPTURE.md"), epoch, am, rm, capacity, covers, flip, callCount, int(time.Since(start).Seconds()), authN, restM, k)

	// The flip MUST be consistent with the measured gate — this is the one hard
	// invariant (a green gate that did not enable, or a red gate that did, is a
	// harness bug). Gate colour itself is DATA, never a test failure.
	if flip.enabled != rm.Green {
		t.Fatalf("FLIP INCONSISTENT: restart gate green=%v but agent authority enabled=%v", rm.Green, flip.enabled)
	}
}

type flipResult struct {
	enabled bool
	detail  string
}

// verifyFlip drives the agent's condition.restart over the real MCP door against a
// freshly seeded condition and reports whether the authority is enabled (i.e. the
// response is NOT RESTART_DISABLED).
func verifyFlip(t *testing.T, ctx context.Context, conn *pgwire.Conn, bin, key, dsn, agentSubject string, _ bool) flipResult {
	sc := RestartScenario{ID: "flip_probe", Class: "fuel.exhausted", Message: "flip probe", Restarts: []string{"resume"}, Correct: "resume"}
	condID, err := SeedRestartCondition(ctx, conn, sc, agentSubject)
	if err != nil {
		t.Fatalf("flip seed: %v", err)
	}
	var frameHash string
	conn.QueryRow(ctx, `SELECT encode(sha256(frames),'hex') FROM continuation
	  WHERE id=(SELECT continuation_id FROM durable_condition WHERE id=$1)`, []any{condID}, &frameHash)
	sess := mustSession(t, ctx, bin, key, dsn)
	defer sess.Close()
	resp, err := sess.Tool("condition.restart", map[string]any{
		"condition_id": condID, "restart_name": "resume", "expectedHash": frameHash})
	if err != nil {
		t.Fatalf("flip probe restart: %v", err)
	}
	code, _ := resp["code"].(string)
	if code == "RESTART_DISABLED" {
		return flipResult{enabled: false, detail: "agent condition.restart refused RESTART_DISABLED"}
	}
	status, _ := resp["status"].(string)
	return flipResult{enabled: true, detail: "agent condition.restart accepted (status=" + status + ", code=" + code + ")"}
}

func mustSession(t *testing.T, ctx context.Context, bin, key, dsn string) *MCPSession {
	t.Helper()
	s, err := StartMCP(ctx, bin, key, dsn)
	if err != nil {
		t.Fatalf("start mcp: %v", err)
	}
	return s
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeHuman(t *testing.T, path string, epoch int, am AuthoringMetrics, rm RestartMetrics, capacity float64, covers bool, flip flipResult, calls, secs, authN, restM, k int) {
	t.Helper()
	verdict := func(green, partial bool) string {
		if partial {
			return "OPEN (partial — LLM died mid-run; harness left resumable)"
		}
		if green {
			return "GREEN"
		}
		return "RED (run-but-below-floor)"
	}
	s := fmt.Sprintf(`# M5 real-LLM eval capture (ADR-12 §3a/§5/§7) — epoch %d

Captured from real `+"`claude -p`"+` runs driving the real MCP agent plane. Every
number below is computed from persisted m5_eval_result rows. LLM calls (≥): %d;
wall: %ds.

## §3a authoring pass@k  — %s
- corpus run: N=%d of %d (full corpus), k=%d (pinned)
- pass@1 = %.3f  (floor ≥ %.2f)
- pass@k = %.3f  (floor ≥ %.2f)
- iterations-to-green P95 = %.1f
- ADR §3a task-suite floor is N≥%d; this run is a real sample of the pinned
  corpus (residue: scale-to-N≥50 is operator-schedulable — harness runs at any N).

## §7 restart-decision accuracy  — %s
- corpus run: M=%d of %d, floor M≥%d
- accuracy = %.3f  (floor ≥ %.2f)

## §5 eval-derived fuel capacity
- ADR formula: ceil(P95_iter=%.1f × cost_full_pipeline=%.0f × margin=%.1f) = %.0f (the floor)
- provisioned capacity (written to admission_capacity, never throttling): %.0f
- ADR formula covers a P95-honest passing attempt's fuel: %v%s

## The mechanized flip (§7)
- restart gate green: %v
- agent condition.restart authority enabled (via the real MCP door): %v
- consistent: %v — %s

Flip decision: %s
`,
		epoch, calls, secs,
		verdict(am.Green, am.Partial), am.N, len(AuthoringCorpus), k, am.PassAt1, FloorPassAt1, am.PassAtK, FloorPassAtK, am.P95Iter, FloorAuthoringN,
		verdict(rm.Green, rm.Partial), rm.M, len(RestartCorpus), FloorRestartM, rm.Accuracy, FloorRestartAcc,
		am.P95Iter, CostFullPipeline, FuelMargin, math.Ceil(am.P95Iter*CostFullPipeline*FuelMargin), capacity, covers, coverNote(covers),
		rm.Green, flip.enabled, flip.enabled == rm.Green, flip.detail,
		flipVerdict(rm.Green, rm.Partial),
	)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func coverNote(covers bool) string {
	if covers {
		return ""
	}
	return " — formula under-covered (P95_iter is low: each iteration incurs a dry-run AND a commit full-pipeline charge); provisioned capacity was ADJUSTED UP to cover, recorded as a §5 admission"
}

func flipVerdict(green, partial bool) string {
	if partial {
		return "HELD (restart gate partial/open — agent authority stays DISABLED)"
	}
	if green {
		return "FLIPPED — agent-facing condition.restart ENABLED (gate read green)"
	}
	return "REFUSED — agent-facing condition.restart stays DISABLED (gate read red)"
}
