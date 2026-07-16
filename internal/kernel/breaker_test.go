package kernel

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestReaperBreakerStateMachine (RED-path 6, ADR-13 §5): a deterministic drive of
// the sliding-window trip / half-open / close state machine under an injected
// clock — no DB. Correctness never depends on the breaker; this proves its own
// transitions.
func TestReaperBreakerStateMachine(t *testing.T) {
	var nowNS atomic.Int64
	nowNS.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, nowNS.Load()) }
	advance := func(d time.Duration) { nowNS.Add(int64(d)) }

	b := newReaperBreaker(ReactorConfig{
		ReapRateMax:     5,
		BreakerWindow:   60 * time.Second,
		BreakerCooldown: 30 * time.Second,
		ProbeBatch:      10,
		ReapBatch:       100,
	})
	b.now = clock

	// Closed: a full batch is permitted.
	if got := b.allowedBatch(); got != 100 {
		t.Fatalf("closed allowedBatch = %d, want 100", got)
	}
	if b.snapshot().State != "closed" {
		t.Fatalf("initial state = %s, want closed", b.snapshot().State)
	}

	// A reap pass over the rate threshold TRIPS the breaker OPEN.
	b.observe(20, 0, 1200)
	if s := b.snapshot(); s.State != "open" || s.Trips != 1 {
		t.Fatalf("after storm: state=%s trips=%d, want open/1", s.State, s.Trips)
	}
	if s := b.snapshot(); s.LagMS != 1200 {
		t.Fatalf("lag_ms = %d, want 1200", s.LagMS)
	}

	// While cooling, re-offers are PAUSED.
	if got := b.allowedBatch(); got != 0 {
		t.Fatalf("open (cooling) allowedBatch = %d, want 0", got)
	}
	advance(10 * time.Second)
	if got := b.allowedBatch(); got != 0 {
		t.Fatalf("open (mid-cooldown) allowedBatch = %d, want 0", got)
	}

	// After the cooldown, the breaker goes HALF-OPEN and permits one probe batch.
	advance(25 * time.Second) // total 35s > 30s cooldown
	if got := b.allowedBatch(); got != 10 {
		t.Fatalf("half-open allowedBatch = %d, want probe 10", got)
	}
	if b.snapshot().State != "half-open" {
		t.Fatalf("state = %s, want half-open", b.snapshot().State)
	}

	// A probe that DRAINS (0 re-offers) CLOSES the breaker.
	b.observe(0, 0, 0)
	if b.snapshot().State != "closed" {
		t.Fatalf("after drained probe: state = %s, want closed", b.snapshot().State)
	}
	if got := b.allowedBatch(); got != 100 {
		t.Fatalf("closed allowedBatch = %d, want 100", got)
	}

	// Re-trip, then a probe that stays HOT re-OPENS.
	b.observe(20, 0, 500)
	if b.snapshot().State != "open" {
		t.Fatalf("re-trip state = %s, want open", b.snapshot().State)
	}
	advance(31 * time.Second)
	_ = b.allowedBatch()  // → half-open
	b.observe(10, 8, 900) // probe still hot (full probe batch + high re-expiry)
	if s := b.snapshot(); s.State != "open" || s.Trips != 3 {
		t.Fatalf("hot probe: state=%s trips=%d, want open/3", s.State, s.Trips)
	}

	// The re-expiry ratio alone (> 50%) also trips from closed.
	b2 := newReaperBreaker(ReactorConfig{ReapRateMax: 1000, ProbeBatch: 10, ReapBatch: 100})
	b2.now = clock
	b2.observe(4, 3, 100) // rate under max, but 3/4 = 75% re-expiry
	if b2.snapshot().State != "open" {
		t.Fatalf("re-expiry trip: state = %s, want open", b2.snapshot().State)
	}
}

// TestReaperBreakerReapStorm (RED-path 6 integration): a real reap storm trips the
// breaker, re-offers pause during cooldown, and recovery drains + closes after the
// half-open probe — with no task lost (every seeded task eventually re-offered).
func TestReaperBreakerReapStorm(t *testing.T) {
	e := newReactorEnv(t)
	ctx := context.Background()
	contID := e.aContinuation(t, "app/brk")

	// A manual reactor with a tiny rate cap + fast cooldown so the storm trips it.
	r := e.newManualReactor(ReactorConfig{
		ReapBatch:       500,
		ReapRateMax:     5,
		BreakerWindow:   60 * time.Second,
		BreakerCooldown: 40 * time.Millisecond,
		ProbeBatch:      500,
	})

	// Seed 40 expired running resume tasks (a reap storm).
	seed := func(n int) {
		for i := 0; i < n; i++ {
			e.exec(t, `INSERT INTO task (id, kind, run_at, status, lease_owner, lease_until, payload)
			  VALUES (gen_random_uuid(),'resume',now(),'running',gen_random_uuid(),now()-interval '1 minute',
			    jsonb_build_object('continuation_id',$1::text,'step_seq',0))`, contID)
		}
	}
	seed(40)

	// Pass 1: re-offers the storm and TRIPS the breaker (40 > 5).
	if err := r.reaperOnce(ctx); err != nil {
		t.Fatalf("reaperOnce: %v", err)
	}
	if s := r.breaker.snapshot(); s.State != "open" || s.Trips < 1 {
		t.Fatalf("after storm: state=%s trips=%d, want open/>=1", s.State, s.Trips)
	}
	reofferedPass1 := e.intScalar(t, `SELECT count(*) FROM task WHERE status='ready' AND kind='resume'`)
	if reofferedPass1 == 0 {
		t.Fatalf("pass 1 re-offered nothing")
	}

	// Seed MORE expired work; during cooldown the breaker PAUSES re-offers.
	seed(20)
	before := e.intScalar(t, `SELECT count(*) FROM task WHERE status='running' AND lease_until<now()`)
	if err := r.reaperOnce(ctx); err != nil {
		t.Fatalf("reaperOnce(paused): %v", err)
	}
	after := e.intScalar(t, `SELECT count(*) FROM task WHERE status='running' AND lease_until<now()`)
	if after != before {
		t.Fatalf("breaker OPEN did not pause re-offers: expired-running %d -> %d", before, after)
	}
	if r.breaker.snapshot().State != "open" {
		t.Fatalf("state = %s during cooldown, want open", r.breaker.snapshot().State)
	}

	// After the cooldown, the half-open probe drains the remaining storm and CLOSES.
	time.Sleep(60 * time.Millisecond)
	if err := r.reaperOnce(ctx); err != nil { // probe (half-open) drains the rest
		t.Fatalf("reaperOnce(probe): %v", err)
	}
	// One more pass with no expired work closes the breaker.
	if err := r.reaperOnce(ctx); err != nil {
		t.Fatalf("reaperOnce(close): %v", err)
	}
	if s := r.breaker.snapshot(); s.State != "closed" {
		t.Fatalf("post-recovery state = %s, want closed", s.State)
	}
	// No task lost: nothing is still stuck running-expired.
	if stuck := e.intScalar(t, `SELECT count(*) FROM task WHERE status='running' AND lease_until<now()`); stuck != 0 {
		t.Fatalf("%d tasks stuck running-expired after recovery (work lost)", stuck)
	}
}
