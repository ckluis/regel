package kernel

import (
	"context"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
)

// cron_test.go — STAGE-E D10 red-path: the cron task kind is driven. An admitted
// workflow scheduled every ~500ms via std/taak.schedule fires repeatedly with
// exactly-once effect recording per fire, and the durable cron row survives a
// kernel (reactor) restart.
//
// RED evidence: with no cron driver loop in the reactor (its state before D10) the
// kind='cron' row is inserted but never claimed — zero tick workflows ever fire and
// the outbox log.write count stays 0, so the ">= 2 fires" assertion fails. The
// cronOnce loop is the control.
func TestCronDrivesRecurringWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("cron timing test")
	}
	e := newReactorEnv(t)

	// The target workflow: each fire records exactly one log.write external effect
	// (one outbox row keyed UNIQUE(continuation_id, step_seq, ordinal)).
	tick := `import { write } from "std/log";
export function tick(): number { write("cron-fired"); return 1; }`
	if v := e.admit(t, tick, "app/cron", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit tick: %q %+v", v.Outcome, v.Diagnostics)
	}

	// The scheduler workflow: registers a @every:500ms cron for app/cron/tick.
	sched := `import { schedule } from "std/taak";
export function sched(): number { schedule("@every:500", "app/cron/tick"); return 0; }`
	sv := e.admit(t, sched, "app/sched", nil)
	if sv.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit sched: %q %+v", sv.Outcome, sv.Diagnostics)
	}

	fires := func() int64 {
		return e.intScalar(t, `SELECT count(*) FROM outbox WHERE class='log.write'`)
	}
	distinctContinuations := func() int64 {
		return e.intScalar(t, `SELECT count(DISTINCT continuation_id) FROM outbox WHERE class='log.write'`)
	}

	// Kernel A: start the reactor and the scheduler; wait for >= 2 fires.
	rA := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 80 * time.Millisecond, LeaseSeconds: 5})
	e.start(t, sv.Hashes["app/sched/sched"], nil, map[string]any{"subject": "op", "operator": true})

	waitFor(t, func() bool {
		// one cron row exists (the durable schedule)
		return e.intScalar(t, `SELECT count(*) FROM task WHERE kind='cron'`) == 1 && fires() >= 2
	}, 8*time.Second, "cron to fire >= 2 times")
	firesA := fires()
	rA.Stop() // kernel A "down" — the in-memory reactor loops stop; the cron row persists

	// Exactly-once effect recording: each fire recorded its log.write exactly once —
	// the number of outbox rows equals the number of distinct firing continuations.
	if fires() != distinctContinuations() {
		t.Fatalf("effect recording not exactly-once: %d outbox rows across %d continuations",
			fires(), distinctContinuations())
	}

	// Kernel B: a FRESH reactor on the same DB resumes firing the durable cron row.
	rB := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 80 * time.Millisecond, LeaseSeconds: 5})
	defer rB.Stop()
	waitFor(t, func() bool { return fires() >= firesA+2 }, 8*time.Second, "cron to keep firing after restart")

	if fires() != distinctContinuations() {
		t.Fatalf("post-restart exactly-once broken: %d rows / %d continuations", fires(), distinctContinuations())
	}
	t.Logf("CRON VERIFIED: fired %d times across a reactor restart (>= %d before, >= %d after), exactly-once per fire",
		fires(), firesA, firesA+2)
}

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
