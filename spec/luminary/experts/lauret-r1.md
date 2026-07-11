# ARNAULD LAURET — Phase 6 re-audit (revision 8, contract hygiene)

## RULING (revision 8): **SATISFIED**

All four seams closed with substance, and — the consumer-first probe — the Verdict is
**one coherent object**. ADR-12 does not redefine it: "The Verdict is ADR-07 §6's object,
byte-identical" (ADR-12 §2). R1-04's `delta`/`seeders` and R1-08's
`outcome`/`refusal_id`/`retry_after` are additive fields on the same ADR-07 §6 schema — no
contradictory shape between the two ADRs.

- **Typed outcome.** `outcome: admitted | already-admitted | rejected | stale-base |
  retry-exhausted | budget-exhausted | busy` — "the one field every door switches on"
  (ADR-07 §6). `retry_after {millis, cause}` is a typed sub-object set exactly on the three
  retryable doors; the ad-hoc `retry-after` string is gone. ADR-09 confirms cross-surface
  reach: "the merge door switch[es] on the typed `Verdict.outcome` enum" (ADR-09 §4).
- **Refusal retrievability.** Every non-green outcome "mints a durable `refusal_id`, the
  primary key of the `gate_refusal` ledger" (ADR-07 §6), incl. pre-`BEGIN` budget/busy;
  `verdict.get {id}` / `catalog://verdict/{id}` resolve against `admission` or
  `gate_refusal`. The demanded spam-flood id is now fetchable.
- **One qname.** "`qname := name \"@\" scope` … one token that round-trips through all
  three surfaces unmodified" (ADR-12 §2); `catalog.search`'s `scope?` demoted to "a
  **filter predicate** … never a second address encoding" — the three encodings retired.
- **SSE invariant.** "every checkpoint that advances `step_seq` emits exactly one frame";
  empty-diff → zero-op frame `[eventSeq, snapshotHash, []]` (advances cursor, carries hash);
  `:keepalive` comment advances no cursor; stale/unknown cursor → full resync (ADR-11 §2).
  This is exactly the gap I flagged, specified both ways.

## DEVIATIONS
- **(a) patch_id required→optional — ACCEPT.** Pre-parse refusals own no content hash;
  `refusal_id` is always present for non-green ("the `refusal_id` never is [null]",
  ADR-03 §1). Net *more* retrievable, not less — honors the contract I asked for.
- **(b) 7 enum values vs "four doors" — ACCEPT.** `rejected` + `busy` are pure widening;
  every original door survives. A superset with no narrowing cannot break a switch I
  demanded be exhaustive.

## gate_refusal DDL scrutiny (ADR-03 §1, integrator-authored)
**Supports the retrievability contract.** `refusal_id uuid PRIMARY KEY` → every id
resolvable; `verdict jsonb NOT NULL` → `verdict.get {refusal_id}` always has a body to
serve; `outcome … CHECK (… IN ('rejected','stale-base','retry-exhausted','budget-exhausted',
'busy'))` faithfully excludes the two green outcomes; `submitted_hashes text[]` nullable,
commented "NULL when a budget/busy refusal precedes parse" — honest nullable semantics, not
a lie of omission. No orphan-refusal *schema* hole: the row carries the full Verdict.
One caveat below (write ordering). Schneier's P2 — `verdict.get` not caller-scoped — stands
from the API lens too: the abuse column claims "own-principal verdicts only" (ADR-12 §2) but
the retrieval-contract prose resolves id→ledger without restating the principal predicate; a
leaked `refusal_id` is a disclosure oracle until that filter is in the *contract*, not just
the abuse note. Not re-filed here; acknowledged as his.

## NEW contract findings
- **[P3] `refusal_id` minted-before-return ≠ persisted-before-return.** Spec says id is
  "minted *before* the refusal is returned" (ADR-07 §6) but never pins the `gate_refusal`
  INSERT before the Verdict leaves. Failure: pre-`BEGIN` budget refusal → mint id → return
  to caller → ledger write fails → caller holds an id `verdict.get` 404s — the orphan the
  contract forbids. Fix: require persist-before-return for the direct (pre-BEGIN) write.
- **[P3] qname grammar reserves no delimiter.** `qname := name "@" scope` (ADR-12 §2) gives
  no character class or split direction; if a `name`/scope segment could contain `@` the
  single-delimiter parse is ambiguous. Fix: one line — `@` reserved, split on the sole `@`.
- **[P3] `already-admitted` carries `admitted:false`.** The retained legacy bool is
  `== (outcome=="admitted")`, so a no-op success reads as a non-admit to any consumer still
  keying the bool — a mild trap for a field we chose to retain. Fix: deprecate the bool.
- **[P3] `retry-exhausted` carries `retry_after`.** Name reads terminal ("exhausted") yet
  it bears `retry_after {cause:"serialization"}` (ADR-07 §6); a client may back-off-retry a
  "give-up". Mildly contradictory; a doc note resolves it. No zero-op-frame flood finding —
  §6 coalescing bounds it (one resume ⇒ one increment ⇒ one frame).

## ORIGINAL FINDINGS
- F1 [P1] no outcome discriminant — **RESOLVED**
- F2 [P2] scoped name three encodings — **RESOLVED**
- F3 [P2] `verdict.get` cannot address refusals — **RESOLVED**
- F4 [P2] SSE cursor invariant unspecified — **RESOLVED**
