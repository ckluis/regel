# CHARITY MAJORS — Infrastructure & Observability

## VERDICT: CONCERNS

Four P1s, one P2, no P0. The mechanisms are sound; the operability of them at 3am is
asserted, not designed. My red-flag trigger fires (no structured instrumentation story)
but the gap is retrofittable, so it is P1, not a blocker.

## FINDINGS

1. **[P1] The "health surface" is invoked, never specified.** Reaper, epoch-lag,
   pool, resync rate are all waved at one undefined surface across twelve ADRs — no
   metric names, no emission format (Prometheus? OTel? logs?), no SLOs, no cardinality
   plan. When `step_seq` stops advancing at 3am there is no defined thing to look at.
   CITE: "is a standing item on the kernel's health surface" (ADR-06, Consequences).

2. **[P1] Every diagnostic signal lives inside the single Postgres it would diagnose.**
   R6's own failure signal is Postgres saturation, yet the ledger, health surface, and
   `SELECT 1` liveness all query that same saturated DB — when it wedges, your
   observability wedges with it. There is no out-of-band telemetry path.
   CITE: "lock/IO saturation on `task`/`continuation`" (RISKS.md, R6).

3. **[P1] Reaper has no backpressure and no reap-rate signal, so a slow DB amplifies
   its own load.** A legitimately-slow step or a saturated Postgres expires the 30s
   lease, the reaper re-offers, a second kernel redoes the work (CAS keeps it correct
   but doubles load) — a retry-storm feedback loop with nothing measuring it or damping
   it. CITE: "flips expired work back to `ready`" (ADR-06, §5).

4. **[P1] `migrate --commit` is a one-way door with a fleet-wide-outage failure mode and
   no rollback drill.** After the flip the prior binary boot-refuses fleet-wide;
   recovery is forward-only, and the "bad epoch that boots but misbehaves" rollback is
   undrilled — boot-refuse emits no operator-distinguishable diagnostic vs a crash-loop.
   CITE: "The fleet flips atomically; the binary refuses to boot against a mismatched"
   (ARCHITECTURE.md, §2f).

5. **[P2] Kernel operational telemetry is an unnamed PII channel outside the verifier
   boundary.** App logs route through V2's sink set, but `continuation_debug`, reaper
   diagnostics, and the health surface are kernel Go — verifier-invisible — and can spill
   vault-adjacent data into external log stores where crypto-shred never reaches.
   CITE: "a novel channel (timing, error shape, a" (RISKS.md, R8 Residual).

## RECOMMENDATIONS

- Write an ADR-13 (observability): name the metric/event schema, the ~20 golden signals
  (reap rate, lease-expiry rate, invalidation-queue depth, `tsgo_ms`, serialization-retry
  rate, resync rate, condition-inbox age), and per-signal SLOs. Verify: grep the ADR for
  concrete metric names and an emission protocol; a reviewer can enumerate them.
- Emit telemetry out-of-band (stdout structured events / OTel exporter), not only into
  Postgres. Verify: kill the DB in a drill and confirm the health/metrics stream survives.
- Add an adaptive-lease or reap-rate circuit breaker and a `reaper_reoffers_total` counter.
  Verify: run R6's saturation drill and observe reap rate flatten, not climb.
- Specify a boot-refuse diagnostic (structured "epoch mismatch: got X want Y" on a health
  port, distinct from crash) and a documented epoch-rollback/roll-forward-under-outage
  runbook. Verify: the ADR-08 boot-refusal red-path test asserts the diagnostic string and
  a measured recovery drill exists.
- State a PII policy for kernel telemetry/`continuation_debug` output. Verify: extend the
  ADR-11 §8 grep kill-test to cover emitted log/metric lines, not just durable rows.

## RED FLAG

NONE. The instrumentation gap trips my documented trigger ("no structured instrumentation
story"), but it is retrofittable and thus fails the P0 irreversibility bar — filed as P1
(finding 1). The epoch one-way-door (finding 4) is irreversible but is a consciously
documented bet gated by dry-run + golden corpus, so I hold it at P1 rather than override
the decision.
