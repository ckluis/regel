package kernel

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- size cap (§5): accrete UI state past 256KB ⇒ truncate, preserve draft -----

func TestSessionSizeCap(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")

	ed := se.mount(t, "app/rx/Widget/form/"+fmtID(id), "human:e", "acme")
	ed.postEvent("input", nameForm, "KEEPME") // a form draft to preserve

	// Accrete UI-local state past the 256KB cap in one event.
	big := strings.Repeat("Z", 300*1024)
	ed.postEvent("click", "detail.bloat", big)

	sess := se.loadSession(t, ed.sid)
	if sess.Draft["name"] != "KEEPME" {
		t.Fatalf("size-cap truncation lost the form draft: %+v", sess.Draft)
	}
	if len(sess.LastSnap) != 0 {
		t.Fatalf("size-cap truncation should drop the retained snapshot, got %d slots", len(sess.LastSnap))
	}
	if !strings.Contains(sess.UILocal["__alert__"], "truncated") {
		t.Fatalf("size-cap breach should raise the truncation alert: %+v", sess.UILocal)
	}
	// The stored CFR is back under the cap.
	frames, _ := sess.frames()
	if len(frames) > sessionCapBytes {
		t.Fatalf("post-truncation CFR still %d bytes (> cap)", len(frames))
	}
}

// --- small invalidation burst: many sessions, one mutation ⇒ all patched -------

func TestSessionInvalidationBurst(t *testing.T) {
	if testing.Short() {
		t.Skip("burst is an integration test")
	}
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")

	const N = 200
	sids := make([]string, 0, N)
	for i := 0; i < N; i++ {
		h := se.mount(t, "app/rx/Widget/detail/"+fmtID(id), "human:a", "acme")
		sids = append(sids, h.sid)
	}
	// One mutation to row 1 ⇒ NOTIFY ⇒ every subscribed session coalesces to one
	// re-render and is patched (step_seq advances to 1).
	ed := se.mount(t, "app/rx/Widget/form/"+fmtID(id), "human:e", "acme")
	ed.postEvent("input", nameForm, "BURST")
	ed.postEvent("submit", "", "")

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n := se.intScalar(t,
			`SELECT count(*) FROM continuation WHERE id = ANY($1::uuid[]) AND step_seq >= 1`, sids)
		if n >= N {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	patched := se.intScalar(t,
		`SELECT count(*) FROM continuation WHERE id = ANY($1::uuid[]) AND step_seq >= 1`, sids)
	if patched != N {
		t.Fatalf("patched sessions = %d, want %d", patched, N)
	}
	// Coalescing: no session over-advanced far beyond one step from one mutation.
	maxSeq := se.intScalar(t, `SELECT coalesce(max(step_seq),0) FROM continuation WHERE id = ANY($1::uuid[])`, sids)
	if maxSeq > 3 {
		t.Fatalf("a single mutation drove a session to step_seq %d — coalescing failed", maxSeq)
	}
	// The queue drained back to (near) empty.
	if d := sseMetricsSnapshot().InvalidationDepth; d < 0 {
		t.Fatalf("invalidation_depth negative: %d", d)
	}
	t.Logf("burst: %d sessions patched, maxSeq=%d, fanout_lag=%dms",
		patched, maxSeq, sseMetricsSnapshot().FanoutLagMS)
}

// --- table view: first paint + per-cell invalidation --------------------------

func TestSessionTableView(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id1 := se.seedWidget(t, "acme", "alpha", 1)
	_ = se.seedWidget(t, "acme", "beta", 2)
	nameForm := slotForField(t, se.srv, "app/rx/Widget", "form", "name")

	// Mount the table view; first paint lists both org=acme rows.
	req := se.ts.URL + "/ui/app/rx/Widget/table"
	code, body, hdr := getWith(t, req, "human:a", "acme")
	if code != 200 {
		t.Fatalf("mount table: %d %s", code, body)
	}
	if !strings.Contains(body, `data-key="`+fmtID(id1)+`"`) || !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Fatalf("table first paint missing rows:\n%s", body)
	}
	sid := hdr.Get("X-Regel-Session")
	tbl := &harness{t: t, base: se.ts.URL, sid: sid}
	c := tbl.openSSE(0)
	defer c.close()
	time.Sleep(120 * time.Millisecond)

	// Mutate row 1's name in horizon acme ⇒ the table (horizon subscriber) is patched.
	ed := se.mount(t, "app/rx/Widget/form/"+fmtID(id1), "human:e", "acme")
	ed.postEvent("input", nameForm, "ALPHA2")
	ed.postEvent("submit", "", "")

	f := c.nextFrame(t, 4*time.Second)
	found := false
	for _, op := range f.Ops {
		if op.Payload == "ALPHA2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("table did not receive the row-1 cell patch: %+v", f)
	}
}

// getWith does a GET with actor + horizon headers.
func getWith(t *testing.T, url, actor, horizon string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Regel-Actor", actor)
	req.Header.Set("X-Regel-Horizon", horizon)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}
