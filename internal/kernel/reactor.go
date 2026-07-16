package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
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

	// Reap-rate breaker (ADR-13 §5). Zero fields take defaults (window 60s,
	// cooldown 30s, rateMax 1000/window, probe 10).
	ReapRateMax     int
	BreakerWindow   time.Duration
	BreakerCooldown time.Duration
	ProbeBatch      int
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
	breaker   *reaperBreaker
}

// StartReactor launches the reactor loops (ADR-06 §5) bound to ctx. Returns a
// handle whose Stop() cancels and joins every loop cleanly.
func (s *Server) StartReactor(ctx context.Context, cfg ReactorConfig) *Reactor {
	rctx, cancel := context.WithCancel(ctx)
	r := &Reactor{srv: s, cfg: cfg.withDefaults(), cancel: cancel, wake: make(chan struct{}, 1),
		breaker: newReaperBreaker(cfg.withDefaults())}
	s.breaker.Store(r.breaker)
	r.wg.Add(7)
	go r.loop(rctx, r.cfg.PollInterval, r.drainOnce, true)        // 2. DRAIN
	go r.loop(rctx, r.cfg.PollInterval, r.timerOnce, false)       // 1. TIMER SCANNER
	go r.loop(rctx, r.cfg.HeartbeatEvery, r.heartbeatOnce, false) // 3. HEARTBEAT
	go r.loop(rctx, r.cfg.ReapEvery, r.reaperOnce, false)         // 4. REAPER
	go r.loop(rctx, r.cfg.PollInterval, r.dispatchOnce, false)    // 6. DISPATCH (outbox)
	go r.loop(rctx, r.cfg.PollInterval, r.cronOnce, false)        // 7. CRON (ADR-06 cron kind)
	go r.listenLoop(rctx)                                         // 5. LISTEN
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
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer r.srv.pool.Release(conn)
	res, err := conn.Exec(ctx, `
WITH due AS (
  SELECT id FROM continuation
  WHERE status='sleeping' AND wake->>'kind'='timer'
    AND wake->>'due' <= to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
  ORDER BY wake->>'due' LIMIT $1 FOR UPDATE SKIP LOCKED
), up AS (
  UPDATE continuation c SET status='ready', updated_at=now()
  FROM due WHERE c.id=due.id
  RETURNING c.id, c.step_seq
)
INSERT INTO task (id, kind, run_at, payload)
SELECT gen_random_uuid(), 'resume', now(),
  jsonb_build_object('continuation_id', id::text, 'step_seq', step_seq)
FROM up`, r.cfg.TimerBatch)
	if err != nil {
		return err
	}
	if res.RowsAffected > 0 {
		_, _ = conn.Exec(ctx, `NOTIFY task`)
		r.signalDrain()
	}
	return nil
}

// --- 2. DRAIN ----------------------------------------------------------------

type claimedTask struct {
	id       string
	contID   string
	seenSeq  int64
	attempts int
}

func (r *Reactor) drainOnce(ctx context.Context) error {
	tasks, err := r.claimTasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := r.runTask(ctx, t); err != nil {
			return err // epoch fence bubbles up to trip
		}
	}
	if len(tasks) == r.cfg.DrainBatch {
		r.signalDrain() // batch was full — keep draining
	}
	return nil
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
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer r.srv.pool.Release(conn)
	secs := r.cfg.LeaseSeconds
	if _, err := conn.Exec(ctx, `
UPDATE task SET lease_until=now()+make_interval(secs=>$1)
 WHERE lease_owner=$2::uuid AND status='running'`, secs, r.srv.kernelID); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
UPDATE continuation SET lease_until=now()+make_interval(secs=>$1)
 WHERE lease_owner=$2::uuid AND status='running'`, secs, r.srv.kernelID)
	return err
}

// --- 4. REAPER ---------------------------------------------------------------

func (r *Reactor) reaperOnce(ctx context.Context) error {
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer r.srv.pool.Release(conn)

	// Measure the oldest expired-lease lag BEFORE re-offering (the signal that
	// climbs and alarms when the breaker pauses re-offers, ADR-13 §5).
	lagMS := r.reapLagMS(ctx, conn)

	// The breaker decides how many rows this pass may re-offer (0 ⇒ paused OPEN).
	batch := r.breaker.allowedBatch()
	if batch <= 0 {
		r.breaker.observe(0, 0, lagMS) // record the lag while paused
		return nil
	}

	// Expired running tasks → re-offer (ready). RETURNING attempts lets us count
	// RE-EXPIRIES (attempts>1: work whose fresh lease already expired before).
	rows, err := conn.Query(ctx, `
WITH exp AS (
  SELECT id FROM task WHERE status='running' AND lease_until<now()
  ORDER BY lease_until LIMIT $1 FOR UPDATE SKIP LOCKED
), up AS (
  UPDATE task t SET status='ready', lease_owner=NULL FROM exp
  WHERE t.id=exp.id RETURNING t.attempts
)
SELECT attempts FROM up`, batch)
	if err != nil {
		return err
	}
	reoffered, reexpired := 0, 0
	for rows.Next() {
		var attempts int
		if err := rows.Scan(&attempts); err != nil {
			rows.Close()
			return err
		}
		reoffered++
		if attempts > 1 {
			reexpired++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if reoffered > 0 {
		cfr.IncReoffers(int64(reoffered))
	}

	// Expired running continuations → ready + fresh resume task with CURRENT
	// step_seq (the old task's payload seq is stale by design). Batch is what the
	// breaker permits, minus what the task re-offers already spent.
	remaining := batch - reoffered
	if remaining < 0 {
		remaining = 0
	}
	res2, err := conn.Exec(ctx, `
WITH exp AS (
  SELECT id, step_seq FROM continuation WHERE status='running' AND lease_until<now()
  ORDER BY lease_until LIMIT $1 FOR UPDATE SKIP LOCKED
), up AS (
  UPDATE continuation c SET status='ready', lease_owner=NULL, updated_at=now()
  FROM exp WHERE c.id=exp.id
  RETURNING c.id, c.step_seq
)
INSERT INTO task (id, kind, run_at, payload)
SELECT gen_random_uuid(), 'resume', now(),
  jsonb_build_object('continuation_id', id::text, 'step_seq', step_seq)
FROM up`, remaining)
	if err != nil {
		return err
	}
	contReoffers := int(res2.RowsAffected)
	if contReoffers > 0 {
		cfr.IncReoffers(int64(contReoffers))
		_, _ = conn.Exec(ctx, `NOTIFY task`)
		r.signalDrain()
	}

	r.breaker.observe(reoffered+contReoffers, reexpired, lagMS)
	return nil
}

// reapLagMS returns the age in ms of the oldest expired lease across tasks and
// continuations (0 when nothing is expired) — the reaper.lag_ms signal.
func (r *Reactor) reapLagMS(ctx context.Context, conn *pgwire.Conn) int64 {
	var lag int64
	_, _ = conn.QueryRow(ctx, `
SELECT COALESCE(EXTRACT(EPOCH FROM (now() - min(lease_until)))*1000, 0)::bigint
FROM (
  SELECT lease_until FROM task WHERE status='running' AND lease_until<now()
  UNION ALL
  SELECT lease_until FROM continuation WHERE status='running' AND lease_until<now()
) x`, nil, &lag)
	if lag < 0 {
		lag = 0
	}
	return lag
}

// --- 6. DISPATCH (outbox delivery, ADR-06 §5) --------------------------------

type claimedDeliver struct {
	id       string
	intentID string
	dedupKey string
	attempts int
}

// dispatchOnce claims a batch of 'deliver' tasks (SKIP LOCKED — one dispatcher per
// task) and pushes each intent across the process boundary via the pluggable sink,
// marking the outbox row delivered EXACTLY once under the dedup key. Effectively-
// once (ADR-05 §7): a crash between the sink call and the mark leaves the task
// running with an expiring lease, so the reaper re-offers it and the intent is
// redelivered once — never lost; an already-delivered row is never re-marked.
func (r *Reactor) dispatchOnce(ctx context.Context) error {
	tasks, err := r.claimDeliverTasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := r.deliverTask(ctx, t); err != nil {
			return err
		}
	}
	if len(tasks) == r.cfg.DrainBatch {
		r.signalDrain()
	}
	return nil
}

func (r *Reactor) claimDeliverTasks(ctx context.Context) ([]claimedDeliver, error) {
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer r.srv.pool.Release(conn)
	rows, err := conn.Query(ctx, `
UPDATE task SET status='running', lease_owner=$1::uuid,
       lease_until=now()+make_interval(secs=>$2), attempts=attempts+1
 WHERE id IN (
   SELECT id FROM task WHERE status='ready' AND kind='deliver' AND run_at<=now()
   ORDER BY run_at FOR UPDATE SKIP LOCKED LIMIT $3)
RETURNING id::text, payload->>'intent_id', payload->>'dedup_key', attempts`,
		r.srv.kernelID, r.cfg.LeaseSeconds, r.cfg.DrainBatch)
	if err != nil {
		return nil, err
	}
	var out []claimedDeliver
	for rows.Next() {
		var c claimedDeliver
		if err := rows.Scan(&c.id, &c.intentID, &c.dedupKey, &c.attempts); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// deliverTask loads the intent, pushes it through the sink, marks the outbox row
// delivered under the dedup CAS, and finishes the task. The sink call is OUTSIDE
// any transaction so a slow/failing sink never holds a lock; the delivered_at CAS
// is the effectively-once fence.
func (r *Reactor) deliverTask(ctx context.Context, t claimedDeliver) error {
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	intent, delivered, found, lerr := cfr.LoadIntent(ctx, conn, t.intentID)
	r.srv.pool.Release(conn)
	if lerr != nil {
		return lerr
	}
	if !found || delivered {
		// Orphan intent or already delivered (idempotent): finish the task.
		return r.finishTask(ctx, t.id, "done")
	}
	if serr := r.srv.deliverySink().Deliver(ctx, intent); serr != nil {
		// Sink failed (or a simulated crash): leave the task for retry, or dead
		// after the ceiling. The outbox row stays undelivered → redelivered later.
		if t.attempts >= 5 {
			return r.finishTask(ctx, t.id, "dead")
		}
		return r.finishTask(ctx, t.id, "ready")
	}
	// Mark delivered exactly once, then finish.
	conn2, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	_, merr := cfr.MarkDelivered(ctx, conn2, t.intentID)
	r.srv.pool.Release(conn2)
	if merr != nil {
		return merr
	}
	cfr.IncDelivered()
	return r.finishTask(ctx, t.id, "done")
}

// --- 7. CRON (ADR-06 cron task kind, BUILD-E D10) ----------------------------

// cronOnce drives the recurring cron task rows (never driven before D10). It
// atomically claims every due cron row (FOR UPDATE SKIP LOCKED) AND advances its
// next fire (run_at += interval) in one CTE — so a due tick is claimed exactly once
// even under concurrent kernels — then spawns each target workflow. The cron row is
// durable, so the schedule survives a kernel restart; each fired workflow's effects
// are exactly-once by the step transaction. A crash between the advance and the
// spawn loses at most that one tick (cron catch-up=1 semantics), never the schedule.
func (r *Reactor) cronOnce(ctx context.Context) error {
	conn, err := r.srv.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	targets, err := r.claimDueCron(ctx, conn)
	r.srv.pool.Release(conn)
	if err != nil || len(targets) == 0 {
		return err
	}
	env := r.srv.stepEnv(r.cfg.LeaseSeconds)
	principal := map[string]any{"subject": "cron", "operator": true}
	for _, target := range targets {
		c2, aerr := r.srv.pool.Acquire(ctx)
		if aerr != nil {
			return aerr
		}
		resolved, ok, rerr := catalog.Resolve(ctx, c2, catalog.ResolveReq{Name: target})
		if rerr != nil {
			r.srv.pool.Release(c2)
			return rerr
		}
		if !ok {
			r.srv.pool.Release(c2) // target no longer resolves — skip this fire; schedule persists
			continue
		}
		_, serr := cfr.StartWorkflow(ctx, c2, env, r.srv.interp, resolved.Hash, nil, principal, cek.TierTrusted)
		r.srv.pool.Release(c2)
		if serr != nil {
			return serr
		}
	}
	r.signalDrain()
	return nil
}

// claimDueCron advances the next fire of every due cron row and returns their
// targets, all in one atomic statement (the run_at advance IS the claim).
func (r *Reactor) claimDueCron(ctx context.Context, conn *pgwire.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `
WITH due AS (
  SELECT id FROM task
  WHERE kind='cron' AND status='ready' AND run_at<=now()
  ORDER BY run_at LIMIT $1 FOR UPDATE SKIP LOCKED
), up AS (
  UPDATE task t
     SET run_at = now() + make_interval(secs => COALESCE((t.payload->>'interval_ms')::float8,1000)/1000.0)
    FROM due WHERE t.id = due.id
  RETURNING t.payload->>'target' AS target
)
SELECT target FROM up`, r.cfg.TimerBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
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
