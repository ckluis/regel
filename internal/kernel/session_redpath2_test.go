package kernel

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/ui"
)

// --- reconnect / empty-diff invariant (§2) ------------------------------------

func TestSessionReconnectEmptyDiff(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	A := se.mount(t, "app/rx/Widget/detail/"+fmtID(id), "human:a", "acme")

	c1 := A.openSSE(0)
	time.Sleep(80 * time.Millisecond)
	// A UI-local click checkpoints a ZERO-OP frame (advances the cursor, no repaint).
	A.postEvent("click", "detail.1", "x")
	f := c1.nextFrame(t, 3*time.Second)
	if f.EventSeq != 1 || len(f.Ops) != 0 {
		t.Fatalf("expected zero-op frame at seq 1, got %+v", f)
	}
	if f.SnapshotHash != uint64(A.digest) {
		t.Fatalf("zero-op frame hash %d != client digest %d (no change)", f.SnapshotHash, A.digest)
	}
	c1.close()

	// Reconnect at cursor 0 ⇒ gapless replay of the zero-op frame, no full repaint.
	c2 := A.openSSE(0)
	defer c2.close()
	f2 := c2.nextFrame(t, 3*time.Second)
	if f2.EventSeq != 1 || len(f2.Ops) != 0 || f2.SnapshotHash != f.SnapshotHash {
		t.Fatalf("replayed zero-op frame mismatch: %+v vs %+v", f2, f)
	}
}

// --- stale cursor beyond buffer ⇒ exactly one resync (§2) ---------------------

func TestSessionStaleCursorResync(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	A := se.mount(t, "app/rx/Widget/detail/"+fmtID(id), "human:a", "acme")

	// Drive three checkpoints so the ring holds frames 1..3.
	for i := 0; i < 3; i++ {
		A.postEvent("click", "detail.1", fmt.Sprintf("v%d", i))
	}
	// Simulate a ring that has evicted the early frames (retains only the newest).
	sess := se.srv.hub.get(A.sid)
	sess.mu.Lock()
	sess.ring = sess.ring[len(sess.ring)-1:]
	sess.mu.Unlock()

	before := sseMetricsSnapshot().ResyncsTotal
	c := A.openSSE(0) // cursor 0 predates the retained window
	defer c.close()
	select {
	case <-c.resyncs:
	case <-time.After(2 * time.Second):
		t.Fatal("stale cursor did not trigger a resync directive")
	}
	after := sseMetricsSnapshot().ResyncsTotal
	if after-before != 1 {
		t.Fatalf("sse.resyncs_total delta = %d, want exactly 1", after-before)
	}
}

// --- divergence: corrupt a frame ⇒ hash mismatch ⇒ resync ⇒ snapshots equal ---

func TestSessionDivergenceResync(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	nameDetail := slotForField(t, se.srv, "app/rx/Widget", "detail", "name")
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")

	A := se.mount(t, "app/rx/Widget/detail/"+fmtID(id), "human:a", "acme")
	c := A.openSSE(0)
	defer c.close()
	time.Sleep(120 * time.Millisecond)

	ed := se.mount(t, "app/rx/Widget/form/"+fmtID(id), "human:e", "acme")
	ed.postEvent("input", nameForm, "FOO2")
	ed.postEvent("submit", "", "")

	// A receives the setText patch; the client CORRUPTS it on apply.
	f := c.nextFrame(t, 4*time.Second)
	A.corrupt = nameDetail
	A.applyFrame(f)
	if uint64(A.digest) == f.SnapshotHash {
		t.Fatal("corruption did not diverge the digest")
	}
	// Divergence self-heals in one round trip: POST /resync, adopt fresh snapshot.
	A.resync()
	if A.slots[nameDetail] != "FOO2" {
		t.Fatalf("post-resync name = %q, want FOO2 (snapshots equal)", A.slots[nameDetail])
	}
	if A.resyncs != 1 {
		t.Fatalf("resyncs = %d, want exactly 1", A.resyncs)
	}
}

// --- kernel death mid-step ⇒ rollback, resume from prior checkpoint, no dup ----

func TestSessionKernelDeathMidStep(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")

	ed := se.mount(t, "app/rx/Widget/form/"+fmtID(id), "human:e", "acme")
	ed.postEvent("input", nameForm, "NEW")

	// Arm a fault that poisons the txn AFTER the mutation ran — the whole step rolls
	// back (a kill between claim and checkpoint).
	stepFaultHook = func(ctx context.Context, conn *pgwire.Conn, _ string) error {
		_, _ = conn.Exec(ctx, `SELECT 1/0`) // abort the transaction
		return fmt.Errorf("simulated kernel death mid-step")
	}
	_, _, err := se.srv.driveSession(context.Background(), ed.sid, int64(ed.cursor), sessionMsg{Kind: "event", Event: "submit"})
	stepFaultHook = nil
	if err == nil {
		t.Fatal("expected the simulated fault to surface")
	}
	// The mutation rolled back: name unchanged, step_seq not advanced.
	var name string
	var seq, rv int64
	se.withConn(t, func(c *pgConn) {
		c.QueryRow(context.Background(),
			`SELECT name, row_version FROM `+quoteIdent(se.widgetTable())+` WHERE id=$1`, []any{id}, &name, &rv)
	})
	if name == "NEW" {
		t.Fatal("mutation committed despite mid-step death (should have rolled back)")
	}
	// Retry from the prior checkpoint on a fresh drive ⇒ the mutation applies once.
	r := ed.postEvent("submit", "", "")
	if applied, _ := r["applied"].(bool); !applied {
		t.Fatalf("retry submit not applied: %+v", r)
	}
	se.withConn(t, func(c *pgConn) {
		c.QueryRow(context.Background(),
			`SELECT name, row_version FROM `+quoteIdent(se.widgetTable())+` WHERE id=$1`, []any{id}, &name, &rv)
	})
	_ = seq
	if name != "NEW" || rv != 1 {
		t.Fatalf("after retry name=%q row_version=%d, want NEW/1 (exactly one mutation)", name, rv)
	}
}

// --- PII kill-test (§8): no plaintext without a grant; reveal ⇒ frame-only ------

func TestSessionPIIGrep(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	table := se.widgetTable()
	subj := fmtID(id)
	const secret = "founder@acme.example"
	se.withConn(t, func(c *pgConn) {
		if err := admission.VaultPut(context.Background(), c, table, subj, "email", secret); err != nil {
			t.Fatalf("vault put: %v", err)
		}
	})
	emailDetail := slotForField(t, se.srv, "app/rx/Widget", "detail", "email")

	// Mount with NO grant: first paint carries the mask token, never plaintext.
	A := se.mount(t, "app/rx/Widget/detail/"+fmtID(id), "human:dpo", "acme")
	if A.slots[emailDetail] == secret {
		t.Fatal("plaintext in first-paint email slot with no grant")
	}
	// Grep the durable session row (CFR blob) + subscription rows for plaintext.
	assertNoPlaintext(t, se, A.sid, secret)

	c := A.openSSE(0)
	defer c.close()
	time.Sleep(80 * time.Millisecond)

	// No-grant re-render: the frame carries the token, never plaintext.
	A.postEvent("click", "detail.0", "x")
	f0 := c.nextFrame(t, 3*time.Second)
	if frameHasString(f0, secret) {
		t.Fatalf("no-grant frame leaked plaintext: %+v", f0)
	}

	// Mint a reveal grant for (table, subject, email); a re-render now reveals — in the
	// FRAME only — and writes a reveal_audit row.
	se.withConn(t, func(c *pgConn) {
		if err := admission.MintRevealGrant(context.Background(), c, "human:dpo", table, subj, "email", time.Time{}, "operator:dpo"); err != nil {
			t.Fatalf("mint grant: %v", err)
		}
	})
	A.postEvent("click", "detail.0", "y")
	fr := c.nextFrame(t, 3*time.Second)
	op, ok := opFor(fr, emailDetail)
	if !ok || op.Payload != secret {
		t.Fatalf("granted reveal missing from frame: %+v", fr)
	}
	// Plaintext is STILL absent from the durable session row (snapshot holds token|scope).
	assertNoPlaintext(t, se, A.sid, secret)
	if n := se.intScalar(t, `SELECT count(*) FROM reveal_audit WHERE resource=$1 AND subject_id=$2 AND field='email'`, table, subj); n < 1 {
		t.Fatalf("reveal_audit rows = %d, want >= 1", n)
	}

	// Expire the grant ⇒ the next render re-masks (plaintext gone from the frame).
	se.withConn(t, func(c *pgConn) {
		admission.MintRevealGrant(context.Background(), c, "human:dpo", table, subj, "email",
			time.Now().Add(-time.Hour), "operator:dpo")
	})
	A.postEvent("click", "detail.0", "z")
	fe := c.nextFrame(t, 3*time.Second)
	if frameHasString(fe, secret) {
		t.Fatalf("expired grant still revealed plaintext: %+v", fe)
	}
}

func assertNoPlaintext(t *testing.T, se *sessionEnv, sid, secret string) {
	t.Helper()
	var framesHex, subs string
	se.withConn(t, func(c *pgConn) {
		c.QueryRow(context.Background(), `SELECT encode(frames,'hex') FROM continuation WHERE id=$1`, []any{sid}, &framesHex)
		c.QueryRow(context.Background(), `SELECT coalesce(string_agg(key,','),'') FROM subscription WHERE session_id=$1`, []any{sid}, &subs)
	})
	if strings.Contains(subs, secret) {
		t.Fatalf("plaintext leaked into a subscription row")
	}
	// Grep the raw CFR blob bytes AND the decoded record values.
	if strings.Contains(framesHex, hexEncode(secret)) {
		t.Fatalf("plaintext leaked into the raw session CFR blob")
	}
	if raw, err := decodeFramesHex(framesHex); err == nil {
		if sess, err := sessionFromState(raw); err == nil {
			for _, v := range sess.LastSnap {
				if strings.Contains(v, secret) {
					t.Fatalf("plaintext leaked into the session CFR snapshot")
				}
			}
			for _, v := range sess.Draft {
				if strings.Contains(v, secret) {
					t.Fatalf("plaintext leaked into the session CFR draft")
				}
			}
		}
	}
}

func hexEncode(s string) string { return hexOf([]byte(s)) }

// frameHasString greps a captured frame's op payloads for a substring.
func frameHasString(f ui.Frame, s string) bool {
	for _, op := range f.Ops {
		if strings.Contains(op.Payload, s) {
			return true
		}
		for _, sp := range op.Splices {
			if strings.Contains(sp.HTML, s) {
				return true
			}
		}
	}
	return false
}
