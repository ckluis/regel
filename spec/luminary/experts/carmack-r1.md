# JOHN CARMACK — Performance & Optimization (R1 re-review, revision 7)

## VERDICT: SATISFIED — CONCERNS cleared; two P3 nits on budget-as-data mechanics.
Revision 7 shipped what I asked for: every performance claim that was an argument is now a
`perf_budget` row a benchmark can fail.

## REVISION 7 — RULING: SATISFIED
- **Budgets before M0.** ADR-04 §8 states all four: CEK ≥1M transitions/sec/core; ≤50k
  transitions/request p95; metering tax ≤10% (governor 0% by monomorphization); ≤1 checkpoint
  write/interaction + CFR delta ≤64KB p95. Benchmark-enforced: "fails the milestone if any
  measured value crosses its budget," wired as the M0 gate (ARCH M0 row). Not deferred to M6.
- **snapshotHash incremental.** ADR-11 §4 replaces the O(view) rehash with the summed digest
  below; §3 duty (d) is "O(changed slots), never a full-view pass." Real.
- **tsgo under concurrency.** ADR-07 §3: N=32 concurrent admissions, "p95 ≤ 40 ms, p99 ≤ 80
  ms … retry rate ≤ 5%," overflow shed **before `BEGIN`** as `ADMISSION_BUSY` (semaphore),
  not a silent stretch. They measured + backpressured rather than moving tsgo out of the txn —
  my "move it" was conditional; this satisfies it.

## snapshotHash PROBE — SOUND
`Σ_slots h(slotId‖value) mod 2⁶⁴`, incremental `h ← h − h(s‖v_old) + h(s‖v_new)`.
- **Order-independence sound; hot path genuinely O(changed slots).** (ℤ/2⁶⁴,+) is an abelian
  group, each slot contributes one term, so the incremental subtract-add **provably equals a
  full recompute** (ADR-11 §4 argument is correct) and a mid-sequence value change — the case
  a position-ordered running hash couldn't fix in place — is handled by one term swap.
- **64-bit collision/cancellation: non-issue as specified.** Additive digests collide cheaply
  *adversarially*, but this is a drift detector, not a security primitive (a hostile client
  owns its DOM anyway). Accidental same-frame cancellation is ~2⁻⁶⁴ and the *next* frame's sum
  catches it ("one-frame delay," Consequences) — self-healing in one round trip; not oversold,
  it "trades cryptographic strength for O(changed-slots) cheapness." **SOUND.** (Nit:
  `slotId‖value` must be delimiter-safe/keyed so two slots can't alias — adequate if truly keyed.)

## DEVIATION RULINGS
- (a) tsgo-concurrency gate at **M1 not M0** — **ACCEPT.** The hermetic tsgo host first exists
  at M1 (ADR-07 §3); you can't benchmark a stage before it's built. Number + semaphore
  specified now, only the run waits. Honest sequencing.
- (b) checkpoint-write **number now, end-to-end verify at M4** — **ACCEPT.** Write path built
  at M0 carries the budget (ADR-04 §8); the blur-storm/50k-session load test needs the reactive
  layer (M4, ADR-11 §5). Number not deferred, only the proof.
- (c) felt-latency gate at **M4→v1** — **NOT A DEVIATION.** The mandate itself says "machine
  gate on M4→v1"; placing it there executes the mandate. Correctly self-labeled.

## perf_budget DDL SCRUTINY (ADR-03 §1 table (6))
`(epoch, metric, tier, budget, measured, milestone), PK (epoch, metric)`. budget vs measured
present; per-epoch history yes (epoch keyed, append-only) — gate-able.
- **Celko's tier-in-PK P3 — CONFIRMED from the perf lens.** Metering-tax has a real per-tier
  split (fuelMeter ≤10% vs governorMeter 0%) and the CEK floor is tier-specific; with `tier`
  *outside* the PK you can't store two tiers of one metric per epoch without smuggling tier
  into `metric` (making the column redundant). PK should be `(epoch, metric, tier)`. **P3.**

## BUDGET NUMBERS — order-of-magnitude sane, one gameable vector
- **1M CEK transitions/sec/core:** sane but *soft*. A Go tree-walker with type-switch dispatch
  + freelisted frames realistically does 5–20M steps/sec/core, so 1M is conservative — won't
  false-fail, but won't catch a 5× regression that still clears it. OK as a floor; could tighten.
- **50k/req (≤~50ms), 10% meter tax, ≤1 write/interaction, ≤64KB CFR delta:** all measurable,
  internally consistent. "≤1 write/interaction" is cleanest — countable, not gameable.

## NEW FINDINGS
1. **[P3] perf_budget can't express comparison sense.** Some metrics are floors (CEK/sec: red
   if <budget), others ceilings (transitions/req, tax, delta: red if >). No direction column,
   so red/green is **not derivable from the row** — a wrong-way gate isn't schema-caught.
2. **[P3] Reference workload unpinned — CEK floor and transitions/req gameable.** §8 measures
   "on the reference workload" but neither its composition nor a representativeness bar is
   pinned; a cheap microbench clears 1M/sec and 50k/req, greening M0. Pin it as a gate artifact.

## ORIGINAL FINDINGS
1. [P1] No perf budget — **RESOLVED** (ADR-04 §8, M0-gated).
2. [P2] snapshotHash O(view) — **RESOLVED** (ADR-11 §4 incremental summed digest).
3. [P2] Checkpoint-write unbudgeted — **RESOLVED** (≤1/interaction, ≤64KB, M4 end-to-end).
4. [P2] tsgo in SERIALIZABLE txn unmeasured — **RESOLVED** (N=32 bench + ADMISSION_BUSY, M1).
RED FLAG: NONE (unchanged — was never P0).
