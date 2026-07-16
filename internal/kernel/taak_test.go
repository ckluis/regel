package kernel

import (
	"context"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

// --- RED-path 3: message match predicates (ADR-05 §5) ------------------------

// TestTaakMatchPredicate: two receivers with disjoint {path,equals} predicates on
// one channel; interleaved sends deliver each message exactly once to the RIGHT
// receiver, and a message matching nobody stays queued until a matching receiver
// arrives.
func TestTaakMatchPredicate(t *testing.T) {
	e := newReactorEnv(t)
	// Each receiver is its own single-export module; all receive on channel "q"
	// with a disjoint {path,equals} predicate over the message's `kind` field.
	recv := func(prefix, want string) string {
		src := `import { receive } from "std/taak";
type Msg = { kind: string; tag: string };
export function w(): string { const m: Msg = receive("q", { path: "kind", equals: "` + want + `" }); return "` + want + `:" + m.tag; }`
		v := e.admit(t, src, prefix, nil)
		if v.Outcome != admission.OutcomeAdmitted {
			t.Fatalf("admit %s: %q (%+v)", prefix, v.Outcome, v.Diagnostics)
		}
		return v.Hashes[prefix+"/w"]
	}
	prin := map[string]any{"subject": "op", "operator": true}
	idA := e.start(t, recv("app/mpa", "A"), nil, prin)
	idB := e.start(t, recv("app/mpb", "B"), nil, prin)

	r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	// Both park sleeping on the message wake with their predicates.
	e.waitStatus(t, idA, "sleeping", 5*time.Second)
	e.waitStatus(t, idB, "sleeping", 5*time.Second)

	msg := func(kind, tag string) cek.Value {
		return cek.RecordV([]string{"kind", "tag"}, []cek.Value{cek.StrV(kind), cek.StrV(tag)})
	}
	send := func(v cek.Value) {
		c := e.conn(t)
		defer e.pool.Release(c)
		if _, err := cfr.SendChannel(context.Background(), c, e.srv.stepEnv(0), "q", v, "tester"); err != nil {
			t.Fatalf("SendChannel: %v", err)
		}
	}

	// A message matching nobody yet: kind=C. It must stay queued.
	send(msg("C", "orphan"))
	// Interleave B then A.
	send(msg("B", "beta"))
	send(msg("A", "alpha"))

	e.waitStatus(t, idA, "done", 5*time.Second)
	e.waitStatus(t, idB, "done", 5*time.Second)
	if got := e.result(t, idA); got.S != "A:alpha" {
		t.Fatalf("A result = %+v, want A:alpha", got)
	}
	if got := e.result(t, idB); got.S != "B:beta" {
		t.Fatalf("B result = %+v, want B:beta", got)
	}
	// The orphan (kind=C) is still unclaimed.
	if n := e.intScalar(t, `SELECT count(*) FROM channel_message WHERE channel='q' AND claimed_by IS NULL`); n != 1 {
		t.Fatalf("unclaimed messages = %d, want 1 (the orphan)", n)
	}
	// Each delivered message went to exactly one receiver.
	if n := e.intScalar(t, `SELECT count(*) FROM channel_message WHERE channel='q' AND claimed_by IS NOT NULL`); n != 2 {
		t.Fatalf("claimed messages = %d, want 2", n)
	}

	// A matching receiver arrives for the orphan (kind=C) and claims it exactly once.
	idC := e.start(t, recv("app/mpc", "C"), nil, prin)
	e.waitStatus(t, idC, "done", 5*time.Second)
	if got := e.result(t, idC); got.S != "C:orphan" {
		t.Fatalf("C result = %+v, want C:orphan", got)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM channel_message WHERE channel='q' AND claimed_by IS NULL`); n != 0 {
		t.Fatalf("unclaimed messages = %d, want 0", n)
	}
}

// --- RED-path 4: event wakes (ADR-05 §5) -------------------------------------

// wakeEventsTx runs cfr.WakeEvents inside a SERIALIZABLE transaction (mirroring a
// derived-resource mutation's commit) and returns how many continuations woke.
func (e *reactorEnv) wakeEventsTx(t *testing.T, resource, rowID string) int {
	t.Helper()
	ctx := context.Background()
	c := e.conn(t)
	defer e.pool.Release(c)
	if err := c.BeginSerializable(ctx); err != nil {
		t.Fatalf("begin: %v", err)
	}
	n, err := cfr.WakeEvents(ctx, c, resource, rowID)
	if err != nil {
		_ = c.Rollback(ctx)
		t.Fatalf("WakeEvents: %v", err)
	}
	if err := c.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return n
}

// TestTaakOnChangeEventWake: a workflow parks on Contact changes; an unrelated
// resource mutation does NOT wake it; a matching mutation wakes it exactly once.
func TestTaakOnChangeEventWake(t *testing.T) {
	e := newReactorEnv(t)
	src := `import { onChange } from "std/taak";
export function watch(): string { onChange("Contact", ["c1"]); return "woke"; }`
	v := e.admit(t, src, "app/ev", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	id := e.start(t, v.Hashes["app/ev/watch"], nil, map[string]any{"subject": "op", "operator": true})
	r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	e.waitStatus(t, id, "sleeping", 5*time.Second)

	// Unrelated resource: no wake.
	if n := e.wakeEventsTx(t, "Deal", "c1"); n != 0 {
		t.Fatalf("Deal mutation woke %d, want 0", n)
	}
	// Right resource, wrong row: no wake (watch set is ["c1"]).
	if n := e.wakeEventsTx(t, "Contact", "c2"); n != 0 {
		t.Fatalf("Contact/c2 mutation woke %d, want 0", n)
	}
	if e.status(t, id) != "sleeping" {
		t.Fatalf("workflow woke on an unrelated mutation")
	}
	// Matching mutation: wakes exactly once.
	if n := e.wakeEventsTx(t, "Contact", "c1"); n != 1 {
		t.Fatalf("Contact/c1 mutation woke %d, want 1", n)
	}
	e.waitStatus(t, id, "done", 5*time.Second)
	if got := e.result(t, id); got.S != "woke" {
		t.Fatalf("result = %+v, want woke", got)
	}
	// A second matching mutation does not re-wake a terminal continuation.
	if n := e.wakeEventsTx(t, "Contact", "c1"); n != 0 {
		t.Fatalf("post-terminal mutation woke %d, want 0", n)
	}
}

// --- taak.signal: durable condition + restarts (ADR-05 §6, ADR-10 §6) --------

// TestTaakSignalDurableCondition: taak.signal writes a durable_condition + its
// restart rows and parks manual; resolving the restart resumes the workflow with
// the restart's value delivered at the call point.
func TestTaakSignalDurableCondition(t *testing.T) {
	e := newReactorEnv(t)
	src := `import { signal } from "std/taak";
export function approve(): string {
  const r = signal("app.approval",
    [{ name: "approve", label: "Approve", capability: "operator" }, { name: "abort", label: "Abort" }]);
  return "resolved:" + r.restart;
}`
	v := e.admit(t, src, "app/sig", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q (%+v)", v.Outcome, v.Diagnostics)
	}
	id := e.start(t, v.Hashes["app/sig/approve"], nil, map[string]any{"subject": "op", "operator": true})
	r := e.srv.StartReactor(context.Background(), ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	e.waitStatus(t, id, "condition", 5*time.Second)
	if n := e.intScalar(t, `SELECT count(*) FROM durable_condition WHERE continuation_id=$1 AND class='app.approval' AND status='open'`, id); n != 1 {
		t.Fatalf("open conditions = %d, want 1", n)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM restart r JOIN durable_condition dc ON dc.id=r.condition_id WHERE dc.continuation_id=$1`, id); n != 2 {
		t.Fatalf("restart rows = %d, want 2", n)
	}
	// Resolve the 'approve' restart (operator-gated).
	var condID, restartID string
	e.withConn(t, func(c *pgwire.Conn) {
		if _, err := c.QueryRow(context.Background(),
			`SELECT dc.id::text, r.id::text FROM durable_condition dc JOIN restart r ON r.condition_id=dc.id
			 WHERE dc.continuation_id=$1 AND r.name='approve'`, []any{id}, &condID, &restartID); err != nil {
			t.Fatalf("load restart: %v", err)
		}
	})
	e.exec(t, `UPDATE durable_condition SET status='resolved', resolved_restart=$2,
	  resolved_by='operator', resolved_at=now() WHERE id=$1`, condID, restartID)
	e.exec(t, `UPDATE continuation SET status='ready', updated_at=now() WHERE id=$1`, id)
	e.exec(t, `INSERT INTO task (id, kind, run_at, payload) VALUES (gen_random_uuid(),'resume',now(),
	  jsonb_build_object('continuation_id',$1::text,'step_seq',(SELECT step_seq FROM continuation WHERE id=$1::uuid)))`, id)

	e.waitStatus(t, id, "done", 5*time.Second)
	if got := e.result(t, id); got.S != "resolved:approve" {
		t.Fatalf("result = %+v, want resolved:approve", got)
	}
}
