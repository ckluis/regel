package kernel

// session_test.go is the BUILD-D increment D3 red-path battery (ADR-11 Red-Path
// Tests Implied), driven HTTP/SSE-level via httptest against a live kernel + real
// Postgres — no browser. It includes a small "browser-shaped" Go client harness
// (SSE decode + slot map + FNV-1a-64 digest mirror) that D5 reuses for wan-150.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/ui"
)

// widgetSrc declares a resource with an org (policy predicate column), two plain
// fields, and a pii field (the masking kill-test target).
func widgetSrc(module string) string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Widget = resource({
  fields: { org: "text", name: "text", score: "number", email: "pii:email" },
  policy: orgScoped,
});
`
}

// --- session env: kernel + httptest + reactor/invalidation loops -------------

type sessionEnv struct {
	*reactorEnv
	ts   *httptest.Server
	stop func()
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	e := newReactorEnv(t)
	ts := httptest.NewServer(e.srv.Handler())
	stop := e.srv.StartSessions(context.Background())
	t.Cleanup(func() { stop(); ts.Close() })
	return &sessionEnv{reactorEnv: e, ts: ts, stop: stop}
}

// pgConn aliases the pool connection type for terse test helpers.
type pgConn = pgwire.Conn

// widgetTable returns the physical table for the admitted Widget resource.
func (se *sessionEnv) widgetTable() string { return "res_" + tblSlug("app/rx/Widget") }

func tblSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// --- browser-shaped client harness -------------------------------------------

type harness struct {
	t        *testing.T
	base     string
	sid      string
	cursor   uint64
	slots    map[string]string
	digest   ui.Digest
	resyncs  int
	corrupt  string // slotId to corrupt on next applied frame ("" = none)
}

var slotRE = regexp.MustCompile(`data-slot="([^"]+)"[^>]*>([^<]*)<`)

// mount does GET /ui and seeds the harness slot map + digest from first paint.
func (se *sessionEnv) mount(t *testing.T, view, principal, horizon string) *harness {
	t.Helper()
	req, _ := http.NewRequest("GET", se.ts.URL+"/ui/"+view, nil)
	req.Header.Set("X-Regel-Actor", principal)
	req.Header.Set("X-Regel-Horizon", horizon)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mount %s: %v", view, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("mount %s: %d %s", view, resp.StatusCode, body)
	}
	sid := resp.Header.Get("X-Regel-Session")
	h := &harness{t: t, base: se.ts.URL, sid: sid, slots: map[string]string{}}
	// Seed slot values from first-paint HTML (display text of data-slot elements).
	for _, m := range slotRE.FindAllStringSubmatch(string(body), -1) {
		if strings.Contains(m[0], "data-list") {
			continue
		}
		h.slots[m[1]] = htmlUnescape(m[2])
	}
	h.digest = ui.FullDigest(h.slots)
	return h
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`)
	return r.Replace(s)
}

// postEvent POSTs one event at the harness cursor and returns the JSON response.
func (h *harness) postEvent(event, slotID, value string) map[string]any {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{"slotId": slotID, "event": event, "value": value, "eventSeq": h.cursor})
	resp, err := http.Post(h.base+"/session/"+h.sid+"/event", "application/json", strings.NewReader(string(body)))
	if err != nil {
		h.t.Fatalf("postEvent: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	if applied, _ := out["applied"].(bool); applied {
		if s, ok := out["eventSeq"].(float64); ok {
			h.cursor = uint64(s)
		}
	}
	return out
}

// assertNoFrame fails if any frame arrives within d.
func (c *sseConn) assertNoFrame(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case f := <-c.frames:
		t.Fatalf("unexpected frame %+v", f)
	case <-time.After(d):
	}
}

// applyFrame mirrors the client (duty b/d): apply ops to the slot map, update the
// incremental digest, advance the cursor. A corrupt target injects a wrong value
// to force a divergence.
func (h *harness) applyFrame(f ui.Frame) {
	for _, op := range f.Ops {
		switch op.Kind {
		case ui.OpSetText, ui.OpSetValue:
			val := op.Payload
			if h.corrupt == op.SlotID {
				val = op.Payload + "~CORRUPT"
				h.corrupt = ""
			}
			h.setSlot(op.SlotID, val)
		case ui.OpSpliceList:
			for _, sp := range op.Splices {
				switch sp.Kind {
				case ui.SpliceAdd:
					for id, v := range parseRowSlots(sp.HTML) {
						h.setSlot(id, v)
					}
				case ui.SpliceRemove:
					for id := range h.slots {
						if strings.HasSuffix(id, "#"+sp.Key) {
							h.removeSlot(id)
						}
					}
				}
			}
		}
	}
	h.cursor = f.EventSeq
}

func parseRowSlots(html string) map[string]string {
	out := map[string]string{}
	for _, m := range slotRE.FindAllStringSubmatch(html, -1) {
		out[m[1]] = htmlUnescape(m[2])
	}
	return out
}

func (h *harness) setSlot(id, val string) {
	if old, ok := h.slots[id]; ok {
		h.digest = h.digest.Set(id, old, val)
	} else {
		h.digest = h.digest.Add(id, val)
	}
	h.slots[id] = val
}
func (h *harness) removeSlot(id string) {
	if old, ok := h.slots[id]; ok {
		h.digest = h.digest.Remove(id, old)
		delete(h.slots, id)
	}
}

// resync mirrors the client's divergence recovery: POST /resync, adopt the fresh
// snapshot, recompute the digest.
func (h *harness) resync() {
	h.resyncs++
	resp, err := http.Post(h.base+"/session/"+h.sid+"/resync", "application/json", nil)
	if err != nil {
		h.t.Fatalf("resync: %v", err)
	}
	defer resp.Body.Close()
	var res resyncResult
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &res)
	h.slots = map[string]string{}
	for k, v := range res.Snapshot {
		h.slots[k] = v
	}
	h.digest = ui.FullDigest(h.slots)
	h.cursor = res.EventSeq
}

// --- SSE reader --------------------------------------------------------------

type sseConn struct {
	frames  chan ui.Frame
	resyncs chan struct{}
	body    io.ReadCloser
}

func (h *harness) openSSE(cursor uint64) *sseConn {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.base+"/session/"+h.sid+"/events?last_event_id="+strconv.FormatUint(cursor, 10), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("openSSE: %v", err)
	}
	c := &sseConn{frames: make(chan ui.Frame, 64), resyncs: make(chan struct{}, 8), body: resp.Body}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var event string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data := strings.TrimPrefix(line, "data: ")
				if event == "resync" {
					select {
					case c.resyncs <- struct{}{}:
					default:
					}
					event = ""
					continue
				}
				raw, derr := base64.StdEncoding.DecodeString(data)
				if derr != nil {
					continue
				}
				f, ferr := ui.DecodeFrame(raw)
				if ferr == nil {
					c.frames <- f
				}
			case line == "":
				event = ""
			}
		}
	}()
	return c
}

func (c *sseConn) close() { c.body.Close() }

// nextFrame waits for one frame or fails.
func (c *sseConn) nextFrame(t *testing.T, d time.Duration) ui.Frame {
	t.Helper()
	select {
	case f := <-c.frames:
		return f
	case <-time.After(d):
		t.Fatalf("no SSE frame within %s", d)
		return ui.Frame{}
	}
}

// --- helpers -----------------------------------------------------------------

func (se *sessionEnv) admitWidget(t *testing.T) {
	t.Helper()
	v := se.admit(t, widgetSrc("app/rx"), "app/rx", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Widget: %q (%+v)", v.Outcome, v.Diagnostics)
	}
}

func (se *sessionEnv) seedWidget(t *testing.T, org, name string, score int) int64 {
	t.Helper()
	tbl := se.widgetTable()
	var id int64
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(context.Background(),
			`INSERT INTO `+quoteIdent(tbl)+` (org, name, score) VALUES ($1,$2,$3) RETURNING id`,
			[]any{org, name, score}, &id); err != nil {
			t.Fatalf("seed: %v", err)
		}
	})
	return id
}

func slotForField(t *testing.T, srv *Server, resource, kind, field string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := srv.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.pool.Release(conn)
	vm, err := loadViewMeta(ctx, conn, resource)
	if err != nil {
		t.Fatal(err)
	}
	for _, sl := range vm.template(kind).Slots {
		if sl.Field == field {
			return sl.ID
		}
	}
	t.Fatalf("no %s slot for field %q", kind, field)
	return ""
}

var _ = fmt.Sprintf
