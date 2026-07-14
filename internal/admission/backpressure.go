package admission

import (
	"context"
	"math"
	"sync/atomic"
	"time"

	"regel.dev/regel/internal/pgwire"
)

// backpressure.go is the pre-BEGIN admission door (ADR-07 §3 R1-07 + ADR-12 §5):
// the two refusals that open NO transaction and write their durable refusal
// straight to gate_refusal. It wraps admitWithRetries with, in order:
//
//   1. the per-principal admission-fuel token bucket (ADR-12 §5) — checked
//      before BEGIN, charged after the attempt by the DEEPEST stage reached
//      (a parse-fail is cheap, a full typecheck+verify is expensive), so garbage
//      is cheap to reject and flooding is priced; exhaustion ⇒ outcome
//      "budget-exhausted" (ADMISSION_BUDGET) + retry_after{cause:"budget-refill"};
//   2. the admission-control semaphore (ADR-07 §3) — bounding concurrent
//      in-transaction typechecks; an admission that would exceed it ⇒ outcome
//      "busy" (ADMISSION_BUSY) + retry_after{cause:"admission-busy"}.
//
// Both refusals mint a durable refusal_id (finishRefusal) before returning, so a
// pre-BEGIN refusal is as retrievable by id as any in-transaction verdict.

// admitRetries counts serialization retries across all admissions — the ADR-07 §3
// R1-07 concurrent-admission benchmark reads its delta for the retry rate.
var admitRetries atomic.Int64

// AdmissionConcurrency bounds concurrent in-transaction typechecks (ADR-07 §3,
// sized from the N=32 benchmark). The binding constraint is not tsgo-in-txn
// latency (well within p95≤40ms/p99≤80ms even at higher concurrency) but the I4
// `name_pointer_history` GiST exclusion index, whose SSI predicate locking is
// page-coarse — so concurrent admissions false-conflict there regardless of
// target scope, and the serialization-retry rate crosses 5% above S=2. The
// benchmark therefore sizes the semaphore at 2 and sheds the excess as
// ADMISSION_BUSY backpressure rather than thrashing the conflict window with
// retries (RESIDUE: a finer-grained I4 predicate lock would raise this bound).
// A var so the benchmark can resize it.
var AdmissionConcurrency = 2

// admissionSem is the process-wide admission-control semaphore.
var admissionSem = newSemaphore(AdmissionConcurrency)

// setAdmissionConcurrency resizes the semaphore (benchmark/test hook).
func setAdmissionConcurrency(n int) {
	AdmissionConcurrency = n
	admissionSem = newSemaphore(n)
}

// busyBackoffMs is the fixed retry_after for an admission-busy refusal.
const busyBackoffMs = 25

// minCharge is the cheapest admission-fuel charge (a pre-parse refusal). The
// bucket must hold at least this to admit an attempt at all.
const minCharge = 1.0

// semaphore is a non-blocking counting semaphore: tryAcquire never waits, so an
// over-bound admission is shed as ADMISSION_BUSY rather than queued behind the
// conflict window.
type semaphore struct{ ch chan struct{} }

func newSemaphore(n int) *semaphore {
	if n < 1 {
		n = 1
	}
	return &semaphore{ch: make(chan struct{}, n)}
}

func (s *semaphore) tryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *semaphore) release() {
	select {
	case <-s.ch:
	default:
	}
}

// Admit is the admission door (ADR-07 §1). It applies the two pre-BEGIN
// backpressure gates, then runs the transactional pipeline, then charges the
// fuel bucket by the deepest stage reached. A non-nil error is an internal fault;
// every ordinary/backpressure refusal is a typed Verdict.
func Admit(ctx context.Context, conn *pgwire.Conn, patch Patch, auth Principal, im *Image) (Verdict, error) {
	// 1. per-principal admission-fuel bucket (pre-BEGIN).
	enough, retryMs, err := fuelCheck(ctx, conn, auth)
	if err != nil {
		return Verdict{}, err
	}
	if !enough {
		return preRefuse(ctx, conn, patch, auth, im, OutcomeBudgetExhausted, "ADMISSION_BUDGET",
			&RetryAfter{Millis: retryMs, Cause: "budget-refill"},
			"the principal's admission-fuel bucket is exhausted; the submission was refused before any transaction opened — retry after the bucket refills")
	}

	// 2. admission-control semaphore (pre-BEGIN).
	if !admissionSem.tryAcquire() {
		return preRefuse(ctx, conn, patch, auth, im, OutcomeBusy, "ADMISSION_BUSY",
			&RetryAfter{Millis: busyBackoffMs, Cause: "admission-busy"},
			"the gate is at its concurrent-admission bound; the submission was refused before any transaction opened — retry shortly")
	}
	defer admissionSem.release()

	v, err := admitWithRetries(ctx, conn, patch, auth, im)
	if err != nil {
		return v, err
	}
	// Charge the bucket by the deepest stage the attempt reached (best-effort:
	// a bookkeeping write must not fail an otherwise-decided admission).
	_ = fuelCharge(ctx, conn, auth, chargeForVerdict(v))
	return v, nil
}

// preRefuse builds a pre-BEGIN refusal Verdict, mints its durable refusal_id, and
// writes it straight to gate_refusal (finishRefusal opens no transaction because
// conn is not in one). It never touches the admission ledger (ADR-12 §5).
func preRefuse(ctx context.Context, conn *pgwire.Conn, patch Patch, auth Principal, im *Image, outcome, code string, ra *RetryAfter, msg string) (Verdict, error) {
	v := Verdict{
		Outcome:      outcome,
		Hashes:       map[string]string{},
		Stages:       []Stage{},
		Epoch:        im.Epoch,
		BaseSnapshot: time.Now().UTC().Format(time.RFC3339Nano),
		RetryAfter:   ra,
		Diagnostics: []Diagnostic{{
			StageOrVerifier: "admission-control", Code: code, Severity: "error",
			Message: msg,
			Fix:     "resubmit after the retry_after backoff",
		}},
		Delta:   Delta{},
		Seeders: []Seeder{},
	}
	if err := finishRefusal(ctx, conn, auth, patch, &v); err != nil {
		return Verdict{}, err
	}
	return v, nil
}

// --- admission-fuel bucket ---------------------------------------------------

// fuelCheck provisions (if needed), refills by elapsed time, and reads the
// principal's bucket. It reports whether at least minCharge tokens remain and, if
// not, the retry_after millis until the bucket refills to minCharge. An unmetered
// kind (no capacity row) fails open (enough=true).
func fuelCheck(ctx context.Context, conn *pgwire.Conn, auth Principal) (enough bool, retryMs uint32, err error) {
	subject := auth.Subject()
	// Provision the bucket from the agent-kind capacity (full on first sight).
	if _, err = conn.Exec(ctx, `
INSERT INTO admission_fuel (principal, capacity, tokens, refill_per_sec)
SELECT $1, c.capacity, c.capacity, c.refill_per_sec
  FROM admission_capacity c WHERE c.agent_kind = $2
ON CONFLICT (principal) DO NOTHING`, subject, auth.ActorKind); err != nil {
		return false, 0, err
	}
	// Refill by elapsed wall time, then read.
	var tokens, refill float64
	found, err := conn.QueryRow(ctx, `
UPDATE admission_fuel
   SET tokens = LEAST(capacity, tokens + refill_per_sec * EXTRACT(EPOCH FROM (now() - updated_at))),
       updated_at = now()
 WHERE principal = $1
RETURNING tokens, refill_per_sec`, []any{subject}, &tokens, &refill)
	if err != nil {
		return false, 0, err
	}
	if !found {
		return true, 0, nil // unmetered kind: fail open
	}
	if tokens >= minCharge {
		return true, 0, nil
	}
	if refill <= 0 {
		return false, 60000, nil // no refill configured: long backoff
	}
	ms := math.Ceil((minCharge-tokens)/refill*1000.0)
	if ms < 1 {
		ms = 1
	}
	return false, uint32(ms), nil
}

// fuelCharge debits the bucket by cost (floored at 0). Best-effort.
func fuelCharge(ctx context.Context, conn *pgwire.Conn, auth Principal, cost float64) error {
	if cost <= 0 {
		return nil
	}
	_, err := conn.Exec(ctx,
		`UPDATE admission_fuel SET tokens = GREATEST(0, tokens - $2) WHERE principal = $1`,
		auth.Subject(), cost)
	return err
}

// chargeForVerdict prices an attempt by the DEEPEST pipeline stage it reached
// (ADR-12 §5: parse-fail cheap, full typecheck+verify expensive). The deepest
// stage is the last one recorded in the Verdict timeline.
func chargeForVerdict(v Verdict) float64 {
	stage := ""
	if n := len(v.Stages); n > 0 {
		stage = v.Stages[n-1].Stage
	}
	return stageCost(stage)
}

// stageCost maps a pipeline stage name to its admission-fuel cost. The ordering
// mirrors the pipeline's cheapest-first stage order (ADR-07 §1).
func stageCost(stage string) float64 {
	switch stage {
	case "", "typecheck-budget", "lower", "seeders":
		return 1 // pre-typecheck: a cheap syntactic/scope refusal
	case "insert":
		return 2
	case "tsgo":
		return 3 // ran the expensive checker
	default:
		// derive / V1..V6 / migrate / cas / already-admitted: the full pipeline.
		return 5
	}
}
