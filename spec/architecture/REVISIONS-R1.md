# REVISIONS-R1 — Phase 3 revision ledger (luminary REPORT.md mandated revisions 1–14)

Status: R1-01…R1-14 ALL APPLIED; integration pass DONE.
Marker convention: every edit site carries a grep-able `R1-<nn>:` marker in the touched
file; integration-pass edit sites carry `R1-INT:`.

## R1-01 — I4 temporal exclusion made enforceable (P0, Celko)
- Files: ADR-03-catalog-schema.md
- Changed: `name_pointer_history` **unpartitioned** (empirically verified on PG 16.13: exclusion constraints are unsupported on any partitioned table; RANGE and HASH variants both fail). `btree_gist` + `tstzrange &&` exclusion now creatable; rationale block documents PG17+ future path. Invariant I4 rewritten. New "CI Verification Gates" section: real-DDL-creatable gate + overlap-rejection kill-test (fails if constraint absent/uncreatable) + no-false-positive guard.
- Markers: 3 in ADR-03. Deviations: none.

## R1-02 — regel-native differential oracle (P0, Bach)
- Files: ADR-04-interpreter.md, ADR-07-admission-pipeline.md
- Changed: ADR-04 §6 gains harness 3 — independent reference reducer (no shared production code paths, dev-only) covering contract enforcement, derived boundary validators, effect-class ordering; divergence in verdicts/validator outcomes/effect order/values = red; three seeded wrong-evaluation mutants (one per layer) must turn the corpus red. ADR-07 §5: oracle wired into release/kill-test gate — green pipeline impossible while oracle red or any seeded mutant survives.
- Markers: ADR-04 ×6, ADR-07 ×3. Deviations: none.

## R1-03 — immortal-store recovery (Allspaw, via clash C2; reverts to P0 if ungated)
- Files: ADR-02-canonical-printer.md, ADR-03-catalog-schema.md
- Changed: ADR-02 new §5.5 self-certifying byte-restore (correct iff rehashes to address; fails closed on digest mismatch; no role ever regains UPDATE — out-of-band audited break-glass physical repair, no standing credential). ADR-03: scrubber DDL note, new invariant I9, new §4a scrubber-trip runbook (detect→quarantine→restore→verify→resume), CI Gate 4 release-gated fault-injection recovery drill with explicit revert-to-P0 language.
- Markers: ADR-02 ×2, ADR-03 ×5. Deviations: none.

## R1-04 — injection defense (Karpathy/Schneier, via clash C1; reverts to P0 if ungated)
- Files: ADR-12-mcp-agent-plane.md, ADR-07-admission-pipeline.md
- Changed: ADR-12 §2 confused-deputy adversary in abuse model; new §4a injection corpus co-equal with PII sweep, M5-BLOCKING, revert-to-P0 language; §6 content-seeder attribution (`{source_kind, source_ref, scope, seeded_by|"unattributed"}`, scope-chain-validated); §7 machine-computed capability/PII/DDL delta rendered beside every green Verdict (render is precondition of approve for surface-widening patches). ADR-07 §1 seeder set bound at admission; §6 Verdict gains `delta` + `seeders`; new red-path tests both files.
- Markers: ADR-12 ×6, ADR-07 ×3. Deviations: none.

## R1-05 — epoch fleet coherence (Kleppmann/Majors/Allspaw)
- Files: ADR-08-epochs-migration.md, ADR-06-kernel-reactor.md, ARCHITECTURE.md
- Changed: one-row `epoch_current` table updated only inside `--commit` (+NOTIFY); per-request/resume fence = guard read piggybacked on every work transaction's first round trip, re-checked at COMMIT (SSI rw-conflict with the flip's UPDATE makes O4-in-txn race-proof); fail-close = terminal drain (503 + diagnostic, rollback, leases lapse, replacement restart). Structured boot-refuse/fence-trip diagnostic (observed/required epoch, binary_version, manifest roots, kernel_id, ts, action). O4 enumeration moved inside the SERIALIZABLE commit; new obligation O5 (fleet coherence). New §6a epoch revert/roll-forward runbook + release-gated staging drill. ARCHITECTURE §2(f), schema box, seam row ADR-08→ADR-06, O1–O5, M6 gate.
- Markers: ADR-08 ×12, ADR-06 ×2, ARCHITECTURE ×4. Deviations: none. Note: fence requires ADR-05 §7 step transaction SERIALIZABLE — integrator syncs ADR-05 wording.

## R1-06 — ADR-13 observability created (Majors)
- Files: **ADR-13-observability.md (new)**, ADR-05, ADR-06, ADR-08, ADR-11
- Changed: ADR-13 = the spec for "health surface" (resolves the 12-ADR dangling reference): dotted metric schema + cardinality bounds; 24 golden signals (+2 meta) incl. `epoch.fence_tripped`/`epoch.boot_refused` from R1-05; initial SLOs (calibrated at M0/M1/M2/M4/M6); out-of-band emission surviving Postgres loss (stdout JSON + owned OTLP-shaped push exporter, ring-buffered, never blocks; in-Postgres health surface demoted to a view); reaper backpressure (bounded batches, token bucket, jittered backoff) + reap-rate breaker (60s window, structured trip signal, half-open probe) integrated in ADR-06 §5; PII policy (typed-fields-only emitter, seeded-PII CI sweep red-path). Surgical pointer edits in ADR-05/06/08/11.
- Markers: ADR-13 + 4 ADRs, 12 total. Deviations: none (judgment call: owned exporter, no OTel SDK dependency — matches owned-wire-client precedent).

## R1-07 — performance budgets (Carmack; felt-latency gate via clash C3/Jobs)
- Files: ADR-04, ADR-11, ADR-07, ARCHITECTURE.md
- Changed: ADR-04 new §8 budgets as `perf_budget` data rows — CEK ≥1M transitions/sec/core; ≤50k transitions/request p95; metering tax ≤10%; checkpoint ≤1 write/interaction, CFR delta ≤64KB p95 — benchmark-enforced before M0 closes. ADR-11 §4 snapshotHash redesigned: incremental order-independent summed digest `Σ h(slotId‖value) mod 2^64`, O(changed slots); new §9 WAN felt-latency machine gate (`wan-150`: 150ms RTT, 1.6/0.768 Mbps; input→echo ≤50ms, action→confirmed-commit ≤300ms p95) gating M4→v1. ADR-07 §3 tsgo-in-txn measured at N=32 concurrent admissions (p95 ≤40ms / p99 ≤80ms, retry ≤5%), `ADMISSION_BUSY` pre-BEGIN backpressure, not silent stretch. ARCHITECTURE M0/M4 gates wired; optimistic-echo trigger switched from complaints to the machine gate.
- Markers: ADR-04 ×4, ADR-11 ×10, ADR-07 ×4, ARCHITECTURE ×3.
- **Deviations (documented, justified):** (1) tsgo-under-concurrency budget gate-enforced at M1, not M0 — hermetic tsgo host doesn't exist at M0; (2) checkpoint-write budget *number* set before M0 but end-to-end reference-app verification is an M4 gate (sessions exist at M4); (3) felt-latency gate is M4→v1 by the mandate's own design.

## R1-08 — contract hygiene (Lauret)
- Files: ADR-07, ADR-12, ADR-11
- Changed: ADR-07 §6 typed `Verdict.outcome` enum — `admitted | already-admitted | rejected | stale-base | retry-exhausted | budget-exhausted | busy`; typed `retry_after {millis, cause}`; durable `refusal_id` for every refusal incl. pre-BEGIN budget/parse/busy, retrievable via `verdict.get {id}` / `catalog://verdict/{id}`. ADR-12: one canonical scoped-name grammar `qname := name "@" scope` spec'd once, used across tools/resources/search (three encodings retired); refusal retrieval surfaced through agent plane; §5 refusal ledger mints `refusal_id` before any refusal returns. ADR-11 §2 SSE invariant: cursor = checkpoint `step_seq`; empty diffs emit zero-op frame (advances cursor, carries snapshotHash); heartbeat = `:keepalive` comment (no cursor advance); stale/unknown cursor → full resync.
- Markers: ADR-07 ×6, ADR-12 ×10, ADR-11 ×2.
- **Deviations (documented):** `patch_id` relaxed required→optional on Verdict (pre-parse refusals have no content hash; offset by always-present `refusal_id` — net more retrievable); enum has 7 values vs finding's 4 doors (added `rejected`, `busy` for completeness — no narrowing).

## R1-09 — native-TCB harness + attestation (Schneier)
- Files: ADR-10-std-world.md, ADR-07, ADR-12
- Changed: ADR-10 new §8 native-TCB adversarial harness (`gate/native-tcb/`, release gate, monotone coverage rows): seeded vault-leaking / contract-violating / effect-order-violating native std bodies must be caught (vault CHECK+V2, oracle+V4, oracle+step-txn) with explicit trusted-for statement where authority is irreducible. ADR-10 §2 boot-time attestation: `H_dispatch` = SHA-256 over sorted (intrinsic, signature hash, body hash) triples pinned in the epoch row; recomputed every boot; mismatch = structured boot-refuse cause (extends R1-05 diagnostic). ADR-07 §3 deterministic `MAX_PARSE_DEPTH` ceiling ahead of all budgets, refusal `PARSE_DEPTH` with retrievable `refusal_id`. ADR-12 §3 timing-indistinguishable name resolution: visibility-first shared fast-fail path + fixed latency floor; KS + p99-gap statistical test as release gate.
- Markers: ADR-10 ×7, ADR-07 ×4, ADR-12 ×2. Deviations: none.

## R1-10 — testing depth (Bach)
- Files: ADR-02, ADR-04, ADR-05, ADR-07
- Changed: ADR-07 dual mutation testing extended to grammar gate + resolver (relocated-ban mutants must die; harness self-test red-path; `component` key generalizes coverage rows). ADR-05 new §8.5 `continuation_coverage` rows (frame_kind × cfr_version × decoder) — required grid computed from closed enumerable sets, monotone floor, coverage regression = release blocker; red-path tests 11–12 added. ADR-02 world-rehash canary now two legs — encoder leg (stored AST) + load-bearing pipeline leg replaying full parse→lower from `canonical_text` over the whole historical corpus. ADR-04 new §6.5 cross-kernel randomized hermeticity probe (≥2 kernel instances, distinct builds where available, randomized map seeds/scheduling/cold checker; divergence red; nightly per-release gate; self-seeded validation).
- Markers: ADR-02 ×1, ADR-04 ×1, ADR-05 ×3, ADR-07 ×3. Deviations: none. (Applying agent was cut off before reporting; edits verified complete by direct inspection.)

## R1-11 — machine-gated milestone ladder (Torvalds)
- Files: ARCHITECTURE.md, RISKS.md
- Changed: ARCHITECTURE §5 reframed "machine gate, not a promise"; new §5.1 — `spec/milestone-gates.toml` manifest (per-milestone gate-suites + path/label attribution globs); mechanical CI refusal via branch-protection required checks (M(n+1) cannot merge while any M(0..n) suite red/quarantined/skipped); per-milestone gate-set table wiring R1-01/03/07/10→M0, R1-02/09/10→M1, R1-02/10→M2, R1-09→M3, R1-07→M4, R1-04/09→M5, R1-05→M6; single signed release-owner-only append-only-audited auto-expiring `gate-override` escape hatch; gate-of-the-gate self-test (seeded red M(n) must mechanically block an M(n+1) merge). RISKS R5 rewritten: staging is now mechanism; residuals (manifest rot, misclassified work, override abuse) each mitigated; kill-test added. Severity reduced; reordering left to integrator.
- Markers: ARCHITECTURE ×8, RISKS ×3. Deviations: none. Note: `spec/milestone-gates.toml` is an M0 CI deliverable, not spec — referenced, not created.

## R1-12 — coherence batch (Celko/Evans)
- Files: ADR-03, ADR-05, GLOSSARY.md
- Changed, per item: (1) scope attributed to `name_pointer` (GLOSSARY rewrites + ADR-03 §2 note; DDL already correct); (2) "condition" split into three named senses in GLOSSARY, drift-site prose renamed in ADR-05; (3) continuation-kind taxonomy (workflow/session/request) reconciled with the CHECK; (4) `kind='module'` REMOVED from the definition CHECK (unused corpus-wide); (5) `continuation.epoch` purpose stated (provenance stamp; readers = lattice-narrowing enumeration + observability; fleet gating is the R1-05 fence, not this column); (6) `wake_kind_shape` jsonb-discriminator CHECK on ADR-05 (ADR-03 has no load-bearing discriminator jsonb — noted); (7) `durable_condition` gains `resolved_restart_fk` (ALTER, circular ref), `resolved_consistency` state CHECK, `class_shape` shape CHECK; (8) `name_pointer_history.admission_id REFERENCES admission(id)` added to unpartitioned DDL; (9) resolver visibility predicate specified in ADR-03 §3 (`module_of(name)=:caller_module`) + GLOSSARY "visibility" term, cross-ref ADR-12 §3 R1-09. Red-path tests extended (ADR-03 +2, ADR-05 +2).
- Markers: ADR-03 ×7, ADR-05 ×11, GLOSSARY ×6.
- **Deviation (documented, justified):** SQL status enum literal `'condition'` kept (language-level split only) — renaming the literal would create fresh drift with ARCHITECTURE.md; integrator adopts three-term vocabulary in ARCHITECTURE prose.

## R1-13 — agent-competence evals (Karpathy)
- Files: ADR-12
- Changed: new §3a authoring eval — N≥50 monotone task suite against the REAL ADR-07 pipeline, pass@1 ≥0.5 AND pass@k ≥0.9, M5-BLOCKING (same standing as §4a); §5 fuel capacity eval-derived — `capacity = ceil(P95_iterations_to_green × cost_full_pipeline × 1.5)`, re-derived per epoch + dialect bump, untraceable-to-P95 = red; §7 restart-decision accuracy ≥0.95 over M≥30 labeled scenarios gating the agent-facing `condition.restart` authority — red/absent metric does NOT block M5, instead `condition.restart` ships disabled until green (narrowing of authority, not of the gate — the mandate's requested policy). 3 new red-path tests; Constraint #2 updated.
- Markers: ADR-12 ×8. Deviations: none.

## R1-14 — C3 riders (Jobs/Torvalds)
- Files: ADR-10, ARCHITECTURE.md
- Changed: ADR-10 §5 vocabulary-addition policy = reversibility asymmetry (deferrals deletable, additions immortal epoch surface → bias-to-defer with honesty riders); `multiselect` re-specified as verifier-checked sugar desugaring to `relation`(hasMany)+`select`-multi, V6-parity byte-identical, no new field-type row / mask bundle / totality pair / native TCB, conditioned on the reference app exercising a tag field; §7 charts/aggregation deferred-with-riders paragraph; sugar-is-pure-expansion red-path test. ARCHITECTURE §5: product #2 must be analytics-shaped (tests closure at the known charts/aggregation gap; roster not "closed" until measured against a real analytics product); §5/§5.1 M6 stranger-review gate on the reference dashboard as a mechanical gate entry (verdict recorded or reads red); §6 exclusion rows updated.
- Markers: ADR-10 ×5, ARCHITECTURE ×6. Deviations: none.

## Integration pass — DONE

All accumulated-notes items below applied or dispositioned; every integration edit site
is marked `R1-INT:`. Edits made (file → what):

- ADR-03 → admission ledger gains the R1-04 `seeders` + `verdict_delta` columns; new §1 table-(6) block DDLs `gate_refusal` (refusal_id PK, non-green `outcome` CHECK, nullable `submitted_hashes`), `verifier_coverage`, `perf_budget`, `continuation_coverage` — every "DDL'd in ADR-03, flagged there" pointer now resolves.
- ADR-05 → §7 step transaction explicitly `ISOLATION LEVEL SERIALIZABLE` (the R1-05 fence/O4 dependency).
- ADR-06 → boot sequence names the `H_dispatch` attestation recompute/compare (R1-09); `task.payload` gains a per-kind discriminator CHECK (R1-12 pattern); §4 notes `:caller_module` = `''` at external entry, C-register-derived inside evaluation.
- ADR-07 → two DDL pointers corrected (admission columns live in ADR-03 §1, not §5; gate_refusal now authored). Verdict schema verified coherent as one object (R1-04 delta/seeders + R1-08 outcome/refusal_id) — no contradiction found.
- ADR-08 → epoch table gains `dispatch_attestation` (H_dispatch, R1-09); boot-refuse diagnostic gains pinned/computed `h_dispatch` fields.
- ADR-10 → §2 pointer corrected: the epoch attestation column is DDL'd in ADR-08 §2, not ADR-03 (contradiction between R1-09's edit and the epoch DDL's home).
- ADR-04 → note that C's `def_hash` supplies the resolver's `:caller_module` (R1-12).
- ADR-09 → PR-check/merge door switches on typed `Verdict.outcome` (R1-08 pointer).
- ADR-11 → §7 multiselect renders via the existing relation/select-multi path per ADR-10 §5 sugar (R1-14 traceability, no new surface).
- ADR-13 → registered-extension candidates: R1-13 agent eval gauges, R1-07 perf/felt-latency measured values, optional R1-08 zero-op-frame counters.
- ARCHITECTURE → twelve→thirteen ADRs (3 sites); two ADR-13 seam rows; §2(c) three-term "condition" vocabulary (SQL literal kept); M2 harness count two→three; §5.1 gate-sets gain ADR-13 M0/M2/M4/M6 wiring, R1-07 tsgo-in-txn at M1, R1-13 eval gates at M5, PG16+ note at M0.
- RISKS → R1 (byte-restore + two-leg canary), R2 (I4 + decode floor), R4 (oracle + corpus-breadth residual), R5 (severity-reduced note; slot kept — deepest-bet-first ordering, renumbering would break cross-refs), R6 (signals/breaker; envelope-measured residual), R7 (TCB harness + attestation downgrade), R8 (telemetry + timing-oracle partially closed), R11 (confused-deputy + delta-informed approver + eval-backed competence), bet #6 (R1-14 closure riders).
- GLOSSARY → added qname, confused deputy, content seeder/third principal, blast-radius delta, injection corpus, health surface→ADR-13, golden signal, signal registry, reap-rate breaker, re-expiry ratio, reversibility asymmetry, stranger-review gate, verifier-checked sugar; fixed verifier-suite roster (four→six) and interpreter line (CPS→defunctionalized CEK) to match ADR-07/ADR-04.
- SUMMARY → refreshed to post-R1 orientation (13 ADRs, REVISE→applied, two P0 resolutions, machine-gated ladder, REVISIONS-R1.md pointer); 25 lines.

Already satisfied / moot (no edit): ADR-05 §8.5 DDL pointer (true once ADR-03 gained the table); ARCHITECTURE I4-kill-test-in-ladder and M6 stranger-review manifest entry (R1-11/R1-14 already wired them); ADR-03 §3 resolver visibility text (R1-12 already covers caller_module semantics); `admission.tsgo_ms` as an ADR-13 signal (already golden row #3).

## Accumulated cross-file notes for the integration pass
- (R1-11) ADR-13 observability suites should be named in the §5.1 milestone-gates manifest (M0 registry/stall alarm, M2 exporter + Postgres-loss + reaper-saturation drills, M4 SLOs).
- (R1-12) ADR-06 `task.payload` jsonb needs the same discriminator CHECK; resolver `:caller_module` binding = `''` for external request entry; ADR-04 supplies `:caller_module` from C-register `def_hash`→module; ARCHITECTURE §2 (~line 110) should adopt the three-term "condition" vocabulary (keep SQL literal).
- (R1-13) ARCHITECTURE M5 gate rows: add authoring pass@k floor + fuel-from-eval-P95 entries; note restart-accuracy gates `condition.restart` authority, not M5. ADR-13 candidate signals: `agent.pass_at_k`, `agent.iterations_to_green_p95`, `agent.restart_decision_accuracy`. RISKS R11: competence now eval-backed; residual = eval-suite breadth.
- (R1-14) RISKS: one-app-closure risk mitigated by riders; residual = closure unproven until product #2 runs. ADR-11: pointer to ADR-10 §5 multiselect sugar spec for render-path traceability (no new surface). GLOSSARY candidates: reversibility asymmetry, stranger-review gate, verifier-checked sugar. milestone-gates manifest M6 gains stranger-review entry.
- ADR-05 §7 step-transaction isolation wording must say SERIALIZABLE (R1-05 fence dependency).
- ADR-03 DDL additions flagged by other revisions: admission-row content-seeder columns (+ optional persisted delta) [R1-04]; `gate_refusal.refusal_id` PK + `outcome` column, nullable `submitted_hashes` [R1-08]; `perf_budget` table [R1-07]; epoch `dispatch_attestation` column [R1-09]; `continuation_coverage` table [R1-10]. R1-12 owns ADR-03 edits — integrator reconciles whatever R1-12 doesn't cover.
- ADR-08 §2 epoch table: gains `H_dispatch` attestation column + boot-refuse reason (R1-09).
- ADR-06 boot sequence: attestation recompute/compare step (R1-09).
- ARCHITECTURE.md: seam rows for ADR-13 (suggested text in R1-06 agent report — registry-compiled-into-epoch-binary + ADR-06→ADR-13 reaper pacing); milestone wiring M0 (registry/stdout/stall alarm, perf budgets), M2 (exporter, Postgres-loss + reaper-saturation drills), M4 (fan-out SLOs, felt-latency + checkpoint gates), M5 (injection corpus), M6 (SLO recalibration, drills in release suite); conformance harness count two→three (R1-02); PG16+ pin sufficient (R1-01); I4 kill-test into ladder (R1-01/11).
- RISKS.md: I4 risk mitigated (R1-01); Bach P0-2 mitigated + residual corpus-breadth note (R1-02); R1 "nothing survives a moved hash" → rehearsed byte-restore, R5 staging-mechanism note (R1-03); R11 confused-deputy named (R1-04); R6 signals + stampede damped, R8 telemetry residual partially closed (R1-06); R6 "envelope asserted not measured" discharged by M0 gate (R1-07); native-TCB "tests not proofs" + unattested-binary downgraded, parse-depth + timing-oracle P2s resolved (R1-09).
- GLOSSARY: terms flagged — confused-deputy/content-seeder/third principal, blast-radius delta, injection corpus (R1-04); health surface→ADR-13, golden signal, reap-rate breaker, re-expiry ratio, signal registry (R1-06); `qname` (R1-08). R1-12 owns GLOSSARY edits — integrator reconciles leftovers.
- ADR-09: PR-check renderer switches on Verdict `outcome` (R1-08) — pointer fix if needed.
- ADR-13: consider `perf_budget` measured values + felt-latency/tsgo metrics as signals (R1-07); zero-op-frame/heartbeat signal optional (R1-08).

## Post-integration verification (ARCH-R1 orchestrator)
- Final marker matrix grep-verified: every revision's `R1-<nn>:` marker present in exactly the files its ledger entry claims (plus additive cross-refs in ADR-13 for R1-07/R1-13). Secondary cross-reference mentions may appear as parenthetical `(…, R1-<nn>)` without a colon; every touched file's primary edit site carries the colon form. Two anchors normalized to colon form post-integration: ADR-11 §2 (R1-08 SSE invariant), ADR-12 §2 (R1-08 qname grammar).
- SUMMARY.md = 25 lines, reflects post-R1 state. ADR-13 exists. Integration edits marked `R1-INT:` in 15 files.
