package kernel

// session_http.go is the ADR-11 §2 HTTP surface: GET /ui (mount + first paint),
// GET /session/{id}/events (SSE down), POST /session/{id}/event (POST up), POST
// /session/{id}/resync (full re-render). SSE frames carry id=eventSeq and
// data=base64(EncodeFrame); heartbeats are `:keepalive` comments that never carry
// an id (they never advance the client cursor, §2).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/ui"
)

// heartbeatEvery is the SSE keepalive interval (§2: comments, never a cursor move).
const heartbeatEvery = 15 * time.Second

// --- GET /ui/{view...} : mount ------------------------------------------------

func (s *Server) handleMount(w http.ResponseWriter, r *http.Request) {
	view := r.PathValue("view")
	principal := sessionPrincipal(r)
	horizon := sessionHorizon(r)
	// BUILD-E D3: ?component=<catalogName> overlays a hand-authored component into
	// the (detail) slot over the view's resource row.
	component := r.URL.Query().Get("component")
	// BUILD-E scenario d: ?as_of=<RFC3339> resolves the template AS-OF that instant
	// (append-only derived_artifact), so the first paint renders the schema/behavior
	// the world had then — a rollback observed through the UI. Absent ⇒ head (live).
	var asOf *time.Time
	if a := r.URL.Query().Get("as_of"); a != "" {
		t, e := time.Parse(time.RFC3339, a)
		if e != nil {
			http.Error(w, "mount: bad as_of: "+e.Error(), http.StatusBadRequest)
			return
		}
		asOf = &t
	}
	res, err := s.mountSession(r.Context(), view, principal, horizon, component, asOf)
	if err != nil {
		http.Error(w, "mount: "+err.Error(), http.StatusBadRequest)
		return
	}
	page := mountPage(res.SessionID, res.EventSeq, res.HTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Regel-Session", res.SessionID)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, page)
}

// mountPage wraps the first-paint HTML with the session id, the initial eventSeq
// cursor, and the ~15KB client (loaded from /session/client.js).
func mountPage(sessionID string, eventSeq int64, body string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<title>regel</title></head><body>`)
	b.WriteString(`<div id="rg-root" data-session="`)
	b.WriteString(escapeAttrHTML(sessionID))
	b.WriteString(`" data-seq="`)
	b.WriteString(strconv.FormatInt(eventSeq, 10))
	b.WriteString(`">`)
	b.WriteString(body)
	b.WriteString(`</div><script src="/session/client.js"></script></body></html>`)
	return b.String()
}

func escapeAttrHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// sessionPrincipal binds the render principal (X-Regel-Actor, dev stub).
func sessionPrincipal(r *http.Request) string {
	if h := r.Header.Get("X-Regel-Actor"); h != "" {
		return h
	}
	return "human:anon"
}

// sessionHorizon binds the org horizon (X-Regel-Horizon, dev stub); "" ⇒ the
// single global horizon "*".
func sessionHorizon(r *http.Request) string {
	if h := r.Header.Get("X-Regel-Horizon"); h != "" {
		return h
	}
	return "*"
}

// --- GET /session/{id}/events : SSE down --------------------------------------

func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sess := s.hub.ensure(sessionID)
	cursor := lastEventID(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	subID, ch, backlog, stale := sess.subscribe(cursor)
	defer sess.unsubscribe(subID)

	if stale {
		// Cursor predates the retained buffer (§2): direct the client to resync.
		incResyncs()
		_, _ = io.WriteString(w, "event: resync\ndata: stale-cursor\n\n")
		flusher.Flush()
	}
	for _, rf := range backlog {
		writeFrame(w, rf)
	}
	flusher.Flush()

	hb := time.NewTicker(heartbeatEvery)
	defer hb.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case rf, open := <-ch:
			if !open {
				return
			}
			writeFrame(w, rf)
			flusher.Flush()
		case <-hb.C:
			// A heartbeat is a comment with NO id — it never advances the cursor (§2).
			_, _ = io.WriteString(w, ":keepalive\n\n")
			flusher.Flush()
		}
	}
}

func writeFrame(w io.Writer, rf ringFrame) {
	_, _ = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", rf.seq, base64.StdEncoding.EncodeToString(rf.data))
}

// lastEventID reads the reconnect cursor from the Last-Event-ID header or the
// last_event_id query param (EventSource sets the header; the query aids tests).
func lastEventID(r *http.Request) uint64 {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("last_event_id")
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	return n
}

// --- POST /session/{id}/event : POST up ---------------------------------------

func (s *Server) handleSessionEvent(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req struct {
		SlotID   string `json:"slotId"`
		Event    string `json:"event"`
		Value    string `json:"value"`
		EventSeq int64  `json:"eventSeq"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad event json: " + err.Error()})
		return
	}
	msg := sessionMsg{Kind: "event", SlotID: req.SlotID, Event: req.Event, Value: req.Value}
	frame, claimed, err := s.driveSession(r.Context(), sessionID, req.EventSeq, msg)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if !claimed {
		// Double-fire / stale cursor: dropped idempotently (§5). Report current seq.
		writeJSON(w, 200, map[string]any{"applied": false, "eventSeq": req.EventSeq})
		return
	}
	writeJSON(w, 200, map[string]any{"applied": true, "eventSeq": frame.EventSeq, "ops": len(frame.Ops)})
}

// --- POST /session/{id}/resync : full re-render -------------------------------

func (s *Server) handleSessionResync(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	res, err := s.resyncSession(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	incResyncs()
	writeJSON(w, 200, res)
}

// resyncResult is a full-resync payload: a fresh skeleton, the fresh slot snapshot
// map (display values the client adopts wholesale), the freshly-summed digest, and
// the current cursor. It does NOT advance step_seq (a repaint, not a checkpoint).
type resyncResult struct {
	EventSeq uint64            `json:"eventSeq"`
	Digest   uint64            `json:"digest"`
	HTML     string            `json:"html"`
	Snapshot map[string]string `json:"snapshot"` // slotId -> display value
}

func (s *Server) resyncSession(ctx context.Context, sessionID string) (resyncResult, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return resyncResult{}, err
	}
	defer s.pool.Release(conn)

	// The resync re-render reads the continuation, the template artifact, the mask
	// context, and the data rows — one snapshot pins them (L7), so a resync issued
	// while an admission commits a template flip cannot cross-wire the rebuilt frame.
	var (
		seq   int64
		html  string
		state map[string]ui.Materialized
	)
	if rerr := serveReadSnapshot(ctx, conn, func() error {
		var framesHex string
		found, e := conn.QueryRow(ctx,
			`SELECT encode(frames,'hex'), step_seq FROM continuation WHERE id=$1 AND kind='session'`,
			[]any{sessionID}, &framesHex, &seq)
		if e != nil {
			return e
		}
		if !found {
			return fmt.Errorf("session: no such session %q", sessionID)
		}
		st, derr := decodeFramesHex(framesHex)
		if derr != nil {
			return derr
		}
		sess, serr := sessionFromState(st)
		if serr != nil {
			return serr
		}
		// Resync/live steps resolve the HEAD template (asOf nil). An as-of mount is a
		// read-only historical FIRST PAINT (BUILD-E scenario d); subsequent live steps
		// track head — documented cut, not a correctness gap for a point-in-time view.
		vm, verr := loadViewMeta(ctx, conn, sess.Resource, nil)
		if verr != nil {
			return verr
		}
		mc, merr := admission.BuildMaskCtx(ctx, conn, sess.Principal)
		if merr != nil {
			return merr
		}
		html, state, _, _, _, err = renderView(ctx, conn, vm, sess, mc, nil)
		return err
	}); rerr != nil {
		return resyncResult{}, rerr
	}
	disp := displayMapOf(state)
	return resyncResult{
		EventSeq: uint64(seq),
		Digest:   uint64(ui.FullDigest(disp)),
		HTML:     html,
		Snapshot: disp,
	}, nil
}

// --- GET /session/client.js ---------------------------------------------------

func (s *Server) handleClientJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = io.WriteString(w, clientJS)
}

// StartSessions launches the reactive-layer background loops (ADR-11 §5/§6): the
// invalidation LISTEN loop and the idle-TTL session sweeper. Torn down on ctx
// cancel; returns a stop function.
func (s *Server) StartSessions(ctx context.Context) (stop func()) {
	cctx, cancel := context.WithCancel(ctx)
	go s.invIndex.listenLoop(cctx)
	go s.sweepLoop(cctx)
	return cancel
}

// sweepLoop deletes idle sessions (30-min TTL from updated_at): the row, its
// subscriptions (ON DELETE CASCADE), and its SSE ring (ADR-11 §5).
func (s *Server) sweepLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweepIdleSessions(ctx)
		}
	}
}

func (s *Server) sweepIdleSessions(ctx context.Context) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer s.pool.Release(conn)
	rows, err := conn.Query(ctx, `
SELECT id::text FROM continuation
 WHERE kind='session' AND status IN ('sleeping','ready')
   AND updated_at < now() - make_interval(secs=>$1)`, int(idleTTL.Seconds()))
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		s.closeSession(ctx, conn, id)
	}
}

// closeSession removes a session and its dependents (message claims + subscriptions
// cascade), and drops its SSE ring.
func (s *Server) closeSession(ctx context.Context, conn *pgwire.Conn, id string) {
	_, _ = conn.Exec(ctx, `UPDATE channel_message SET claimed_by=NULL WHERE claimed_by=$1`, id)
	_, _ = conn.Exec(ctx, `DELETE FROM continuation WHERE id=$1 AND kind='session'`, id)
	s.hub.drop(id)
}

// decodeFramesHex hex-decodes a session's frames blob and CFR-decodes it to state.
func decodeFramesHex(framesHex string) (*cek.State, error) {
	raw := make([]byte, len(framesHex)/2)
	for i := 0; i < len(raw); i++ {
		hi := hexNibble(framesHex[i*2])
		lo := hexNibble(framesHex[i*2+1])
		if hi < 0 || lo < 0 {
			return nil, fmt.Errorf("session: bad frames hex")
		}
		raw[i] = byte(hi<<4 | lo)
	}
	return cfr.Decode(raw)
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
