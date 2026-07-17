# regel — claim ⇄ evidence ledger (Stage E, v1 close-out)

*Every load-bearing claim in the concept document
(`experimentalArchitectures/regel.html`) mapped to {a **test**, a **demo** step, or a
**named residue**}. No claim un-evidenced and un-labeled. Tests are `go test`
identifiers at HEAD; demos are scripts under `scripts/` (all exit 0 at the Stage-E
gate); residues are named in `spec/gates/STAGE-E.md` §residues with a why-safe.*

Legend: **T** = red-path test (fails without the machinery) · **D** = runnable demo
with captured output · **R** = named residue.

## §1 The claims (31)

| # | Claim (concept-doc language) | Kind | Evidence |
|---|---|---|---|
| C1 | Code enters by admission transaction — canonical print + tsgo + verifiers + DDL + name pointers commit in ONE transaction | T+D | `internal/admission` pipeline suite (zero-trace asserts, `assertZeroTrace`); `scripts/demo-admit-rollback.sh` steps 1–3 |
| C2 | Content-addressed: the hash is the identity; exactly one canonical rendering; renames are metadata | T | `internal/rast TestMutationSameHash` (13 perturbations ⇒ same hash); `internal/gitproj TestDeterminismReleaseGate` (two-fold byte-identical); `regel canary` + `TestWorldRehashCanaryGreenThenTamper` |
| C3 | Structural diffs are the only diffs | T | printer normalization suite (`internal/rast`); git projection folds canonical text only (`TestGitOracleVerifiesRepo`) |
| C4 | Pauses as continuations; ANY node resumes; effects exactly-once | T+D | `TestKill9CrossKernelExactlyOnce`, `TestKill9TaakWorkflowRestart`; `scripts/demo-kill9-resume.sh`, `scripts/demo-taak.sh` |
| C5 | A paused workflow resumes against the exact code hashes it started with (as-of for code deletes workflow versioning) | T+D | `TestTwoEpochStrandedImpossibility` (provenance stamp stays epoch-1 across two migrations); `scripts/scenario-c-deploy-survive.sh` |
| C6 | Environments serialize stably (the "years" bet) | T+R | ADR-05 kill suite 10/10 + year-old-resume (Stage B); golden CFR corpus + decode-coverage monotone floor (`internal/cfr/golden_test.go`, 30 blobs); **R-B2-scope**: "years" remains simulation-proven — real wall-clock years are unfalsifiable in v1 (why-safe: golden corpus per epoch makes decode drift a release-blocking regression, the failure mode the claim actually risks) |
| C7 | Capability environments: an unauthorized call is unnameable, not merely denied | T | Stage-B capability smuggle test (forged CapToken refused pre-machine-re-entry, zero trace); `TestSQLQueryTypedParameterized` leg (c) ungranted parks `capability.revoked` |
| C8 | Deploy is a commit; rollback is a WHERE clause (as-of) | T+D | `scripts/demo-admit-rollback.sh`; `scripts/scenario-d-asof-rollback.sh` (UI-observed); HTTP `?as_of=` + `?as_of=` session mount (`internal/kernel/asof_mount_test.go`) |
| C9 | Nothing lands unverified — engineer, tenant, agent walk ONE gate; no privileged CI side door | T+D | same `admission.Admit` path serves CLI/HTTP/MCP/git-merge (`gitproj.Merge` IS the admission txn, Stage C); scenario-a (tenant), scenario-b (agent), demo-admit-rollback (engineer) |
| C10 | tsgo — a real static checker — runs inside the insert transaction, in milliseconds | T | Stage-C N=32 tsgo-in-txn p95 = 12ms (perf_budget rows); `TYPECHECK_BUDGET` deterministic ceiling |
| C11 | Rejected code never becomes code (zero trace) | T+D | `assertZeroTrace` battery across V1–V6 fixtures; REVIEW-PRE-E §3 hand-driven rejections (0 rows, audited refusals); scenario-b REFUSED leg |
| C12 | Two tiers: trusted unmetered; tenant/agent fuel-metered | T | Stage-B exact-budget fuel (`TestExactBudgetFuel`, park at exactly T-1); metering tax ≈0% (Stage A) |
| C13 | Fuel exhaustion parks as a durable condition — never panics | T+D | fuel-park tests (Stage B); `scripts/demo-mcp-session.sh` budget-exhaustion leg |
| C14 | Workflows, UI sessions, durable conditions are continuation rows with wake conditions | T | sessions as `continuation kind='session'` rows (ADR-11 suite); `TestTaakSignalDurableCondition`; match-predicates + event wakes (Stage D) |
| C15 | One history tier, both substances: "who changed this workflow" ≡ "who changed this record" — the SOC 2 answer is a SELECT | T+D | trigger-written history tables (`TestHistoryExcludesPii`); admission ledger + `gate_refusal` audit rows; scenario-a asserts history preserved across schema evolution |
| C16 | The kernel ships zero business logic — app logic is rows | T+D | the proof CRM: `grep -rin crm internal/ cmd/` non-test = 0 app logic; `TestCRMReferenceAppEndToEnd` admits `crm/` from disk; `scripts/crm-setup.sh` |
| C17 | Verifiers in the txn: capability-audit, pii-flow, contracts, catalog-parity (+capture, derive-parity) | T | V1–V6 red-path fixtures + hostile corpus (19+) + dual mutation (13 mutants, all killed) + monotone `verifier_coverage` |
| C18 | "Declared but unenforced cannot be admitted" — catalog parity | T | V3 catalog-parity red-path fixture family (Stage C) |
| C19 | PII flow: no vault value escapes unmasked; six-leaf render masking | T+D | `TestC4V2FullControlFlowEscapesReject` (full statement grammar, Stage E), `TestV2PiiEscapeSurvivesCollidingTypeImport`, PII telemetry sweep; `scripts/demo-erf-derive.sh` grep-ABSENT steps |
| C20 | Vault + crypto-shred: ciphertext permanently undecryptable, attestation written, history stays clean | T+D | `TestVaultSealAndCryptoShred`; `scripts/scenario-e-pii-shred.sh` (oracle-recomputed attestation; base+history+session grep ABSENT); `regel vault-put` + `regel shred` CLI doors |
| C21 | Durable-condition restarts render as choices for an operator — or an agent | T+R | operatorPlane condition inbox (`TestOperatorPlaneListsConditionsAndRefusal`); agent-facing decisions measured by the M5 §7 gate; **R-restart-flip**: agent `condition.restart` remains DISABLED unless the §7 gate row reads green at the pinned epoch (flip is mechanized + red-pathed) |
| C22 | Deploys and node loss drop nothing | T+D | kill -9 mid-step/mid-workflow suites; O5 epoch fence + `--wait-for-epoch`; `scripts/scenario-c-deploy-survive.sh` (parked workflow survives TWO deploys); 10k/50k storms exactly-once |
| C23 | Settings is an admission client; a tenant's custom field is a scoped overlay row; config and code are one substance | D+R | `scripts/scenario-a-field-add.sh` (tenant field-add through the gate, `--base` optimistic concurrency, old sessions resync); **R-settings-form**: no point-and-click Settings FORM ships in v1 — the tenant door is the HTTP admission POST (why-safe: the gate, scoping, and re-derivation are the product truth being claimed; the form is presentation) |
| C24 | The workflow canvas and form designer are projectional editors over the catalog | R | **R-projectional-editors**: not built in v1 (concept-doc surface, no GATE-1 §4 deliverable binds it; why-safe: derived form/table/board/dashboard prove the catalog→UI direction; the editor direction rides the same admission door scenario-a drives) |
| C25 | The MCP server ships in the kernel: catalog query, fetch by hash, patch submit, verdicts as structured data | T+D | 11 tools/6 resources/3 prompts over real JSON-RPC (Stage C); `scripts/demo-mcp-session.sh`; `scripts/scenario-b-agent-patch.sh` (dry-run → approval token → commit → live) |
| C26 | Agents are ordinary capability principals under admission-fuel budgets | T | agent-key binding + fuel bucket + BUSY-shed (Stage C); M5 harness drives real `regel mcp` under the §5-derived capacity row |
| C27 | std/ batteries complete for the B2B envelope, versioned with the epoch, no raw-HTML escape hatch | T+R | 72-entry genesis, two-fresh-DB byte-identical + 219-boundary kill sweep; `TestD3ComponentOutside25Rejected`; **R-std-envelope**: `files` and `i18n` batteries are stubs-with-shape only (why-safe: no v1 scenario consumes them; Rule-of-Three growth is the stated model; the CRM proved the envelope it needed — sql/identity/mail/http/time/money/crypto/test/log/erf/ui/taak) |
| C28 | The epoch upgrades dialect, engine, stdlib, and gate as ONE atomic step | T+D | epoch = (kernel binary, std-manifest-root) boot-refusal (`TestGateC/D` battery); `regel migrate N --commit` all-or-nothing under kill (`TestMigrateCommitAtomicityKill`); `scripts/drill-bad-epoch-revert.sh` |
| C29 | Go + Postgres are the only dependencies: vendored tsgo, owned interpreter, owned wire client, vetted AEAD/KDF; no cgo/Node/npm | T | `go.mod` = typescript-go only; owned `internal/pgwire`/`internal/cek`; genesis attestation pins the dispatch bijection |
| C30 | Trusted code runs on a shared heap; TypeScript's unsound corners survive in the trusted tier — verifier coverage is the security boundary, stated as such | T+R | native-TCB adversarial harness (`TestNativeTCBHarness`, monotone coverage + trusted-for rows); **R-enumerative-boundary**: verifier coverage is enumerative, not proof (GATE-1 R3, why-safe: hostile corpus + dual mutation + monotone floors make the boundary's regressions release-blocking; the residual is stated, not silent) |
| C31 | The one-gate mind-blow works end-to-end on a real app: CRM as rows — accounts, contacts, activities, workflows, components, dashboards | T+D | `TestCRMReferenceAppEndToEnd`; `scripts/crm-setup.sh` + scenarios a–e, all captured in `spec/gates/STAGE-E.md` |

**Tally: 31 claims → 27 carry a red-path test, 14 carry a runnable demo, 6 carry a
named residue (C6, C21, C23, C24, C27, C30 — every one with a why-safe above; C24 is
the only claim whose evidence is residue-ONLY).**

## §2 The 15 DEFER-V2 why-safes (REVIEW-PRE-E §4 dispositions, restated for v1 readers)

| ID | Deferred item | Why it is safe to ship v1 |
|---|---|---|
| A1 | tsgo `MAX_PARSE_DEPTH` post-parse; `Diagnostic.Col` UTF-16 | fork-internal edits forbidden; depth still enforced post-parse; col is display-only |
| A5 | `LOWER_UNSUPPORTED`: infer, call/construct sigs, variadic tuples, qualified names | fail-closed — unsupported syntax cannot admit; dialect-surface expansion, not a hole |
| A6 | RE2 regex opacity, native-stub `unknown` sigs, H_dispatch intrinsic symbol, UTF-16 collation | std/ built at D closed the live paths; remainder is hardening on opaque values |
| A7c | server-side SCRAM | kernel↔PG runs on trusted local links in v1; DSN auth rides libpq semantics |
| B3 | full per-statement fault-injection sweep | kill -9 + aborted-txn families cover the crash classes; full injection is depth, not a gap |
| B9 | stated Stage-B build deviations (thunk park, join-parent jsonb, best-of-3 microbench) | each documented + accepted at the B gate; none is load-bearing for correctness |
| C2 | I4 GiST predicate-lock coarseness caps admission semaphore at S=2 | BUSY-shed protects correctness; finer locking is throughput optimization only |
| C3 | `TYPECHECK_TIMEOUT` abandons the checker goroutine | rare secondary path; the deterministic `TYPECHECK_BUDGET` ceiling fires first |
| C6 | hosted-forge wiring (webhooks/push-creds/branch-protection) | rides operator infra per ADR-09; the fold + merge door are proven without it |
| D5 | H_dispatch uses the intrinsic symbol, not a true Go body hash | attests build-consistency; boot-refusal catches image drift either way |
| D7 | derivation gaps: `pii(address)` ⇒ DERIVE_PARTIAL, `pii(relation/select)` unrepresentable, FK/hasMany, multiselect | every gap fails closed at admission — the row never exists; nothing admits un-derived |
| D8 | reactive minimalism (collapsed error slots, setValue first-paint, org-only policy) | named cuts with correct fallbacks; none masks data or drops a patch |
| D9 | 50k-storm live-SSE subset = 100; single-machine perf numbers | all 50k do real work; budgets calibrated + pinned; cross-machine repro is an ops step |
| D10b | OTLP push exporter absent (typed stdout emitter stands in) | observability transport, not truth: every metric is also a queryable row (cron half of D10 was DISCHARGED at Stage E — `TestCronDrivesRecurringWorkflow`) |
| L4/L5 | OTLP collector round-trip gate; ring-buffer event priority | both bind to the L4 exporter that does not ship in v1; stdout emitter is grep-gated |

## §3 UX papercuts (recorded by PHASE R hand-driving; behavior is CORRECT)

1. CLI `--as-of` demands strict RFC3339 offsets (`Z`/`±HH:MM`) and rejects Postgres's
   `-04` text form — HTTP `?as_of=` accepts both. (Alignment nit, not a correctness bug.)
2. Whole-second as-of near an admission boundary can miss by sub-second —
   `valid_from` is microsecond-precise; inherent to point-in-time reads. Scripts pin
   µs-precision timestamps (see scenario-d).
3. `--declare` expects the verifier's stripped capability token (`mail.send`), not the
   import path (`std/mail.send`), and the mismatch message does not reveal the
   expected form.

Stage-E additions (same class): board card titles use the first text field of the
resource (derivation heuristic); the as-of session mount is read-only first-paint
(live steps track head); as-of observes SCHEMA/BEHAVIOR, not historical row data
(derived-table point-in-time reconstruction from history is unbuilt — named residue in
STAGE-E.md).
