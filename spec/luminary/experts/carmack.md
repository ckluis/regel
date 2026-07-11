# JOHN CARMACK — Performance & Optimization

## VERDICT: CONCERNS

The mechanisms are sound and the deep-bet artifacts (printer, CFR, CEK) are correctly
kept off the evaluation hot path. My concern is singular and consistent: for a system
whose central bet is "interpreted TS on one Postgres is fast enough," there is not one
number in the target — no step-throughput floor, no per-request transition budget, no
checkpoint-write ceiling. Every performance claim is an argument, and one hot-path
algorithm is worse than the design's own stated goal.

## FINDINGS

1. **[P1] No performance budget exists — only an assertion and a deferred benchmark.**
   The entire viability case rests on the I/O-bound-envelope argument, yet nothing states
   a target steps/sec, a transitions-per-typical-request ceiling, or a metering-tax
   number; the one benchmark measures only the meter branch, not absolute throughput, and
   fires at M6 — after the whole stack is built on the unmeasured floor. A fully-reified
   tree-walking CEK with heap frames per composite node and a boxed `Value` union is
   plausibly 10–50× a bytecode VM; "fast enough" here is a claim, not a fact.
   CITE: "the envelope claim is asserted, not yet measured on the reference" (RISKS.md, R6 Residual)

2. **[P2] `snapshotHash` is O(view size) per event, defeating the O(change) diff design.**
   The layer's whole point is "ship only changed bytes," but every frame the server
   rehashes *all* `(slotId, value)` pairs; a 2000-slot dashboard pays a 2000-element FNV
   pass on every keystroke-blur, and the client's "running hash" cannot be updated
   in-place for a mid-sequence value change either. Per-event cost is thus proportional to
   view size, not change size — the exact tax the slot-diff design claims to avoid, on the
   highest-traffic surface.
   CITE: "Every frame carries `snapshotHash` = FNV-1a-64 over `(slotId, value)` pairs" (ADR-11 §4)

3. **[P2] Checkpoint-write-per-interaction is unbudgeted write amplification on the one Postgres.**
   Every UI event — including per-field blur validation and events that mutate only
   UI-local state — forces a step-transaction row write of up to a 256 KB CFR to the
   single database; a 20-field form blur-validated is 20 writes, and a 50k-session storm
   is 50k transactions for one mutation, all "within the drain budget" with no budget
   number given.
   CITE: "a checkpoint write per interaction, accepted inside the I/O-bound envelope." (ADR-11, Consequences)

4. **[P2] tsgo — the acknowledged expensive stage — runs inside the SERIALIZABLE admission txn.**
   Holding a heavy CPU-bound typecheck open inside the serializable transaction lengthens
   the conflict window linearly with typecheck time, inflating the serialization-retry
   rate under concurrent admission exactly where RISKS names "typecheck is the expensive
   stage"; no bound on tsgo-ms-in-transaction or expected retry rate is stated.
   CITE: "invoked once per admission inside the ADR-03 transaction" (ADR-04 §3)

## RECOMMENDATIONS

- Set numeric budgets before M0 closes, verified by the M2/M4 benchmarks: target
  CEK-transitions/sec, a transitions-per-reference-request ceiling, and a hard
  metering-tax % — fail the milestone if unmet, don't just record a corpus.
- Make `snapshotHash` incremental or scoped: hash only the changed slots plus a
  cheap positional accumulator, or switch to a per-slot version vector; verify with a
  microbench asserting per-event hash cost is O(changed slots), not O(total slots).
- Publish a checkpoint-write budget (bytes/interaction, writes/sec/session) and add a
  reference-app load test asserting writes-per-interaction ≤ 1 and CFR delta size bounded;
  gate M4 on it.
- Measure tsgo-ms held inside the SERIALIZABLE transaction and the resulting retry rate
  under a concurrent-admission load test; if the window dominates, move typecheck to a
  pre-transaction snapshot-consistent phase and re-validate at commit.

## RED FLAG

NONE — the performance envelope is unmeasured, but it is consciously budgeted, reversible
(AOT seam reserved, vertical scaling stated), and correctness is not at stake; this is
P1/P2 engineering debt, not an irreversible P0.
