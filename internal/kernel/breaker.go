package kernel

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// breakerState is the reap-rate breaker's closed/open/half-open state (ADR-13 §5).
type breakerState int32

const (
	breakerClosed   breakerState = iota // re-offers flow at full batch
	breakerOpen                         // re-offers paused (tripped), cooling down
	breakerHalfOpen                     // one probe batch permitted
)

func (s breakerState) String() string {
	switch s {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// reaperBreaker is the sliding-window trip/half-open state machine that paces the
// reaper (ADR-13 §5 / ADR-06 §5). Over a sliding window it tracks the re-offer
// rate and the re-expiry ratio (re-offered work whose fresh lease also expires
// uncommitted — the signature of a fleet that cannot keep up); either exceeding
// its threshold OPENS the breaker, pausing re-offers. After a cooldown it goes
// HALF-OPEN and permits one probe batch: a probe that drains CLOSES it, a probe
// that keeps re-expiring re-OPENS it. Correctness never depends on the breaker
// (ADR-05 §7: the lease is liveness-only), so an open breaker only delays
// recovery — visibly, via reaper.lag_ms — never corrupts.
type reaperBreaker struct {
	mu sync.Mutex

	window      time.Duration
	cooldown    time.Duration
	rateMax     int     // max re-offers per window before tripping
	reexpiryMax float64 // max re-expiry ratio before tripping (default 0.5)
	probeBatch  int     // probe batch size in half-open (default 10)
	fullBatch   int     // normal reap batch

	now func() time.Time // injectable clock (tests)

	state    breakerState
	reoffers []time.Time // re-offer event times within the window
	openedAt time.Time

	// signals (ADR-13 §5) — atomics so healthz can read them lock-free.
	stateGauge atomic.Int64 // 0 closed / 1 open (or half-open)
	trips      atomic.Int64
	lagMS      atomic.Int64
}

func newReaperBreaker(cfg ReactorConfig) *reaperBreaker {
	window := cfg.BreakerWindow
	if window <= 0 {
		window = 60 * time.Second
	}
	cooldown := cfg.BreakerCooldown
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	rateMax := cfg.ReapRateMax
	if rateMax <= 0 {
		rateMax = 1000 // generous default: production reaps are bounded batches
	}
	probe := cfg.ProbeBatch
	if probe <= 0 {
		probe = 10
	}
	return &reaperBreaker{
		window:      window,
		cooldown:    cooldown,
		rateMax:     rateMax,
		reexpiryMax: 0.5,
		probeBatch:  probe,
		fullBatch:   cfg.ReapBatch,
		now:         time.Now,
	}
}

// allowedBatch returns how many rows the current pass may re-offer (0 ⇒ paused),
// advancing OPEN→HALF-OPEN when the cooldown elapses.
func (b *reaperBreaker) allowedBatch() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerOpen:
		if b.now().Sub(b.openedAt) >= b.cooldown {
			b.state = breakerHalfOpen
			return b.probeBatch
		}
		return 0
	case breakerHalfOpen:
		return b.probeBatch
	default:
		return b.fullBatch
	}
}

// observe records the outcome of one reap pass: reoffered rows this pass,
// reexpired (re-offered rows that had already been leased before — attempts>1),
// and the oldest expired-lease lag in ms. It runs the trip/close transitions.
func (b *reaperBreaker) observe(reoffered, reexpired int, lagMS int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lagMS.Store(lagMS)
	t := b.now()
	// Slide the window.
	cut := t.Add(-b.window)
	pruned := b.reoffers[:0]
	for _, ts := range b.reoffers {
		if ts.After(cut) {
			pruned = append(pruned, ts)
		}
	}
	b.reoffers = pruned
	for i := 0; i < reoffered; i++ {
		b.reoffers = append(b.reoffers, t)
	}
	ratio := 0.0
	if reoffered > 0 {
		ratio = float64(reexpired) / float64(reoffered)
	}
	overRate := len(b.reoffers) > b.rateMax
	overRatio := ratio > b.reexpiryMax

	switch b.state {
	case breakerClosed:
		if overRate || overRatio {
			b.trip(t, len(b.reoffers), ratio, lagMS)
		}
	case breakerHalfOpen:
		// The probe just ran (reoffered this pass). Drained ⇒ close; still hot ⇒ re-open.
		if reoffered == 0 || (!overRatio && reoffered < b.probeBatch) {
			b.state = breakerClosed
			b.stateGauge.Store(0)
			b.reoffers = b.reoffers[:0]
			b.emit("reaper.breaker_closed", len(b.reoffers), ratio, lagMS)
		} else {
			b.trip(t, len(b.reoffers), ratio, lagMS)
		}
	case breakerOpen:
		// Still cooling — no transition on observe (allowedBatch drives OPEN→HALF).
	}
}

// trip opens the breaker (caller holds b.mu).
func (b *reaperBreaker) trip(t time.Time, windowReoffers int, ratio float64, lagMS int64) {
	b.state = breakerOpen
	b.openedAt = t
	b.stateGauge.Store(1)
	b.trips.Add(1)
	b.emit("reaper.breaker_tripped", windowReoffers, ratio, lagMS)
}

// emit writes the structured breaker event (ADR-13 §5) on the §4 stdout path.
func (b *reaperBreaker) emit(event string, windowReoffers int, ratio float64, lagMS int64) {
	ev := map[string]any{
		"event":           event,
		"window_reoffers": windowReoffers,
		"reexpiry_ratio":  ratio,
		"oldest_lag_ms":   lagMS,
		"breaker_state":   b.state.String(),
		"ts":              b.now().UTC().Format(time.RFC3339Nano),
	}
	body, _ := json.Marshal(ev)
	fmt.Fprintln(os.Stdout, string(body))
}

// snapshot exposes the breaker's signals (healthz + tests).
type breakerSnapshot struct {
	State string `json:"reaper_breaker_state"`
	Trips int64  `json:"reaper_breaker_trips_total"`
	LagMS int64  `json:"reaper_lag_ms"`
}

func (b *reaperBreaker) snapshot() breakerSnapshot {
	b.mu.Lock()
	st := b.state.String()
	b.mu.Unlock()
	return breakerSnapshot{State: st, Trips: b.trips.Load(), LagMS: b.lagMS.Load()}
}
