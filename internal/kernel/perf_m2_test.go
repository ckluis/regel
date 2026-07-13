package kernel

import (
	"context"
	"sort"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

// TestPerfBudgetM2 measures the Stage-B store/reactor budgets and writes them to
// perf_budget rows (epoch 1, milestone M2), the same discipline as the cek M0
// bench (R1-07: a crossed budget fails like a failed kill-test):
//
//   - continuation.resume_latency_ms_p95 — wake-due → claimed → done for a simple
//     workflow, p95 over 50 resumes; budget 5000ms (ADR-13).
//   - cfr.blob_bytes_p95 — the checkpointed CFR blob across every park of the
//     kill-9 demo workflow; budget 65536 bytes (ADR-04 §8).
//   - step.abort_rate — serialization aborts / step attempts over the run;
//     budget 0.05 (ADR-05 §7 BUILD-B).
func TestPerfBudgetM2(t *testing.T) {
	e := newReactorEnv(t)
	ctx := context.Background()

	// --- (a) resume latency p95 over 50 resumes ------------------------------
	src := `import { sleep } from "std/wf";
export function w(): number { sleep(1); return 1; }`
	v := e.admit(t, src, "app/perfm2", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/perfm2/w"]

	// Seed one post-sleep parked state, clone 50 sleeping rows all due now.
	seedID := e.start(t, hash, nil, map[string]any{"subject": "op", "operator": true})
	if out, _ := e.stepOnce(t, seedID); out.Kind != cek.OutParked {
		t.Fatalf("seed park kind=%d", out.Kind)
	}
	const R = 50
	e.withConn(t, func(c *pgwire.Conn) {
		var framesHex string
		if _, err := c.QueryRow(ctx, `SELECT encode(frames,'hex') FROM continuation WHERE id=$1`,
			[]any{seedID}, &framesHex); err != nil {
			t.Fatalf("read seed frames: %v", err)
		}
		if _, err := c.Exec(ctx, `UPDATE continuation SET status='cancelled' WHERE id=$1`, seedID); err != nil {
			t.Fatalf("cancel seed: %v", err)
		}
		// created_at = the wake-due instant (due is set to the same now()), so
		// done.updated_at - created_at IS the wake-due→claimed→done latency.
		if _, err := c.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
SELECT gen_random_uuid(),'workflow',$1,1,1,('\x'||$2)::bytea,
  jsonb_build_object('kind','timer','due',
    to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')),
  'sleeping','{"subject":"op","operator":true}',1
FROM generate_series(1,$3)`, hash, framesHex, R); err != nil {
			t.Fatalf("bulk insert %d timers: %v", R, err)
		}
	})

	before := cfr.MetricsSnapshot()
	r := e.srv.StartReactor(ctx, ReactorConfig{PollInterval: 20 * time.Millisecond})
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if e.intScalar(t, `SELECT count(*) FROM continuation WHERE status='done'`) >= R {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.Stop()
	if done := e.intScalar(t, `SELECT count(*) FROM continuation WHERE status='done'`); done != R {
		t.Fatalf("done = %d, want %d", done, R)
	}
	after := cfr.MetricsSnapshot()

	latencies := make([]float64, 0, R)
	e.withConn(t, func(c *pgwire.Conn) {
		rows, err := c.Query(ctx, `
SELECT EXTRACT(EPOCH FROM (updated_at - created_at)) * 1000.0
FROM continuation WHERE status='done'`)
		if err != nil {
			t.Fatalf("latency query: %v", err)
		}
		for rows.Next() {
			var ms float64
			if err := rows.Scan(&ms); err != nil {
				rows.Close()
				t.Fatalf("scan latency: %v", err)
			}
			latencies = append(latencies, ms)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("latency rows: %v", err)
		}
	})
	sort.Float64s(latencies)
	latencyP95 := latencies[(len(latencies)*95)/100]

	aborts := after.SerializationAborts - before.SerializationAborts
	attempts := int64(R) + aborts
	abortRate := float64(aborts) / float64(attempts)

	// --- (b) CFR blob size across every park of the kill-9 workflow ----------
	kv := e.admit(t, killWorkflow, "app/perfkill", nil)
	if kv.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit kill workflow: %q (%+v)", kv.Outcome, kv.Diagnostics)
	}
	kid := e.start(t, kv.Hashes["app/perfkill/w"], nil, map[string]any{"subject": "op", "operator": true})
	var blobSizes []float64
	for park := 0; park < 8; park++ { // 4 sleeps then done
		out, claimed := e.stepOnce(t, kid)
		if !claimed {
			t.Fatalf("park %d: claim lost", park)
		}
		if out.Kind == cek.OutDone {
			break
		}
		if out.Kind != cek.OutParked {
			t.Fatalf("park %d kind=%d", park, out.Kind)
		}
		n := e.intScalar(t, `SELECT octet_length(frames) FROM continuation WHERE id=$1`, kid)
		blobSizes = append(blobSizes, float64(n))
		e.exec(t, `UPDATE continuation SET status='ready' WHERE id=$1 AND status='sleeping'`, kid)
	}
	if len(blobSizes) != 4 {
		t.Fatalf("kill workflow parked %d times, want 4", len(blobSizes))
	}
	sort.Float64s(blobSizes)
	blobP95 := blobSizes[(len(blobSizes)*95)/100]

	t.Logf("M2 PERF: continuation.resume_latency_ms_p95=%.1f (budget 5000, n=%d)  "+
		"cfr.blob_bytes_p95=%.0f (budget 65536, parks=%d, sizes=%v)  "+
		"step.abort_rate=%.4f (budget 0.05, aborts=%d/%d attempts)",
		latencyP95, len(latencies), blobP95, len(blobSizes), blobSizes, abortRate, aborts, attempts)

	// --- perf_budget rows (epoch 1, milestone M2) -----------------------------
	writeBudget := func(metric, tier string, budget, measured float64, ok bool) {
		e.withConn(t, func(c *pgwire.Conn) {
			if _, err := c.Exec(ctx, `
INSERT INTO perf_budget (epoch, metric, tier, budget, measured, milestone)
VALUES (1, $1, $2, $3, $4, 'M2')
ON CONFLICT (epoch, metric) DO UPDATE SET measured=EXCLUDED.measured`,
				metric, tier, budget, measured); err != nil {
				t.Fatalf("write perf_budget %s: %v", metric, err)
			}
		})
		if !ok {
			t.Fatalf("M2 budget regression: %s measured=%.2f crosses budget %.2f", metric, measured, budget)
		}
	}
	writeBudget("continuation.resume_latency_ms_p95", "trusted", 5000, latencyP95, latencyP95 <= 5000)
	writeBudget("cfr.blob_bytes_p95", "trusted", 65536, blobP95, blobP95 <= 65536)
	writeBudget("step.abort_rate", "trusted", 0.05, abortRate, abortRate <= 0.05)

	if n := e.intScalar(t, `SELECT count(*) FROM perf_budget WHERE epoch=1 AND milestone='M2'`); n != 3 {
		t.Fatalf("perf_budget M2 rows = %d, want 3", n)
	}
}
