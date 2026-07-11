# JAMES BACH — Testing & QA Strategy

## VERDICT: FAIL

The design's automated *checking* is unusually disciplined, but two suites that are named as the gate for a failure class structurally cannot see that class. One rises to a red flag.

## FINDINGS

1. **[P0] The differential-conformance oracle is blind to the behavior regel adds.** The fuzz runs "generated type-correct, capability-free subset programs" past a `node` oracle fed a type-stripped projection — but regel puts types and contracts *in* the hash and derives boundary validators from them, and effects only exist in capability-bearing code. So the one oracle that lets "semantics never be hand-reasoned" covers exactly the vanilla-TS core where regel and node agree by construction, and never touches contract enforcement or effect-class evaluation — where wrong values reach immortal rows and history. CITE: "via a harness-owned type-stripping projection of the" (ADR-04, §6 Conformance).

2. **[P1] Mutation scoring gates the six verifiers, not the enforcement surface.** The bans, floating-promise/acyclicity/R1–R5, and import closure were relocated into the grammar gate and resolver; dual mutation testing scores "the verifier code itself," so the bulk of the trust boundary gets one hostile-corpus fixture each — the exact coverage mutation testing exists to distrust. A weakened grammar-gate ban survives unless a fixture hits that precise edge. CITE: "everything relocatable to grammar, printer, or resolver has been" (ADR-07, Consequences).

3. **[P2] Continuation and interpreter corpora have no measured coverage floor.** Verifiers carry monotone `verifier_coverage` rows; the golden-continuation, CFR, and test262 corpora are unmeasured fixture bags — no gate requires a fixture per frame-kind × CFR-version or per `r<n>` decoder path, so "stays green" is achievable with a thin corpus that never exercises a rare `FinallyK`-across-await decode. CITE: "the golden corpus covers captured fixtures, not every reachable state" (RISKS, R2).

4. **[P2] The world-rehash canary is near-tautological.** It replays stored ASTs through normalize→encode→hash — deterministic over unchanged bytes, so it only detects encoder edits, never re-runs parse/lower over the historical corpus. A lowering or tsgo change that re-maps existing `canonical_text` to a different AST (hence hash) is invisible to the canary. CITE: "nightly replay of every historical definition through" (ADR-02, §5).

5. **[P2] Hermeticity is under-probed.** "Submitted twice" on one warm kernel proves in-process determinism, not the class that makes verdicts probeable: Go map-iteration order, checker goroutine scheduling, or tsgo internal state carried across admissions — none of which "twice, same process" perturbs. CITE: "the same (patch, snapshot, epoch) submitted twice yields" (ADR-07, Red-Path/Hermeticity).

## RECOMMENDATIONS

- Add a regel-native differential oracle for contract and effect-class semantics: evaluate every admitted contract combinator and effect ordering under the machine and under an independent reference reducer, gate on divergence — verify by seeding a known wrong-evaluation and confirming the corpus turns red.
- Extend dual mutation testing to the grammar gate and resolver; require a non-trivial mutation score for each relocated ban, verified by a seeded ban-weakening mutant that must fail the release.
- Emit `continuation_coverage` rows (frame-kind × CFR-version × wake-kind × `r<n>` decoder touched) with a monotone floor mirroring `verifier_coverage`; verify by asserting an untouched decoder path fails the gate.
- Run the hermeticity test across two independently-launched kernels with randomized map seeds and a cold checker, N times; verify by injecting a map-ordered diagnostic emission and confirming detection.

## RED FLAG

CATEGORY: CORRECTNESS
CITE: "via a harness-owned type-stripping projection of the" (ADR-04, §6 Conformance)
CONSEQUENCE: The conformance harness is claimed to remove hand-reasoned semantics, yet by stripping types and excluding capabilities it validates only the vanilla-TS subset that node and the machine agree on by construction. regel's own additions — contract enforcement, derived boundary validators, effect-class ordering — remain hand-reasoned, ungated by any oracle. A wrong-evaluation bug there stays green through every conformance run and writes corrupted values into the INSERT-only definition store and the history tier, where they are irreversible and outlive the fix.
