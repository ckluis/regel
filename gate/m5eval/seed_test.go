package m5eval

import (
	"context"
	"testing"
	"time"
)

// seed_test.go is the DETERMINISTIC (no-LLM) red-path suite for the harness
// itself — the four guards the task names:
//   (1) a seeded known-good solution ADMITS and PASSES the oracle;
//   (2) a seeded known-bad solution (wrong behavior but admissible) ADMITS but
//       FAILS the oracle — the harness cannot be gamed by admission alone;
//   (3) a tampered pin (changed k or corpus hash) is DETECTED and refused;
//   (4) the metric computation is correct on synthetic results.
// These run in ordinary CI when PG is present and skip cleanly otherwise.

// --- (4) corpus sizing + pin hashing (pure, no DB) ---------------------------

func TestCorpusInvariants(t *testing.T) {
	if len(AuthoringCorpus) < 12 {
		t.Fatalf("authoring corpus too small: %d", len(AuthoringCorpus))
	}
	if len(RestartCorpus) < FloorRestartM {
		t.Fatalf("restart corpus below ADR-12 §7 floor M>=%d: have %d", FloorRestartM, len(RestartCorpus))
	}
	// every restart scenario's Correct must be an offered restart, and no Unsafe
	// name may equal Correct (a self-inconsistent label).
	for _, s := range RestartCorpus {
		if !inList(s.Correct, s.Restarts) {
			t.Fatalf("scenario %s: correct %q not in offered restarts %v", s.ID, s.Correct, s.Restarts)
		}
		if inList(s.Correct, s.Unsafe) {
			t.Fatalf("scenario %s: correct %q also listed unsafe", s.ID, s.Correct)
		}
	}
	// hashes are stable + kind-tagged.
	if AuthoringCorpusHash() == RestartCorpusHash() {
		t.Fatal("corpus hashes collide")
	}
	if AuthoringCorpusHash() != AuthoringCorpusHash() {
		t.Fatal("authoring hash not deterministic")
	}
	// unique task ids.
	seen := map[string]bool{}
	for _, tk := range AuthoringCorpus {
		if seen[tk.ID] {
			t.Fatalf("duplicate authoring id %s", tk.ID)
		}
		seen[tk.ID] = true
	}
}

// --- (2) oracle self-test on the corpus references (pure, no DB) --------------

// The known-good Reference must satisfy its own oracle; the KnownBad must FAIL it.
// This proves the behavioral oracle discriminates correctness (not just
// admissibility) BEFORE any LLM call is spent.
func TestOracleDiscriminates(t *testing.T) {
	for _, tk := range AuthoringCorpus {
		ok, why := tk.BehaviorOK(tk.Reference)
		if !ok {
			t.Fatalf("task %s: known-good reference FAILS its own oracle: %s", tk.ID, why)
		}
		if tk.KnownBad != "" {
			bad, _ := tk.BehaviorOK(tk.KnownBad)
			if bad {
				t.Fatalf("task %s: known-bad solution PASSES the oracle (oracle is gameable)", tk.ID)
			}
		}
	}
}

// --- metric computation correctness (pure, no DB) ----------------------------

func TestMetricComputation(t *testing.T) {
	// 4 tasks, k=3. task A green attempt1(iter2), B green attempt2(iter1),
	// C green attempt3, D never green.
	rs := []AttemptResult{
		{TaskID: "A", Attempt: 1, Passed: true, Iterations: 2},
		{TaskID: "B", Attempt: 1, Passed: false, Iterations: 4},
		{TaskID: "B", Attempt: 2, Passed: true, Iterations: 1},
		{TaskID: "C", Attempt: 1, Passed: false},
		{TaskID: "C", Attempt: 2, Passed: false},
		{TaskID: "C", Attempt: 3, Passed: true, Iterations: 3},
		{TaskID: "D", Attempt: 1, Passed: false},
		{TaskID: "D", Attempt: 2, Passed: false},
		{TaskID: "D", Attempt: 3, Passed: false},
	}
	m := ComputeAuthoring(rs, 3, 4)
	// pass@1: only A green on attempt 1 of 4 tasks = 0.25.
	if m.PassAt1 != 0.25 {
		t.Fatalf("pass@1 want 0.25 got %v", m.PassAt1)
	}
	// pass@k: A,B,C green within 3 = 0.75.
	if m.PassAtK != 0.75 {
		t.Fatalf("pass@k want 0.75 got %v", m.PassAtK)
	}
	if m.Green {
		t.Fatalf("0.25/0.75 is below floor, must be red")
	}
	// restart metric: 29/31 correct etc.
	restart := []AttemptResult{}
	for i := 0; i < 30; i++ {
		restart = append(restart, AttemptResult{TaskID: string(rune('a' + i)), Attempt: 1, Passed: i < 29})
	}
	rm := ComputeRestart(restart, 30)
	if rm.M != 30 || rm.Correct != 29 {
		t.Fatalf("restart M/correct: %d/%d", rm.M, rm.Correct)
	}
	if rm.Accuracy < 0.96 || rm.Accuracy > 0.97 {
		t.Fatalf("restart accuracy ~0.9667, got %v", rm.Accuracy)
	}
	if !rm.Green {
		t.Fatal("29/30 >= 0.95 with M=30 must be green")
	}
	// under the size floor is red even at 100%.
	small := []AttemptResult{{TaskID: "x", Attempt: 1, Passed: true}}
	if ComputeRestart(small, 1).Green {
		t.Fatal("M=1 must be red regardless of accuracy (below ADR §7 floor)")
	}
}

// --- (3) pin tamper detection (DB) -------------------------------------------

func TestPinTamperDetected(t *testing.T) {
	w := setupGate(t)
	ctx := context.Background()
	hash := AuthoringCorpusHash()
	// first pin succeeds.
	if err := EnsurePin(ctx, w.conn, w.epoch, "authoring", PinnedK, hash, len(AuthoringCorpus)); err != nil {
		t.Fatalf("first pin: %v", err)
	}
	// re-pin with the same (k, hash) is idempotent-OK.
	if err := EnsurePin(ctx, w.conn, w.epoch, "authoring", PinnedK, hash, len(AuthoringCorpus)); err != nil {
		t.Fatalf("idempotent re-pin: %v", err)
	}
	// changing k against the existing pin is DETECTED.
	if err := EnsurePin(ctx, w.conn, w.epoch, "authoring", PinnedK+1, hash, len(AuthoringCorpus)); err == nil {
		t.Fatal("changed k must be detected as a tampered pin")
	}
	// changing the corpus hash is DETECTED.
	if err := EnsurePin(ctx, w.conn, w.epoch, "authoring", PinnedK, hash+"XX", len(AuthoringCorpus)); err == nil {
		t.Fatal("changed corpus hash must be detected as a tampered pin")
	}
}

// --- (1)+(2) admission + oracle through the REAL MCP door (DB) ----------------

// TestSeededSolutionsThroughRealDoor proves, without any LLM, that the reference
// solutions ADMIT through the real MCP pipeline AND pass the oracle, and that the
// known-bad solutions ADMIT but FAIL the oracle. This is the harness's own
// "can't be gamed by admission alone" red-path, exercised end to end.
func TestSeededSolutionsThroughRealDoor(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the subprocess door in -short")
	}
	w := setupGate(t)
	sess := w.startMCP()
	// sample a few tasks to keep the DB test quick (the oracle self-test above
	// already covers all references behaviorally).
	sample := []string{"add_two", "factorial", "is_even", "greet_concat", "gcd"}
	for _, id := range sample {
		tk := taskByID(t, id)
		good, err := sess.Tool("patch.submit", map[string]any{
			"source": tk.Reference, "module": tk.Module, "scope": w.scope, "commit": true})
		if err != nil {
			t.Fatalf("%s good submit: %v", id, err)
		}
		if good["outcome"] != "admitted" {
			t.Fatalf("%s: known-good must admit, got %v", id, good["outcome"])
		}
		if ok, why := tk.BehaviorOK(tk.Reference); !ok {
			t.Fatalf("%s: known-good must pass oracle: %s", id, why)
		}
		// known-bad to a sibling module so the name does not clash.
		bad, err := sess.Tool("patch.submit", map[string]any{
			"source": tk.KnownBad, "module": tk.Module + "_bad", "scope": w.scope, "commit": true})
		if err != nil {
			t.Fatalf("%s bad submit: %v", id, err)
		}
		if bad["outcome"] != "admitted" {
			t.Fatalf("%s: known-bad must still ADMIT (it is well-typed): got %v", id, bad["outcome"])
		}
		if ok, _ := tk.BehaviorOK(tk.KnownBad); ok {
			t.Fatalf("%s: known-bad must FAIL the oracle (admission alone must not pass)", id)
		}
	}
	_ = time.Second
}

func taskByID(t *testing.T, id string) AuthoringTask {
	t.Helper()
	for _, tk := range AuthoringCorpus {
		if tk.ID == id {
			return tk
		}
	}
	t.Fatalf("no task %s", id)
	return AuthoringTask{}
}
