# JAMES BACH — Testing & QA Strategy — R1 RE-AUDIT

## VERDICT: PASS (red flag cleared)

Both mandated revisions shipped with substance, not just markers. My P0 red flag is CLEARED.

## Revision 2 (P0 — my red flag): regel-native differential oracle — **SATISFIED**

The oracle is real and correctly scoped. ADR-04 §6 harness 3 evaluates "the same program
**plus its inputs**" through the production CEK machine and an independent reference reducer,
comparing four observables — verdicts, per-input validator outcomes, effect-class order, and
produced values — and "Any divergence in any of the four … turns the corpus red" (ADR-04 §6).
Coverage is exactly the three layers I named blind: contract enforcement, derived boundary
validators, effect-class ordering (§6 a/b/c).

- **Genuinely independent, or could it share the bug?** Independent. "shares **no** code path
  with the production CEK machine: not the step function, not the frame-kind dispatch, not the
  contract/validator/effect implementations, not the `Meter`" (ADR-04 §6). It explicitly names
  the failure mode I would have probed: "A bug that lives in a routine both evaluators call is
  invisible to any differential test" (ADR-04 §6). Correct diagnosis, correct fix.
- **Do the mutants test the oracle or the mocks?** The oracle. Mutants are seeded in the
  *production evaluator*, one per layer, and "**must** turn the corpus red" (ADR-04 §6); a
  survivor "is a release blocker." They drive real machine↔reducer divergence, not mock paths.
- **Gate wired?** Yes. "The release pipeline is **red** (a green pipeline is impossible)
  whenever either (i) any differential divergence exists … or (ii) any of the three seeded
  wrong-evaluation mutants … survives" (ADR-07 §5). Self-test also carried as a red-path test
  (ADR-07, "Regel-native oracle self-test").

**RED FLAG: CLEARED.** Resolving artifacts: ADR-04 §6 harness 3 (oracle + independence +
seeded-mutant requirement) and ADR-07 §5 (release-gate wiring). The blindness is closed:
regel-added semantics are now witnessed by a second, disagreeing evaluator that cannot share
the bug.

## Revision 10 (testing depth, 4 items): **SATISFIED**

Checked extra carefully given the applying agent was cut off; all four legs are present and real.
- **Dual mutation → grammar gate + resolver** — SATISFIED. "the grammar gate and the resolver
  are mutation-tested as first-class targets"; a mutant that "**weakens a relocated ban** …
  **must be killed** by the hostile corpus" (ADR-07 §5), backed by the harness self-test
  red-path (ADR-07). `verifier_coverage.component` now names grammar-gate/resolver.
- **continuation_coverage (frame_kind × cfr_version × decoder), monotone floor** — SATISFIED.
  ADR-05 §8.5: required grid is "a *computed* set, not a guess"; "A run whose covered set does
  **not** include every previously-covered triple is a **regression and a release blocker**";
  reachable-but-uncovered = red. Red-path test 11 asserts an untouched decoder path fails.
- **World-rehash canary — parse→lower from canonical_text** — SATISFIED. ADR-02 §5 now two legs;
  the load-bearing pipeline leg re-runs "the **full parse→lower pipeline from each row's
  `canonical_text`**"; "**Red** is any historical row whose `canonical_text` no longer lowers to
  its stored address." Directly watches the text↔AST seam my finding named tautology-blind.
- **Cross-kernel randomized hermeticity probe** — SATISFIED. ADR-04 §6.5 + ADR-05 test 12: "≥ 2
  independently-launched kernel instances … distinct kernel builds … randomized Go map seeds,
  randomized goroutine/checker scheduling, and a cold checker"; self-validated by injecting a
  map-ordered emission. Kills the "twice on one warm process" weakness I flagged.

## Extra scrutiny — integrator-authored coverage DDL (ADR-03 §1 table-(6))

Addressed: the tuples support the semantics my revisions demand. `continuation_coverage
(epoch, frame_kind, cfr_version, decoder, covered bool)` PK `(epoch, frame_kind, cfr_version,
decoder)` lets the gate SELECT the covered-triple set and diff it against both the prior floor
(monotone) and the computed required grid (a `covered=false` row for a reachable triple reads
red). `verifier_coverage (epoch, component, threat_class_ids[], …, mutation_score)` supports
threat-inventory monotonicity (array ⊇) and score-regression gating. The floor is computable and
enforceable from these columns. Minor: reachability itself is a harness-computed set, not a stored
column, and cross-epoch monotonicity is CI logic, not a DB constraint — consistent with how
`verifier_coverage` already works; acceptable, not a defect.

## Original-finding transitions

- F1 [P0] oracle blind to regel-added semantics — **RESOLVED** (R1-02).
- F2 [P1] mutation scoring gates verifiers not enforcement surface — **RESOLVED** (R1-10, grammar
  gate + resolver mutation-tested).
- F3 [P2] continuation/interpreter corpora unmeasured floor — **RESOLVED** (continuation_coverage).
- F4 [P2] world-rehash canary near-tautological — **RESOLVED** (pipeline leg).
- F5 [P2] hermeticity under-probed — **RESOLVED** (cross-kernel randomized probe).

## New findings introduced by the revisions

- **[P3] Oracle's reference reducer is continuation-free** — "it never needs to pause"
  (ADR-04 §6). Effect-class ordering (layer c) that spans `await`/pause points is therefore not
  differentially witnessed against an independent oracle; such orderings are covered only for
  *sameness* by the hermeticity probe, not for *correctness*. Narrow residual (contracts and
  validators are synchronous-evaluable), well below red-flag weight, but the "no regel-added
  evaluation stays hand-reasoned" claim (ADR-04, Constraint 5) has one thin seam: effect order
  across pauses. Recommend a small synchronous-surrogate effect-trace case set or an explicit
  scope note.

No P0/P1/P2 introduced.
