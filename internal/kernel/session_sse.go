package kernel

// session_sse.go is the in-memory SSE fan-out (ADR-11 §2): a per-session bounded
// ring buffer of retained patch frames plus the set of live subscriber channels.
// The ring is the reconnect backlog — a Last-Event-ID within it replays exactly;
// a cursor older than the oldest retained frame is a gap that forces a full resync
// (§2). Frames are the owned binary codec (ui.EncodeFrame); the SSE handler base64s
// them onto the wire. Losing this cache loses nothing durable — the session row and
// its checkpoint are the truth (ADR-06 §3 cache-over-Postgres).

import (
	"sync"

	"regel.dev/regel/internal/ui"
)

// ringCap bounds the retained-frame backlog per session. A reconnect whose cursor
// predates this window resyncs (§2).
const ringCap = 256

type ringFrame struct {
	seq  uint64
	data []byte // ui.EncodeFrame bytes
}

type sseSession struct {
	mu      sync.Mutex
	ring    []ringFrame
	subs    map[int]chan ringFrame
	nextSub int
}

type sseHub struct {
	mu       sync.Mutex
	sessions map[string]*sseSession
}

func newSSEHub() *sseHub { return &sseHub{sessions: map[string]*sseSession{}} }

func (h *sseHub) ensure(sessionID string) *sseSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sessionID]
	if s == nil {
		s = &sseSession{subs: map[int]chan ringFrame{}}
		h.sessions[sessionID] = s
	}
	return s
}

func (h *sseHub) get(sessionID string) *sseSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[sessionID]
}

func (h *sseHub) drop(sessionID string) {
	h.mu.Lock()
	s := h.sessions[sessionID]
	delete(h.sessions, sessionID)
	h.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	for id, ch := range s.subs {
		close(ch)
		delete(s.subs, id)
	}
	s.mu.Unlock()
}

// push encodes and retains a frame, fanning it out to every live subscriber. A
// zero-op frame is retained and fanned like any other — it advances the cursor
// (§2 empty-diff invariant).
func (h *sseHub) push(sessionID string, f ui.Frame) {
	s := h.ensure(sessionID)
	rf := ringFrame{seq: f.EventSeq, data: ui.EncodeFrame(f)}
	s.mu.Lock()
	s.ring = append(s.ring, rf)
	if len(s.ring) > ringCap {
		s.ring = s.ring[len(s.ring)-ringCap:]
	}
	for _, ch := range s.subs {
		select {
		case ch <- rf:
		default: // a slow subscriber drops the live frame; its cursor gap resyncs on reconnect
		}
	}
	s.mu.Unlock()
}

// subscribe registers a live subscriber and returns its channel, the retained
// backlog with seq>cursor, and whether the cursor was STALE (a gap beyond the
// retained window ⇒ the caller signals a resync). id is used to unsubscribe.
func (s *sseSession) subscribe(cursor uint64) (id int, ch chan ringFrame, backlog []ringFrame, stale bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = s.nextSub
	s.nextSub++
	ch = make(chan ringFrame, ringCap)
	s.subs[id] = ch
	// Determine the oldest retained seq. A cursor > 0 that is older than
	// (oldest-1) missed frames the ring no longer holds ⇒ stale ⇒ resync.
	if len(s.ring) > 0 {
		oldest := s.ring[0].seq
		if cursor+1 < oldest {
			stale = true
		}
		for _, rf := range s.ring {
			if rf.seq > cursor {
				backlog = append(backlog, rf)
			}
		}
	}
	return id, ch, backlog, stale
}

func (s *sseSession) unsubscribe(id int) {
	s.mu.Lock()
	if ch, ok := s.subs[id]; ok {
		close(ch)
		delete(s.subs, id)
	}
	s.mu.Unlock()
}
