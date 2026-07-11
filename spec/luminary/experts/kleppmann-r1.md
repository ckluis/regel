# MARTIN KLEPPMANN — R1 re-audit (revision 5: epoch fleet coherence)
## RULING ON REVISION 5: SATISFIED

The fence is real, priced, and correctly argued. `epoch_current` has exactly one writer:
"updated in exactly one place — inside the `migrate N --commit` SERIALIZABLE transaction"
(ADR-08 §2). The guard is transactional, not a racy pre-check: every kernel transaction
"reads the one-row `epoch_current` table (ADR-08 §2) as part of its first batched round trip"
(ADR-06 §6), re-checked at COMMIT. Fail-close is terminal: the kernel "emits
`epoch.fence_tripped` with `action: \"drained_and_exited\"`" (ADR-06 §6) — no limp mode.
O4 moved inside the commit: "an rw-cycle SSI refuses, so either the park or the flip
commits, never both" (ADR-08 §4); O5 has a race-form red path ("exactly one of the two
transactions commits, under every interleaving", ADR-08 Red-Path). NOTIFY is correctly a
latency optimization, never load-bearing — the guard read is authoritative on both paths.

## SSI PROBE — answered

**Does the piggybacked guard read create the rw-antidependency in every interleaving?**
Where it must, yes. The O4 cycle is genuine: park reads `epoch_current` (flip writes it),
flip's enumeration reads `continuation` (park inserts into it) — two rw-antidependencies,
a cycle, and Postgres SSI aborts one; predicate reads are covered by SIREAD locks, so an
insert into the scanned range cannot slip past. The subtle case is the NON-conflicting
epoch-E step that races the flip: SERIALIZABLE means its COMMIT-time re-read sees its own
snapshot (old n) and passes, and a lone step→flip rw-edge is no cycle — the step COMMITS
wall-clock-after the flip. This is CORRECT: it serializes before the flip, touched nothing
the flip read, and its pre-flip snapshot is a consistent E-world — the invariant actually
enforced is serialization-order coherence, not wall-clock coherence — the right invariant.
Deferrable snapshots are safe (serial-safe by construction, never straddle); long-lived
SERIALIZABLE txns are safest (fixed E-view of std pointers). READ COMMITTED serves are the
one exposure — see N3. **Is epoch_current a universal conflict partner?** No. SIREAD locks conflict only with
writes, and the flip is the sole writer — reader-reader is free; contention concentrates
entirely at the flip, by design. The standing cost claim ("one SSI read predicate on one
hot page", ADR-08 §4a) is accurate.

## INTEGRATOR DEPENDENCY CHECK — present and sufficient

ADR-05 §7: "the step transaction runs **`ISOLATION LEVEL SERIALIZABLE`**, not READ
COMMITTED", with the `BEGIN ISOLATION LEVEL SERIALIZABLE;` block shown. Every park and
resume rides this transaction, so the resume-path fence and the O4 rw-cycle both hold.

## ORIGINAL FINDINGS — transitions

1. [P1] Epoch flip not fleet-atomic — **RESOLVED** (ADR-08 §4a O5 + ADR-06 §6 + M6 gate).
2. [P2] Subscription-index coherence — **UNCHANGED** (not in rev 5's scope; carries).
3. [P2] Fan-out in writer txn — **UNCHANGED** (carries; pressure rises, see N1).
4. [P2] O4 TOCTOU — **RESOLVED** (enumeration inside SERIALIZABLE `--commit` + fence).

## NEW FINDINGS (introduced by the revisions)

- **N1 [P2] SERIALIZABLE-everywhere has no abort budget or retry policy.** Rev 5 made every
  step + admission txn SERIALIZABLE (ADR-05 §7), so all concurrent steps now run SSI against
  each other on shared business tables — page-granularity SIREAD false positives under load
  are a known cost, and §5's same-transaction event fan-out (carried finding 3) multiplies it.
  Aborts are SAFE (CAS + lease re-offer), but nothing specifies the `serialization_failure`
  retry discipline or budgets abort rate; R1-07's `perf_budget` omits it. Fix: retry-on-40001
  policy + abort-rate signal in ADR-13.
- **N2 [P3] Prose overclaims the flip's blast.** "in-flight epoch-E transactions abort and
  their work is re-offered" (ADR-08 §4a) — only flip-conflicting ones abort; non-conflicting
  E-steps commit, serialized before the flip. A drill asserting all-E-aborts would be false.
- **N3 [P2] A READ COMMITTED serve can touch the N-catalog before the guard fires.** "under
  READ COMMITTED the re-check sees a fresh snapshot" (ADR-08 §4a) is not atomic with COMMIT:
  an E-serve reads `epoch_current=E`, then a later per-statement read hits a flipped std
  pointer = N-hash absent from its dispatch table → dispatch-miss before the pre-COMMIT guard
  fail-closes. Fail-close holds (error, not wrong answer) but §4a's "no ... mismatched pair"
  wants no *touch*. Fix: run serve txns REPEATABLE READ so guard + std reads share a snapshot.
- **N4 [P3] Guard list vs. obligation mismatch.** O5 says *every* kernel transaction, but the
  enumeration is four kinds ("request service, admission, continuation step/claim/park, task
  claim", ADR-08 §4a); reaper lease-flips, heartbeats, and timer scans are unfenced — benign
  (evaluation is fenced downstream), but state the exemption lest a future reaper evaluate.

## RED FLAG: NONE. Verdict for my slice moves CONCERNS → SATISFIED-WITH-NOTES.
