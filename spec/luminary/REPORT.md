# LUMINARY REPORT — regel Phase 1 architecture (adversarial review, Luminary v2.1 protocol)

Run: 2026-07-10 · mode `architecture` · roster of 12 (7 mode pins + 2 always-in + 3 risk-surface pulls; over soft cap, documented) · process per spec/luminary/RUNBOOK.md (distilled live from ckluis.github.io/luminaryTeam + linked orchestrator prompt v2.1). Working artifacts: PHASE-0-1-frame.md, experts/*.md (12), PHASE-3.5-convergence.md, clashes/C1–C3, PHASE-5-synthesis.md.

---

## VERDICT: **REVISE**

Two red flags were accepted as P0 (Celko: uncreatable I4 exclusion constraint; Bach: conformance oracle blind to regel-added semantics). Both have crisp, spec-level resolution paths — this is REVISE, not NO-GO. No finding invalidates the fusion architecture itself: the deep bets (code-as-rows, admission gate, state-capture continuations, epochs) survived twelve adversarial lenses with their commit-point arguments intact; what failed is enforcement machinery around the bets. Mapping per PHASE-0-1 frame: open P0s resolvable by revision → REVISE; GO is earned when the two P0 red flags are cleared by their declaring members (Celko, Bach) and revisions 3/4 land as release-gated (else their withdrawn flags revert to P0).

## Findings by severity (final, post-clash; 0 UNVERIFIED — all 18 P0/P1 citations grep-verified)

**P0 — BLOCKER (2)**
1. [ADR-03] `name_pointer_history` is `PARTITION BY RANGE (valid_from)` while I4 ("one hash per instant") rests on a `tstzrange &&` exclusion constraint Postgres cannot create on that partitioning — overlapping windows commit; as-of resolution returns two hashes; a resume/rollback binds the wrong immortal code. (Celko, red flag, DATA INTEGRITY)
2. [ADR-04/07] The differential-conformance oracle strips types and runs capability-free programs only — structurally blind to contract enforcement, derived boundary validators, and effect-class ordering, i.e. exactly the semantics regel adds; a wrong-evaluation bug there stays green forever and writes corrupted values into the INSERT-only store. (Bach, red flag, CORRECTNESS)

**P1 — CRITICAL (17)** — grouped; full detail in experts/*.md and the synthesis matrix:
- Immortal-store recovery is detection-without-repair; resolved in clash to hash-verified byte-restore + release-gated drill, reverts to P0 if ungated (Allspaw).
- Agent plane has no prompt-injection threat model (confused-deputy vector unmodeled, content-seeder unattributed); resolved in clash to M5-blocking injection corpus + abuse-model amendment + approval capability/PII/DDL delta (Karpathy, bounded by Schneier).
- Epoch flip is not fleet-atomic — boot is fenced, running kernels are not; no rollback drill; no boot-refuse diagnostic (Kleppmann, Majors, Allspaw).
- No performance number exists anywhere — envelope "asserted, not yet measured," benchmark deferred to M6; snapshotHash O(view) per event; checkpoint-write amplification unbudgeted; tsgo inside the SERIALIZABLE txn (Carmack); felt-slow UI shipped pending complaints → replaced by WAN felt-latency machine gate (Jobs, via clash).
- Observability: "health surface" invoked in all 12 ADRs, specified in none; all diagnostics live inside the Postgres they diagnose; reaper has no backpressure (Majors).
- Security perimeter excludes its own native-Go TCB (vault routing, std/http, std/sql get "tests, not proofs"); binary is an unattested trust root (Schneier).
- Verdict object has no typed outcome discriminant across its four doors (Lauret).
- Mutation testing skips the grammar gate/resolver where the bans were relocated; world-rehash canary is near-tautological against the #1 declared risk (Bach).
- Milestone ladder ("staging is process, not mechanism") is a promise, not a machine gate — elevated because revisions above depend on it (Torvalds).

**P2 — IMPORTANT (29)** — headline items: glossary/schema drift (scope on wrong aggregate; "condition" ×3 meanings; continuation-kind taxonomy) (Evans); jsonb load-bearing discriminators unchecked, durable_condition resolution integrity, history→admission FK missing, resolver cannot enforce `private` (Celko); parse-depth DoS pre-budget, cross-tenant timing oracle, one-human product gate (Schneier); scoped-name in three encodings, unretrievable refusals, SSE cursor invariant unspecified (Lauret); cross-kernel subscription coherence, fan-out in writer txn, O4 TOCTOU (Kleppmann); multiselect/collaboration taste debt (Jobs); dry-run fuel self-throttling, restart-decision safety (Karpathy); corpus coverage floors, hermeticity probe depth (Bach); kernel telemetry as unmasked PII channel (Majors).

**P3 — IMPROVEMENT (6)** — AOT/self-hosting seams (correctly inert), epoch-surface accretion metric, charts-in-v1 (downgraded from P1 in C3, riders attached), for-loop diagnostic UX, glossary minor.

## Clash outcomes (all COMPROMISE, none escalated)
- **C1 Karpathy vs Schneier** (injection P0?): P0 → P1 hard M5 gate. Pivot: admission rows attribute agent + approver but not the third principal who seeded the content the agent read — vs. structural containment (vault CHECK, default-deny, one-scope blast) showing no direct path to secrets.
- **C2 Allspaw vs Schneier** (repair path vs mutation door): P0 → P1 conditional. Pivot: content addressing makes restore self-certifying (correct iff it rehashes to the address) — recovery without standing mutation privilege; supersede-around fails for byte corruption. Reverts to P0 if the drill is not release-gated.
- **C3 Jobs vs Torvalds** (convergence-mandated; expand v1 surface vs N=1 closure): charts P1→P3 with riders (product #2 analytics-shaped; stranger-review at M6); optimistic-echo P1 held but as a felt-latency machine gate; multiselect P2 as verifier-checked sugar. Pivot: reversibility asymmetry — deferrals are deletable, vocabulary additions are immortal epoch surface; and "staging is process, not mechanism" turned against wait-for-complaints latency.

## Synthesis
The corpus is unusually honest about its bets and unusually disciplined about collapsing layers (three FAIL-grade reviewers each opened by conceding this). The failures cluster in one shape: **the design proves its invariants by naming an enforcement mechanism, and in four places the named mechanism cannot do the job** — a DDL Postgres will not create (P0-1), an oracle that cannot see the semantics it gates (P0-2), a canary that cannot see the drift it watches, and a milestone ladder enforced by intention. The second cluster is **operability**: detection is everywhere, recovery is nowhere rehearsed, and the observability surface is a name without a spec. Third: **the agent plane models the agent as the only adversary** while feeding it attacker-influenceable context. All three clusters are revisable without moving any load-bearing architectural decision; scoreboard and full recommendation matrix (owners, done-when, workstreams A–D, milestone gates) are in PHASE-5-synthesis.md.

## Mandated revisions (numbered; ADRs touched)
1. **[ADR-03]** Make I4 enforceable: unpartition `name_pointer_history` or repartition so the temporal exclusion constraint is creatable; CI must execute the real DDL and prove overlap rejection. *(P0 — Celko clears)*
2. **[ADR-04, ADR-07]** Add a regel-native differential oracle covering contract enforcement, derived boundary validators, and effect-class ordering against an independent reference reducer; seeded wrong-evaluation must turn the corpus red. *(P0 — Bach clears)*
3. **[ADR-02, ADR-03]** Immortal-store recovery: hash-verified byte-restore (fails closed on digest mismatch; no role ever regains UPDATE), scrubber-trip runbook, release-gated fault-injection recovery drill. *(reverts to P0 if ungated)*
4. **[ADR-12, ADR-07]** Injection defense: confused-deputy adversary added to the abuse model; injection corpus co-equal with the PII sweep as an M5-blocking gate; content-seeder attribution in admission rows; machine-computed capability/PII/DDL delta rendered beside every green Verdict in the approval queue. *(reverts to P0 if ungated)*
5. **[ADR-08, ADR-06, ARCHITECTURE]** Epoch fleet coherence: per-request/resume epoch fence (running kernel fail-closes on newer catalog epoch), structured boot-refuse diagnostic, authored + drilled epoch revert/roll-forward runbook, O4 enumeration inside the SERIALIZABLE commit.
6. **[new ADR-13; touches ADR-05/06/08/11]** Observability: named metric schema + ~20 golden signals + SLOs, out-of-band emission that survives Postgres loss, reaper backpressure + reap-rate breaker, PII policy for kernel telemetry.
7. **[ADR-04, ADR-11, ADR-07, ARCHITECTURE]** Performance budgets before M0 closes (CEK steps/sec floor, transitions/request ceiling, metering-tax %, checkpoint-write budget); incremental/scoped snapshotHash; tsgo-ms-in-txn measured under concurrent admission; WAN-throttled felt-latency machine gate on M4→v1.
8. **[ADR-07, ADR-11, ADR-12]** Contract hygiene: typed Verdict `outcome` enum (schema'd retry-after), refusal ids retrievable including pre-BEGIN budget refusals, one scoped-name grammar across tools/resources/search, SSE empty-diff/cursor invariant specified.
9. **[ADR-10, ADR-07, ADR-12]** Native-TCB adversarial harness (seeded vault-leaking/contract-violating std bodies) as a release gate; deterministic parse-depth ceiling ahead of all budgets; timing-indistinguishable name resolution; boot-time attestation of the binary dispatch table pinned in the epoch row.
10. **[ADR-02, ADR-04, ADR-05, ADR-07]** Testing depth: dual mutation testing extended to grammar gate + resolver; `continuation_coverage` rows (frame-kind × CFR-version × decoder) with monotone floor; world-rehash canary replays parse→lower from `canonical_text`, not stored AST only; cross-kernel randomized hermeticity probe.
11. **[ARCHITECTURE, RISKS]** Machine-gate the milestone ladder: CI refuses M(n+1) merges while M(n) kill-tests are red. (Substrate for revisions 3, 5, 7, 10.)
12. **[ADR-03, ADR-05, GLOSSARY]** Coherence batch: scope attributed to `name_pointer` (definition is scope-free by construction); "condition" split into three named terms; continuation-kind taxonomy reconciled with the CHECK; `kind='module'` defined or removed; `continuation.epoch` purpose stated; CHECK-shaped jsonb discriminators; `durable_condition` FK + state CHECK; `name_pointer_history.admission_id` FK; resolver visibility predicate.
13. **[ADR-12]** Agent-competence evals before M5: authoring pass@k against the dialect, fuel budget sized from eval P95 iterations, restart-decision accuracy metric.
14. **[ADR-10, ARCHITECTURE]** C3 riders: product #2 must be analytics-shaped (tests closure at the known gap) with a stranger-review gate on the reference dashboard at M6; `multiselect` as verifier-checked sugar if the reference app exercises a tag field.

## Re-audit contract (targeted re-review)
Only the declaring/filing member re-checks: rev 1, 12 → Celko (+Evans on 12) · rev 2, 10 → Bach · rev 3 → Allspaw (Schneier verifies the no-UPDATE boundary) · rev 4, 13 → Karpathy (+Schneier) · rev 5 → Kleppmann + Majors + Allspaw · rev 6 → Majors · rev 7 → Carmack (+Jobs on the latency gate) · rev 8 → Lauret · rev 9 → Schneier · rev 11 → Torvalds · rev 14 → Jobs + Torvalds. Unchanged findings carry forward; transitions marked RESOLVED/REGRESSED/UNCHANGED/WITHDRAWN; only the declaring member clears their own red flag.
