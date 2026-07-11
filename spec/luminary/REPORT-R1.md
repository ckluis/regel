# LUMINARY REPORT-R1 — regel Phase 1 architecture, targeted re-review (iteration 1 of max 2)

Run: 2026-07-10 · Phase 6 re-audit per REPORT.md's re-review contract · roster = the 12 flagging experts from the re-review map, each re-checking only their mandated revisions against the revised corpus (spec/architecture/, post ARCH-R1, ledger REVISIONS-R1.md). Per-expert artifacts: spec/luminary/experts/<name>-r1.md (12 files). Experts ran on opus; adjudication and this verdict on fable. Unchanged round-1 findings carry forward and were not re-litigated.

---

## VERDICT: **GO**

All 14 mandated revisions ruled SATISFIED by their declaring/filing members. Both accepted P0 red flags cleared by their declarers with cited resolving artifacts. Both withdrawn-conditional red flags stay withdrawn — the revert triggers (revisions 3/4 shipping ungated) did not occur; both gates verified as mechanical `required` checks in ARCHITECTURE §5.1, not prose. All documented deviations accepted. Zero new P0 or P1 findings; the re-review surfaced 7 new P2 and 16 new P3, all backlog-grade hardening of the very machinery the revisions added — none blocks, none moves a load-bearing decision. GO is per REPORT.md's own frame: "GO is earned when the two P0 red flags are cleared by their declaring members and revisions 3/4 land as release-gated." Both conditions are now met.

## Per-revision satisfaction table (14/14 SATISFIED · 0 PARTIALLY · 0 NOT)

| Rev | ADRs | Re-reviewer(s) | Ruling | Key evidence |
|---|---|---|---|---|
| 1 | ADR-03 | Celko (declarer) | **SATISFIED** | `name_pointer_history` unpartitioned (PG16-verified); `btree_gist` + `tstzrange &&` exclusion creatable; CI executes real DDL + overlap-rejection kill-test (23P01) |
| 2 | ADR-04, 07 | Bach (declarer) | **SATISFIED** | Reference reducer shares no production code path; seeded mutants live in the production evaluator (not mocks), one per layer; hard release gate in ADR-07 §5 |
| 3 | ADR-02, 03 | Allspaw + Schneier (boundary) | **SATISFIED** | Self-certifying byte-restore fails closed; I9 no-role-ever-regains-UPDATE; §4a runbook; CI Gate 4 drill wired into §5.1 M0 gate-set as `required` check. Boundary: **HOLDS** |
| 4 | ADR-12, 07 | Karpathy + Schneier (containment) | **SATISFIED** | Confused-deputy in abuse model; injection corpus M5-BLOCKING as `required` §5.1 check; seeder attribution scope-chain-validated; delta render = precondition of approve. Containment: **HOLDS** — delta machine-derived from content-addressed AST, not agent claims |
| 5 | ADR-08, 06, 05, ARCH | Kleppmann + Majors + Allspaw | **SATISFIED** | One-row `epoch_current` fence; SSI probe passed — O4 park-vs-commit is a genuine two-edge rw-cycle SSI aborts; fence enforces serialization-order coherence (the right invariant); ADR-05 §7 SERIALIZABLE present and sufficient; fence_tripped/boot_refused golden signals #14/#15; revert runbook drilled at M6 |
| 6 | ADR-13 (new), 05/06/08/11 | Majors (declarer) | **SATISFIED** | ADR-13 resolves the 12-ADR dangling "health surface"; 24+2 signals, 11 SLOs; out-of-band emission verified Postgres-independent end-to-end; reaper breaker in ADR-06 §5; typed-emitter PII policy + CI sweep. Owned OTLP exporter: **RATIFIED** |
| 7 | ADR-04, 11, 07, ARCH | Carmack + Jobs (latency gate) | **SATISFIED** | Four budgets as benchmark-enforced `perf_budget` rows gating M0; snapshotHash summed-digest probe: **SOUND** (abelian, O(changed slots), 64-bit fine for a drift detector); tsgo N=32 + ADMISSION_BUSY; wan-150 gate un-gameable (50ms echo < one RTT — only real echo passes), M4→v1 |
| 8 | ADR-07, 12, 11 | Lauret (declarer) | **SATISFIED** | 7-value typed `outcome`; every refusal (incl. pre-BEGIN) durable + retrievable; one qname grammar, three encodings retired; SSE cursor invariant specified. Verdict verified as ONE coherent object post R1-04+R1-08 |
| 9 | ADR-10, 07, 12 | Schneier (declarer) | **SATISFIED** | Native-TCB harness release-gated with monotone coverage + honest trusted-for statements; MAX_PARSE_DEPTH ahead of all budgets; timing floor + KS release gate; H_dispatch pinned/recomputed/boot-refuse |
| 10 | ADR-02, 04, 05, 07 | Bach (declarer) | **SATISFIED** | All four legs shipped despite the cut-off applying agent: grammar-gate/resolver mutants, `continuation_coverage` monotone floor, two-leg parse→lower canary, cross-kernel randomized hermeticity probe |
| 11 | ARCH, RISKS | Torvalds (declarer) | **SATISFIED** | Mechanism, not promise: branch-protection required checks, quarantined-reads-as-red, signed auto-expiring override, gate-of-the-gate self-test; residuals honestly stated in R5 |
| 12 | ADR-03, 05, GLOSSARY | Celko + Evans | **SATISFIED** | All nine coherence items substantively met; three-term "condition" vocabulary adopted in prose (not just glossary); taxonomy matches CHECK string-for-string |
| 13 | ADR-12 | Karpathy (declarer) | **SATISFIED** | pass@k runs the REAL ADR-07 pipeline, M5-blocking; fuel formula traceable to eval P95, re-derived per epoch; restart-accuracy narrowing policy (ships disabled until green) honors the mandate — the stronger reading |
| 14 | ADR-10, ARCH | Jobs + Torvalds | **SATISFIED** | Analytics-shaped product #2 rider binding (roster not "closed" until measured); M6 stranger-review is a mechanical gate entry (un-run reads red); multiselect strictly desugars — no new field-type row / mask bundle / totality pair / native TCB |

## Red-flag resolutions (all four closed)

| Flag | Holder | Round-1 status | R1 status |
|---|---|---|---|
| I4 uncreatable exclusion constraint (DATA INTEGRITY) | Celko | Accepted P0 | **CLEARED** by declarer — ADR-03 §1 unpartitioned DDL + CI Gate 2 |
| Conformance oracle blind to regel semantics (CORRECTNESS) | Bach | Accepted P0 | **CLEARED** by declarer — ADR-04 §6 harness 3 + ADR-07 §5 |
| Recovery detection-without-repair | Allspaw | Withdrawn-conditional (C2) | **STAYS WITHDRAWN** — drill is a mechanical M0 `required` gate; revert trigger did not occur. Schneier no-UPDATE boundary: HOLDS |
| Agent-plane injection unmodeled | Karpathy | Withdrawn-conditional (C1) | **STAYS WITHDRAWN** — injection corpus is a mechanical M5 `required` gate; revert trigger did not occur. Schneier containment boundary: HOLDS |

## Deviation rulings (all documented deviations ruled; none rejected)

| # | Deviation (REVISIONS-R1.md) | Ruled by | Ruling |
|---|---|---|---|
| 1 | R1-07: tsgo-under-concurrency gate at M1, not M0 | Carmack | **ACCEPT** — hermetic tsgo host doesn't exist until M1 |
| 2 | R1-07: checkpoint-write budget number at M0, end-to-end verification at M4 | Carmack | **ACCEPT** — sessions exist only at M4; the number itself is not deferred |
| 3 | R1-08: `patch_id` required→optional on Verdict | Lauret | **ACCEPT** — always-present `refusal_id` makes the surface net more retrievable |
| 4 | R1-12: SQL status enum literal `'condition'` kept | Celko + Evans | **ACCEPT** (both) — literal anchors sense (3), maps 1:1 to a named term; renaming a persisted enum creates fresh drift |
| — | R1-07: felt-latency gate at M4→v1 | Carmack | **NOT A DEVIATION** — the mandate's own wording |
| — | R1-08: 7 outcome values vs "four doors" | Lauret | **ACCEPT** — pure widening, no door lost |

## ARCH-R1 scrutiny items (all cleared)

- **Integrator-authored DDL, ADR-03 table (6):** sound per Celko (keys/CHECKs/NULL semantics clean), Bach (coverage tuples support monotone floor + required grid), Lauret (gate_refusal honors retrievability contract). Residuals filed as P3s (perf_budget tier-in-PK — Celko, Carmack-confirmed; missing direction column — Carmack).
- **ADR-06 payload_shape key names:** match the payload shapes ADR-06 actually uses (Celko).
- **ADR-13 registered-extensions weight for agent-eval gauges:** acceptable (Karpathy + Majors, independently) — M5-blocking authority lives in §5.1 required checks, not ADR-13; golden = live-3am-alarm subset, a principled distinction. Nothing that pages is misfiled.
- **spec/milestone-gates.toml referenced-not-created:** **ACCEPTABLE** (Torvalds) — genuinely an M0 CI deliverable; required contents fully enumerated in the §5.1 table and a self-test fails on a missing/drifted manifest, so it is not spec-by-reference.

## New findings introduced by the revisions (0 P0 · 0 P1 · 7 P2 · 16 P3)

**P2 — next-phase tickets, owner = declaring expert (do not block GO):**
1. [Allspaw · ADR-03 §4a] Quarantine/hold-dependents containment leg has no DDL-backed state; Gate 4 rehearses repair but never asserts a bound dependent is actually held fail-closed during the incident.
2. [Karpathy · ADR-12 §3a] pass@k floor gameable via the operator-set retry ceiling `k` — pin `k` per epoch.
3. [Schneier · ADR-07/12] `verdict.get` refusal retrieval is not caller-scoped — a leaked/guessed `refusal_id` is a cross-principal disclosure oracle.
4. [Majors · ADR-13] Owned OTLP exporter has no collector-round-trip conformance gate (silent wire-encoder bug risk — the real hand-rolling hazard).
5. [Majors · ADR-13] Shared ring buffer drop-oldest can evict rare trip events under a metric flood; no event priority.
6. [Kleppmann · ADR-05/08] SERIALIZABLE-everywhere has no serialization-failure retry policy or abort-rate budget.
7. [Kleppmann · ADR-06] A READ COMMITTED serve can dispatch-miss on std-pointers before the guard fires — serve transactions should be REPEATABLE READ.

**P3 — backlog (16):** perf_budget PK needs tier (Celko; Carmack-confirmed); reference reducer is continuation-free, effect-order-across-await not oracle-checked (Bach); "delta" headword not canonical ×3 surfaces + one bare "condition" survives in ARCHITECTURE §6 (Evans); H_dispatch attests build-consistency not provenance; timing indistinguishability CI-proven only (Schneier); reap-rate breaker can flap near the 50% threshold (Majors); prose overclaims all in-flight E-work aborts; O5 reaper/heartbeat/timer exemption unstated (Kleppmann); no floor/ceiling direction column; reference workload unpinned so CEK floor is microbench-gameable (Carmack); orphan refusal-id on failed pre-BEGIN ledger write; qname reserves no `@` handling; `already-admitted` legacy-bool trap; `retry-exhausted` vs `retry_after` naming tension (Lauret); §5.1 markdown gate table can drift silently against the manifest (Torvalds). Full detail in experts/*-r1.md.

## Finding transitions (originals touched by the revisions)
All original findings addressed by the 14 revisions marked **RESOLVED** by their filers (Celko 5/5, Bach 5/5, Karpathy 4/4, Schneier 5/5, Majors 5/5, Carmack 4/4, Lauret 4/4, Evans 5/5; Allspaw F1/F3, Kleppmann F1/F4, Jobs F1–F3, Torvalds F2/F4). Zero REGRESSED. Out-of-scope carries (unchanged, carried forward with prior priorities): Allspaw F2/F4, Kleppmann F2/F3, Jobs F4/F5, Torvalds F1/F3.

## Acceptance check (this report)
Every expert in REPORT.md's re-review map ran (12/12, artifacts in experts/*-r1.md) · every one of the 14 revisions has a satisfaction ruling (14 SATISFIED) · both conditional red flags explicitly resolved (both STAY WITHDRAWN; boundaries HOLD) · all documented deviations ruled (6 ruled, 0 rejected, 1 classified not-a-deviation) · verdict unambiguous: **GO**. The 7 new P2s enter the tracked backlog with owners; none is a mandate — no iteration 2 is required.
