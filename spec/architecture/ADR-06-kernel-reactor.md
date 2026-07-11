# ADR-06: Kernel and reactor

## Status

Accepted — Phase 1

## Context

BRIEF §4 requires the kernel-side execution architecture: the concurrency model against
kern's epoll/io_uring language, the owned Postgres wire client's scope and failure
behavior, the hot-state cache and cold start, the request lifecycle with its suspend
points, statelessness/scale-out, and the queue/cron/recovery machinery. Constraint #2
demands the reactor stay thin with heavy lifting in SQL; constraint #6 demands the
walking skeleton be exactly this lifecycle.

Cross-ADR dependencies, stated explicitly:
- The kernel hosts ADR-04's machine and drives ADR-05's rows; the claim/lease protocol
  it executes is ADR-05 §7 — this ADR adds only the drain, heartbeat, and reaper around
  it.
- The cache dichotomy below is ADR-02/03's structure made operational: content by hash
  is immortal (I6, cache forever), names are mutable pointers (invalidate).
- Admission (ADR-03 §5) runs on this kernel's wire client inside one SERIALIZABLE
  transaction; every NOTIFY described here is published by that commit.
- ADR-08 §2/§4a own the epoch pair, boot refusal, the `epoch_current` row, and the O5
  fleet-coherence obligation with its structured diagnostic; §6 below is the runtime
  half this kernel executes — the per-transaction fence and terminal drain.
- ADR-13 is the specification of the "health surface" this ADR's duties land on and
  of the telemetry paths that outlive Postgres; the §5 reaper below implements
  ADR-13 §5's backpressure/breaker contract (R1-06: health surface and reaper pacing
  owned by ADR-13).

## Decision

### 1. Concurrency: goroutine-per-request — the netpoller is kern's reactor

The kernel does **not** hand-roll an event loop. kern specified an explicit
epoll/io_uring reactor because SBCL is not natively an async multiplexer; Go inverts
the premise — the runtime's netpoller *is* an epoll/kqueue (and, as it matures,
io_uring) reactor, and the scheduler multiplexes goroutines over it. Rebuilding that
loop would fight the runtime, forfeit multicore parallelism, and reintroduce callback
inversion. All three proposals converged here; the convergence stands.

- One goroutine per request and per continuation-resume, over a semaphore-bounded pool.
- "Reactor" in regel names the thin logical scheduler: the HTTP/MCP listeners, one
  LISTEN/NOTIFY subscriber, the timer scanner, and the SKIP LOCKED task workers — a
  small fixed set of goroutines, zero business logic.
- kern's honest edge (idle clients exhausting workers) is honored structurally: a
  parked continuation is a row — it holds **no goroutine and no connection** (ADR-04
  §2, ADR-05). Single-writer ordering where needed comes from Postgres row locks, not
  from a single thread.

### 2. Owned Postgres wire client: tight v1 scope, destroy-on-desync

Owned because cgo and third-party drivers are banned, and because the red path *is* the
driver: the classic production wound is a pooled connection poisoned by a mid-query
error. v1 scope, exhaustively:

- Startup + SCRAM-SHA-256 auth; TLS via Go stdlib.
- Extended query protocol (parse/bind/execute) with a prepared-statement cache.
- Explicit transaction control, including the ADR-03 SERIALIZABLE admission and the
  ADR-05 step transaction; `FOR UPDATE SKIP LOCKED` polling.
- LISTEN/NOTIFY (the bus).
- Out-of-band cancellation and clean post-error resynchronization; text result format;
  UTF-8 validated at every string boundary.
- **Excluded from v1: pipelining, COPY, and the binary result format.** None is on the
  walking-skeleton path; each is an additive extension behind the same client.

Failure behavior (the load-bearing rules, grafted from the red-path proposal):

- **Any protocol desync or mid-query error destroys the connection — it is never
  returned to the pool.** A pooled connection is at a clean message boundary or it is
  dead.
- Postgres restart/failover surfaces as a clean failure to in-flight requests (5xx, or
  retry for idempotent reads); the pool reconnects with capped exponential backoff plus
  jitter, so a fleet reconnect never stampedes.
- Health checks are a real `SELECT 1` round trip at a clean boundary, never a liveness
  guess; the pool is bounded and hands out connections with deadlines so a slow query
  cannot silently pin a worker.

### 3. Hot-state cache: immortal by hash, epoch-guarded pointers

The dichotomy is decided as **both, split by mutability** — the two cache models in the
question are the two halves of ADR-02/03's design:

- **Immutable, cached forever, never invalidated:** canonical ASTs and their decoded
  machine-ready forms, keyed by content hash. A hash is its own version; this cache is
  coherent across the fleet with zero coordination.
- **Mutable, invalidated:** the name→hash pointer table and capability-grant bundles.
  Every admission commit publishes `NOTIFY catalog, '<name>,<scope>'`; grant changes
  publish `NOTIFY grants, '<subject>'`. Each domain also maintains a transactional
  epoch counter (a `bigint` bumped in the mutating transaction); kernels stamp cache
  entries with the epoch they were built at and compare against the NOTIFY-updated
  in-memory value on use, reloading a pointer within the request when stale. A kernel
  that missed a NOTIFY (netsplit) detects the lag the moment it reads any epoch-stamped
  row from Postgres and force-reloads — self-healing, no silently-stale window (grafted
  from the red-path proposal).
- Active continuations being driven are leased (ADR-05 §7), never authoritative in
  memory; scherm sessions stay hot in memory *with* a row checkpoint behind them.

**Boot sequence, before any cache fills** (R1-INT: attestation step named in the boot
path, R1-09): a booting kernel first runs the ADR-08 §2 epoch checks — manifest-root
equality, dispatch-table bijection, and the `H_dispatch` recompute-and-compare against
`epoch.dispatch_attestation` (ADR-10 §2) — and refuses to serve with the structured
boot-refuse diagnostic on any mismatch; only then does it subscribe and serve.

**Cold start is nothing:** a fresh kernel boots with empty caches, subscribes to the
channels, and lazily loads on first use — one pointer SELECT plus one AST-by-hash
SELECT, then warm forever. Content addressing makes a cold cache slow-once, never
wrong; there is no warmup artifact and no herd, because by-hash loads are spread across
distinct keys and pointer loads are epoch-checked singles.

### 4. Request lifecycle: park only at `await`

```
accept (goroutine from bounded pool)
  → epoch fence                         [catalog epoch_current ≤ binary epoch, checked
                                          inside the serving transaction — §6; newer
                                          catalog epoch → fail-close, never limp]
  → resolve route → def_hash            [pointer cache, epoch-checked, reload-on-stale]
  → load canonical AST by hash          [immortal cache]
  → build root capability table          [grant bundles ∩ imports — ADR-04 §5]
  → drive the CEK machine                [governorMeter trusted / fuelMeter sandbox]
      ├─ runs to completion              → render response, return goroutine
      ├─ await, satisfiable inline       → kernel performs the I/O, stepping continues
      └─ await, deferred wake            → ADR-05 step tx: claim/effect/checkpoint;
                                            release goroutine and connection; PARKED
  → wake (timer / message / event / join / restart choice)
      → any kernel claims via ADR-05 §7 CAS → fresh goroutine → re-enter the machine
```

Routes are catalog rows (a handler definition keyed by method + path pattern), so
routing is a name resolution like any other. R1-INT: an external entry point (inbound
HTTP request, operator query, agent tool call) resolves with `:caller_module = ''`, so
only `exported` pointers match (ADR-03 §3, R1-12); once evaluation is inside a
definition, the machine binds `:caller_module` from the C register's own `def_hash`
(ADR-04 §2) — `private` names are unreachable from outside code by construction. A synchronous HTTP caller of a parked
evaluation receives `202` with the continuation id; scherm SSE sessions hold their
channel and stream byte-patches on wake. Suspend points are exactly ADR-04's `await`
transitions plus its fuel/governor parks — nothing else detaches a request from its
goroutine.

### 5. Statelessness, scale-out, one task table, lease/heartbeat recovery

Two kernels are identical iff they run the same pinned epoch binary (same machine, std,
verifiers, CFR readers) — an identity the §6 fence enforces per transaction, not only
at boot. A kernel holds **zero authoritative state** — every cache
derives from immortal or epoch-guarded rows — so any kernel serves any request and
resumes any continuation; scale up = bigger Postgres, scale out = more identical
kernels. Kernel identity (`lease_owner`) is an ephemeral boot-time uuid, never durable.

One `task` table backs queue, cron, timers, resumes, and outbox delivery (unification
grafted from the simplest-thing proposal):

```sql
CREATE TABLE task (
  id           uuid PRIMARY KEY,
  kind         text NOT NULL CHECK (kind IN ('resume','cron','deliver')),
  run_at       timestamptz NOT NULL,
  payload      jsonb NOT NULL,          -- resume: continuation_id + step_seq seen
                                        -- cron: schedule + target def_hash
                                        -- deliver: outbox intent id + dedup key
  status       text NOT NULL DEFAULT 'ready' CHECK (status IN ('ready','running','done','dead')),
  attempts     int NOT NULL DEFAULT 0,
  lease_owner  uuid,
  lease_until  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- R1-INT: load-bearing jsonb gets CHECK-shaped validation, same discipline as ADR-05's
  -- wake_kind_shape (R1-12): a payload missing the keys its kind dispatches on is a row
  -- that means nothing. Shape per kind, keys asserted present:
  CONSTRAINT payload_shape CHECK (
    (kind = 'resume'  AND payload ? 'continuation_id' AND payload ? 'step_seq') OR
    (kind = 'cron'    AND payload ? 'schedule'        AND payload ? 'target')   OR
    (kind = 'deliver' AND payload ? 'intent_id'       AND payload ? 'dedup_key'))
);
CREATE INDEX ON task (run_at) WHERE status = 'ready';
```

- **Drain** (every kernel, N at a time):
  `UPDATE task SET status='running', lease_owner=$me, lease_until=now()+'30 seconds',
  attempts=attempts+1 WHERE id IN (SELECT id FROM task WHERE status='ready' AND
  run_at<=now() ORDER BY run_at FOR UPDATE SKIP LOCKED LIMIT $n) RETURNING *;`
  — each row goes to exactly one kernel. `NOTIFY task` on insert wakes idle pollers so
  latency is not poll-bound and idle kernels never spin.
- **Timer wakes** insert their resume task when due (the ADR-05 partial-index scanner);
  message/event wakes insert it in the triggering transaction; cron tasks re-insert
  their next occurrence on completion — recurrence as data.
- **Lease/heartbeat/reaper (dead-kernel recovery):** 30-second leases, 10-second
  heartbeats extending `lease_until`. A reaper query on every kernel
  (`status='running' AND lease_until < now()`) flips expired work back to `ready` —
  **paced, never open-loop** (R1-06: reaper backpressure + reap-rate breaker, ADR-13
  §5): reap passes are bounded batches (`reap_batch`, default 100) spent from a
  per-kernel token bucket; an expired row becomes reap-eligible only after an
  attempt-scaled, jittered backoff; and a sliding-window breaker opens — halting
  re-offers and emitting the structured `reaper.breaker_tripped` event — when the
  re-offer rate exceeds `reap_rate_max` or the re-expiry ratio (re-offered work whose
  fresh lease also expires uncommitted) exceeds 50%, half-opening on a probe batch
  after cooldown. A slow Postgres therefore flattens the reap rate instead of
  amplifying its own load; the pause is safe because the lease is liveness-only
  (ADR-05 §7) and visible because `reaper.lag_ms` climbs and alarms.
  Recovery composes three guarantees: at-least-once claim (lease expiry re-offers),
  exactly-once commit (ADR-05 §7 — the effect and checkpoint are one transaction, so a
  reclaimed task resumes from the last committed checkpoint), and zombie fencing (a
  slow original owner fails the `step_seq` CAS at commit; for `deliver` tasks the
  outbox dedup key bounds redelivery). No loss, no double-fire, no operator.
- Tasks exceeding an attempt ceiling flip to `dead` and signal a `step.failed` durable
  condition (ADR-05 §6) — failure surfaces as restarts, never as a silent dead-letter
  queue.

### 6. Epoch fence: a running kernel fail-closes on a newer catalog epoch (R1-05: per-request/resume fence + terminal drain)

Boot fencing (ADR-08 §2) guarantees a kernel never *starts* against the wrong epoch;
this section guarantees it never *continues* against one. Without it, a kernel that
booted under epoch E would keep serving requests and resuming continuations after
`migrate N --commit`, resolving names to std-N hashes absent from its native dispatch
table — two kernels applying different epochs' semantics to the same rows.

- **Guard, inside the transaction that serves the work.** Every transaction this
  kernel opens — request service, admission (ADR-03 §5), step/claim/park (ADR-05 §7),
  task claim (§5) — reads the one-row `epoch_current` table (ADR-08 §2) as part of
  its first batched round trip, and again batched immediately before COMMIT. The
  check is transactionally consistent with the work it fences, never a racy
  pre-check. Admission and step transactions run SERIALIZABLE, so the guard read also
  hands the flip an rw-conflict: work racing `migrate --commit` is aborted by SSI,
  which is what closes ADR-08's O4 window. Cost: one PK read of one hot row per
  round trip the kernel already makes — no extra network hop, no lock.
- **Fail-close, defined.** On a guard mismatch (catalog `n` newer than the binary's
  epoch) or a `NOTIFY epoch` announcing one, the kernel enters **terminal drain**: it
  stops accepting connections (503 plus the ADR-08 §2 structured diagnostic on the
  health port), stops claiming tasks, ROLLBACKs every fenced in-flight transaction,
  and lets its leases lapse. Nothing is lost and nothing double-fires: rollback plus
  the ADR-05 §7 exactly-once composition means the reaper re-offers the work and an
  epoch-N kernel resumes it from the last committed checkpoint; SSE sessions drop and
  reconnect to an N-kernel, resyncing via ADR-11 §4.
- **Recovery is replacement, not re-sync.** The epoch is compiled into the binary
  (semantics, verifiers, std dispatch — ADR-08 §1), so a fenced kernel has nothing it
  could reload: it emits `epoch.fence_tripped` with `action: "drained_and_exited"`
  and exits nonzero within the drain deadline (the 30-second lease TTL). The
  supervisor restarts it; the boot fence then refuses the stale binary with the same
  structured diagnostic. Operators stage epoch-N binaries with `--wait-for-epoch`
  (ADR-08 §2) before the flip, so fleet handover costs the drain deadline, not a
  deploy.

## Alternatives Considered

- **Hand-rolled epoll/io_uring event loop (kern's literal mechanism):** rejected
  unanimously by all three proposals and here. Go's netpoller already is that loop;
  owning a second one buys callback inversion and single-core reactors for zero
  governed-evaluation benefit. kern's *intent* — non-blocking multiplexing, no
  thread-per-connection, workers fungible because state is durable — is kept in full.
- **prior-art-faithful: maximal v1 wire client (pipelining, COPY both directions,
  binary results).** Rejected for v1: nothing on the walking skeleton needs them, and
  the pipelining argument (checkpoint + effect in one round trip) is a latency
  optimization inside a transaction that is already atomic. All three are additive
  behind the same client. Adopted from it: the semaphore-bounded pool framing and the
  state-is-durable/workers-are-fungible reading of the LMAX/Redis lineage.
- **simplest-thing: NOTIFY-only cache invalidation, `locked_at` without a recovery
  protocol.** Rejected: a missed NOTIFY leaves a silently stale kernel, and a dead
  kernel's claimed work needs a stated reaper, not an `attempts` column. Adopted from
  it: the single `task` table for queue/cron/timers/resumes and the tight v1 client
  scope.
- **red-path-first (winner):** §2's failure rules, §3's epoch-guarded cache, and §5's
  lease/heartbeat/reaper with the CAS fence are its design. Corrections: its COPY and
  binary-result client scope is cut to the skeleton set, and its separate wake-scanner
  path is folded into the one `task` drain.

## Consequences

- The kernel stays four responsibilities — admit, evaluate, resume, remember — plus a
  wire client; queue, cron, pubsub, sessions, and recovery are all Postgres rows and
  two SQL idioms (SKIP LOCKED, NOTIFY). Heavy lifting is in SQL by construction.
- Deploying a kernel is starting a binary; killing one loses at most unheartbeated
  leases, which the reaper re-offers within 30 seconds. There are no sticky sessions
  and no drain ceremonies beyond letting leases lapse.
- The gate's throughput and the task queue's throughput are both bounded by Postgres.
  That is the envelope argument, accepted: scale up means a bigger Postgres before it
  means cleverer kernels.
- Every operational duty this ADR creates — scrubber (ADR-03), reaper, epoch-lag
  self-heal, epoch fence trips and terminal drains (§6), pool health — is a standing
  item on the kernel's health surface, which is no longer a name: ADR-13 specifies
  its signals (`reaper.lag_ms`, `reaper.reoffers_total`, `reaper.breaker_state`,
  `task.ready_depth`, `pg.select1_latency_ms`, `pg.conns_destroyed_total`,
  `pg.pool_in_use`), their SLOs, and the Postgres-independent emission path they
  ride (R1-06: health surface specified in ADR-13).

## Red-Path Tests Implied

- **Dead kernel holding claims:** SIGKILL a kernel mid-step with claimed tasks and a
  running continuation; every lease expires; another kernel completes all work from the
  last checkpoints; effects fire exactly once (composes ADR-05 tests 1 and 5).
- **Mid-query poison:** inject a protocol error mid-result-set; assert the connection
  is destroyed, never pooled, and the next request gets a clean connection (the named
  kern kill-test).
- **PG restart/failover:** in-flight requests fail cleanly; pool reconnects with
  backoff+jitter; no reconnect stampede at fleet scale; no garbled bytes ever served.
- **Stale cache:** admit a pointer move on kernel A while kernel B's NOTIFY is dropped;
  B's next epoch-stamped read triggers force-reload; B never evaluates the superseded
  hash.
- **Two kernels, one cron tick:** SKIP LOCKED hands each due task to exactly one
  kernel under sustained contention; the ADR-05 wake storm (10k timers) drains without
  double-execution.
- **Zombie fence:** partition a kernel mid-claim, let the lease lapse and the work be
  redone, heal the partition; the zombie's commit fails the CAS; no double effect.
- **Cold start:** a fresh kernel serves its first request with empty caches in two
  SELECTs and is warm thereafter; a parked session resumes on a node that has never
  seen it.
- **Attempt ceiling:** a permanently-failing task flips to `dead` and surfaces a `step.failed`
  condition with its restarts rendered in the operator plane.
- **Reaper saturation (R1-06: breaker red path, ADR-13 test 2):** saturate Postgres
  until steps outlive the 30-second lease; the reap rate flattens instead of
  climbing — the breaker opens, `reaper.breaker_tripped` carries its window stats,
  load decreases after the trip — and every expired unit completes exactly once
  after recovery; no retry-storm feedback loop forms.
- **Running-kernel epoch fence (R1-05: fail-close red path):** `migrate N --commit` lands while this kernel
  serves traffic and resumes continuations under epoch E; its next commit attempt
  rolls back on the §6 guard, `epoch.fence_tripped` is emitted with every ADR-08 §2
  field, the kernel drains and exits within the lease TTL, and every released task
  completes exactly once on an epoch-N kernel — no request or resume is ever answered
  by a mismatched (binary, catalog) pair.

## Constraints Discharged or Budgeted

1. **Discharged (the runtime half).** Park/resume-on-any-node is statelessness plus
   ADR-05's rows; the lease/reaper/CAS triad is what makes a years-long pause survive
   any fleet.
2. **Discharged.** The reactor is inherited from the Go runtime, not built; everything
   heavy is SQL; the kernel ships zero business logic and one owned driver whose scope
   fits in eight bullet points.
3. **Budgeted.** Dead tasks and exhausted retries surface as ADR-05 durable conditions;
   this ADR adds no second failure vocabulary.
4. **Not implicated** beyond serving the canonical AST from the immortal cache.
5. **Budgeted.** The kernel builds every root capability table (no ambient authority
   at the host boundary) and enforces the destroy-on-desync rule that keeps verifier
   verdicts flowing over sound connections.
6. **Discharged for the skeleton.** Admit → row → evaluate → respond is §4 verbatim;
   KC-class red paths (driver resync, stale cache, lease reclaim) are the bootstrap,
   built and green before any feature.
