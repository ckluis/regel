# Phase 5 — Synthesis (orchestrator, domain-neutral)

=== PHASE 5: SYNTHESIS — roster: Torvalds, Evans, Kleppmann, Carmack, Lauret, Majors, Allspaw, Jobs, Celko, Schneier, Karpathy, Bach ===

## 1. Citation verification
All 18 citations backing P0/P1 findings were mechanically grep-verified verbatim against the corpus (see orchestrator log). 0 UNVERIFIED. Celko's substantive P0 claim was additionally fact-checked: Postgres cannot create an exclusion constraint on a partitioned table whose partition-key column participates via `&&` rather than `=` (unsupported entirely pre-PG17; PG17+ requires partition keys as equality members), so the ADR-03 DDL as written either fails or does not enforce I4. The claim stands.

## 2. Resolution (priority changes, each with one line)
- Karpathy F1: P0 → **P1, hard M5-blocking release gate** — C1 clash: consequence contained structurally (vault CHECK, default-deny, one-scope blast), but the confused-deputy vector is genuinely unmodeled; red flag withdrawn on the gate condition.
- Allspaw F1: P0 → **P1 conditional** (reverts to P0 if the recovery drill is not a release-gated kill-test) — C2 clash: content addressing makes byte-restore self-certifying (correct iff it rehashes to the immortal address), so recovery is a procedural gap, not a structural one.
- Jobs F1 (charts): P1 → **P3 with riders** — C3 clash: vocabulary additions are immortal epoch surface while deferrals are deletable; riders = product #2 analytics-shaped + stranger-review gate at M6.
- Jobs F2 (optimistic echo): **P1 held, reformulated** — wait-for-complaints trigger replaced by a WAN-throttled felt-latency machine gate blocking M4→v1.
- Jobs F3 (multiselect): **P2 held, reformulated** — verifier-checked sugar over relation, not a 14th field type.
- Torvalds F2: P2 → **P1** — elevated: machine-gated milestones are the enforcement substrate that revisions 3, 5, 7, and 10 depend on; as a promise it silently voids them.
- Bach F4 (canary tautology): P2 → **P1** — elevated: it structurally voids the named mitigation of RISKS.md's #1 risk (hash identity drift).
- Celko F1 and Bach F1: **accepted as P0** (red flags accepted; see §4).
- All other findings hold as proposed.

## 3. Scoreboard (final priorities; verdict transitions from clash outcomes marked)
| Member | Verdict | P0 | P1 | P2 | P3 | UNV | Red flag |
|---|---|---|---|---|---|---|---|
| Torvalds | CONCERNS | 0 | 1 | 1 | 2 | 0 | none |
| Kleppmann | CONCERNS | 0 | 1 | 3 | 0 | 0 | none |
| Carmack | CONCERNS | 0 | 1 | 3 | 0 | 0 | none |
| Evans | CONCERNS | 0 | 0 | 3 | 2 | 0 | none |
| Celko | **FAIL** | 1 | 0 | 4 | 0 | 0 | **DATA INTEGRITY — accepted P0** |
| Schneier | CONCERNS | 0 | 1 | 4 | 0 | 0 | none |
| Lauret | CONCERNS | 0 | 1 | 3 | 0 | 0 | none |
| Majors | CONCERNS | 0 | 4 | 1 | 0 | 0 | none |
| Allspaw | FAIL → CONCERNS | 0 | 3 | 1 | 0 | 0 | DATA INTEGRITY — withdrawn in C2 (conditional) |
| Bach | **FAIL** | 1 | 2 | 2 | 0 | 0 | **CORRECTNESS — accepted P0** |
| Jobs | CONCERNS | 0 | 1 | 2 | 2 | 0 | none |
| Karpathy | FAIL → CONCERNS | 0 | 2 | 2 | 0 | 0 | SECURITY — withdrawn in C1 (gate condition) |
| **Totals** | | **2** | **17** | **29** | **6** | **0** | 2 accepted / 2 withdrawn-conditional |

## 4. Red flags
**ACCEPTED (P0 — these block; resolution = mandated revision + re-audit by declaring member):**
- Celko / DATA INTEGRITY: `name_pointer_history` partition scheme makes the I4 exclusion constraint uncreatable; overlapping windows commit; as-of returns two hashes. → Revision 1; cleared only by Celko citing executed DDL + overlap-rejection test.
- Bach / CORRECTNESS: type-stripped, capability-free conformance oracle is structurally blind to regel-added semantics (contracts, derived validators, effect ordering) that write into the immortal store. → Revision 2; cleared only by Bach citing a regel-native oracle turning a seeded wrong-evaluation red.

**WITHDRAWN-CONDITIONAL (P1 with reversion clauses):**
- Allspaw / DATA INTEGRITY: withdrawn on condition the hash-verified byte-restore + recovery drill is a release-gated kill-test (reverts to P0 if delivered as an ops doc). → Revision 3.
- Karpathy / SECURITY: withdrawn on condition the injection corpus + confused-deputy model is an M5-blocking release gate. → Revision 4.

## 5. Matrix
Coverage statement (Phase 0.7): this audit covers the written Phase 1 architecture corpus; it cannot cover implementation correctness, real performance, or the unfetched concept docs. Verdicts bind only to PROVIDED evidence.

| Pri | Recommendation (revision #) | Advocate | Trade-off accepted | Risk if skipped | Owner | Done when |
|---|---|---|---|---|---|---|
| P0 | 1. Repartition/unpartition history so I4 exclusion is creatable; CI executes DDL + overlap test | Celko | Partition-pruning perf on history | As-of silently binds wrong code | ADR-03 author | DDL runs; overlap insert RAISES |
| P0 | 2. regel-native differential oracle: contracts, derived validators, effect ordering vs independent reducer | Bach | Second reducer to maintain | Wrong values reach immortal rows, green CI | ADR-04/07 author | Seeded wrong-eval turns corpus red |
| P1 | 3. Hash-verified byte-restore + scrubber-trip runbook + release-gated recovery drill; no standing UPDATE ever | Allspaw (bounded by Schneier) | Drill maintenance cost | Detection dead-ends; total-blast surface unrecoverable | ADR-02/03 author | Fault-injected corrupt byte → measured time-to-contained |
| P1 | 4. Injection corpus (co-equal with PII sweep, M5 gate) + confused-deputy in abuse model + content-seeder attribution + approval capability/PII/DDL delta | Karpathy (bounded by Schneier) | Corpus upkeep per epoch | Injected trusted agent farms verified-malicious admissions at the weak human gate | ADR-12 author | Corpus green incl. error paths; delta rendered in queue |
| P1 | 5. Epoch fleet coherence: per-request epoch fence, boot-refuse diagnostic, drilled revert/roll-forward runbook, O4 inside SERIALIZABLE commit | Kleppmann+Majors+Allspaw | Fence check per dispatch | E-binary serves N-catalog post-flip; undrillable Sev-1 | ADR-08/06 author | Live-flip test: E-kernel fail-closes, never dispatch-misses |
| P1 | 6. ADR-13 observability: named metrics/SLOs, out-of-band emission, reaper backpressure + reap-rate breaker, telemetry PII policy | Majors | New ADR + exporter | 3am step_seq stall has nothing to look at; diagnostics die with the DB | new ADR-13 | DB-kill drill: health stream survives; reap rate flattens under R6 drill |
| P1 | 7. Numeric perf budgets before M0 closes (steps/sec, transitions/request, metering %, checkpoint bytes); incremental snapshotHash; tsgo-ms-in-txn measured; WAN felt-latency machine gate on M4→v1 | Carmack + Jobs | Budget discipline may force early AOT | Whole stack built on unmeasured floor; felt-slow UI ships | milestone owner | Budgets in CI; M2/M4 gates enforce; throttled clickthrough passes |
| P1 | 8. Verdict typed outcome enum + schema'd retry-after + retrievable refusal ids + one scoped-name grammar + SSE empty-diff/cursor invariant | Lauret | Minor schema churn now | Post-ship enum addition breaks four surfaces at once | ADR-07/11/12 author | Red-path: every non-admit returns known enum; round-trip name test passes |
| P1 | 9. Native-TCB adversarial harness (seeded vault-leak/contract-violation bodies) as release gate; parse-depth ceiling; timing-indistinguishable resolution; boot attestation of dispatch table | Schneier | Harness build cost | TCB with real authority sits outside the attacked boundary | ADR-10/07 author | Seeded native leak fails release; 10^5-nest returns PARSE_BUDGET |
| P1 | 10. Mutation testing extended to grammar gate/resolver; continuation_coverage rows with monotone floor; canary replays parse→lower from canonical_text; cross-kernel hermeticity probe | Bach | Slower nightly | Relocated bans un-scored; identity-drift canary tautological | ADR-02/04/05/07 author | Seeded ban-weakening mutant fails; untouched decoder path fails gate |
| P1 | 11. Milestone ladder is a machine gate: CI refuses M(n+1) merges while M(n) kill-tests red | Torvalds | CI rigidity | Deepest-bet ordering rests on a promise; revisions 3/5/7/10 unenforced | CI owner | Fake M2 branch with disabled M1 kill-test is rejected |
| P2 | 12. Coherence batch: glossary scope→name_pointer; split "condition"; reconcile continuation kinds; define/remove kind='module'; continuation.epoch purpose; jsonb discriminator CHECKs; durable_condition FK+state CHECK; history admission FK; resolver visibility predicate | Evans + Celko | Spec-text churn | Implementers build the glossary, not the schema | ADR-03/05 + GLOSSARY author | Greps/constraint red-paths in revisions pass |
| P2 | 13. Agent-competence evals pre-M5: authoring pass@k, fuel-vs-iteration budget from eval P95, restart-decision accuracy | Karpathy | Eval infra cost | "Convergent" loop that never converges; honest agents hit budget walls | ADR-12 author | Eval rows in CI beside verifier_coverage |
| P2 | 14. C3 riders: product #2 analytics-shaped; stranger-review gate on reference dashboard at M6; multiselect as verifier-checked sugar if reference app has a tag field | Jobs + Torvalds | Delayed chart gratification | N=1 closure never tested where it's known weakest | product owner | Second product ships zero new primitives or roster reopens |

Deferred P2s (tracked, not mandated): Kleppmann F2 (cross-kernel subscription-index coherence — fold into rev 8 verification), Kleppmann F3 (high-fanout event wakes in writer txn), Schneier F3 timing details beyond rev 9, Jobs F4 (presence/field-merge), Karpathy F3/F4 beyond rev 13, Majors F5 (fold into rev 6), Bach F3/F5 (fold into rev 10), Allspaw F4 (step.failed restart set — fold into rev 3).

**RESOLVED RED FLAGS:** none yet (nothing re-audited).
**OPEN RED FLAGS (block):** Celko (rev 1), Bach (rev 2). Conditional reversion clauses live on rev 3 (Allspaw) and rev 4 (Karpathy).
**ACCEPTED RISKS:** unmeasured perf envelope until rev-7 budgets land; N=1 vocabulary closure until product #2; permanent epoch/decoder surface accretion (tracked metric); human approval gate remains residual attack surface (rev-4 delta narrows it); single-Postgres blast radius accepted for v1 (rev 6 keeps diagnosis alive).
**NEXT AUDIT TARGETS:** executed DDL + verifier code at M0/M1 (Celko, Bach re-audit); recovery + epoch drills (Allspaw, Majors); injection corpus + agent evals at M5 (Karpathy, Schneier); SBOM/licensing once deps vendored (Meeker); DX/docs onboarding once artifacts exist (Jansen, Procida); concept-doc alignment (unprovided in this run).

GATE: Phase 5 complete. Next: Phase 5b.

=== PHASE 5b: PLAN ASSEMBLY — roster: (orchestrator; inherits Phase 5 verbatim) ===

**Workstream A — Identity & Store Integrity** (revs 1, 3, 10, 12): fix history DDL → coherence batch → byte-restore + drill → canary de-tautologized + continuation_coverage. Every step gated by rev 11's machine ladder. Milestone: M0/M1 cannot close while rev 1–2 red.
**Workstream B — Evaluation Correctness & Performance** (revs 2, 7): regel-native oracle first (blocks trust in everything the interpreter writes), then budgets wired into M2/M4 gates; felt-latency gate lands with M4.
**Workstream C — Operations & Epochs** (revs 5, 6, 11): ADR-13 + machine milestone gate early (they enforce the rest); epoch fence + drills before first real `migrate --commit`.
**Workstream D — Surfaces & Agents** (revs 4, 8, 9, 13, 14): Verdict enum + name grammar before any client code; native-TCB harness with std build-out; injection corpus + agent evals gate M5; C3 riders gate M6.
**Sequence rule inherited:** every P0/P1 resolution appears as an explicit step before the work it blocks (rev 1 before any catalog reliance; rev 2 before interpreter trust; rev 11 before all other gates are meaningful).
**Milestones:** M0/M1 passage = revs 1, 2, 11 green + rev 3 drill exists. M2/M4 passage = rev 7 budgets met, rev 5 fence tested, rev 6 DB-kill drill. M5 passage = revs 4, 13 gates green. M6 passage = rev 14 riders + re-audit scoreboard clean.
**Deferred:** the tracked P2 list above, each owner = touched-ADR author, ticketed in spec/STATE.md at next update.

GATE: Phase 5b complete. Next: Phase 6 (iteration — operator-directed; re-audit contract in REPORT.md).
