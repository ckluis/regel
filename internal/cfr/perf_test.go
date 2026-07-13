package cfr

import (
	"testing"
	"time"

	"regel.dev/regel/internal/cek"
)

// TestPerfBudgetM0 measures the ADR-04 §8 M0 benchmarks against the reference
// microbench and writes them to perf_budget rows (epoch 1, milestone M0). A
// measured value crossing its budget fails the milestone, exactly like a failed
// kill-test (R1-07).
func TestPerfBudgetM0(t *testing.T) {
	e := setupDB(t)
	ctx := ctxT(t)
	in, names := e.seed(t, burnProgram, nil, nil)
	hash := names["burn"]
	arg := []cek.Value{cek.NumV(2000)}

	// Warm the AST cache.
	in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierTrusted})

	const iters = 300
	var totalTransitions int64
	t0 := time.Now()
	for i := 0; i < iters; i++ {
		o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierTrusted})
		if o.Kind != cek.OutDone {
			t.Fatalf("governor run kind=%d", o.Kind)
		}
		totalTransitions += o.Transitions
	}
	govDur := time.Since(t0)

	t1 := time.Now()
	for i := 0; i < iters; i++ {
		o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierSandbox, Fuel: 1 << 40, Alloc: 1 << 40})
		if o.Kind != cek.OutDone {
			t.Fatalf("fuel run kind=%d", o.Kind)
		}
	}
	fuelDur := time.Since(t1)

	stepsPerSec := float64(totalTransitions) / govDur.Seconds()
	taxPct := (fuelDur.Seconds() - govDur.Seconds()) / govDur.Seconds() * 100
	if taxPct < 0 {
		taxPct = 0
	}
	t.Logf("M0: cek_steps_per_sec=%.0f  metering_tax_pct=%.2f", stepsPerSec, taxPct)

	writeBudget := func(metric, tier string, budget, measured float64, ok bool) {
		if _, err := e.conn.Exec(ctx, `
INSERT INTO perf_budget (epoch, metric, tier, budget, measured, milestone)
VALUES (1, $1, $2, $3, $4, 'M0')
ON CONFLICT (epoch, metric) DO UPDATE SET measured=$4`,
			metric, tier, budget, measured); err != nil {
			t.Fatalf("write perf_budget %s: %v", metric, err)
		}
		if !ok {
			t.Fatalf("M0 budget regression: %s measured=%.2f crosses budget %.2f", metric, measured, budget)
		}
	}

	writeBudget("cek_steps_per_sec", "trusted", 1_000_000, stepsPerSec, stepsPerSec >= 1_000_000)
	writeBudget("metering_tax_pct", "sandbox", 10, taxPct, taxPct <= 10)

	// Verify the rows landed.
	var n int
	e.conn.QueryRow(ctx, `SELECT count(*) FROM perf_budget WHERE epoch=1 AND milestone='M0'`, nil, &n)
	if n != 2 {
		t.Fatalf("expected 2 perf_budget rows, got %d", n)
	}
}
