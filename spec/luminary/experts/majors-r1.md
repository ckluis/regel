# CHARITY MAJORS — R1 targeted re-review (Phase 6)

## VERDICT: CONCERNS CLEARED — all five originals RESOLVED; two P2 + one P3 new.
The health surface is now a spec, not a gesture. ADR-13 does the job the 12 dangling
references needed. Two residuals sit in the owned exporter and the ring buffer.

## 1. Revision 6 (mine, ADR-13): **SATISFIED**
- **Schema+cardinality:** §1 dotted `subsystem.signal`, closed prefixes, compiled registry
  ("a printf that mints an unregistered name does not compile"); §1a ≤6 labels, closed
  enums, unbounded ids banned as labels ("ride events"), <10k series. Enumerable spec.
- **Signals+SLOs:** §2 = 24 golden + 2 meta; §3 binds 11 SLOs, each "initial, to be
  calibrated" at a named milestone. Exceeds the ~20 ask.
- **Postgres-loss survival (probed hard):** §4 "the emit path takes zero Postgres round
  trips." Three channels off the in-process registry: stdout JSON (supervisor captures),
  external-sink push exporter, health port "from process memory." The listener is OUTSIDE
  the patient — in-Postgres surface is "a view, never the truth." Test 1 kills the primary
  mid-load, asserts stdout/exporter/health-port keep serving `pg.*` red. The finding-2
  inversion is genuinely fixed end to end.
- **Reaper breaker:** integrated in BOTH ADR-13 §5 and ADR-06 §5 ("**paced, never
  open-loop**") — bounded batches, token bucket, jittered backoff, 60s breaker on rate OR
  50% re-expiry ratio, half-open probe.
- **PII+red-path:** §6 typed-fields-only emitter ("a call that tries to pass a `Value`
  does not compile"); test 3 greps stdout+exporter+health-port with a fail-proving self-test.

## 2. Judgment — owned OTLP exporter (no OTel SDK): **RATIFIED.**
Not "add logging later" — that mode is deferral; here schema, signals, and paths are
specified now and compile-gated into the epoch binary. Matches the ADR-06 wire-client
precedent exactly; "OTLP wire shape … any standard collector works" keeps the escape
hatch; owning a fixed versioned encoder is bounded. Ratified, with the wire-conformance
caveat in N1 (owning the encoder without a collector-round-trip gate is where hand-rolling
actually bites — and that gate is currently absent).

## 3. Revision 5, my slice (diagnostics/telemetry): **SATISFIED.**
`epoch.fence_tripped` (#14) / `epoch.boot_refused` (#15) are golden, "adopted
field-for-field." ADR-08 §2 struct carries observed/required_epoch, binary_version, BOTH
manifest roots, kernel_id, ts, action (+in_flight_aborted, leases_released, h_dispatch).
Every 3am field named is present; `action`+distinct event name separate refuse from
crash-loop; rides §4's Postgres-independent paths. Slice clears.

## 4. Scrutiny — registered extensions carry R1-13 evals + R1-07 perf as candidates:
**ACCEPTABLE.** The registry is the superset; golden is its live-3am-alarm subset. Eval
gauges and perf/felt-latency values are CI/benchmark facts measured per-run/per-epoch —
they turn a *build* red, they don't page an operator; their milestone gating lives in the
R1-11 milestone-gates manifest, and ADR-13 exports them as gauges only so a floor
regression trends on the alarming surface. Correct taxonomy: gate-in-CI,
visible-in-registry, not promoted to a golden signal they aren't. Test 6 rightly exempts
registered extensions from the golden-equivalence check. Nothing that pages at 3am is misfiled.

## 5. NEW findings (severity-tagged; not re-litigation)
- **[P2] N1 — owned OTLP exporter has no wire-conformance gate.** §4 asserts "any standard
  collector works," but test 1 asserts only that batches *keep serving*, not that a real
  collector *ingests* them; a protobuf/field bug in the hand-rolled encoder ships silently.
  Add a collector-round-trip conformance test pinning the OTLP proto version. CITE: "assert
  the stdout stream, exporter batches, and health port keep serving" (ADR-13 test 1).
- **[P2] N2 — shared ring buffer drop-oldest evicts incident onset and rare trip events.**
  §4's buffer is drop-oldest, metrics and events sharing it: an outage past 10 min loses
  the onset (highest-value window), and a metric flood can evict a rare
  `reaper.breaker_tripped`/`scrubber_tripped`. No event-over-metric priority or
  first-occurrence reservation. CITE: "sink outage → drop-oldest" (ADR-13 §4).
- **[P3] N3 — breaker can flap near the 50% re-expiry threshold.** A marginally-over fleet
  oscillates open→cooldown→half-open→re-open, toggling `reaper.breaker_state` (alarm
  noise). Load stays bounded (10 probes/30s) so cosmetic, not a stampede; add
  hysteresis/dwell. CITE: "half-opening on a probe batch after cooldown" (ADR-06 §5).

## Original findings disposition
- F1 health surface unspecified (P1) → **RESOLVED** (§2, 24 signals).
- F2 diagnostics inside the diagnosed Postgres (P1) → **RESOLVED** (§4; test 1) — residual narrowed to N2, not the inversion.
- F3 reaper no backpressure/reap-rate signal (P1) → **RESOLVED** (§5 + ADR-06 §5; #12/#13) — residual N3.
- F4 boot-refuse vs crash (P1, my slice) → **RESOLVED** (ADR-08 §2 + §6a runbook + M6 drill); one-way-door irreversibility is Allspaw/Kleppmann's.
- F5 kernel telemetry as unmasked PII channel (P2) → **RESOLVED** (§6 typed emitter; test 3).
