package kernel

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

// --- reactor test harness ----------------------------------------------------

type reactorEnv struct {
	srv  *Server
	pool *pgwire.Pool
	base pgwire.Config
	db   string
}

// newReactorEnv spins a scratch DB (migrate + genesis), a pool, and a Server with
// its pinned epoch — the shared fixture for the reactor integration tests.
func newReactorEnv(t *testing.T) *reactorEnv {
	t.Helper()
	ctx := context.Background()
	base, err := pgwire.ParseDSN(baseDSN())
	if err != nil {
		t.Skipf("no test PG: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Skipf("connect admin: %v", err)
	}
	db := randName("regel_rct_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		admin.Close()
		t.Fatalf("create db: %v", err)
	}
	admin.Close()

	cfg := base
	cfg.Database = db
	boot, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	if err := catalog.Bootstrap(ctx, boot, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := admission.Genesis(ctx, boot, admission.BuildImage()); err != nil {
		t.Fatalf("genesis: %v", err)
	}
	boot.Close()

	pool := pgwire.NewPool(cfg, 32)
	srv, err := New(ctx, pool)
	if err != nil {
		t.Fatalf("kernel.New: %v", err)
	}
	e := &reactorEnv{srv: srv, pool: pool, base: base, db: db}
	t.Cleanup(func() {
		pool.Close()
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	})
	return e
}

func (e *reactorEnv) admit(t *testing.T, src, prefix string, base map[string]string) admission.Verdict {
	t.Helper()
	return admitSrc(t, e.pool, src, prefix, base)
}

// admitDecl admits source declaring the given capabilities (V1 requires a
// capability-bearing call to be declared in the patch envelope).
func (e *reactorEnv) admitDecl(t *testing.T, src, prefix string, declared []string) admission.Verdict {
	t.Helper()
	ctx := context.Background()
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer e.pool.Release(conn)
	patch := admission.Patch{
		Modules:         []admission.ModuleSrc{{ModuleName: prefix, Source: src}},
		TargetScope:     admission.Scope{Kind: 0, ID: ""},
		BaseHashes:      map[string]string{},
		DefaultDeclared: declared,
	}
	v, err := admission.Admit(ctx, conn, patch,
		admission.Principal{ActorKind: "engineer", ActorID: "dev", Via: "cli"}, admission.BuildImage())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return v
}

// withConn acquires a pool conn, runs fn, and always releases it — helpers must
// never hold a conn across calls (the pool is bounded).
func (e *reactorEnv) withConn(t *testing.T, fn func(*pgwire.Conn)) {
	t.Helper()
	c, err := e.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer e.pool.Release(c)
	fn(c)
}

// conn acquires a conn the CALLER must release via e.pool.Release.
func (e *reactorEnv) conn(t *testing.T) *pgwire.Conn {
	t.Helper()
	c, err := e.pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return c
}

// start starts a workflow via the shared store door and returns its id.
func (e *reactorEnv) start(t *testing.T, hash string, args []cek.Value, principal map[string]any) string {
	t.Helper()
	var id string
	e.withConn(t, func(c *pgwire.Conn) {
		var err error
		id, err = cfr.StartWorkflow(context.Background(), c, e.srv.stepEnv(0), e.srv.Interp(), hash, args, principal, cek.TierTrusted)
		if err != nil {
			t.Fatalf("StartWorkflow: %v", err)
		}
	})
	return id
}

// stepOnce drives exactly one step of a continuation against its current seq.
func (e *reactorEnv) stepOnce(t *testing.T, id string) (cek.Outcome, bool) {
	t.Helper()
	ctx := context.Background()
	var out cek.Outcome
	var claimed bool
	e.withConn(t, func(c *pgwire.Conn) {
		var seq int64
		ok, err := c.QueryRow(ctx, `SELECT step_seq FROM continuation WHERE id=$1`, []any{id}, &seq)
		if err != nil || !ok {
			t.Fatalf("read seq: ok=%v err=%v", ok, err)
		}
		resume := func(st *cek.State, d cek.Delivery, p cek.Principal) cek.Outcome {
			return e.srv.Interp().Resume(ctx, st, d, p)
		}
		out, claimed, err = cfr.ClaimAndStep(ctx, c, e.srv.stepEnv(30), e.srv.Interp(), id, seq, resume)
		if err != nil {
			t.Fatalf("ClaimAndStep: %v", err)
		}
	})
	return out, claimed
}

func (e *reactorEnv) status(t *testing.T, id string) string {
	t.Helper()
	var s string
	e.withConn(t, func(c *pgwire.Conn) {
		ok, err := c.QueryRow(context.Background(), `SELECT status FROM continuation WHERE id=$1`, []any{id}, &s)
		if err != nil || !ok {
			t.Fatalf("status: ok=%v err=%v", ok, err)
		}
	})
	return s
}

func (e *reactorEnv) intScalar(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	e.withConn(t, func(c *pgwire.Conn) {
		if _, err := c.QueryRow(context.Background(), sql, args, &n); err != nil {
			t.Fatalf("scalar %q: %v", sql, err)
		}
	})
	return n
}

func (e *reactorEnv) waitStatus(t *testing.T, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if e.status(t, id) == want {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("continuation %s did not reach %q within %s (last=%q)", id, want, timeout, e.status(t, id))
}

func (e *reactorEnv) result(t *testing.T, id string) cek.Value {
	t.Helper()
	var v cek.Value
	e.withConn(t, func(c *pgwire.Conn) {
		var ok bool
		var err error
		v, ok, err = cfr.LoadResult(context.Background(), c, id)
		if err != nil || !ok {
			t.Fatalf("LoadResult %s: ok=%v err=%v", id, ok, err)
		}
	})
	return v
}

// exec runs a mutation on a released conn.
func (e *reactorEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	e.withConn(t, func(c *pgwire.Conn) {
		if _, err := c.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	})
}

// --- Test 1: wf.sleep end-to-end ---------------------------------------------

const wfSleepSrc = `import { sleep, send } from "std/wf";
export function w(): number {
  let a = 1;
  sleep(50);
  a = a + 2;
  send("log", a);
  sleep(50);
  return a + 39;
}`

func TestReactorSleepEndToEnd(t *testing.T) {
	e := newReactorEnv(t)
	v := e.admit(t, wfSleepSrc, "app/wf", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/wf/w"]
	id := e.start(t, hash, nil, map[string]any{"subject": "op", "operator": true})

	r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	e.waitStatus(t, id, "done", 10*time.Second)
	if got := e.result(t, id); got.Tag != cek.TagF64 || got.N != 42 {
		t.Fatalf("result = %+v, want 42", got)
	}
	if seq := e.intScalar(t, `SELECT step_seq FROM continuation WHERE id=$1`, id); seq < 3 {
		t.Fatalf("step_seq = %d, want >= 3", seq)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, id); n != 1 {
		t.Fatalf("outbox rows = %d, want exactly 1 (the send)", n)
	}
}

// --- Test 2: receive / send --------------------------------------------------

const wfReceiveSrc = `import { receive } from "std/wf";
export function w(): string {
  const m: string = receive("ch");
  return "got:" + m;
}`

func TestReactorReceiveSend(t *testing.T) {
	e := newReactorEnv(t)
	v := e.admit(t, wfReceiveSrc, "app/rx", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/rx/w"]

	t.Run("send_after_park", func(t *testing.T) {
		id := e.start(t, hash, nil, map[string]any{"subject": "op", "operator": true})
		r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
		defer r.Stop()
		// Wait until the workflow is sleeping on the message wake.
		e.waitStatus(t, id, "sleeping", 5*time.Second)
		c := e.conn(t)
		to, err := cfr.SendChannel(context.Background(), c, e.srv.stepEnv(0), "ch", cek.StrV("hello"), "tester")
		e.pool.Release(c)
		if err != nil {
			t.Fatalf("SendChannel: %v", err)
		}
		if to != id {
			t.Fatalf("delivered_to = %q, want %q", to, id)
		}
		e.waitStatus(t, id, "done", 5*time.Second)
		if got := e.result(t, id); got.Tag != cek.TagStr || got.S != "got:hello" {
			t.Fatalf("result = %+v, want got:hello", got)
		}
		if n := e.intScalar(t, `SELECT count(*) FROM channel_message WHERE channel='ch' AND claimed_by=$1`, id); n != 1 {
			t.Fatalf("claimed messages = %d, want 1", n)
		}
	})

	t.Run("send_before_park", func(t *testing.T) {
		// Queue a message first, THEN start the receiver: it claims immediately.
		c := e.conn(t)
		_, serr := cfr.SendChannel(context.Background(), c, e.srv.stepEnv(0), "ch2", cek.StrV("early"), "tester")
		e.pool.Release(c)
		if serr != nil {
			t.Fatalf("SendChannel: %v", serr)
		}
		src := `import { receive } from "std/wf";
export function w2(): string { const m: string = receive("ch2"); return "got:" + m; }`
		v := e.admit(t, src, "app/rx", nil)
		if v.Outcome != admission.OutcomeAdmitted {
			t.Fatalf("admit w2: %q (%+v)", v.Outcome, v.Diagnostics)
		}
		id := e.start(t, v.Hashes["app/rx/w2"], nil, map[string]any{"subject": "op", "operator": true})
		r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
		defer r.Stop()
		e.waitStatus(t, id, "done", 5*time.Second)
		if got := e.result(t, id); got.S != "got:early" {
			t.Fatalf("result = %+v, want got:early", got)
		}
	})
}

// --- Test 3: join (all / race) + crash leg -----------------------------------

const wfAllSrc = `import { all } from "std/wf";
export function three(): number[] { return all([() => 1, () => 2, () => 3]); }`

const wfRaceSrc = `import { race, sleep } from "std/wf";
export function raceTwo(): number {
  return race([
    () => { sleep(30); return 10; },
    () => { sleep(400); return 20; },
  ]);
}`

func TestReactorJoinAll(t *testing.T) {
	e := newReactorEnv(t)
	v := e.admit(t, wfAllSrc, "app/join", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	id := e.start(t, v.Hashes["app/join/three"], nil, map[string]any{"subject": "op", "operator": true})
	r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	// Parent should spawn exactly 3 children.
	e.waitStatus(t, id, "done", 10*time.Second)
	if n := e.intScalar(t, `SELECT count(*) FROM continuation WHERE principal->>'join_parent'=$1`, id); n != 3 {
		t.Fatalf("child rows = %d, want 3", n)
	}
	got := e.result(t, id)
	if got.Tag != cek.TagArray {
		t.Fatalf("result not array: %+v", got)
	}
	arr := got.Ref.(*cek.ArrayObj).Elems
	if len(arr) != 3 || arr[0].N != 1 || arr[1].N != 2 || arr[2].N != 3 {
		t.Fatalf("all results = %+v, want [1 2 3] in order", arr)
	}
	// Idempotency (crash leg): the join flip is a status CAS, so exactly one resume
	// task at the flip step_seq (1) ever targets the parent — even if a child step
	// is re-offered. (step_seq 0 is the initial start task.)
	if n := e.intScalar(t, `SELECT count(*) FROM task WHERE payload->>'continuation_id'=$1 AND payload->>'step_seq'='1'`, id); n != 1 {
		t.Fatalf("parent flip tasks = %d, want exactly 1", n)
	}
	// Re-offer a done child's resume task: it is a clean claim-loss, no re-flip.
	e.exec(t, `INSERT INTO task (id, kind, run_at, payload)
SELECT gen_random_uuid(),'resume',now(),jsonb_build_object('continuation_id',id::text,'step_seq',step_seq)
FROM continuation WHERE principal->>'join_parent'=$1 LIMIT 1`, id)
	time.Sleep(200 * time.Millisecond)
	if n := e.intScalar(t, `SELECT count(*) FROM task WHERE payload->>'continuation_id'=$1 AND payload->>'step_seq'='1'`, id); n != 1 {
		t.Fatalf("after duplicate child task, parent flip tasks = %d, want still 1", n)
	}
}

func TestReactorJoinRace(t *testing.T) {
	e := newReactorEnv(t)
	v := e.admit(t, wfRaceSrc, "app/join", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	id := e.start(t, v.Hashes["app/join/raceTwo"], nil, map[string]any{"subject": "op", "operator": true})
	r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	e.waitStatus(t, id, "done", 10*time.Second)
	if got := e.result(t, id); got.Tag != cek.TagF64 || got.N != 10 {
		t.Fatalf("race result = %+v, want 10 (the fast leg)", got)
	}
	// The slow loser is cancelled.
	if n := e.intScalar(t, `SELECT count(*) FROM continuation WHERE principal->>'join_parent'=$1 AND status='cancelled'`, id); n != 1 {
		t.Fatalf("cancelled losers = %d, want 1", n)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM continuation WHERE principal->>'join_parent'=$1 AND status='done'`, id); n != 1 {
		t.Fatalf("done winners = %d, want 1", n)
	}
}

// --- Test 4: as-of resume (runs original def_hash) ---------------------------

func TestReactorAsOfResume(t *testing.T) {
	e := newReactorEnv(t)
	mk := func(ret int) string {
		return fmt.Sprintf(`import { sleep } from "std/wf";
export function w(): number { sleep(40); return %d; }`, ret)
	}
	v1 := e.admit(t, mk(1), "app/asof", nil)
	if v1.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit v1: %q", v1.Outcome)
	}
	h1 := v1.Hashes["app/asof/w"]
	id := e.start(t, h1, nil, map[string]any{"subject": "op", "operator": true})

	// Drive one step manually: parks on sleep.
	if out, claimed := e.stepOnce(t, id); !claimed || out.Kind != cek.OutParked {
		t.Fatalf("step1: claimed=%v kind=%d", claimed, out.Kind)
	}
	if e.status(t, id) != "sleeping" {
		t.Fatalf("status after step1 = %q, want sleeping", e.status(t, id))
	}

	// Re-admit three times (pointer moves; new hashes).
	prev := h1
	for _, ret := range []int{2, 3, 4} {
		v := e.admit(t, mk(ret), "app/asof", map[string]string{"app/asof/w": prev})
		if v.Outcome != admission.OutcomeAdmitted {
			t.Fatalf("re-admit v%d: %q (%+v)", ret, v.Outcome, v.Diagnostics)
		}
		prev = v.Hashes["app/asof/w"]
	}

	// Wake the timer and drive to completion.
	time.Sleep(60 * time.Millisecond)
	e.exec(t, `UPDATE continuation SET status='ready' WHERE id=$1 AND status='sleeping'`, id)
	if out, _ := e.stepOnce(t, id); out.Kind != cek.OutDone {
		t.Fatalf("resume kind=%d, want done", out.Kind)
	}
	if got := e.result(t, id); got.N != 1 {
		t.Fatalf("as-of result = %+v, want 1 (original v1 semantics, not new head)", got)
	}
}

// --- Test 5: capability revoked ----------------------------------------------

const wfMailSrc = `import { sleep } from "std/wf";
import { send } from "std/mail";
export function w(): string { sleep(40); send("a@b.c", "hi"); return "sent"; }`

func TestReactorCapabilityRevokedAtCall(t *testing.T) {
	e := newReactorEnv(t)
	// The submitting principal (engineer:dev) must hold the grant at admission.
	e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('engineer:dev','mail.send','','test')`)
	v := e.admitDecl(t, wfMailSrc, "app/mail", []string{"mail.send"})
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/mail/w"]
	// Grant, start, park on sleep.
	e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('wfuser','mail.send','','test')`)
	id := e.start(t, hash, nil, map[string]any{"subject": "wfuser", "operator": false})
	if out, _ := e.stepOnce(t, id); out.Kind != cek.OutParked {
		t.Fatalf("step1 kind=%d", out.Kind)
	}

	// Revoke the grant while parked; wake; the mail.send call parks capability.revoked.
	e.exec(t, `DELETE FROM grant_row WHERE subject='wfuser'`)
	e.exec(t, `UPDATE continuation SET status='ready' WHERE id=$1`, id)
	e.stepOnce(t, id)
	if s := e.status(t, id); s != "condition" {
		t.Fatalf("status = %q, want condition", s)
	}
	if n := e.intScalar(t,
		`SELECT count(*) FROM durable_condition WHERE continuation_id=$1 AND class='capability.revoked'`, id); n != 1 {
		t.Fatalf("capability.revoked conditions = %d, want 1", n)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, id); n != 0 {
		t.Fatalf("outbox rows = %d, want 0 (mail.send never fired)", n)
	}

	// Re-grant + resolve the re-grant restart → completes.
	e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('wfuser','mail.send','','test')`)
	e.withConn(t, func(c *pgwire.Conn) {
		var condID string
		if _, err := c.QueryRow(context.Background(),
			`SELECT id FROM durable_condition WHERE continuation_id=$1 AND status='open' ORDER BY signaled_at DESC LIMIT 1`,
			[]any{id}, &condID); err != nil {
			t.Fatalf("find condition: %v", err)
		}
		if err := cfr.PickRestart(context.Background(), c, condID, "re-grant", nil, "operator", []string{"operator"}); err != nil {
			t.Fatalf("PickRestart re-grant: %v", err)
		}
	})
	if out, _ := e.stepOnce(t, id); out.Kind != cek.OutDone {
		t.Fatalf("resume after re-grant kind=%d, want done", out.Kind)
	}
	if got := e.result(t, id); got.S != "sent" {
		t.Fatalf("result = %+v, want sent", got)
	}
	// The re-grant restart delivers its value AT the parked mail.send call point
	// (ParkSignal semantics — the landed increment-1 machine), so the call is not
	// re-executed and no new mail.send effect is recorded. The workflow completes.
}

func TestReactorCapabilityTokenRefusedAtClaim(t *testing.T) {
	e := newReactorEnv(t)
	// A trivial sleep workflow gives us a real, decodable parked ParkWake state.
	src := `import { sleep } from "std/wf";
export function w(): number { sleep(40); return 7; }`
	v := e.admit(t, src, "app/tok", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q", v.Outcome)
	}
	id := e.start(t, v.Hashes["app/tok/w"], nil, map[string]any{"subject": "wfuser2", "operator": false})
	if out, _ := e.stepOnce(t, id); out.Kind != cek.OutParked {
		t.Fatalf("park kind=%d", out.Kind)
	}

	// Inject a capability token whose grant does not exist onto the parked state.
	var framesHex string
	e.withConn(t, func(c *pgwire.Conn) {
		if _, err := c.QueryRow(context.Background(),
			`SELECT encode(frames,'hex') FROM continuation WHERE id=$1`, []any{id}, &framesHex); err != nil {
			t.Fatalf("read frames: %v", err)
		}
	})
	st, err := cfr.Decode(mustHex(t, framesHex))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	st.Val = cek.NewCapToken("mail.send") // wfuser2 holds no such grant
	reblob, err := cfr.Encode(st)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	e.exec(t, `UPDATE continuation SET frames=('\x'||$2)::bytea, status='ready' WHERE id=$1`, id, hexStr(reblob))

	// Resume: the token is refused at the claim, before the machine re-enters.
	e.stepOnce(t, id)
	if s := e.status(t, id); s != "condition" {
		t.Fatalf("status = %q, want condition (capability.revoked)", s)
	}
	if n := e.intScalar(t,
		`SELECT count(*) FROM durable_condition WHERE continuation_id=$1 AND class='capability.revoked'`, id); n != 1 {
		t.Fatalf("capability.revoked = %d, want 1", n)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, id); n != 0 {
		t.Fatalf("outbox = %d, want 0 (machine never re-entered)", n)
	}
}

// --- Test 6: epoch fence -----------------------------------------------------

func TestEpochFenceStoreLevel(t *testing.T) {
	e := newReactorEnv(t)
	src := `import { sleep } from "std/wf";
export function w(): number { sleep(40); return 1; }`
	v := e.admit(t, src, "app/ep", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q", v.Outcome)
	}
	id := e.start(t, v.Hashes["app/ep/w"], nil, map[string]any{"subject": "op", "operator": true})

	// Advance the live catalog epoch past the kernel's pinned epoch (1).
	e.exec(t, `INSERT INTO epoch (n, std_manifest_root, dispatch_attestation) VALUES (2,'x','y')`)
	e.exec(t, `UPDATE epoch_current SET n=2 WHERE one=true`)

	ctx := context.Background()
	resume := func(st *cek.State, d cek.Delivery, p cek.Principal) cek.Outcome {
		return e.srv.Interp().Resume(ctx, st, d, p)
	}
	var fenceErr error
	var claimed bool
	e.withConn(t, func(c *pgwire.Conn) {
		_, claimed, fenceErr = cfr.ClaimAndStep(ctx, c, cfr.StepEnv{KernelID: e.srv.KernelID(), KernelEpoch: 1, LeaseSeconds: 30},
			e.srv.Interp(), id, 0, resume)
	})
	var fence cfr.ErrEpochFence
	if fenceErr == nil || !asFence(fenceErr, &fence) {
		t.Fatalf("expected ErrEpochFence, got %v (claimed=%v)", fenceErr, claimed)
	}
	if fence.Observed != 2 || fence.Required != 1 {
		t.Fatalf("fence = %+v, want observed 2 required 1", fence)
	}
	// No claim consumed: still ready at step_seq 0.
	if s := e.status(t, id); s != "ready" {
		t.Fatalf("status = %q, want ready (untouched)", s)
	}
	if seq := e.intScalar(t, `SELECT step_seq FROM continuation WHERE id=$1`, id); seq != 0 {
		t.Fatalf("step_seq = %d, want 0 (untouched)", seq)
	}
}

func TestEpochFenceReactorDrains(t *testing.T) {
	e := newReactorEnv(t)
	src := `import { sleep } from "std/wf";
export function w(): number { sleep(40); return 1; }`
	v := e.admit(t, src, "app/ep2", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q", v.Outcome)
	}
	// Flip the epoch BEFORE the reactor drains the ready workflow.
	e.exec(t, `INSERT INTO epoch (n, std_manifest_root, dispatch_attestation) VALUES (2,'x','y')`)
	e.exec(t, `UPDATE epoch_current SET n=2 WHERE one=true`)
	e.start(t, v.Hashes["app/ep2/w"], nil, map[string]any{"subject": "op", "operator": true})

	r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e.srv.Draining() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("reactor did not enter terminal drain on epoch fence")
}

// --- Test 7: retry / abort ---------------------------------------------------

func TestRetrySerializableExhausts(t *testing.T) {
	before := cfr.MetricsSnapshot()
	attempts := 0
	err := cfr.RetrySerializable(context.Background(), "fab", func(int) error {
		attempts++
		return &pgwire.PgError{Code: "40001", Severity: "ERROR", Message: "fabricated"}
	})
	if err == nil {
		t.Fatal("expected the fabricated 40001 to surface after exhaustion")
	}
	if attempts != 5 {
		t.Fatalf("attempts = %d, want 5", attempts)
	}
	after := cfr.MetricsSnapshot()
	if after.RetryExhausted-before.RetryExhausted != 1 {
		t.Fatalf("RetryExhausted delta = %d, want 1", after.RetryExhausted-before.RetryExhausted)
	}
	if after.SerializationAborts-before.SerializationAborts != 5 {
		t.Fatalf("SerializationAborts delta = %d, want 5", after.SerializationAborts-before.SerializationAborts)
	}
}

func TestConcurrentClaimExactlyOne(t *testing.T) {
	e := newReactorEnv(t)
	src := `import { sleep } from "std/wf";
export function w(): number { sleep(40); return 1; }`
	v := e.admit(t, src, "app/race2", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q", v.Outcome)
	}
	id := e.start(t, v.Hashes["app/race2/w"], nil, map[string]any{"subject": "op", "operator": true})

	ctx := context.Background()
	var wg sync.WaitGroup
	var claims [2]bool
	run := func(i int) {
		defer wg.Done()
		conn, err := e.pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer e.pool.Release(conn)
		resume := func(st *cek.State, d cek.Delivery, p cek.Principal) cek.Outcome {
			return e.srv.Interp().Resume(ctx, st, d, p)
		}
		_ = cfr.RetrySerializable(ctx, "step", func(int) error {
			_, c, err := cfr.ClaimAndStep(ctx, conn, e.srv.stepEnv(30), e.srv.Interp(), id, 0, resume)
			claims[i] = claims[i] || c
			return err
		})
	}
	wg.Add(2)
	go run(0)
	go run(1)
	wg.Wait()
	n := 0
	for _, c := range claims {
		if c {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("exactly one claim must win, got %d", n)
	}
}

// --- Test 8: reaper re-offers a stranded continuation ------------------------

func TestReaperReoffersStranded(t *testing.T) {
	e := newReactorEnv(t)
	src := `import { send } from "std/wf";
export function w(): number { send("r", 7); return 7; }`
	v := e.admit(t, src, "app/reap", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q", v.Outcome)
	}
	id := e.start(t, v.Hashes["app/reap/w"], nil, map[string]any{"subject": "op", "operator": true})

	// Strand it: a dead kernel that claimed (step_seq→1, running) then vanished.
	e.exec(t, `
UPDATE continuation SET status='running', lease_owner=gen_random_uuid(),
       lease_until=now()+make_interval(secs=>1), step_seq=1 WHERE id=$1`, id)

	r := e.srv.StartReactor(context.Background(), ReactorConfig{
		PollInterval: 15 * time.Millisecond, ReapEvery: 150 * time.Millisecond, LeaseSeconds: 30,
	})
	defer r.Stop()

	e.waitStatus(t, id, "done", 6*time.Second)
	if got := e.result(t, id); got.N != 7 {
		t.Fatalf("result = %+v, want 7", got)
	}
	// Effect fires exactly once (outbox UNIQUE would catch a double).
	if n := e.intScalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, id); n != 1 {
		t.Fatalf("outbox rows = %d, want exactly 1", n)
	}
}

// --- Test 9: wake storm ------------------------------------------------------

func TestWakeStorm(t *testing.T) {
	if testing.Short() {
		t.Skip("wake storm is a heavy integration test")
	}
	e := newReactorEnv(t)
	// mail.send records an outbox row without scanning the continuation table (as
	// channel.send's receiver lookup does), so the storm measures exactly-once
	// under contention without the pathological SSI conflict of 2000 sends to a
	// channel with no receivers. Operator principal bypasses the runtime gate.
	e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('engineer:dev','mail.send','','test')`)
	src := `import { sleep } from "std/wf";
import { send } from "std/mail";
export function w(): number { sleep(1); send("a@b.c", "hi"); return 1; }`
	v := e.admitDecl(t, src, "app/storm", []string{"mail.send"})
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/storm/w"]

	// Get one post-sleep parked state, then bulk-insert 2000 sleeping timers due now.
	seedID := e.start(t, hash, nil, map[string]any{"subject": "op", "operator": true})
	if out, _ := e.stepOnce(t, seedID); out.Kind != cek.OutParked {
		t.Fatalf("seed park kind=%d", out.Kind)
	}
	const N = 2000
	c := e.conn(t)
	var framesHex string
	if _, err := c.QueryRow(context.Background(),
		`SELECT encode(frames,'hex') FROM continuation WHERE id=$1`, []any{seedID}, &framesHex); err != nil {
		e.pool.Release(c)
		t.Fatalf("read seed frames: %v", err)
	}
	// Cancel the seed so it does not also complete (keeps the count clean).
	if _, err := c.Exec(context.Background(), `UPDATE continuation SET status='cancelled' WHERE id=$1`, seedID); err != nil {
		e.pool.Release(c)
		t.Fatalf("cancel seed: %v", err)
	}
	pastDue := "2000-01-01T00:00:00.000000Z"
	for i := 0; i < N; i++ {
		if _, err := c.Exec(context.Background(), `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
VALUES (gen_random_uuid(),'workflow',$1,1,1,('\x'||$2)::bytea,
  jsonb_build_object('kind','timer','due',$3::text),'sleeping','{"subject":"op","operator":true}',1)`,
			hash, framesHex, pastDue); err != nil {
			e.pool.Release(c)
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	e.pool.Release(c)

	before := cfr.MetricsSnapshot()
	start := time.Now()

	// Three reactors with distinct kernel ids on the same DB.
	ctx := context.Background()
	reactors := make([]*Reactor, 0, 3)
	for i := 0; i < 3; i++ {
		srv, err := New(ctx, e.pool)
		if err != nil {
			t.Fatalf("New reactor srv %d: %v", i, err)
		}
		reactors = append(reactors, srv.StartReactor(ctx, ReactorConfig{
			PollInterval: 20 * time.Millisecond, DrainBatch: 64, TimerBatch: 512,
		}))
	}
	defer func() {
		for _, r := range reactors {
			r.Stop()
		}
	}()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		done := e.intScalar(t, `SELECT count(*) FROM continuation WHERE status='done'`)
		if done >= N {
			break
		}
		time.Sleep(50 * time.Millisecond)
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
	after := cfr.MetricsSnapshot()
	aborts := after.SerializationAborts - before.SerializationAborts
	attempts := int64(N) + aborts // lower bound on step attempts
	rate := float64(aborts) / float64(attempts)
	t.Logf("wake storm: %d workflows, elapsed=%s, aborts=%d, abort_rate=%.4f (<=0.05 budget), reoffers=%d",
		N, elapsed, aborts, rate, after.Reoffers-before.Reoffers)
	if rate > 0.05 {
		t.Fatalf("abort rate %.4f exceeds the 5%% budget", rate)
	}
}

// --- helpers -----------------------------------------------------------------

func asFence(err error, target *cfr.ErrEpochFence) bool {
	f, ok := err.(cfr.ErrEpochFence)
	if ok {
		*target = f
	}
	return ok
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	return b
}

func hexStr(b []byte) string { return hex.EncodeToString(b) }
