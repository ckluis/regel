# JOHN ALLSPAW — Resilience & Safety Engineering

## VERDICT: FAIL (1 proposed P0; red flag declared)

Reliability is the presence of recovery, not the absence of failure. This design is
unusually strong at *detecting* and *fail-closing* its catastrophic modes — and unusually
silent on what a human does in the minute after detection. It rehearses the trip, not the
recovery.

## FINDINGS

1. [P0] **Immortal-store corruption is detected but has no repair path.** The `definition`
   table is INSERT-only with privileges revoked, so a scrubber-detected byte/address
   mismatch names a row that cannot be corrected, dropped, or re-canonicalized — detection
   dead-ends. Blast is total (only code identity), yet no runbook, quarantine, or
   supersede-around motion is rehearsed for the moment the scrubber fires in production.
   CITE: "a bit-flipped `ast` column is caught by the ADR-03 scrubber" (ADR-02, Red-Path Tests Implied)

2. [P1] **Git self-heal has no circuit breaker.** Every projection force-restores
   unconditionally on SHA mismatch; if the fold itself turns nondeterministic, the design's
   own named failure — a restore loop — hammers the mirror with no bound, backoff, or
   operator brake. The loop is listed as a symptom but given no arrestor.
   CITE: "mirror SHAs diverge, self-heal loops" (RISKS.md, R9 Breaks)

3. [P1] **A bad epoch flip is forward-only — there is no rollback drill.** The flip is
   fleet-atomic and boot-refusal strands old binaries, so an engine defect the golden corpus
   missed can only be chased *forward* through the same gate that admitted it. No "revert the
   epoch" motion is rehearsed for the case where rolling forward is itself unsafe.
   CITE: "Sev-1 ships as `regel migrate 8.1`. The freeze has no exception clause" (ADR-08 §6)

4. [P2] **Corrupt-CFR fail-closed has no recovery restart.** Failing into `step.failed` is
   correct, but no restart is enumerated for it — unlike `fuel.exhausted`/`capability.revoked`
   — so the human's only implicit exit is abort-and-lose-the-workflow, an unrehearsed loss.
   CITE: "fails deserialization closed into a `step.failed` condition" (ADR-05, Red-Path test 4)

## RECOMMENDATIONS

- Add an incident runbook + drill for a production scrubber/canary trip: freeze the affected
  address's dependents, `supersedes`-re-admit a corrected row, and reconcile stranded
  continuations — verified by a fault-injection test that corrupts one immortal `ast` byte
  and measures time-to-contained.
- Bound self-heal: cap consecutive force-restores per interval, then halt-and-alarm instead
  of looping; verify with a nondeterministic-fold fixture asserting the breaker trips and the
  mirror is not restore-stormed.
- Author and drill an epoch-revert path (or explicitly document why forward-only is safe);
  verify by shipping a deliberately-bad epoch in staging and measuring fleet recovery time.
- Define a `step.failed`/corrupt-CFR restart set (e.g. `escalate`, `abort-with-audit`) with
  an owner and an alert; verify the poison-CFR test asserts a human-actionable choice, not
  just a parked row.

## RED FLAG

CATEGORY: DATA INTEGRITY
CITE: "a bit-flipped `ast` column is caught by the ADR-03 scrubber" (ADR-02, Red-Path Tests Implied)
CONSEQUENCE: The scrubber and nightly canary can detect that the world's sole identity has
moved or corrupted, but the store that holds it is INSERT-only and undeletable and the
design names no recovery. Detection without repair on a total-blast surface is a
catastrophic mode with no rehearsed path out: when it trips in production, a human has
nothing to do next.
