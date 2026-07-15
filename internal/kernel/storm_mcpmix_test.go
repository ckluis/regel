package kernel

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/mcp"
	"regel.dev/regel/internal/pgwire"
)

// TestWakeStormWithMCPMix is the STAGE-B §11 directive: at Stage C, re-run the wake
// storm CONCURRENTLY WITH the real Stage-C MCP transaction mix before trusting the 5%
// abort headroom. Three reactors drain N due timers while a pool of MCP-plane workers
// hammers the SAME PG through the real MCP door (ServeStdio, one gate) with:
//   - patch.submit {commit:true}  — the real admission gate transaction,
//   - patch.submit {commit:false} — the rolled-back dry-run,
//     both of which pass the pre-BEGIN S=2 admission semaphore (excess ⇒ ADMISSION_BUSY),
//   - resource.mutate — a derived-row write that does NOT take the semaphore, so it is
//     un-shed concurrent write pressure racing the reactor's step transactions.
//
// It measures, under the mix:
//   - exactly-once still holds: N done, exactly N outbox rows, ZERO duplicate keys;
//   - the reactor step-txn abort_rate stays within the ADR-05 §7 ≤5% budget — the
//     admission excess is shed as ADMISSION_BUSY rather than converted into reactor
//     serialization retries (the §11 claim: the semaphore sheds, it does not inflate);
//   - the admission-side busy-shed rate, reported SEPARATELY (it is not a reactor abort).
//
// NOT -short-guarded: this is the gate evidence STAGE-B §11 asks for, so it runs by
// default. N=1500 (vs the 10k storm) is justified: the contention regime is identical
// (SSI false-conflicts on name_pointer_history for the admission txns + the reactor's
// claim/step conflict window), and 1500 timers keep three reactors saturated for the
// whole admission-mix window while draining in a few seconds — inside the suite budget.
func TestWakeStormWithMCPMix(t *testing.T) {
	e := newReactorEnv(t)
	ctx := context.Background()

	// --- workflow under storm: post-sleep it records one outbox row via mail.send ---
	e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('engineer:dev','mail.send','','test')`)
	src := `import { sleep } from "std/wf";
import { send } from "std/mail";
export function w(): number { sleep(1); send("a@b.c", "hi"); return 1; }`
	v := e.admitDecl(t, src, "app/stormmix", []string{"mail.send"})
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit storm wf: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/stormmix/w"]

	// --- the MCP door + a metered-free agent principal + a derived resource ---------
	srv, err := mcp.New(ctx, e.pool)
	if err != nil {
		t.Fatalf("mcp.New: %v", err)
	}
	const mixKey = "k-storm-mix"
	const mixOrg = "mixorg"
	e.exec(t, `INSERT INTO agent_key (key_hash, actor_kind, actor_id, scope_kind, scope_id) VALUES ($1,'agent','amix',2,$2)`,
		mcp.HashKey(mixKey), mixOrg)
	seedMixResource(t, e, mixOrg)

	// --- seed N due timers (same mechanism as the 10k storm) ------------------------
	const N = 2500
	seedDueTimers(t, e, hash, N)

	before := cfr.MetricsSnapshot()
	start := time.Now()

	// --- three reactors, distinct kernel ids, one shared DB -------------------------
	reactors := make([]*Reactor, 0, 3)
	for i := 0; i < 3; i++ {
		rsrv, err := New(ctx, e.pool)
		if err != nil {
			t.Fatalf("New reactor srv %d: %v", i, err)
		}
		reactors = append(reactors, rsrv.StartReactor(ctx, ReactorConfig{
			PollInterval: 20 * time.Millisecond, DrainBatch: 128, TimerBatch: 1024,
		}))
	}
	defer func() {
		for _, r := range reactors {
			r.Stop()
		}
	}()

	// --- the MCP transaction mix, racing the reactor until the storm drains ---------
	// The mix opens REAL admission transactions (patch.submit) and derived-row writes
	// (resource.mutate) against the same PG the reactor steps on, and contends the
	// pre-BEGIN S=2 admission semaphore. Workers back off on ADMISSION_BUSY (respecting
	// retry_after, as a real client does).
	//
	// tsgo cost is deliberately BOUNDED: only worker 0 submits VALID (tsgo-running)
	// patches — a handful, spaced out — enough to prove the gate genuinely admits +
	// dry-runs alongside the storm, without a sustained compiler-CPU spike that would
	// perturb the sibling in-package tsgo benchmarks. The BULK of the load is cheap:
	// parse-failing patch.submit (a real gate transaction that fails pre-tsgo at the
	// grammar gate — this is what floods the S=2 semaphore into ADMISSION_BUSY) plus
	// resource.mutate row writes. The pre-existing 10k wake storm proves a PG-heavy
	// reactor is fine in parallel; the one thing that is NOT fine is unbounded
	// concurrent tsgo, so the mix keeps it bounded while still exercising every door.
	stop := make(chan struct{})
	var attempts, busy, admitted, dryRuns, mutations atomic.Int64
	var modCtr atomic.Int64
	var wg sync.WaitGroup
	const workers = 4
	for i := 0; i < workers; i++ {
		wIdx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				n := attempts.Add(1)
				gotBusy := false
				switch {
				case wIdx == 0 && n%6 == 0: // rare REAL green admission (tsgo)
					mod := fmt.Sprintf("app/mix/m%d", modCtr.Add(1))
					out := mixCall(srv, mixKey, `{"name":"patch.submit","arguments":`+
						`{"source":"export const c: number = 1;\n","module":"`+mod+`","scope":"org.`+mixOrg+`","commit":true}}`)
					if isBusy(out) {
						busy.Add(1)
						gotBusy = true
					} else if isAdmitted(out) {
						admitted.Add(1)
					}
					time.Sleep(20 * time.Millisecond) // space the tsgo runs out
				case wIdx == 0 && n%6 == 3: // rare REAL dry-run (tsgo, rolled back)
					out := mixCall(srv, mixKey, `{"name":"patch.submit","arguments":`+
						`{"source":"export const d: number = 2;\n","module":"app/mix/dry","scope":"org.`+mixOrg+`","commit":false}}`)
					if isBusy(out) {
						busy.Add(1)
						gotBusy = true
					} else if isAdmitted(out) {
						dryRuns.Add(1)
					}
					time.Sleep(20 * time.Millisecond)
				case n%2 == 0: // cheap gate txn: parse-fails pre-tsgo, floods S=2 ⇒ BUSY
					commit := "true"
					if n%4 == 0 {
						commit = "false" // both commit:true and commit:false dry-run
					}
					out := mixCall(srv, mixKey, `{"name":"patch.submit","arguments":`+
						`{"source":"export const bad = ;\n","module":"app/mix/bad","scope":"org.`+mixOrg+`","commit":`+commit+`}}`)
					if isBusy(out) {
						busy.Add(1)
						gotBusy = true
					}
				default: // resource.mutate — derived-row write, NO semaphore (un-shed)
					out := mixCall(srv, mixKey, `{"name":"resource.mutate","arguments":`+
						`{"resource":"app/mix/Contact","op":"insert","values":{"name":"n","email":"e@x.example"}}}`)
					if strings.Contains(out, `ok\":true`) {
						mutations.Add(1)
					}
				}
				if gotBusy {
					time.Sleep(3 * time.Millisecond) // retry_after backoff
				}
			}
		}()
	}

	// --- wait for the storm to drain ------------------------------------------------
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if e.intScalar(t, `SELECT count(*) FROM continuation WHERE status='done'`) >= N {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	elapsed := time.Since(start)
	close(stop)
	wg.Wait()

	// --- exactly-once under the mix -------------------------------------------------
	done := e.intScalar(t, `SELECT count(*) FROM continuation WHERE status='done'`)
	if done != N {
		t.Fatalf("done = %d, want %d (elapsed %s)", done, N, elapsed)
	}
	outboxRows := e.intScalar(t, `SELECT count(*) FROM outbox`)
	if outboxRows != N {
		t.Fatalf("outbox rows = %d, want %d (exactly-once)", outboxRows, N)
	}
	dupes := e.intScalar(t, `
SELECT count(*) FROM (
  SELECT continuation_id, step_seq, ordinal FROM outbox
  GROUP BY continuation_id, step_seq, ordinal HAVING count(*) > 1) d`)
	if dupes != 0 {
		t.Fatalf("duplicate outbox keys = %d, want 0 (exactly-once broke under the mix)", dupes)
	}

	// --- reactor abort budget (ADR-05 §7): the mix must not push it past 5% ---------
	after := cfr.MetricsSnapshot()
	aborts := after.SerializationAborts - before.SerializationAborts
	attemptsLB := int64(N) + aborts // lower bound on reactor step attempts
	abortRate := float64(aborts) / float64(attemptsLB)
	reoffers := after.Reoffers - before.Reoffers

	// --- admission busy-shed rate (reported SEPARATELY — not a reactor abort) -------
	totalAttempts := attempts.Load()
	semaphoreOps := admitted.Load() + dryRuns.Load() + busy.Load()
	busyShed := 0.0
	if semaphoreOps > 0 {
		busyShed = float64(busy.Load()) / float64(semaphoreOps)
	}

	t.Logf("STORM+MCP-MIX: N=%d done=%d outbox=%d (0 dupes), elapsed=%s | "+
		"reactor aborts=%d abort_rate=%.4f (<=0.05 budget) reoffers=%d | "+
		"MCP mix: attempts=%d admitted=%d dryRuns=%d mutations=%d BUSY-shed=%d busy_shed_rate=%.4f",
		N, done, outboxRows, elapsed, aborts, abortRate, reoffers,
		totalAttempts, admitted.Load(), dryRuns.Load(), mutations.Load(), busy.Load(), busyShed)

	if abortRate > 0.05 {
		t.Fatalf("reactor abort_rate %.4f exceeds the 5%% budget UNDER the MCP mix — the mix inflated it", abortRate)
	}
	// The mix must have actually exercised the semaphore (else the test proves nothing):
	// with 8 workers against S=2, the excess is shed as ADMISSION_BUSY.
	if busy.Load() == 0 {
		t.Fatalf("no ADMISSION_BUSY shed — the S=2 semaphore was never contended (mix did not race)")
	}
	if admitted.Load() == 0 {
		t.Fatalf("no admission committed under the mix — the gate never actually ran alongside the storm")
	}
	t.Logf("PASS storm+MCP-mix: exactly-once holds under the mix; reactor abort_rate %.4f within budget; "+
		"ADMISSION_BUSY shed %d of %d semaphore ops (%.1f%%) instead of inflating reactor retries",
		abortRate, busy.Load(), semaphoreOps, busyShed*100)
}

// The verdict JSON is embedded as a STRING value inside the tools/call result, so its
// quotes arrive escaped on the wire: isBusy/isAdmitted match the escaped tokens.
func isBusy(out string) bool     { return strings.Contains(out, `outcome\":\"busy`) }
func isAdmitted(out string) bool { return strings.Contains(out, `outcome\":\"admitted`) }

// mixCall drives one tools/call through the REAL MCP door (ServeStdio, one line +
// EOF) and returns the raw response text for outcome classification.
func mixCall(srv *mcp.Server, key, callBody string) string {
	line := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + callBody + "}\n"
	var out bytes.Buffer
	_ = srv.ServeStdio(context.Background(), &mcp.Session{APIKey: key}, strings.NewReader(line), &out)
	return out.String()
}

// seedMixResource admits a Contact resource at org.<mixOrg> as the mix agent, so the
// resource.mutate leg has a derived table (with a PII field) to write.
func seedMixResource(t *testing.T, e *reactorEnv, mixOrg string) {
	t.Helper()
	ctx := context.Background()
	src := `import { resource } from "std/resource";
export const Contact = resource({
  fields: { name: "text", email: "pii:email" },
});
`
	p := admission.Patch{
		Modules:     []admission.ModuleSrc{{ModuleName: "app/mix", Source: src}},
		TargetScope: admission.Scope{Kind: 2, ID: mixOrg},
		BaseHashes:  map[string]string{},
	}
	auth := admission.Principal{ActorKind: "agent", ActorID: "amix", Via: "mcp", Chain: catalog.Chain{OrgID: mixOrg}}
	e.withConn(t, func(c *pgwire.Conn) {
		v, err := admission.Admit(ctx, c, p, auth, admission.BuildImage())
		if err != nil || v.Outcome != admission.OutcomeAdmitted {
			t.Fatalf("seed mix resource: %v / %q %+v", err, v.Outcome, v.Diagnostics)
		}
	})
}

// seedDueTimers parks one continuation post-sleep, snapshots its frames, then bulk-
// inserts N sleeping timers all due in the past (the 10k-storm mechanism at N scale).
func seedDueTimers(t *testing.T, e *reactorEnv, hash string, n int) {
	t.Helper()
	ctx := context.Background()
	seedID := e.start(t, hash, nil, map[string]any{"subject": "op", "operator": true})
	if out, _ := e.stepOnce(t, seedID); out.Kind != cek.OutParked {
		t.Fatalf("seed park kind=%d", out.Kind)
	}
	c := e.conn(t)
	defer e.pool.Release(c)
	var framesHex string
	if _, err := c.QueryRow(ctx, `SELECT encode(frames,'hex') FROM continuation WHERE id=$1`, []any{seedID}, &framesHex); err != nil {
		t.Fatalf("read seed frames: %v", err)
	}
	if _, err := c.Exec(ctx, `UPDATE continuation SET status='cancelled' WHERE id=$1`, seedID); err != nil {
		t.Fatalf("cancel seed: %v", err)
	}
	if _, err := c.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
SELECT gen_random_uuid(),'workflow',$1,1,1,('\x'||$2)::bytea,
  jsonb_build_object('kind','timer','due','2000-01-01T00:00:00.000000Z'),'sleeping','{"subject":"op","operator":true}',1
FROM generate_series(1,$3)`, hash, framesHex, n); err != nil {
		t.Fatalf("bulk insert %d timers: %v", n, err)
	}
}
