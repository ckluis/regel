# ADR-13: Observability — the health surface, specified

## Status

Accepted — Phase 1 revision. Created by luminary mandated revision R1-06: the "health
surface" invoked across ADR-01–ADR-12 is specified here (Majors P1: surface named but
never defined; diagnostics living inside the Postgres they diagnose; reaper without
backpressure; Majors P2: kernel telemetry as an unmasked PII channel).

**This ADR is the specification of the "health surface."** Every occurrence of the
phrase in ADR-01 through ADR-12, ARCHITECTURE.md, and RISKS.md resolves to this
document: the health surface *is* the §2 signal registry emitted over the §4 paths
under the §6 PII policy, judged against the §3 SLOs. No other definition exists, and
no ADR may add a diagnostic that is not a named signal here or a registered extension
of this schema.

## Context

Twelve ADRs assign standing operational duties — scrubber, reaper, epoch-lag
self-heal, fence trips, pool health, resync rate — to a surface no ADR defines: no
metric names, no emission format, no SLOs, no cardinality plan. When `step_seq` stops
advancing at 3am there is no defined thing to look at. Worse, every diagnostic row
lives inside the single Postgres it would diagnose — RISKS R6's own failure signal is
Postgres saturation, yet the ledger, the health queries, and `SELECT 1` liveness all
query that same saturated database, so a Postgres incident blinds the operator at
exactly the moment observability matters. And the ADR-06 reaper re-offers expired
leases with no pacing: a legitimately-slow fleet expires leases, the reaper re-offers,
a second kernel redoes the work — CAS keeps it correct but doubles load — a
retry-storm feedback loop with nothing measuring or damping it. Finally, kernel
telemetry (reaper diagnostics, `continuation_debug`, error text) is kernel Go —
verifier-invisible — and could spill vault-adjacent data into external log stores
where crypto-shred never reaches.

Cross-ADR dependencies, stated explicitly:
- ADR-05 §7: the lease is liveness-only — "correctness never depends on the lease."
  That asymmetry is the load-bearing premise of §5's breaker: pausing reaping delays
  recovery, it can never corrupt state.
- ADR-06 §2/§5/§6: the wire client's pool health, the task drain and reaper this ADR
  paces, and the terminal-drain contract whose diagnostics ride §4's paths.
- ADR-08 §2: the structured `epoch.boot_refused`/`epoch.fence_tripped` diagnostics
  (R1-05) — adopted into the §2 registry field-for-field; they were this ADR's
  emission model before this ADR existed (JSON line on stdout + health port).
- ADR-03 §4a: the scrubber-trip runbook already requires findings to reach "the
  out-of-band health surface (not only into the Postgres being diagnosed)" — §4 is
  the mechanism that sentence was owed.
- ADR-11 §4/§6/§8: resync counting, invalidation-drain bounding, and the PII grep
  kill-test that §6 extends to emitted telemetry.
- ADR-07/ADR-12: verdict outcomes and the refusal ledger, whose rates are admission-
  and agent-plane golden signals.

## Decision

### 1. Two shapes of telemetry, one naming schema

The kernel emits exactly two shapes:

- **Metrics** — aggregated counters, gauges, and histograms held in an in-process
  registry and exported periodically. Low cardinality by construction (§1a).
- **Events** — individual structured JSON objects for discrete occurrences that
  warrant per-instance fields (`epoch.fence_tripped`, `reaper.breaker_tripped`,
  `store.scrubber_tripped`). High-cardinality identifiers (continuation ids,
  `def_hash`es, kernel ids) live **only** here, never as metric labels.

**Naming convention:** dotted `subsystem.signal`, lowercase, snake_case within
segments. The subsystem prefixes are closed: `admission` `cek` `continuation` `task`
`reaper` `epoch` `store` `sse` `pg` `cap` `agent` `condition` `telemetry`. Counters
end in `_total`; durations end in `_ms` and are histograms; gauges are bare nouns.
Events use the same dotted names with past-tense verbs (`_tripped`, `_refused`).

**The registry is a compiled artifact of the epoch.** Signal names, types, label
sets, and units are declared in one Go table compiled into the kernel binary and
versioned with the epoch (ADR-08 §1): renaming a signal is an epoch change, visible
in the migration findings like any other. Ad-hoc emission — a printf that mints an
unregistered name — does not compile; the emitter API accepts only registry entries.

### 1a. Cardinality bounds

- A metric declares its full label set in the registry: at most **6 labels**, each
  with values from a **closed enum** checked at compile time (verdict outcome, wake
  kind, condition class, effect class, task kind, breaker state) or a bounded
  operator-set id (scope id, epoch `n`).
- Unbounded values — principal ids, continuation ids, hashes, node paths, session
  ids, kernel boot uuids — are **banned as label values**; they ride events. An
  emitter handed an undeclared label value collapses it to `__other` and increments
  `telemetry.label_overflow_total`; CI lints the registry for undeclared labels.
- Budget: the full registry cross-product stays under 10k series per kernel; the
  registry declaration is where a reviewer checks this by multiplication, not in
  production.

### 2. The golden signals (~20, enumerated)

This table is the health surface. Each row names the signal, its shape, what it
means at 3am, and the owning subsystem. CI asserts registry ↔ table equivalence
(red-path test 6).

| # | Signal | Shape | Meaning | Emitter |
|---|--------|-------|---------|---------|
| 1 | `admission.latency_ms` | histogram | Wall time of the full gate transaction, BEGIN→COMMIT/ROLLBACK | ADR-03 §5 / ADR-07 |
| 2 | `admission.verdicts_total{outcome}` | counter | Verdict rate by typed outcome (admitted, rejected, refused, retry-exhausted, stale-base) | ADR-07 |
| 3 | `admission.tsgo_ms` | histogram | Typecheck time inside the SERIALIZABLE transaction — the revision-7 measurement, standing | ADR-07 §3 |
| 4 | `admission.ssi_retries_total` | counter | Serialization-failure retries of gate transactions | ADR-07 / ADR-03 |
| 5 | `cek.steps_total` | counter | Machine transition rate (steps/sec derived); a stall with ready work present is the deepest alarm | ADR-04 |
| 6 | `cek.fuel_exhausted_total{tier}` | counter | Fuel/governor parks by meter tier (fuelMeter, governorMeter) | ADR-04 |
| 7 | `continuation.parked{kind,status}` | gauge | Continuation-store depth — the deepest bet's standing inventory | ADR-05 |
| 8 | `continuation.resume_latency_ms` | histogram | Wake-due → claimed: how stale is a ready continuation | ADR-05 / ADR-06 |
| 9 | `continuation.cas_losses_total` | counter | Claim-CAS losses, including zombie fences — contention and partition visibility | ADR-05 §7 |
| 10 | `task.ready_depth` / `task.oldest_ready_age_ms` | gauge | Task-queue depth and age of the oldest undrained ready task | ADR-06 §5 |
| 11 | `reaper.lag_ms` | gauge | now − oldest expired, un-reaped lease: recovery debt | ADR-06 §5 |
| 12 | `reaper.reoffers_total{kind}` | counter | Reap rate — the counter whose absence let a stampede go unmeasured | ADR-06 §5 |
| 13 | `reaper.breaker_state` + `reaper.breaker_tripped` | gauge + event | §5 breaker: 0 closed / 1 open / 2 half-open; the trip event carries window stats | ADR-06 §5 / §5 here |
| 14 | `epoch.fence_tripped` | event | R1-05 runtime fence diagnostic, adopted field-for-field from ADR-08 §2 | ADR-06 §6 / ADR-08 |
| 15 | `epoch.boot_refused` | event | R1-05 boot-refusal diagnostic, adopted field-for-field from ADR-08 §2 | ADR-08 §2 |
| 16 | `store.scrubber_trips_total` + `store.scrubber_tripped` | counter + event | I5 scrubber / world-rehash-canary mismatches — corruption of the sole code identity | ADR-03 §4/§4a / ADR-02 §5 |
| 17 | `sse.resyncs_total` | counter | snapshotHash divergence resyncs — a rising rate is a client bug alarm | ADR-11 §4 |
| 18 | `sse.invalidation_depth` / `sse.fanout_lag_ms` | gauge | Invalidation-queue depth and enqueue→patch-sent drain lag | ADR-11 §6 |
| 19 | `pg.select1_latency_ms` | histogram | Pool health-probe round trip — Postgres as seen from the kernel | ADR-06 §2 |
| 20 | `pg.conns_destroyed_total` | counter | Destroy-on-desync events — poisoned-connection detections | ADR-06 §2 |
| 21 | `pg.pool_in_use` / `pg.pool_wait_ms` | gauge / histogram | Pool saturation and time-to-connection | ADR-06 §2 |
| 22 | `cap.denials_total{class}` | counter | Capability denials: resume re-validation failures, vault CHECK rejections, reveal-grant denials | ADR-04 §5 / ADR-05 §4 / ADR-10 |
| 23 | `agent.refusals_total{reason}` | counter | Refusal-ledger writes: `ADMISSION_BUDGET`, `CAP_UNGRANTED`, scope escalation | ADR-12 §5/§6 |
| 24 | `condition.open_age_ms{class}` | gauge | Age of the oldest open durable condition — the operator-inbox lag | ADR-05 §6 |
| 25 | `pg.serialization_aborts_total{txn}` | counter | BUILD-B (REPORT-R1 P2-6): 40001/40P01 aborts by transaction kind (step, admission, send, drain) — the SSI cost SERIALIZABLE-everywhere pays, now measured | ADR-05 §7 |
| 26 | `pg.serialization_retry_exhausted_total` | counter | BUILD-B (REPORT-R1 P2-6): steps whose 5-attempt retry budget exhausted and fell back to the lease/reaper path | ADR-05 §7 |

Plus the meta-signals `telemetry.dropped_total` and `telemetry.label_overflow_total`
(§4, §1a) — the pipeline watches itself.

**Registered extensions from the R1 revisions** (R1-INT: candidate signals the other
revisions produced, entering as registry entries — not new golden rows — under §1's
extension rule). From R1-07: the `perf_budget` measured values (ADR-04 §8), the
`wan-150` felt-latency results (ADR-11 §9), and tsgo-in-txn under concurrency (already
golden as `admission.tsgo_ms`, #3) are benchmark/CI facts exported per run as
low-cardinality gauges labeled by metric. From R1-13: `agent.pass_at_k`,
`agent.iterations_to_green_p95`, and `agent.restart_decision_accuracy` — eval-derived
per epoch/dialect bump (ADR-12 §3a/§5/§7), CI rows first, exported as per-epoch gauges
so a floor regression is visible on the same surface that alarms. From R1-08, optional:
`sse.zero_op_frames_total` and heartbeat counters (ADR-11 §2) if cursor-invariant
debugging warrants them.

### 3. SLOs — initial, to be calibrated at M-milestone benchmarks

Every target below is **initial, to be calibrated** when the revision-7 performance
budgets land (M0 CEK floor, M1 gate benchmarks, M2 runtime, M4 felt-latency machine
gate, M6 release suite). The *existence* of the SLO and the signal it binds to are
normative now; the numbers are the first stake in the ground, not folklore.

| Signal | SLO (initial) | Calibrated at |
|--------|---------------|---------------|
| `admission.latency_ms` | p95 ≤ 1.5 s, p99 ≤ 5 s for interactive-scope patches | M1 |
| `admission.verdicts_total` | Gate availability ≥ 99.9%/month: the gate *answers* (any typed outcome), even under load | M1 |
| `cek.steps_total` | No stall: rate > 0 whenever ready work exists; a 30 s stall pages | M0 |
| `continuation.resume_latency_ms` | p95 ≤ 5 s, p99 ≤ lease TTL (30 s) | M2 |
| `reaper.lag_ms` | p99 ≤ 2× lease TTL (60 s); breaker open > 5 min pages | M2 |
| `sse.fanout_lag_ms` | p95 ≤ 500 ms; the 50k-session storm drains within its ADR-11 budget | M4 |
| `sse.resyncs_total` | < 0.1% of frames sent | M4 |
| `pg.select1_latency_ms` | p99 ≤ 100 ms; two consecutive probe failures mark the kernel degraded on its health port | M2 |
| `pg.serialization_aborts_total` | BUILD-B: `step.abort_rate` (aborts/attempts) ≤ 5% sustained over a 5-min window; measured by the ADR-05 test-9 wake storm; a `perf_budget` row | M2 |
| `epoch` flip | fence-trip → fleet serving on N ≤ the drain deadline (30 s lease TTL) | M6 |
| `store.scrubber_trips_total` | **Zero.** Any trip pages and opens the ADR-03 §4a runbook — this SLO is never "calibrated" upward | M0 onward |
| `telemetry.dropped_total` | < 1% of events under a 5-minute sink outage (ring-buffer sizing check) | M2 |

`cap.denials_total` and `agent.refusals_total` carry no target — they are security
signals where the interesting number is the anomaly, not the level; they get
rate-of-change alarms, operator-set.

**BUILD-D (D5b): `sse.fanout_lag_ms` — the 500 ms p95 SLO binds INTERACTIVE fan-out,
not a one-shot 50k full fan-out; the storm is governed by a calibrated drain budget.**
`sse.fanout_lag_ms` measures, per ADR-13 §2's own definition, the *enqueue→patch-sent
drain lag* of a single invalidated session. For an ordinary interactive invalidation
(a fan-out that fits within a drain tick's bounded-pool capacity) the 500 ms p95 target
stands. It is **arithmetically unmeetable** for the tail of a **one-shot 50k
single-horizon full fan-out** on a single node: with a bounded worker pool the last
session's lag is O(N / workers × per-drive-cost), which the D5b gate measured at
**p50 ≈ 16.2 s, p95 ≈ 30.8 s** for N=50k on 16 workers (Apple M4, local Postgres 16,
idle). That is not a regression — it is the physics of draining 50k real
claim→resume→diff→checkpoint transactions on one node. The catastrophic full fan-out is
therefore governed by a **separate calibrated storm drain budget**, not by the
interactive p95: `sse.storm50k.drain_ms` ≤ **90 s** (measured **≈33 s**), with the
per-session tail pinned as `sse.fanout_lag_ms.p50` ≤ **45 s** (≈16.2 s) and
`sse.fanout_lag_ms.p95` ≤ **75 s** (≈30.8 s) — all `perf_budget` rows (epoch 1, M4),
red on regression. Coalescing is proven at the same gate (K=20 concurrent-mutation
burst collapsed to ≈1.14 re-renders per session, ~17.6×), and the kernel stays live
throughout (healthz p-max 1 ms from process memory during the 33 s drain). The bound
that is *not* relaxed: **exactly-once** — every one of the 50k sessions re-renders
exactly once per NOTIFY (max step_seq == 1).

### 4. Out-of-band emission: telemetry survives Postgres loss

**The emit path takes zero Postgres round trips.** Three channels, all fed from the
in-process registry and event stream:

1. **Structured JSON lines on stdout** — every event, unconditionally. This is the
   channel ADR-08 §2's diagnostics already use; it survives everything the process
   survives and is captured by whatever supervises the binary.
2. **An owned push exporter** — batches metrics and events every 10 s to an
   operator-configured external sink, speaking the OTLP/HTTP wire shape. The wire
   format is adopted; the SDK is not — the exporter is a small owned artifact, same
   philosophy as the ADR-06 wire client (no third-party runtime dependency). A
   bounded in-memory ring buffer (default 10 minutes) backs it: sink outage →
   drop-oldest and count `telemetry.dropped_total`; the exporter is fire-and-forget
   off the serving path and can never block, slow, or fail a request, step, or
   admission.
3. **The health port** — the port ADR-06 §6 and ADR-08 §2 already require — serves
   `/healthz` and a current-metrics snapshot **from process memory**, no database
   round trip. Push is primary (kernels are ephemeral and fenced kernels exit);
   scrape of the health port is a secondary, pull-shaped view of the same registry.

**The in-Postgres health surface is demoted to a convenience view.** Operator-plane
panels may render best-effort rollups queried from Postgres or from the sink, but no
diagnostic exists *only* as a row in the primary — a view, never the truth (the
ADR-05 §2 rule, applied to diagnostics). When Postgres is the casualty, channel 1–3
keep reporting exactly the `pg.*` signals that say so; the drill in red-path test 1
proves it, and ADR-03 §4a's "out-of-band health surface" sentence now has its
mechanism.

### 5. Reaper backpressure and the reap-rate breaker

The ADR-06 §5 reaper (task and continuation leases alike) gains pacing; ADR-06's
text integrates this contract where the reaper lives.

- **Bounded batches.** A reap pass flips at most `reap_batch` rows (default 100,
  operator-set), under the same SKIP LOCKED discipline as the drain.
- **Paced re-offers.** Re-offers spend from a per-kernel token bucket
  (`reap_rate_max`, operator-set); an expired row becomes reap-eligible only at
  `lease_until + min(2^(attempts−1), 60) s` with jitter — a row that keeps expiring
  backs off instead of hammering.
- **The breaker.** Over a sliding 60 s window the reaper tracks its re-offer rate
  and its **re-expiry ratio** — the fraction of re-offered work whose fresh lease
  also expires without a commit (the signature of a fleet that cannot keep up, where
  re-offering faster only amplifies load). Either the rate exceeding `reap_rate_max`
  or the ratio exceeding 50% **opens the breaker**: re-offers stop, the gauge
  `reaper.breaker_state` goes to 1, and one structured event
  `reaper.breaker_tripped {window_reoffers, reexpiry_ratio, oldest_lag_ms, action}`
  is emitted on the §4 paths. After a 30 s cooldown the breaker goes **half-open**
  and re-offers a probe batch of 10; probes committing closes it, probes re-expiring
  re-opens it.
- **Why this is safe:** ADR-05 §7 — correctness never depends on the lease, only
  liveness does. An open breaker trades recovery latency for stability, and the
  trade is *visible*: `reaper.lag_ms` climbs and alarms rather than the fleet
  silently DDoSing its own database. Nothing is lost; work re-offers when the
  breaker closes, and the exactly-once composition is untouched.

### 6. PII policy for kernel telemetry

Kernel telemetry is inside the security perimeter, not beside it. The policy is
structural first, swept second:

- **Never in any telemetry event, metric label, health-port response, or exported
  batch:** dialect runtime values (arguments, returns, payloads), vault contents in
  any form (plaintext *or* mask tokens), the values of names under `private`,
  `durable_condition.payload` (telemetry carries `class` only), CFR blob contents,
  form drafts, and principal natural-language identity (emails, names).
- **Allowed:** content addresses (`def_hash` — code identity, not data), uuids, node
  paths, counts, durations, enum classes, epoch numbers, scope ids.
- **Structurally enforced:** the emitter API takes typed fields only — enums,
  numbers, durations, addresses, ids. There is no string-interpolation path from a
  dialect `Value` into an emission; machine diagnostics are template + node path,
  never value snapshots. A telemetry call that tries to pass a `Value` does not
  compile.
- **`continuation_debug` stays home:** the ADR-05 §2 debug projection is
  operator-plane, capability-gated, and is **never** exported over any §4 channel —
  it renders on demand to an authorized principal and nowhere else.
- **Swept:** the ADR-11 §8 no-plaintext-without-grant kill-test extends to
  emissions — red-path test 3 greps the stdout stream, exporter batches, and
  health-port responses for the seeded values, and CI turns red on a hit.

## Alternatives Considered

- **Telemetry as rows in the primary Postgres (the implicit status quo):** rejected
  as the finding itself — the instrument fails with the patient. Retained only as
  the §4 convenience view, explicitly not the sole copy of anything.
- **Full OTel SDK dependency:** rejected — the corpus bans third-party runtime
  machinery where an owned artifact is small (ADR-06's owned wire client is the
  precedent). The OTLP wire shape is adopted so any standard collector works; the
  emitting code is owned and fits the registry design.
- **Pull-only scrape (Prometheus model):** rejected as primary — fenced kernels
  drain and exit within 30 s (ADR-06 §6) and take their un-scraped final samples
  with them; push with a ring buffer keeps the last words. The health port remains
  as the pull-shaped secondary.
- **Free-form logging as the observability substrate:** rejected — an unbounded PII
  channel and an unnamed schema are precisely Majors's P2 and P1. Everything is a
  registered signal or it does not compile.
- **Adaptive lease TTL instead of a breaker:** rejected — the lease TTL is a
  constant the claim/fence protocol (ADR-05 §7) and the drain deadline (ADR-06 §6)
  are priced against; a self-tuning TTL couples observability into correctness
  machinery. The attempt-scaled reap-eligibility backoff is adopted as the safe half
  of the idea.

## Consequences

- The 3am question has an answer: ~24 named signals, each with an owner, a shape,
  and (where it gates operations) a target. "Check the health surface" is now an
  instruction, not a gesture.
- A Postgres incident no longer blinds the operator: the emit path is
  Postgres-free, and the `pg.*` signals are precisely what keeps reporting during
  one. The convenience view degrades; the truth does not.
- The reaper can no longer stampede: bounded batches, paced re-offers, and a breaker
  whose trip is itself a structured signal. The cost — delayed recovery under
  saturation — is deliberate, bounded, and visible on `reaper.lag_ms`.
- Kernel telemetry joins the security perimeter: the PII policy is enforced by the
  emitter's type surface and swept by CI, closing the verifier-invisible channel.
- The kernel gains one small owned artifact (registry + exporter) and every future
  ADR gains an obligation: a new operational duty ships with its registry entry, or
  it does not exist on the health surface.
- SLO numbers are honest placeholders: normative in existence, provisional in value,
  each tied to the milestone benchmark that will calibrate it.

## Red-Path Tests Implied

1. **Postgres-loss visibility drill (release-gated):** partition, then kill, the
   primary mid-load; assert the stdout stream, exporter batches, and health port
   keep serving with `pg.*` signals red and `select1` probes failing; on heal, the
   fleet recovers and the telemetry gap is zero (ring buffer covered the outage).
2. **Reaper saturation (the RISKS R6 drill):** saturate Postgres until steps outlive
   the 30 s lease; assert the reap rate flattens instead of climbing — the breaker
   opens, `reaper.breaker_tripped` is emitted with window stats, load decreases
   after the trip — and every parked/expired unit completes exactly once after
   recovery (composes ADR-05 tests 1/5).
3. **Seeded-PII telemetry sweep:** run the ADR-11 §8 kill-test's seeded-PII
   scenario; grep the captured stdout stream, exporter batches, and health-port
   responses for the seeded values ⇒ absent. Harness self-test: seed a deliberate
   violation through a test-only bypass ⇒ CI turns red — the sweep is proven able
   to fail.
4. **Cardinality bomb:** flood one metric with 10k distinct undeclared label
   values ⇒ series collapse to `__other`, `telemetry.label_overflow_total` counts
   them, registry memory stays bounded; the CI registry lint rejects an undeclared
   label at build time.
5. **Sink outage:** block the exporter's sink for 10 minutes under load ⇒ ring
   buffer drops oldest, `telemetry.dropped_total` counts, and serving-path latency
   (admission, step, SSE) is unchanged — telemetry backpressure never reaches work.
6. **Registry ↔ spec equivalence:** CI asserts every §2 signal exists in the
   compiled registry and every registry entry is documented in §2 (or a registered
   extension) — drift in either direction is red.
7. **The stall alarm:** freeze the task drain with ready work present ⇒
   `cek.steps_total` stall and `task.oldest_ready_age_ms` alarms fire within 30 s —
   the literal "step_seq stopped advancing at 3am" scenario has a defined, tested
   thing that fires.

## Constraints Discharged or Budgeted

1. **Budgeted.** The deepest bet gets standing instruments: `continuation.parked`,
   `continuation.resume_latency_ms`, and the stall alarm watch the store's health
   for years the way the golden corpus watches its semantics.
2. **Budgeted — the stated exception.** Telemetry is the one subsystem that must
   *not* put its heavy lifting in Postgres, because it exists to outlive Postgres;
   the exception is bounded to an in-memory registry and one small owned exporter,
   and the convenience view keeps the operator plane SQL-shaped.
3. **Consumed.** Breaker trips, scrubber trips, and fence trips that need a human
   decision still surface as durable conditions with restarts (ADR-05 §6) — signals
   alarm, conditions act; this ADR adds no second failure vocabulary.
4. **Not implicated,** beyond the rule that diagnostics carry node paths and
   addresses, never value snapshots.
5. **Budgeted.** §6 extends the security boundary over the kernel's own emissions —
   the PII sweep covers telemetry exactly as the ADR-11 grep covers durable rows,
   closing the verifier-invisible channel Majors named.
6. **Budgeted.** Every SLO is stamped "initial, to be calibrated" at a named
   milestone; the Postgres-loss and saturation drills join the release suite beside
   the ADR-03 and ADR-08 drills — rehearsed capabilities, not hypotheses.
