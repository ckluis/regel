package kernel

import (
	"context"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
)

// TestWakeStorm10k is ADR-05 Red-Path Test 9 at full scale: 10,000 sleeping
// timers, all due, drained by three reactor instances (distinct kernel ids, one
// shared DB). Every workflow completes exactly once — 10000 done, exactly 10000
// outbox rows (zero dupes, enforced by the UNIQUE dedup key) — inside the ≤5%
// serialization-abort budget (ADR-05 §7). Guarded by -short.
//
// Explicit invocation:
//
//	go test ./internal/kernel/ -run TestWakeStorm10k -count=1 -timeout 600s -v
func TestWakeStorm10k(t *testing.T) {
	if testing.Short() {
		t.Skip("10k wake storm is a heavy integration test")
	}
	e := newReactorEnv(t)
	ctx := context.Background()

	// mail.send records an outbox row without scanning the continuation table, so
	// the storm measures exactly-once under contention without the pathological SSI
	// conflict of thousands of channel sends against a receiver-less channel.
	e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('engineer:dev','mail.send','','test')`)
	src := `import { sleep } from "std/wf";
import { send } from "std/mail";
export function w(): number { sleep(1); send("a@b.c", "hi"); return 1; }`
	v := e.admitDecl(t, src, "app/storm10k", []string{"mail.send"})
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/storm10k/w"]

	// One post-sleep parked state, then a single set-based insert of N due timers.
	seedID := e.start(t, hash, nil, map[string]any{"subject": "op", "operator": true})
	if out, _ := e.stepOnce(t, seedID); out.Kind != cek.OutParked {
		t.Fatalf("seed park kind=%d", out.Kind)
	}
	const N = 10000
	c := e.conn(t)
	var framesHex string
	if _, err := c.QueryRow(ctx, `SELECT encode(frames,'hex') FROM continuation WHERE id=$1`, []any{seedID}, &framesHex); err != nil {
		e.pool.Release(c)
		t.Fatalf("read seed frames: %v", err)
	}
	// Cancel the seed so it does not also complete (keeps the count clean).
	if _, err := c.Exec(ctx, `UPDATE continuation SET status='cancelled' WHERE id=$1`, seedID); err != nil {
		e.pool.Release(c)
		t.Fatalf("cancel seed: %v", err)
	}
	if _, err := c.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
SELECT gen_random_uuid(),'workflow',$1,1,1,('\x'||$2)::bytea,
  jsonb_build_object('kind','timer','due','2000-01-01T00:00:00.000000Z'),'sleeping','{"subject":"op","operator":true}',1
FROM generate_series(1,$3)`, hash, framesHex, N); err != nil {
		e.pool.Release(c)
		t.Fatalf("bulk insert %d timers: %v", N, err)
	}
	e.pool.Release(c)

	before := cfr.MetricsSnapshot()
	start := time.Now()

	// Three reactors, distinct kernel ids, one shared DB.
	reactors := make([]*Reactor, 0, 3)
	for i := 0; i < 3; i++ {
		srv, err := New(ctx, e.pool)
		if err != nil {
			t.Fatalf("New reactor srv %d: %v", i, err)
		}
		reactors = append(reactors, srv.StartReactor(ctx, ReactorConfig{
			PollInterval: 20 * time.Millisecond, DrainBatch: 128, TimerBatch: 1024,
		}))
	}
	defer func() {
		for _, r := range reactors {
			r.Stop()
		}
	}()

	deadline := time.Now().Add(300 * time.Second)
	for time.Now().Before(deadline) {
		if e.intScalar(t, `SELECT count(*) FROM continuation WHERE status='done'`) >= N {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	elapsed := time.Since(start)

	done := e.intScalar(t, `SELECT count(*) FROM continuation WHERE status='done'`)
	if done != N {
		t.Fatalf("done = %d, want %d (elapsed %s)", done, N, elapsed)
	}
	outboxRows := e.intScalar(t, `SELECT count(*) FROM outbox`)
	if outboxRows != N {
		t.Fatalf("outbox rows = %d, want %d (exactly-once)", outboxRows, N)
	}
	// Zero duplicate effects (the UNIQUE key would already have aborted any, but
	// prove it as data too).
	dupes := e.intScalar(t, `
SELECT count(*) FROM (
  SELECT continuation_id, step_seq, ordinal FROM outbox
  GROUP BY continuation_id, step_seq, ordinal HAVING count(*) > 1) d`)
	if dupes != 0 {
		t.Fatalf("duplicate outbox keys = %d, want 0", dupes)
	}

	after := cfr.MetricsSnapshot()
	aborts := after.SerializationAborts - before.SerializationAborts
	attempts := int64(N) + aborts // lower bound on step attempts
	rate := float64(aborts) / float64(attempts)
	reoffers := after.Reoffers - before.Reoffers
	t.Logf("WAKE STORM 10k: %d workflows done, outbox=%d (0 dupes), elapsed=%s, aborts=%d, abort_rate=%.4f (<=0.05 budget), reoffers=%d",
		N, outboxRows, elapsed, aborts, rate, reoffers)
	if rate > 0.05 {
		t.Fatalf("abort rate %.4f exceeds the 5%% budget", rate)
	}
}
