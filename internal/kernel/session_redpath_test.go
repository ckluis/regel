package kernel

// session_redpath_test.go: the ADR-11 Red-Path Tests Implied, HTTP/SSE-level.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/ui"
)

func fmtID(id int64) string { return fmt.Sprintf("%d", id) }

func opFor(f ui.Frame, slotID string) (ui.Op, bool) {
	for _, o := range f.Ops {
		if o.SlotID == slotID {
			return o, true
		}
	}
	return ui.Op{}, false
}

// --- exactness + policy-respecting invalidation ------------------------------

func TestSessionInvalidationExactness(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id1 := se.seedWidget(t, "acme", "foo", 1)
	id2 := se.seedWidget(t, "acme", "bar", 2)

	nameDetail := slotForField(t, se.srv, "app/rx/Widget", "detail", "name")
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")

	A := se.mount(t, "app/rx/Widget/detail/"+fmtID(id1), "human:a", "acme")
	B := se.mount(t, "app/rx/Widget/detail/"+fmtID(id2), "human:b", "acme")
	C := se.mount(t, "app/rx/Widget/detail/"+fmtID(id1), "human:c", "other") // horizon excludes row 1

	ca := A.openSSE(0)
	defer ca.close()
	cb := B.openSSE(0)
	defer cb.close()
	cc := C.openSSE(0)
	defer cc.close()
	time.Sleep(150 * time.Millisecond) // let the SSE subscriptions register

	// Editor mutates row 1's name in horizon acme.
	ed := se.mount(t, "app/rx/Widget/form/"+fmtID(id1), "human:e", "acme")
	ed.postEvent("input", nameForm, "FOO2")
	r := ed.postEvent("submit", "", "")
	if applied, _ := r["applied"].(bool); !applied {
		t.Fatalf("submit not applied: %+v", r)
	}

	// A (subscribed to row 1 in horizon acme) receives the setText patch.
	fa := ca.nextFrame(t, 4*time.Second)
	op, ok := opFor(fa, nameDetail)
	if !ok || op.Payload != "FOO2" {
		t.Fatalf("A did not receive the name patch: frame=%+v", fa)
	}
	// B (a different row) and C (excluded horizon) receive NOTHING.
	cb.assertNoFrame(t, 700*time.Millisecond)
	cc.assertNoFrame(t, 700*time.Millisecond)
}

// --- double event: one CAS win, one mutation, one patch, seq advances once ----

func TestSessionDoubleEvent(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")

	ed := se.mount(t, "app/rx/Widget/form/"+fmtID(id), "human:e", "acme")
	ed.postEvent("input", nameForm, "NEW")

	// Two identical submit POSTs at the SAME eventSeq (a retry / double-click).
	seq := ed.cursor
	body := fmt.Sprintf(`{"slotId":"","event":"submit","value":"","eventSeq":%d}`, seq)
	code1, b1, _ := post(t, se.ts.URL+"/session/"+ed.sid+"/event", body)
	code2, b2, _ := post(t, se.ts.URL+"/session/"+ed.sid+"/event", body)
	if code1 != 200 || code2 != 200 {
		t.Fatalf("codes %d %d (%s %s)", code1, code2, b1, b2)
	}
	appliedCount := 0
	if strings.Contains(b1, `"applied":true`) {
		appliedCount++
	}
	if strings.Contains(b2, `"applied":true`) {
		appliedCount++
	}
	if appliedCount != 1 {
		t.Fatalf("exactly one submit must win the CAS, got %d (%s / %s)", appliedCount, b1, b2)
	}
	// step_seq advanced exactly once past the input step (input=1, submit=2).
	var seqNow int64
	se.withConn(t, func(c *pgConn) {
		c.QueryRow(context.Background(), `SELECT step_seq FROM continuation WHERE id=$1`, []any{ed.sid}, &seqNow)
	})
	if seqNow != 2 {
		t.Fatalf("step_seq = %d, want 2 (input + one submit)", seqNow)
	}
	// The mutation happened exactly once (name is NEW, row_version bumped once).
	var name string
	var rv int64
	se.withConn(t, func(c *pgConn) {
		c.QueryRow(context.Background(),
			`SELECT name, row_version FROM `+quoteIdent(se.widgetTable())+` WHERE id=$1`, []any{id}, &name, &rv)
	})
	if name != "NEW" || rv != 1 {
		t.Fatalf("name=%q row_version=%d, want NEW/1 (one mutation)", name, rv)
	}
}

// --- concurrent edit: first commits; second gets conflict alert, draft intact -

func TestSessionConcurrentEdit(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")
	alertSlot := slotForField(t, se.srv, "app/rx/Widget", "form", "__alert__")

	// Two form sessions opened against the SAME row_version (0).
	e1 := se.mount(t, "app/rx/Widget/form/"+fmtID(id), "human:1", "acme")
	e2 := se.mount(t, "app/rx/Widget/form/"+fmtID(id), "human:2", "acme")

	e1.postEvent("input", nameForm, "FROM_ONE")
	r1 := e1.postEvent("submit", "", "")
	if applied, _ := r1["applied"].(bool); !applied {
		t.Fatalf("first submit not applied")
	}

	e2.postEvent("input", nameForm, "FROM_TWO")
	e2.postEvent("submit", "", "") // stale row_version ⇒ conflict

	// The row keeps the first writer's value; the second did not clobber.
	var name string
	se.withConn(t, func(c *pgConn) {
		c.QueryRow(context.Background(), `SELECT name FROM `+quoteIdent(se.widgetTable())+` WHERE id=$1`, []any{id}, &name)
	})
	if name != "FROM_ONE" {
		t.Fatalf("row name = %q, want FROM_ONE (no silent clobber)", name)
	}
	// e2's session carries the conflict alert AND its draft is intact.
	sess2 := se.loadSession(t, e2.sid)
	if !strings.Contains(sess2.UILocal["__alert__"], "changed") {
		t.Fatalf("e2 missing conflict alert: %q", sess2.UILocal["__alert__"])
	}
	if sess2.Draft["name"] != "FROM_TWO" {
		t.Fatalf("e2 draft not preserved: %+v", sess2.Draft)
	}
	_ = alertSlot
}

// loadSession decodes a session's current CFR for assertions.
func (se *sessionEnv) loadSession(t *testing.T, sid string) *sessionCFR {
	t.Helper()
	var framesHex string
	se.withConn(t, func(c *pgConn) {
		ok, err := c.QueryRow(context.Background(),
			`SELECT encode(frames,'hex') FROM continuation WHERE id=$1`, []any{sid}, &framesHex)
		if err != nil || !ok {
			t.Fatalf("load session frames: ok=%v err=%v", ok, err)
		}
	})
	st, err := decodeFramesHex(framesHex)
	if err != nil {
		t.Fatalf("decode session: %v", err)
	}
	sess, err := sessionFromState(st)
	if err != nil {
		t.Fatalf("session from state: %v", err)
	}
	return sess
}
