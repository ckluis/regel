package kernel

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"strings"
	"testing"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
)

// hermeticWorkflow parks twice with RICH live state across both pauses: an
// array (mixed int/f64), a record, a closure over the array, a bigint, and an
// f64 with a non-trivial bit pattern. The final value folds every one of them
// in, so any decode/re-encode divergence (map order, scheduler, GC pressure)
// surfaces as a different result or a different re-checkpointed blob.
const hermeticWorkflow = `import { sleep } from "std/wf";
export function w(): string {
  const xs = [1, 2, 3.25, 4];
  const rec = { name: "hermetic", count: 7 };
  const big = 123456789012345678901234567890n;
  const f = (x: number): number => x * 2 + xs.length;
  const tiny = 0.1 + 0.2;
  sleep(50);
  let s = 0;
  for (const x of xs) { s = s + x; }
  const mid = s + f(rec.count) + tiny;
  sleep(50);
  return rec.name + ":" + mid + ":" + (big + 1n);
}`

// TestHermeticityCrossKernel is ADR-05 Red-Path Test 12: ONE parked continuation
// is cloned K=6 times and each clone is resumed by SEPARATE `regel step-once`
// process invocations (distinct processes → distinct Go map seeds), with
// GOMAXPROCS and GOGC varied across invocations for scheduling/GC variation.
// All runs must be byte-identical: same printed step-once JSON, identical
// sha256 over the re-checkpointed frames blob at the mid park, and identical
// sha256 over the terminal result bytea. Any divergence is red — a hidden
// dependence on map order, scheduling, or build-carried state.
//
// Explicit invocation:
//
//	go test ./internal/kernel/ -run TestHermeticityCrossKernel -count=1 -v
func TestHermeticityCrossKernel(t *testing.T) {
	if testing.Short() {
		t.Skip("hermeticity probe spawns 12 step-once processes")
	}
	e := newProcEnv(t)
	bin := regelBin(t)
	ctx := context.Background()

	hash := e.admit(t, hermeticWorkflow, "app/herm", "w")

	// Seed: start + one in-process step → parks at sleep #1 with the rich state.
	srv, err := New(ctx, e.pool)
	if err != nil {
		t.Fatalf("kernel.New: %v", err)
	}
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	seedID, err := cfr.StartWorkflow(ctx, conn, srv.stepEnv(0), srv.Interp(), hash, nil,
		map[string]any{"subject": "op", "operator": true}, cek.TierTrusted)
	if err != nil {
		e.pool.Release(conn)
		t.Fatalf("StartWorkflow: %v", err)
	}
	resume := func(st *cek.State, d cek.Delivery, p cek.Principal) cek.Outcome {
		return srv.Interp().Resume(ctx, st, d, p)
	}
	out, claimed, err := cfr.ClaimAndStep(ctx, conn, srv.stepEnv(30), srv.Interp(), seedID, 0, resume)
	e.pool.Release(conn)
	if err != nil || !claimed || out.Kind != cek.OutParked {
		t.Fatalf("seed park: claimed=%v kind=%d err=%v", claimed, out.Kind, err)
	}

	// Clone the parked row K times: fresh uuid, SAME frames bytes, status='ready'
	// so `regel step-once` can claim each directly.
	const K = 6
	clones := make([]string, K)
	for i := 0; i < K; i++ {
		clones[i] = e.cloneContinuation(t, seedID)
	}
	// Retire the seed so nothing else touches it.
	e.exec(t, `UPDATE continuation SET status='cancelled' WHERE id=$1`, seedID)

	// Env variation per clone: distinct processes already give distinct map
	// seeds; vary GOMAXPROCS and GOGC for scheduler/GC variation on top.
	envs := [][]string{
		{"GOMAXPROCS=1", "GOGC=50"},
		{"GOMAXPROCS=4", "GOGC=50"},
		{"GOMAXPROCS=1", "GOGC=100"},
		{"GOMAXPROCS=4", "GOGC=100"},
		{"GOMAXPROCS=1", "GOGC=400"},
		{"GOMAXPROCS=4", "GOGC=400"},
	}

	var midOut, doneOut, framesDig, resultDig [K]string
	for i, id := range clones {
		// Step 1 of the clone: resume from park #1 → re-parks at sleep #2.
		midOut[i] = e.stepOnceProc(t, bin, id, envs[i])
		framesDig[i] = e.text(t, `SELECT encode(sha256(frames),'hex') FROM continuation WHERE id=$1`, id)
		st := e.text(t, `SELECT status FROM continuation WHERE id=$1`, id)
		if st != "sleeping" {
			t.Fatalf("clone %d after mid step: status=%q, want sleeping", i, st)
		}
		// Wake it (timer due is ~50ms out; flip directly — the delivery is the same).
		e.exec(t, `UPDATE continuation SET status='ready' WHERE id=$1 AND status='sleeping'`, id)
		// Step 2: resume from park #2 → done.
		doneOut[i] = e.stepOnceProc(t, bin, id, envs[i])
		resultDig[i] = e.text(t, `SELECT encode(sha256(result),'hex') FROM continuation WHERE id=$1`, id)
		if st := e.text(t, `SELECT status FROM continuation WHERE id=$1`, id); st != "done" {
			t.Fatalf("clone %d after final step: status=%q, want done", i, st)
		}
		t.Logf("clone %d (%s): frames_sha256=%s result_sha256=%s", i, envs[i], framesDig[i], resultDig[i])
	}

	// ALL runs byte-identical.
	matches := 0
	for i := 1; i < K; i++ {
		if midOut[i] != midOut[0] {
			t.Fatalf("clone %d mid step-once output diverged:\n%q\nvs\n%q", i, midOut[i], midOut[0])
		}
		if doneOut[i] != doneOut[0] {
			t.Fatalf("clone %d final step-once output diverged:\n%q\nvs\n%q", i, doneOut[i], doneOut[0])
		}
		if framesDig[i] != framesDig[0] {
			t.Fatalf("clone %d re-checkpointed frames diverged: %s vs %s", i, framesDig[i], framesDig[0])
		}
		if resultDig[i] != resultDig[0] {
			t.Fatalf("clone %d result bytea diverged: %s vs %s", i, resultDig[i], resultDig[0])
		}
		matches++
	}
	// Belt and braces: the printed result actually folds the rich state.
	wantFrag := `"hermetic:` // rec.name prefix
	if !strings.Contains(doneOut[0], wantFrag) {
		t.Fatalf("final output %q does not carry the folded result", doneOut[0])
	}
	sum := sha256.Sum256([]byte(doneOut[0]))
	t.Logf("HERMETICITY: %d/%d clones byte-identical across distinct processes "+
		"(GOMAXPROCS 1/4 x GOGC 50/100/400): frames=%s result=%s stdout_sha256=%x",
		matches+1, K, framesDig[0], resultDig[0], sum[:8])
}

// cloneContinuation duplicates a parked continuation row under a fresh uuid with
// status='ready' (claimable by step-once), preserving frames/wake/principal.
func (e *procEnv) cloneContinuation(t *testing.T, srcID string) string {
	t.Helper()
	conn, err := e.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer e.pool.Release(conn)
	var id string
	found, err := conn.QueryRow(context.Background(), `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
SELECT gen_random_uuid(), kind, root_def_hash, epoch, format_ver, frames, wake, 'ready', principal, step_seq
FROM continuation WHERE id=$1
RETURNING id::text`, []any{srcID}, &id)
	if err != nil || !found {
		t.Fatalf("clone %s: found=%v err=%v", srcID, found, err)
	}
	return id
}

// stepOnceProc runs `regel step-once <id>` as its own process with extra env
// vars and returns its stdout (the JSON step summary). The process is fully
// reaped by CombinedOutput before return.
func (e *procEnv) stepOnceProc(t *testing.T, bin, id string, extraEnv []string) string {
	t.Helper()
	cmd := exec.Command(bin, "step-once", "-lease", "30", id)
	cmd.Env = append(append(os.Environ(), "REGEL_PG_DSN="+e.dsn), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("step-once %s (%v): %v\n%s", id, extraEnv, err, out)
	}
	return string(out)
}
