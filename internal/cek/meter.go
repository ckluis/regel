package cek

import "time"

// Meter is the monomorphized metering seam (ADR-04 §4). The step function is
// generic over this type parameter with exactly two instantiations —
// fuelMeter (sandbox) and governorMeter (trusted) — so run[fuelMeter] and
// run[governorMeter] are two compiled loops. The trusted loop pays no per-step
// billing branch beyond the 4096-counter check.
type Meter interface {
	// tick is called once per CEK transition. It returns false on breach.
	tick() bool
	// chargeAlloc bills shallow allocation bytes at allocating transitions. It
	// returns false when the allocation budget is exhausted. The governor is a
	// no-op that always returns true (unmetered stays un-billed, not unbounded).
	chargeAlloc(n int64) bool
	// breachClass names the durable condition to raise on breach.
	breachClass() string
	// restarts returns the named restarts for a breach of this meter.
	restarts() []Restart
}

// fuelMeter is the sandbox tier: step + shallow-allocation budgets, checked at
// each charge. Budgets are remaining counts (park when they cross zero).
type fuelMeter struct {
	steps int64
	alloc int64
}

func (m *fuelMeter) tick() bool {
	m.steps--
	return m.steps >= 0
}
func (m *fuelMeter) chargeAlloc(n int64) bool {
	m.alloc -= n
	return m.alloc >= 0
}
func (m *fuelMeter) breachClass() string { return "fuel.exhausted" }
func (m *fuelMeter) restarts() []Restart { return fuelRestarts() }

// governorMeter is the trusted tier: no billing; a transition counter checked
// every 4096 transitions against a generous step ceiling and a wall deadline.
type governorMeter struct {
	count    int64
	ceiling  int64
	deadline time.Time
}

func (m *governorMeter) tick() bool {
	m.count++
	if m.count&4095 == 0 {
		if m.count > m.ceiling || time.Now().After(m.deadline) {
			return false
		}
	}
	return true
}
func (m *governorMeter) chargeAlloc(int64) bool { return true }
func (m *governorMeter) breachClass() string    { return "runaway" }
func (m *governorMeter) restarts() []Restart    { return runawayRestarts() }

// Tier selects the meter instantiation for a run.
type Tier uint8

const (
	TierSandbox Tier = iota // fuelMeter
	TierTrusted             // governorMeter
)

// Default governor ceilings (ADR-01 §4 / STAGE-A-PLAN pin #7).
const (
	DefaultGovernorCeiling = 50_000_000
	DefaultGovernorWall    = 5 * time.Second
	DefaultFuelSteps       = 100_000
	DefaultFuelAllocBytes  = 10 * 1024 * 1024
)
