package kernel

// session_invalidation.go is the dependency-exact invalidation dispatcher
// (ADR-11 §6). Every admitted mutation commits `NOTIFY regel_invalidate` with
// (resource, rowId, horizon). This listener consumes it, resolves the matching
// sessions from the subscription table (subKey → set(session), rebuilt lazily per
// NOTIFY per ADR-06 cold-start rules), marks them dirty, and enqueues one
// invalidation per matching session. A bounded worker pool drains
// re-render→diff→frame; multiple invalidations for one session within a tick
// COALESCE to one drive ⇒ one step_seq ⇒ one frame (zero-op if nothing changed).
// Instruments sse.invalidation_depth and sse.fanout_lag_ms (ADR-13 §2).

import (
	"context"
	"strings"
	"sync"
	"time"
)

// invalidationChannel is the LISTEN/NOTIFY channel; payload = resource\x1frowId\x1fhorizon.
const invalidationChannel = "regel_invalidate"

// fanoutWorkers bounds the concurrent re-render→diff→frame drain (§6 bounded pool).
// D3 keeps this modest; D5 owns the 50k storm calibration.
const fanoutWorkers = 8

type dirtyItem struct {
	sessionID string
	enqueued  time.Time
}

type invalidationIndex struct {
	srv   *Server
	mu    sync.Mutex
	dirty map[string]time.Time // sessionID -> first-enqueued time (coalescing set)
	queue chan string
}

func newInvalidationIndex(srv *Server) *invalidationIndex {
	return &invalidationIndex{
		srv:   srv,
		dirty: map[string]time.Time{},
		queue: make(chan string, 4096),
	}
}

// listenLoop LISTENs on the invalidation channel and drives the bounded drain.
// pgwire surfaces async NOTIFY only on a round trip (no background reader), so this
// pings at a short cadence to pump them.
func (ix *invalidationIndex) listenLoop(ctx context.Context) {
	// Start the bounded worker pool.
	var wg sync.WaitGroup
	for i := 0; i < fanoutWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ix.worker(ctx)
		}()
	}
	defer wg.Wait()

	conn, err := ix.srv.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer ix.srv.pool.Release(conn)
	if err := conn.Listen(ctx, invalidationChannel); err != nil {
		return
	}
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if perr := conn.Ping(ctx); perr != nil {
				return
			}
			for {
				select {
				case n := <-conn.Notifications():
					if n.Channel == invalidationChannel {
						ix.onNotify(ctx, n.Payload)
					}
				default:
					goto drained
				}
			}
		drained:
		}
	}
}

// onNotify resolves the matching sessions and enqueues them (coalescing).
func (ix *invalidationIndex) onNotify(ctx context.Context, payload string) {
	parts := strings.Split(payload, "\x1f")
	if len(parts) != 3 {
		return
	}
	resource, rowID, horizon := parts[0], parts[1], parts[2]
	sessions, err := ix.matchingSessions(ctx, resource, rowID, horizon)
	if err != nil {
		return
	}
	for _, sid := range sessions {
		ix.enqueue(sid)
	}
}

// matchingSessions rebuilds the subKey→set(session) match from the subscription
// table: a session is woken if it subscribed to this row (key=rowId:<id>) OR this
// horizon (key=horizon:<hz>) of this resource — policy-respecting by construction
// (a session whose horizon excludes the row never subscribed to it, ADR-11 §6).
func (ix *invalidationIndex) matchingSessions(ctx context.Context, resource, rowID, horizon string) ([]string, error) {
	conn, err := ix.srv.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer ix.srv.pool.Release(conn)
	rows, err := conn.Query(ctx, `
SELECT DISTINCT session_id::text FROM subscription
 WHERE resource=$1 AND (key=$2 OR key=$3)`,
		resource, rowIDKey(rowID), horizonKey(horizon))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		out = append(out, sid)
	}
	return out, rows.Err()
}

// enqueue marks a session dirty and queues it, coalescing repeats within a tick.
func (ix *invalidationIndex) enqueue(sessionID string) {
	ix.mu.Lock()
	if _, already := ix.dirty[sessionID]; already {
		ix.mu.Unlock()
		return // coalesced — already pending
	}
	ix.dirty[sessionID] = time.Now()
	ix.mu.Unlock()
	addInvalDepth(1)
	select {
	case ix.queue <- sessionID:
	default:
		// Queue full: clear dirty so a later NOTIFY re-enqueues (never silently drop).
		ix.mu.Lock()
		delete(ix.dirty, sessionID)
		ix.mu.Unlock()
		addInvalDepth(-1)
	}
}

// worker drains the invalidation queue: for each dirty session it clears the dirty
// mark (so invalidations arriving DURING the drive re-enqueue → one more drive) and
// drives one invalidation step at the session's current step_seq.
func (ix *invalidationIndex) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case sid, open := <-ix.queue:
			if !open {
				return
			}
			ix.mu.Lock()
			enq := ix.dirty[sid]
			delete(ix.dirty, sid)
			ix.mu.Unlock()
			ix.driveInvalidation(ctx, sid)
			addInvalDepth(-1)
			if !enq.IsZero() {
				setFanoutLag(time.Since(enq).Milliseconds())
			}
		}
	}
}

// driveInvalidation reads the session's current step_seq and drives one invalidation
// step (a re-render→diff→frame). A dropped claim (session gone, or a concurrent
// event won) is fine — the next NOTIFY re-drives.
func (ix *invalidationIndex) driveInvalidation(ctx context.Context, sessionID string) {
	conn, err := ix.srv.pool.Acquire(ctx)
	if err != nil {
		return
	}
	var seq int64
	var status string
	found, err := conn.QueryRow(ctx,
		`SELECT step_seq, status FROM continuation WHERE id=$1 AND kind='session'`,
		[]any{sessionID}, &seq, &status)
	ix.srv.pool.Release(conn)
	if err != nil || !found {
		return
	}
	// Only an idle (sleeping/ready) session is invalidation-drivable; a running one
	// is mid-step and will re-render with fresh data anyway.
	if status != "sleeping" && status != "ready" {
		return
	}
	_, _, _ = ix.srv.driveSession(ctx, sessionID, seq, sessionMsg{Kind: "invalidate"})
}
