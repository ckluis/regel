# MARTIN KLEPPMANN — Distributed Systems & Data

## VERDICT: CONCERNS

The deep bets are unusually honest: the ADR-05 §7 claim/checkpoint transaction is a
single Postgres transaction, so effect and checkpoint are genuinely atomic, and the
external-effect story is correctly downgraded to "effectively-once with dedup keys...
the honest limit, stated." My red-flag trigger did not fire — every exactly-once claim
carries a commit-point argument. My signature probe (lease expires between write and
ack) passes: the claim CAS holds the continuation row lock through COMMIT, so a
reaper re-offer merely blocks then CAS-fails; correctness never rides the lease. But
four seams past that transaction boundary are underspecified, one at fleet scale.

## FINDINGS

1. **[P1] Epoch flip is not fleet-atomic — only boot is fenced.** "One fleet, one
   epoch" rests on a boot-time manifest check, yet flipping the epoch row does not
   restart running kernels and there is no per-request ADR-08-epoch guard (ADR-06 §3's
   epoch-stamp guards pointer staleness, not the epoch); post-`--commit` an epoch-E
   binary keeps serving an N-catalog, resolving `std/mail` to a swapped N-hash absent
   from its native dispatch table. This is exactly the `E.1` CVE path, where std hashes
   move fleet-wide. CITE: "The fleet flips atomically; the binary refuses to boot against a mismatched" (ARCHITECTURE.md §2f).

2. **[P2] Cross-kernel subscription-index coherence is unproven.** Each kernel matches
   mutations against a private in-memory `subKey → set(session)` index, but a
   subscription written on kernel A is invisible to a mutation on kernel B unless B's
   index has propagated — there is no epoch-stamped self-heal (unlike the pointer cache)
   and no stated NOTIFY on subscription insert. A dropped propagation silently drops B's
   invalidation, leaving A's session stale until the user's next POST — the "by
   construction" claim covers only the intra-render read closure. CITE: "A missed dependency is impossible by construction, not by" (ADR-11 §6).

3. **[P2] High-fanout event wakes are coupled into the writer's transaction.** Session
   invalidation is decoupled (async NOTIFY + index), yet workflow `event`/`message`
   wakes flip subscribers inside the triggering business write; a record-change with
   thousands of `event`-parked continuations bloats that transaction and, under
   serialization contention, can abort ordinary app writes. The two subsystems disagree
   on where fan-out lives. CITE: "flipped to `ready` in the same transaction as the triggering write" (ADR-05 §5).

4. **[P2] O4 lattice-narrowing enumeration is a TOCTOU.** Sleeping continuations holding
   a to-be-banned type are enumerated at migration admission, but step transactions
   (ADR-05 §7) are nowhere stated to be SERIALIZABLE, so an E-kernel parking a
   banned-type continuation between dry-run enumeration and `--commit` can escape the
   scan and strand a continuation the epoch forbids. CITE: "fails to land until every sleeping continuation holding a newly-banned type is" (ADR-08 §4).

## RECOMMENDATIONS

- Add a per-request/per-resume ADR-08-epoch fence: stamp the live `epoch.n` on the
  immortal-hash dispatch path and have a running kernel fail-closed (5xx, drain) when it
  reads a catalog epoch newer than its binary. Verify: `migrate N --commit` while an
  E-kernel serves traffic; assert the E-kernel refuses the first post-flip std call
  rather than dispatch-missing, and that no request is answered by a mismatched pair
  (extend ADR-08's "Boot refusal" test to a running binary).
- State subscription-index coherence explicitly: either NOTIFY on subscription
  insert/delete with the same epoch-stamp self-heal ADR-06 §3 gives pointers, or have
  the mutating kernel consult the authoritative `subscription` table for the horizon.
  Verify: subscribe on kernel A, mutate on kernel B with A's NOTIFY dropped; assert A's
  session still receives the frame (the ADR-11 "Exactness" test run cross-kernel).
- Specify the isolation level of the ADR-05 §7 step transaction, and run O4 enumeration
  inside the `--commit` SERIALIZABLE transaction with continuation creation fenced.
  Verify: park a banned-type continuation concurrently with `--commit`; assert either the
  park or the commit aborts, never both commit.
- For §5 `event` wakes, move high-fanout flips out of the writer onto the ADR-11 NOTIFY
  + bounded-drain path used for sessions. Verify: a record-change with 10k event-parked
  continuations does not raise the writer's abort rate above baseline.

## RED FLAG

NONE — no exactly-once or atomic claim in the target lacks a commit-point/idempotency
argument; the two cross-process gaps (outbox delivery, epoch flip) are stated or
fail-closed, not silent data corruption. Finding 1 is a P1 liveness/rollout gap, not a
P0: it degrades to dispatch-miss on restart, not incorrect output.
