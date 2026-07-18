package kernel

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/pgwire"
)

// R10 — hold-fencing cost model (Stage-F discharge of STAGE-E §9 R10).
//
// The fence: when an operator reverts a bad epoch (ADR-08 §6a), every dependent
// continuation bound to that epoch is HELD FAIL-CLOSED — an epoch_hold audit row
// plus a status flip to 'condition' so the reactor never resumes it against the
// reverted world (admission.RevertEpoch). The DOMINANT cost of that fence is the
// hold, and it grows with the number of dependents.
//
// N and metric. We construct a DEPENDENTS-HEAVY hold: N=5000 live continuations
// bound to the bad epoch (a busy tenant with thousands of parked/live workflows on
// one epoch — well beyond the ~single-digit drill, small enough to run every suite).
// The metric is the wall-clock latency (ms) of the whole revert commit that fences
// them — "time-to-recovered" in ADR-08 §6a's own words — pinned as the
// `epoch.hold_fence_ms` perf_budget row (epoch 1, milestone M6; registered in
// ADR-13 §3, BUILD-F).
//
// The budget is REAL, not decorative. The real fence is set-based: one INSERT ...
// SELECT + one UPDATE over the blast closure, O(1) round trips in N. The per-row
// loop it replaced made 2N round trips; this test measures BOTH over identical
// N-dependent state and asserts (a) the un-batched runaway blows the budget and
// (b) the real batched fence stays comfortably under it. Under R10_RUNAWAY=1 the
// un-batched measurement is routed through the SAME budget gate and fails red —
// captured in evidence-f/r10/red-path.txt.

const (
	r10FenceDependents = 5000
	// Budget sits ~geometric-mean between the set-based fence (~37 ms, best-of-3) and
	// the un-batched runaway (~356 ms) — ~3x headroom each way. Real fence green,
	// un-batched runaway red.
	r10FenceBudgetMS = 120
)

// seedFenceDependents inserts n live continuations stamped to badEpoch — the blast
// closure RevertEpoch fences. One statement, so setup is not the thing measured.
func seedFenceDependents(t *testing.T, e *reactorEnv, badEpoch, n int) {
	t.Helper()
	ctx := context.Background()
	e.withConn(t, func(c *pgwire.Conn) {
		var defHash string
		ok, err := c.QueryRow(ctx, `SELECT hash FROM definition LIMIT 1`, nil, &defHash)
		if err != nil || !ok {
			t.Fatalf("no genesis definition to reference: ok=%v err=%v", ok, err)
		}
		if _, err := c.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal)
SELECT gen_random_uuid(), 'workflow', $1, $2, 1, '\x00'::bytea,
       '{"kind":"timer"}'::jsonb, 'sleeping', '{"subject":"op"}'::jsonb
FROM generate_series(1, $3)`, defHash, badEpoch, n); err != nil {
			t.Fatalf("seed %d dependents: %v", n, err)
		}
	})
	if got := e.intScalar(t, `SELECT count(*) FROM continuation WHERE epoch=$1`, badEpoch); int(got) != n {
		t.Fatalf("seeded %d dependents, want %d", got, n)
	}
}

// measureFence builds a fresh world (bad epoch 2 committed, N dependents stamped to
// it), fences them via either the real set-based RevertEpoch or the un-batched
// runaway, and returns the fence latency in ms. It PROVES fail-closed every time:
// >= N active epoch_hold rows and zero bad-epoch dependents left un-fenced.
func measureFence(t *testing.T, unbatched bool) float64 {
	t.Helper()
	ctx := context.Background()
	e := newReactorEnv(t)
	if err := e.commit(t, 2, nil); err != nil {
		t.Fatalf("commit bad epoch 2: %v", err)
	}
	seedFenceDependents(t, e, 2, r10FenceDependents)

	var ms float64
	var held int
	e.withConn(t, func(c *pgwire.Conn) {
		t0 := time.Now()
		var ids []string
		var err error
		if unbatched {
			ids, err = revertEpochUnbatched(ctx, c, 3, 1)
		} else {
			ids, err = admission.RevertEpoch(ctx, c, 3, 1)
		}
		ms = float64(time.Since(t0).Microseconds()) / 1000.0
		if err != nil {
			t.Fatalf("fence (unbatched=%v): %v", unbatched, err)
		}
		held = len(ids)
	})
	if held < r10FenceDependents {
		t.Fatalf("fence held %d dependents, want >= %d", held, r10FenceDependents)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM epoch_hold WHERE released_at IS NULL`); int(n) < r10FenceDependents {
		t.Fatalf("active epoch_hold rows = %d, want >= %d (not fail-closed)", n, r10FenceDependents)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM continuation WHERE epoch=2 AND status <> 'condition'`); n != 0 {
		t.Fatalf("%d bad-epoch dependents were not fenced to 'condition'", n)
	}
	// The GATE writes the row against the real (batched) fence's env only.
	if !unbatched {
		lastFenceEnv = e
	}
	return ms
}

// lastFenceEnv holds the env whose perf_budget row the gate writes into.
var lastFenceEnv *reactorEnv

func TestR10HoldFenceCost(t *testing.T) {
	// GREEN: real set-based fence, best-of-3 (transient scheduler load under the
	// full `go test ./...` cross-package run cannot fake a regression — same
	// discipline as the ADR-04 §8 M0 microbench).
	greenMS := measureFence(t, false)
	gateEnv := lastFenceEnv
	for i := 1; i < 3; i++ {
		if m := measureFence(t, false); m < greenMS {
			greenMS = m
			gateEnv = lastFenceEnv
		}
	}

	// RED baseline: the un-batched per-row fence over IDENTICAL N-dependent state.
	redMS := measureFence(t, true)

	t.Logf("R10 hold-fence cost @ N=%d dependents: batched(real)=%.1fms  unbatched(runaway)=%.1fms  "+
		"budget=%dms  speedup=%.1fx", r10FenceDependents, greenMS, redMS, r10FenceBudgetMS, redMS/maxF(greenMS, 0.001))

	// The budget is MEANINGFUL, not decorative: the un-batched runaway must blow it.
	if redMS <= float64(r10FenceBudgetMS) {
		t.Fatalf("budget %dms is decorative: un-batched runaway %.1fms did not exceed it — raise r10FenceDependents",
			r10FenceBudgetMS, redMS)
	}

	// RED WITNESS: route the un-batched measurement through the SAME budget gate the
	// real fence uses — it fails red. Captured once into evidence-f/r10/red-path.txt.
	if os.Getenv("R10_RUNAWAY") == "1" {
		assertFenceBudget(t, gateEnv, redMS, "un-batched runaway")
		return
	}

	// THE GATE (green): the real set-based fence is under budget; write the row.
	assertFenceBudget(t, gateEnv, greenMS, "batched (real)")
}

// assertFenceBudget writes the epoch.hold_fence_ms perf_budget row (same shape and
// machinery as the M0/M2/M4 budgets) and fails the milestone if measured crosses it.
func assertFenceBudget(t *testing.T, e *reactorEnv, measured float64, label string) {
	t.Helper()
	ctx := context.Background()
	e.withConn(t, func(c *pgwire.Conn) {
		if _, err := c.Exec(ctx, `
INSERT INTO perf_budget (epoch, metric, tier, budget, measured, milestone)
VALUES (1, 'epoch.hold_fence_ms', 'trusted', $1, $2, 'M6')
ON CONFLICT (epoch, metric) DO UPDATE SET measured=EXCLUDED.measured, budget=EXCLUDED.budget`,
			float64(r10FenceBudgetMS), measured); err != nil {
			t.Fatalf("write perf_budget epoch.hold_fence_ms: %v", err)
		}
	})
	if n := e.intScalar(t, `SELECT count(*) FROM perf_budget WHERE epoch=1 AND metric='epoch.hold_fence_ms'`); n != 1 {
		t.Fatalf("expected 1 epoch.hold_fence_ms perf_budget row, got %d", n)
	}
	e.withConn(t, func(c *pgwire.Conn) {
		var metric, tier, milestone string
		var budget, meas float64
		if _, err := c.QueryRow(ctx,
			`SELECT metric, tier, budget, measured, milestone FROM perf_budget WHERE epoch=1 AND metric='epoch.hold_fence_ms'`,
			nil, &metric, &tier, &budget, &meas, &milestone); err != nil {
			t.Fatalf("read perf_budget row: %v", err)
		}
		t.Logf("perf_budget row: epoch=1 metric=%s tier=%s budget=%.0f measured=%.1f milestone=%s (%s)",
			metric, tier, budget, meas, milestone, label)
	})
	if measured > float64(r10FenceBudgetMS) {
		t.Fatalf("R10 hold-fence budget regression (%s): epoch.hold_fence_ms measured=%.1fms crosses budget %dms",
			label, measured, r10FenceBudgetMS)
	}
}

// revertEpochUnbatched is the OLD, per-row hold this build replaced — kept in the
// test as the runaway baseline. It is a faithful copy of admission.RevertEpoch with
// exactly one difference: the hold does 2N round trips (one INSERT + one UPDATE per
// held dependent) instead of the set-based INSERT ... SELECT + UPDATE. Same motion,
// same fail-closed result; only the cost differs. This is what the budget guards.
func revertEpochUnbatched(ctx context.Context, conn *pgwire.Conn, target, revertTo int) ([]string, error) {
	if err := conn.BeginSerializable(ctx); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = conn.Rollback(ctx)
		}
	}()

	var bad int
	if _, err := conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &bad); err != nil {
		return nil, err
	}
	if target <= bad {
		return nil, fmt.Errorf("revert: target %d must be ahead of the bad epoch %d", target, bad)
	}
	var okRoot, okAttest string
	found, err := conn.QueryRow(ctx,
		`SELECT std_manifest_root, dispatch_attestation FROM epoch WHERE n=$1`,
		[]any{revertTo}, &okRoot, &okAttest)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("revert: prior-good epoch %d not found", revertTo)
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO epoch (n, std_manifest_root, dispatch_attestation, supersedes)
VALUES ($1, $2, $3, $4)`, target, okRoot, okAttest, bad); err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO std_manifest (epoch, hash) SELECT $1, hash FROM std_manifest WHERE epoch=$2
ON CONFLICT DO NOTHING`, target, revertTo); err != nil {
		return nil, err
	}

	rows, err := conn.Query(ctx, fmt.Sprintf(`
SELECT id::text FROM continuation
 WHERE status IN ('sleeping','ready','condition','running')
   AND (epoch = %d
        OR updated_at >= (SELECT created_at FROM epoch WHERE n=%d))`, bad, bad))
	if err != nil {
		return nil, err
	}
	var held []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		held = append(held, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range held {
		if _, err := conn.Exec(ctx, `
INSERT INTO epoch_hold (continuation_id, bad_epoch, revert_epoch, reason)
VALUES ($1,$2,$3,$4) ON CONFLICT (continuation_id, bad_epoch) DO NOTHING`,
			id, bad, target, fmt.Sprintf("bound to reverted epoch %d", bad)); err != nil {
			return nil, err
		}
		if _, err := conn.Exec(ctx,
			`UPDATE continuation SET status='condition', updated_at=now() WHERE id=$1`, id); err != nil {
			return nil, err
		}
	}

	if _, err := conn.Exec(ctx, `UPDATE epoch_current SET n=$1 WHERE one=true`, target); err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`NOTIFY epoch, '%d'`, target)); err != nil {
		return nil, err
	}
	if err := conn.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	return held, nil
}
