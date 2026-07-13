package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
)

// ReactorConfig tunes the reactor's loops (ADR-06 §5). Zero fields take defaults.
type ReactorConfig struct {
	PollInterval   time.Duration
	LeaseSeconds   int
	HeartbeatEvery time.Duration
	ReapBatch      int
	ReapEvery      time.Duration
	DrainBatch     int
	TimerBatch     int
}

func (c ReactorConfig) withDefaults() ReactorConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 250 * time.Millisecond
	}
	if c.LeaseSeconds <= 0 {
		c.LeaseSeconds = 30
	}
	if c.HeartbeatEvery <= 0 {
		c.HeartbeatEvery = 10 * time.Second
	}
	if c.ReapBatch <= 0 {
		c.ReapBatch = 100
	}
	if c.ReapEvery <= 0 {
		c.ReapEvery = time.Second
	}
	if c.DrainBatch <= 0 {
		c.DrainBatch = 64
	}
	if c.TimerBatch <= 0 {
		c.TimerBatch = 256
	}
	return c
}

// Reactor is the thin logical scheduler (ADR-06 §1): a fixed set of goroutines
// (timer scanner, drain, heartbeat, reaper, listener) over one Postgres pool,
// zero business logic. All loops stop on context cancel; an epoch fence trips a
// terminal drain that cancels them and marks the server draining (§6).
type Reactor struct {
	srv *Server
	cfg ReactorConfig

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	wake      chan struct{}
	fenceOnce sync.Once
}

// StartReactor launches the reactor loops (ADR-06 §5) bound to ctx. Returns a
// handle whose Stop() cancels and joins every loop cleanly.
func (s *Server) StartReactor(ctx context.Context, cfg ReactorConfig) *Reactor {
	rctx, cancel := context.WithCancel(ctx)
	r := &Reactor{srv: s, cfg: cfg.withDefaults(), cancel: cancel, wake: make(chan struct{}, 1)}
	r.wg.Add(5)
	go r.loop(rctx, r.cfg.PollInterval, r.drainOnce, true)      // 2. DRAIN
	go r.loop(rctx, r.cfg.PollInterval, r.timerOnce, false)     // 1. TIMER SCANNER
	go r.loop(rctx, r.cfg.HeartbeatEvery, r.heartbeatOnce, false) // 3. HEARTBEAT
	go r.loop(rctx, r.cfg.ReapEvery, r.reaperOnce, false)      // 4. REAPER
	go r.listenLoop(rctx)                                       // 5. LISTEN
	return r
}

// Stop cancels the reactor and waits for all loops to exit.
func (r *Reactor) Stop() {
	r.cancel()
	r.wg.Wait()
}

// loop runs fn every interval (and, if wakeable, whenever the drain is signalled)
// until ctx is done. Each fn is one bounded pass; a returned epoch fence trips the
// terminal drain and stops the reactor.
func (r *Reactor) loop(ctx context.Context, interval time.Duration, fn func(context.Context) error, wakeable bool) {
	defer r.wg.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	var wake <-chan struct{}
	if wakeable {
		wake = r.wake
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
		if err := fn(ctx); err != nil {
			var fence cfr.ErrEpochFence
			if errors.As(err, &fence) {
				r.trip(fence)
				return
			}
			// Other errors are transient (a lost race, a dropped conn): the next
			// tick retries. Never crash the reactor on a single failed pass.
		}
	}
}

// signalDrain nudges the drain loop without blocking.
func (r *Reactor) signalDrain() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// trip performs the ADR-06 §6 terminal drain: emit the structured event, mark the
// server draining (503 on new work), and cancel all loops.
func (r *Reactor) trip(fence cfr.ErrEpochFence) {
	r.fenceOnce.Do(func() {
		r.srv.draining.Store(true)
		ev := map[string]any{
			"event":             "epoch.fence_tripped",
			"observed_epoch":    fence.Observed,
			"required_epoch":    fence.Required,
			"kernel_id":         r.srv.kernelID,
			"ts":                time.Now().UTC().Format(time.RFC3339Nano),
			"action":            "drained_and_exited",
			"in_flight_aborted": true,
			"leases_released":   true,
		}
		b, _ := json.Marshal(ev)
		fmt.Fprintln(os.Stdout, string(b))
		r.cancel()
	})
}

// --- 1. TIMER SCANNER --------------------------------------------------------

func (r *Reactor) timerOnce(ctx context.Context) error {
	return nil // RED stub
}

// --- 2. DRAIN ----------------------------------------------------------------

type claimedTask struct {
	id       string
	contID   string
	seenSeq  int64
	attempts int
}

func (r *Reactor) drainOnce(ctx context.Context) error {
	return nil // RED stub
}

func (r *Reactor) claimTasks(ctx context.Context) ([]claimedTask, error) {
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer r.srv.pool.Release(conn)
	rows, err := conn.Query(ctx, `
UPDATE task SET status='running', lease_owner=$1::uuid,
       lease_until=now()+make_interval(secs=>$2), attempts=attempts+1
 WHERE id IN (
   SELECT id FROM task WHERE status='ready' AND kind='resume' AND run_at<=now()
   ORDER BY run_at FOR UPDATE SKIP LOCKED LIMIT $3)
RETURNING id::text, payload::text, attempts`,
		r.srv.kernelID, r.cfg.LeaseSeconds, r.cfg.DrainBatch)
	if err != nil {
		return nil, err
	}
	var out []claimedTask
	for rows.Next() {
		var id, payload string
		var attempts int
		if err := rows.Scan(&id, &payload, &attempts); err != nil {
			rows.Close()
			return nil, err
		}
		var p struct {
			ContinuationID string `json:"continuation_id"`
			StepSeq        int64  `json:"step_seq"`
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, claimedTask{id: id, contID: p.ContinuationID, seenSeq: p.StepSeq, attempts: attempts})
	}
	return out, rows.Err()
}

func (r *Reactor) runTask(ctx context.Context, t claimedTask) error {
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer r.srv.pool.Release(conn)

	env := r.srv.stepEnv(r.cfg.LeaseSeconds)
	resume := func(st *cek.State, d cek.Delivery, p cek.Principal) cek.Outcome {
		return r.srv.interp.Resume(ctx, st, d, p)
	}
	var claimed bool
	stepErr := cfr.RetrySerializable(ctx, "step", func(int) error {
		_, c, e := cfr.ClaimAndStep(ctx, conn, env, r.srv.interp, t.contID, t.seenSeq, resume)
		claimed = c
		return e
	})

	var fence cfr.ErrEpochFence
	if errors.As(stepErr, &fence) {
		return fence // trip the terminal drain
	}

	switch {
	case stepErr == nil:
		// Step committed, or a clean claim-loss (another kernel won / state moved).
		_ = claimed
		cfr.IncTaskDrained()
		return r.finishTask(ctx, t.id, "done")
	case t.attempts >= 5:
		// Attempt ceiling: dead task + a step.failed condition (ADR-06 §5).
		return r.deadTask(ctx, t, stepErr)
	default:
		// Transient failure: leave the task for retry.
		return r.finishTask(ctx, t.id, "ready")
	}
}

func (r *Reactor) finishTask(ctx context.Context, id, status string) error {
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer r.srv.pool.Release(conn)
	if status == "ready" {
		_, err = conn.Exec(ctx, `UPDATE task SET status='ready', lease_owner=NULL WHERE id=$1`, id)
	} else {
		_, err = conn.Exec(ctx, `UPDATE task SET status=$2, lease_owner=NULL WHERE id=$1`, id, status)
	}
	return err
}

func (r *Reactor) deadTask(ctx context.Context, t claimedTask, cause error) error {
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer r.srv.pool.Release(conn)
	if _, err := conn.Exec(ctx, `UPDATE task SET status='dead', lease_owner=NULL WHERE id=$1`, t.id); err != nil {
		return err
	}
	return cfr.RecordStepFailed(ctx, conn, t.contID, "task exceeded attempt ceiling: "+cause.Error())
}

// --- 3. HEARTBEAT ------------------------------------------------------------

func (r *Reactor) heartbeatOnce(ctx context.Context) error {
	return nil // RED stub
}

// --- 4. REAPER ---------------------------------------------------------------

func (r *Reactor) reaperOnce(ctx context.Context) error {
	return nil // RED stub
}

// --- 5. LISTEN ---------------------------------------------------------------

// listenLoop LISTENs on 'task' and nudges the drain early. pgwire surfaces async
// notifications only on a round trip (no background reader), so this loop pings at
// a short cadence to pump them — polling remains the correctness path (§5).
func (r *Reactor) listenLoop(ctx context.Context) {
	defer r.wg.Done()
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer r.srv.pool.Release(conn)
	if err := conn.Listen(ctx, "task"); err != nil {
		return
	}
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// A cheap round trip surfaces any queued NOTIFY.
			if perr := conn.Ping(ctx); perr != nil {
				return
			}
			select {
			case <-conn.Notifications():
				r.signalDrain()
			default:
			}
		}
	}
}
