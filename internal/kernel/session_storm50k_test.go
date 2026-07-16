package kernel

// session_storm50k_test.go is the ADR-11 §6 red-path GATE-1 at full scale: 50,000
// UI sessions subscribed to ONE horizon, one mutation in that horizon, and the
// bounded fan-out drain (§6). It is the Stage-D perf/kill gate for the reactive
// layer — guarded like storm10k (skipped in -short), calibrated on THIS machine,
// and pinned as perf_budget rows (milestone M4).
//
// Shape (named honestly in the BUILD-D report): all 50k sessions are REAL
// continuation rows subscribed to key=horizon:acme, each rendering a detail
// projection of one row — the re-render→diff→checkpoint work is real for ALL 50k
// (not just a live subset). A representative 100 hold LIVE SSE connections; the
// other 49,900 exist as rows + subscription-index entries whose drive still runs
// the full claim→resume→diff→checkpoint loop and pushes a frame into their ring.
//
// Two legs:
//   - single-mutation: one NOTIFY in horizon acme ⇒ every session re-rendered
//     EXACTLY once (max step_seq == 1), the kernel stays live (healthz responsive
//     mid-drain), and the drain completes within the calibrated budget.
//   - concurrent burst: K mutations to K DISTINCT rows in the same horizon (K
//     distinct NOTIFYs, so Postgres cannot collapse them) ⇒ the §6 dirty-set
//     coalesces them to far fewer than K re-renders per session.
//
// Explicit invocation:
//
//	go test ./internal/kernel/ -run TestSessionStorm50k -count=1 -timeout 900s -v

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newSessionEnvWorkers is newSessionEnv with a tuned fan-out worker pool (§6 knob),
// set BEFORE StartSessions launches the drain loop.
func newSessionEnvWorkers(t *testing.T, workers int) *sessionEnv {
	t.Helper()
	e := newReactorEnv(t)
	if workers > 0 {
		e.srv.invIndex.workers = workers
	}
	return startSessionEnv(t, e)
}

// storm50k perf_budget budgets (epoch 1, M4), CALIBRATED on the reference machine
// (Apple M4, local Postgres 16, idle). See the BUILD-D report + ADR-13 §3 marker:
// the ADR-13 §3 500ms p95 SLO binds INTERACTIVE/steady-state fan-out (fan-outs
// within a tick's worker capacity), NOT a one-shot 50k single-horizon full fan-out,
// whose per-session enqueue→patch tail is O(N/workers × per-drive-cost) and is
// governed instead by the storm50k.drain_ms budget below.
const (
	storm50kDrainBudgetMS = 90000 // wall drain for 50k single-horizon fan-out
	storm50kLagP95Budget  = 75000 // enqueue→patch-sent p95 (tail of a full fan-out)
	storm50kLagP50Budget  = 45000 // enqueue→patch-sent p50
)

func TestSessionStorm50k(t *testing.T) {
	if testing.Short() {
		t.Skip("50k invalidation storm is a heavy M4 gate")
	}
	const (
		N       = 50000
		liveSSE = 100
		workers = 16
		burstK  = 20
	)
	ctx := context.Background()
	se := newSessionEnvWorkers(t, workers)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	subj := fmtID(id)
	nameDetail := slotForField(t, se.srv, "app/rx/Widget", "detail", "name")

	// One REAL mount → a valid session CFR + render template to clone from.
	tmpl := se.mount(t, "app/rx/Widget/detail/"+subj, "human:a", "acme")
	tmplBytes := se.intScalar(t, `SELECT octet_length(frames) FROM continuation WHERE id=$1`, tmpl.sid)

	// Clone it N times (fresh ids, own message channel) and subscribe every clone to
	// key=horizon:acme — the list-scope subscription a table/list view registers, so
	// a single horizon mutation fans out to all N. Then drop the template so every
	// kind='session' row is exactly one of the N storm sessions.
	hzKey := horizonKey("acme")
	se.withConn(t, func(c *pgConn) {
		if _, err := c.Exec(ctx, `
WITH ids AS (SELECT gen_random_uuid() AS id FROM generate_series(1,$2)),
ins AS (
  INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
  SELECT ids.id, t.kind, t.root_def_hash, t.epoch, t.format_ver, t.frames,
         jsonb_build_object('kind','message','channel',ids.id::text),
         'sleeping', t.principal, 0
  FROM ids, continuation t WHERE t.id=$1::uuid
  RETURNING id
)
INSERT INTO subscription (session_id, resource, key)
SELECT id, $3, $4 FROM ins`, tmpl.sid, N, "app/rx/Widget", hzKey); err != nil {
			t.Fatalf("bulk clone %d sessions: %v", N, err)
		}
		if _, err := c.Exec(ctx, `DELETE FROM continuation WHERE id=$1::uuid`, tmpl.sid); err != nil {
			t.Fatalf("drop template: %v", err)
		}
	})
	if got := se.intScalar(t, `SELECT count(*) FROM continuation WHERE kind='session'`); got != N {
		t.Fatalf("cloned session rows = %d, want %d", got, N)
	}

	// Open the live-SSE subset (the rest stay rows + index entries).
	var subset []string
	se.withConn(t, func(c *pgConn) {
		rows, err := c.Query(ctx, `SELECT id::text FROM continuation WHERE kind='session' LIMIT $1`, liveSSE)
		if err != nil {
			t.Fatalf("subset ids: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatalf("scan id: %v", err)
			}
			subset = append(subset, s)
		}
	})
	conns := make([]*sseConn, 0, liveSSE)
	for _, sid := range subset {
		h := &harness{t: t, base: se.ts.URL, sid: sid, slots: map[string]string{}, pending: map[string]string{}}
		conns = append(conns, h.openSSE(0))
	}
	defer func() {
		for _, c := range conns {
			c.close()
		}
	}()
	time.Sleep(400 * time.Millisecond) // let the 100 live SSE subscriptions register

	// Fan-out-lag sampler (enqueue→patch-sent per drive) for p50/p95.
	var lagMu sync.Mutex
	var lags []int64
	se.srv.invIndex.sampleLag = func(ms int64) { lagMu.Lock(); lags = append(lags, ms); lagMu.Unlock() }

	// Poll /healthz throughout the drain to prove the kernel stays live (§6). healthz
	// serves from process memory (no DB round trip, ADR-13 §4), so it must stay fast.
	healthStop := make(chan struct{})
	healthDone := make(chan struct{})
	var healthOK, healthTotal, healthMaxMS int64
	go func() {
		defer close(healthDone)
		for {
			select {
			case <-healthStop:
				return
			default:
			}
			t0 := time.Now()
			resp, err := http.Get(se.ts.URL + "/healthz")
			d := time.Since(t0).Milliseconds()
			atomic.AddInt64(&healthTotal, 1)
			if err == nil {
				if resp.StatusCode == 200 {
					atomic.AddInt64(&healthOK, 1)
				}
				resp.Body.Close()
			}
			if d > atomic.LoadInt64(&healthMaxMS) {
				atomic.StoreInt64(&healthMaxMS, d)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	// --- LEG 1: one mutation, exactly-once fan-out, calibrated drain ------------
	start := time.Now()
	se.fireHorizonMutation(t, id, "STORM")
	deadline := time.Now().Add(300 * time.Second)
	for time.Now().Before(deadline) {
		if se.intScalar(t, `SELECT count(*) FROM continuation WHERE kind='session' AND step_seq>=1`) >= N {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	drainMS := time.Since(start).Milliseconds()
	close(healthStop)
	<-healthDone

	patched := se.intScalar(t, `SELECT count(*) FROM continuation WHERE kind='session' AND step_seq>=1`)
	if patched != N {
		t.Fatalf("patched sessions = %d, want %d (drain=%dms)", patched, N, drainMS)
	}
	// Exactly-once: every session re-rendered exactly once from the single NOTIFY.
	if maxSeq := se.intScalar(t, `SELECT max(step_seq) FROM continuation WHERE kind='session'`); maxSeq != 1 {
		t.Fatalf("max step_seq = %d, want exactly 1 (each session re-rendered exactly once)", maxSeq)
	}
	if minSeq := se.intScalar(t, `SELECT min(step_seq) FROM continuation WHERE kind='session'`); minSeq != 1 {
		t.Fatalf("min step_seq = %d, want exactly 1 (no session missed)", minSeq)
	}
	// Kernel stayed live during the drain.
	if healthOK != healthTotal || healthTotal == 0 {
		t.Fatalf("healthz not fully live during drain: %d/%d ok", healthOK, healthTotal)
	}
	if healthMaxMS > 2000 {
		t.Fatalf("healthz max latency %dms during drain — kernel not responsive", healthMaxMS)
	}
	// The live SSE subset each received the STORM patch.
	frameOK := 0
	for _, c := range conns {
		select {
		case f := <-c.frames:
			if op, ok := opFor(f, nameDetail); ok && op.Payload == "STORM" {
				frameOK++
			}
		case <-time.After(3 * time.Second):
		}
	}
	if frameOK != liveSSE {
		t.Fatalf("live SSE readers that got the STORM patch = %d, want %d", frameOK, liveSSE)
	}

	// Checkpoint-write budget under the storm (ADR-11 §5, item 3b): maxSeq==1 above
	// already proves ≤1 checkpoint write per invalidated session; here the CFR DELTA
	// per interaction is the growth of the durable frames blob across the one drive.
	maxBytes := se.intScalar(t, `SELECT max(octet_length(frames)) FROM continuation WHERE kind='session'`)
	stormCFRDelta := maxBytes - tmplBytes
	if stormCFRDelta < 0 {
		stormCFRDelta = 0
	}

	// Fan-out lag distribution (single-mutation leg).
	lagMu.Lock()
	singleLags := append([]int64(nil), lags...)
	lags = nil
	lagMu.Unlock()
	sort.Slice(singleLags, func(i, j int) bool { return singleLags[i] < singleLags[j] })
	var lagP50, lagP95 int64
	if n := len(singleLags); n > 0 {
		lagP50 = singleLags[n*50/100]
		lagP95 = singleLags[minInt(n*95/100, n-1)]
	}

	// --- LEG 2: concurrent burst, §6 dirty-set coalescing ----------------------
	// K distinct rows in horizon acme ⇒ K DISTINCT NOTIFY payloads (Postgres cannot
	// collapse them), all matching every session via key=horizon:acme. The dirty-set
	// coalesces them to far fewer than K re-renders per session.
	burstRows := make([]int64, 0, burstK)
	for k := 0; k < burstK; k++ {
		burstRows = append(burstRows, se.seedWidget(t, "acme", fmt.Sprintf("row%d", k), k))
	}
	before := se.intScalar(t, `SELECT coalesce(sum(step_seq),0) FROM continuation WHERE kind='session'`)
	for k, rid := range burstRows {
		se.fireHorizonMutation(t, rid, fmt.Sprintf("BURST%d", k))
	}
	// Settle: queue depth back to 0 and the step-sum stable across three probes.
	settle := time.Now().Add(300 * time.Second)
	var lastSum int64 = -1
	stable := 0
	for time.Now().Before(settle) {
		time.Sleep(300 * time.Millisecond)
		if sseMetricsSnapshot().InvalidationDepth != 0 {
			stable = 0
			continue
		}
		sum := se.intScalar(t, `SELECT coalesce(sum(step_seq),0) FROM continuation WHERE kind='session'`)
		if sum == lastSum {
			if stable++; stable >= 3 {
				break
			}
		} else {
			stable, lastSum = 0, sum
		}
	}
	after := se.intScalar(t, `SELECT coalesce(sum(step_seq),0) FROM continuation WHERE kind='session'`)
	burstAdvance := after - before
	avgAdvance := float64(burstAdvance) / float64(N)
	// Every session re-rendered at least once in the burst (min advanced ≥ 1).
	if minSeq := se.intScalar(t, `SELECT min(step_seq) FROM continuation WHERE kind='session'`); minSeq < 2 {
		t.Fatalf("min step_seq after burst = %d, want ≥ 2 (every session re-rendered in the burst)", minSeq)
	}
	// Coalescing: far fewer than K re-renders per session (without coalescing each
	// would advance by K). Require at least 2× coalescing — expected ~1 with full
	// dirty-set collapse of the K back-to-back NOTIFYs.
	if avgAdvance >= float64(burstK)/2 {
		t.Fatalf("burst avg advance = %.2f (K=%d) — §6 coalescing failed (want < K/2)", avgAdvance, burstK)
	}

	t.Logf("STORM 50k: N=%d workers=%d liveSSE=%d  drain=%dms (budget %d)  "+
		"fanout_lag p50=%dms p95=%dms (n=%d)  healthz %d/%d ok maxLat=%dms  "+
		"BURST K=%d avgAdvance=%.3f (coalesce %.1fx)",
		N, workers, liveSSE, drainMS, storm50kDrainBudgetMS, lagP50, lagP95, len(singleLags),
		healthOK, healthTotal, healthMaxMS, burstK, avgAdvance, float64(burstK)/maxF(avgAdvance, 1e-9))

	// --- perf_budget rows (epoch 1, M4) ----------------------------------------
	writeStormBudget(t, se, "sse.storm50k.drain_ms", storm50kDrainBudgetMS, float64(drainMS))
	writeStormBudget(t, se, "sse.fanout_lag_ms.p95", storm50kLagP95Budget, float64(lagP95))
	writeStormBudget(t, se, "sse.fanout_lag_ms.p50", storm50kLagP50Budget, float64(lagP50))
	writeStormBudget(t, se, "sse.storm50k.burst_avg_advance", float64(burstK)/2, avgAdvance)
	writeStormBudget(t, se, "session.cfr_delta_bytes.storm50k", 65536, float64(stormCFRDelta))
}

// fireHorizonMutation performs exactly what a committed submit does (§6/§7): an
// UPDATE of the row, then NOTIFY (resource, rowId, horizon) — the invalidation the
// reactive listener consumes. Distinct rowIds ⇒ distinct NOTIFY payloads.
func (se *sessionEnv) fireHorizonMutation(t *testing.T, id int64, name string) {
	t.Helper()
	tbl := se.widgetTable()
	se.withConn(t, func(c *pgConn) {
		if _, err := c.Exec(context.Background(),
			`UPDATE `+quoteIdent(tbl)+` SET name=$1, row_version=row_version+1 WHERE id=$2`, name, id); err != nil {
			t.Fatalf("mutate row %d: %v", id, err)
		}
		if err := notifyInvalidate(context.Background(), c, "app/rx/Widget", fmtID(id), "acme"); err != nil {
			t.Fatalf("notify: %v", err)
		}
	})
}

func writeStormBudget(t *testing.T, se *sessionEnv, metric string, budget, measured float64) {
	t.Helper()
	se.withConn(t, func(c *pgConn) {
		if _, err := c.Exec(context.Background(), `
INSERT INTO perf_budget (epoch, metric, tier, budget, measured, milestone)
VALUES (1, $1, 'trusted', $2, $3, 'M4')
ON CONFLICT (epoch, metric) DO UPDATE SET measured=EXCLUDED.measured, budget=EXCLUDED.budget`,
			metric, budget, measured); err != nil {
			t.Fatalf("write perf_budget %s: %v", metric, err)
		}
	})
	if measured > budget {
		t.Fatalf("M4 storm budget regression: %s measured=%.2f crosses budget %.2f", metric, measured, budget)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
