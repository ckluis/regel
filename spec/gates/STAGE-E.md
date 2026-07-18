# STAGE-E gate report (= M6 → v1: proof CRM + five scenarios + M5 gates + claim-evidence)

*Author: PHASE E (Stage-E sub-orchestrator). Date: 2026-07-17. Baseline inherited:
`b0cbe90` (PHASE R, REVIEW-PRE-E.md). Verified code HEAD: `31876d8` (uncached suite + all 13 scripts green there); this gate report + the STATE line land as the one commit on top of it. Real
PostgreSQL 16.13. Real LLM (the M5 gates ran against `claude -p`, never faked).
Evidence captures: `spec/gates/evidence-e/` + inline below.*

**Verdict for the operator: STAGE E GREEN with named residues.** The proof CRM
exists entirely as admitted rows (3 erf resources + a taak workflow + a
hand-authored component + a typed std/sql read — zero app logic in Go, grep-proven)
and was built by driving the real CLI/HTTP/MCP doors; all five proof scenarios are
scripted and exit 0 with captured output; the Stage-C OPEN M5 gates were RUN
against a real LLM — §7 restart-decision accuracy 0.968 on M=31 (floor 0.95 /
M≥30) is GREEN and the agent `condition.restart` flip executed through its
mechanized red-pathed gate check; §3a measured pass@1 = pass@3 = 1.00 but on
N=15 < the ADR-12 floor of 50, so the §3a suite-size leg stayed OPEN as a named
residue at gate time (measured floors met, corpus floor not) — **discharged
post-gate 2026-07-17 by run 3 (§4a below): N=52 ≥ 50, pass@1 = pass@3 = 1.00,
all three M5 gates GREEN non-partial, R5+R6 closed**; durability machinery is live
(`migrate N` findings-as-rows + all-or-nothing, golden-continuation corpus with a
monotone decode floor, O1–O5 fences, bad-epoch revert drill with DDL-backed held
dependents, world-rehash canary CLI); the R1-14 stranger-review gate was built
red-path-first and run with a REAL outside reviewer whose first verdict was
UNFINISHED — its specifics were fixed and the re-review reads `finished` (the gate
doing exactly what R1-14 wanted); the three REVIEW-PRE-E §5.5 hardening items
landed red-path-first, including a C4 fix that closed TEN blind-admission holes in
V2/V5. Uncached `go test ./...` is green at final HEAD and all 13 scripts (6
pre-existing demos + crm-setup + 5 scenarios + revert drill) exit 0.

## 1. What was built (≈44 commits, `b0cbe90..HEAD`)

| Commits | Content |
|---|---|
| `950c0b2` | **D11 lowerer fix, done FIRST as directed**: dep edges keyed by nominal `(module,name)`, not content hash — the hash-keyed map dropped one of two edges whenever imports shared a hash (every std type shares the opaque genesis body). Worse than the reported "definition not found": a dropped `Vault` edge BLINDED V2 — a pii escape ADMITTED (RED captured in `internal/admission/collide_dep_test.go`). CLI-verified: multi-export module w/ identical-shaped literals admits two hashes, both eval |
| `5f7d0bd`→`6c41e99` | **std chunk 1**: `std/sql.query` (SELECT-only at the native boundary, capability-gated, runs under the eval's as-of snapshot); row-backed identity (`user_account` + `cek.Reader` seam); real `cfr.DeliverySink` pair (FileSink spool = `regel serve --spool` default, HTTPSink); `regel vault-put` (stdin, real `admission.VaultPut`; demo-erf-derive now uses it); cron task kind driven (`std/taak.schedule("@every:…")`, reactor `cronOnce`, restart-safe). 3 roster adds ⇒ epoch-bumping genesis change, genesis gates re-ran green |
| `68fe6ab`→`aef829e` | **UI chunk 2**: `board(R)` (states-grouped kanban, live state-move splice), `dashboard` (stat tiles: total/enum counts/money sums, live re-aggregation), minimal read-only `operatorPlane` (condition inbox + refusal ledger); hand-authored component→template lowering (D3) with PII non-leaf reject + outside-25-roster reject; board-refusal red-path for stateless resources |
| `bdffce2`→`455eb55` | **The proof CRM + scenarios a/b/d/e** (§3 below) + `?as_of=` session mount (kernel mechanism found by USE in scenario d) + `TestCRMReferenceAppEndToEnd` anchor |
| `33277c6`→`b20413f` | **Durability**: `regel migrate N` (dry-run `migration_finding` rows / `--commit` all-or-nothing / `--revert-to`), golden CFR corpus (30 blobs, monotone coverage manifest), O1–O5 fences (O4 in-txn re-scan, O5 `epoch.fence_tripped` drain), `epoch_hold` DDL + revert drill, `regel canary` (2-leg), scenario c + `TestTwoEpochStrandedImpossibility` |
| `ffaa25e`→`31f7243`, `ab79646`, `f7a6c59` | **M5 harness + runs** (§4): corpus + behavioral oracle + resumable runner over real MCP + `claude -p`; run-1 postmortem fixes (oracle KFor/KForOf/KDoWhile/KSwitch/KUpdate coverage; per-attempt module isolation); run-2 captured evidence |
| `e4b285d`, `f3999bb`, `b69cec8` | **Hardening trio**: D4 `RESIDUE_LOG_SINK` closed (log.write in V2's sink set, corpus fixture, trusted_for row updated); L7 REPEATABLE-READ serve read-phase (`serveReadSnapshot`; RED witness `TestL7ReadCommittedSplitsDispatch`); C4 full-statement-grammar dataflow (§6) |
| `31876d8` + gate run | **R1-14 stranger-review gate** (§7): table + `StrangerReviewGate` (absent verdict reads RED) + real outside review; its UNFINISHED first verdict forced boardTitleField/curateFields/badge fixes (presentation layer only) |

## 2. Acceptance — uncached full suite + all scripts at final HEAD

```
$ go clean -testcache && go test ./... -count=1        (HEAD 31876d8, real PG 16.13)
ok  regel.dev/regel/cmd/regel            3.4s     ok  internal/lower    1.8s
ok  regel.dev/regel/gate/m5eval          4.5s     ok  internal/mcp     10.6s
ok  regel.dev/regel/internal/admission  20.7s     ok  internal/oracle   1.8s
ok  regel.dev/regel/internal/catalog     4.1s     ok  internal/pgwire   2.1s
ok  regel.dev/regel/internal/cek         1.7s     ok  internal/rast     0.4s
ok  regel.dev/regel/internal/cfr         5.9s     ok  internal/tsx      0.7s
ok  regel.dev/regel/internal/gitproj     5.9s     ok  internal/ui       0.4s
ok  regel.dev/regel/internal/kernel    424.8s
(gate/nativetcb, gate/redpath, internal/mutants: no test files)
=== GOTEST_EXIT=0 ===
```

All 13 scripts at final HEAD:

```
demo-admit-rollback EXIT=0    crm-setup                 EXIT=0
demo-kill9-resume   EXIT=0    scenario-a-field-add      EXIT=0
demo-mcp-session    EXIT=0    scenario-b-agent-patch    EXIT=0
demo-erf-derive     EXIT=0    scenario-c-deploy-survive EXIT=0
demo-reactive       EXIT=0    scenario-d-asof-rollback  EXIT=0
demo-taak           EXIT=0    scenario-e-pii-shred      EXIT=0
                              drill-bad-epoch-revert    EXIT=0
(+ scripts/stranger-review.sh GATE OK / exit 0, run twice — §7)
(+ scripts/m5-eval.sh exit 0 — §4)
```

## 3. The proof CRM + the five scenarios (all exit 0, captured)

**CRM (`crm/` sources, admitted through the real gate by `scripts/crm-setup.sh`):**
Account (states→board, money/select→dashboard), Contact (belongsTo + `pii:email`/
`pii:phone` vault-routed), Activity (2×belongsTo, select/longtext/timestamp/boolean);
`followup` std/taak workflow (sleep checkpoint + capability-gated mail.send →
outbox → FileSink spool); `AccountCard` hand-authored component; `activePipeline`
typed std/sql read. **No side-door Go**: `grep -rin crm internal/ cmd/` (non-test)
= illustrative comments only; the e2e anchor `TestCRMReferenceAppEndToEnd` admits
`crm/` from disk on every suite run.

```
crm-setup:   PASS: exactly 3 derived resources
             PASS: Contact.email/phone are vault-routed (no base column)
             PASS: followup done, 1 mail.send intent delivered effectively-once to the FileSink spool
             PASS: table + board + AccountCard component render over the live derived rows
```

**(a) tenant field-add** (`scenario-a-field-add.sh`): re-admission of Account+`owner`
through the HTTP door under `--base` optimistic concurrency; column live, rows/
history intact, form UI shows the field, pre-change template has no owner slot,
concurrent stale-base edit rejected at the `cas` stage.

```
PASS: tenant admin added the owner field with NO engineer — admitted under optimistic concurrency
PASS: owner column added · Globex intact (owner NULL) · 1 history row(s) preserved
PASS: the session opened before the change resynced cleanly (old sessions still render)
PASS: the concurrent stale-base edit was rejected (STALE_BASE, cas stage) — no region column leaked
```

**(b) agent patch over MCP** (`scenario-b-agent-patch.sh`): dry-run verdict
(product-escalation + hash) → `regel approve` one-shot token → fuel-budgeted commit
→ `AccountBadge` LIVE via `?component=`; REFUSED leg (`CAP_UNGRANTED`) with zero
code trace, `gate_refusal` 1→2.

**(c) mid-flight workflow survives deploy** (`scenario-c-deploy-survive.sh`): real
`crm/followup` parked mid-sleep under epoch 1; TWO deploys (`migrate 2 --commit`,
`migrate 3 --commit`); the epoch-1 kernel trips the O5 fence
(`epoch.fence_tripped`, observed 2 / required 1, in-flight aborted, leases
released); `--wait-for-epoch` stages the new kernel; the workflow resumes with
result parity and mail.send exactly-once (outbox=1, dupes=0), provenance stamp
still epoch 1. Two-epoch stranded-impossibility also gated in-suite
(`TestTwoEpochStrandedImpossibility`).

**(d) as-of rollback through the UI** (`scenario-d-asof-rollback.sh`): live mount
renders the `owner` field; `?as_of=T0` mount renders the v1 schema (owner ABSENT);
CLI `eval --as-of` 100 vs live 200 — rollback = as-of for schema, behavior, AND
the UI observing them.

**(e) PII crypto-shred with attestation** (`scenario-e-pii-shred.sh`): seal via
`regel vault-put` (real door), masked render (`••••·<tag>`), `regel shred` →
attestation ORACLE-RECOMPUTED (resource/subject/keys/principal/timestamp
independently matched), post-shred reads return the mask token, key row gone,
ciphertext blob remains undecryptable, plaintext grep-ABSENT from base + history +
every session snapshot.

## 4. The OPEN M5 gates — RUN against a real LLM (`claude -p`), epoch-1 pins as rows

Harness: `gate/m5eval/` + `scripts/m5-eval.sh` — drives the REAL `regel mcp`
JSON-RPC plane with the LLM authoring/deciding; strictly serial; RESUMABLE (every
scored (task,attempt) persists; infra failures leave gaps, never scored fails);
behavioral oracle = the independent regel-native reference reducer (a second
witness sharing no code with the CEK machine) — a known-bad-but-admissible
seed FAILS it (harness not gameable by admission alone). Pins as rows (`eval_pin`):
epoch 1, k=3, corpus content hashes — the L2 fix: k is frozen with the corpus.

**Run 1 (45 authoring attempts + 11 restart scenarios) was a postmortem, kept as
evidence** (`ab79646`): pass@k read 0.733 — every miss a HARNESS artifact, not LLM
capability: (i) the reference reducer lacked KFor et al. — every for-loop
candidate scored a spurious behavior-FAIL ("expression kind 65 not covered");
(ii) pass@k attempts shared one module path — every k>1 attempt collided with
attempt 1's committed def (STALE_BASE). Both fixed red-path-first; run 2 fresh.

**Run 2 (captured: `evidence-e/m5/`, `TestM5EvalRealLLM` 520s, exit 0):**

| Gate | Corpus | Floor | Measured | Verdict |
|---|---|---|---|---|
| §3a authoring pass@k | N=15 tasks × k=3 (45/45 scored, 0 gaps) | pass@1 ≥ 0.5, pass@k ≥ 0.9, **N ≥ 50** | **pass@1 = 1.00, pass@3 = 1.00**, p95 iterations-to-green = 1 | measured floors GREEN; **suite-size leg OPEN (N=15 < 50) — named residue R5** |
| §7 restart-decision accuracy | M=31 scenarios (real condition/restart rows read by the agent over MCP) | acc ≥ 0.95, M ≥ 30 | **0.968 (30/31)**; one miss: `det_notfound_1` (chose abort) | **GREEN** (both floors) |
| §5 eval-derived fuel capacity | from the 45 scored attempts | formula `ceil(p95_iter × 5 × 1.5)` must cover p95 passing fuel | formula floor = 8 < p95 fuel = 10 → capacity **15** written = `ceil(10 × 1.5)`, adjustment RECORDED in `derived_from` | **GREEN-functional** (capacity covers corpus by construction; the formula-vs-measured tension is data, named residue R6) |

**The flip**: agent-facing `condition.restart` was DISABLED with a mechanized gate
check (flip attempt while the gate reads red ⇒ refused `RESTART_DISABLED` —
red-pathed in the harness suite). With §7 green the flip EXECUTED:
`restart_gate_green=true agent_authority_enabled=true`; the post-flip agent call
passed the authority check (refusal reason moved from `RESTART_DISABLED` to a
downstream `INTERNAL` on the harness's synthetic-frames continuation — real
parked-workflow restart mechanics are the Stage-B/D restart suites; named
precisely, residue R7).

### 4a. Run 3 (post-gate, 2026-07-17): the R5/R6 discharge at N=52

The corpus was expanded 15 → 52 tasks (37 new, same closed-dialect families;
monotone append — the original 15 untouched). Every new Reference/KnownBad was
oracle-validated (`TestOracleDiscriminates`) and 5 new tasks red-pathed through
the real MCP door (`TestSeededSolutionsThroughRealDoor`) BEFORE any LLM call;
`TestCorpusInvariants` now enforces N ≥ 50 permanently. The corpus change forced
a NEW pin (k=3 frozen against the new hash — the L2 anti-tuning machinery doing
its job) and a fresh eval DB. Mid-run the LLM door rate-limited after ~135
attempts; the harness left gaps and the resume pass filled exactly those gaps —
the resumability design exercised for real.

**Run 3 (captured: `evidence-e/m5/`, fresh DB + resume, exit 0 both passes):**

| Gate | Corpus | Floor | Measured | Verdict |
|---|---|---|---|---|
| §3a authoring pass@k | N=52 tasks × k=3 (156/156 scored, 0 gaps) | pass@1 ≥ 0.5, pass@k ≥ 0.9, N ≥ 50 | **pass@1 = 1.00, pass@3 = 1.00**, p95 iterations-to-green = 1 | **GREEN — all three legs, R5 discharged** |
| §7 restart-decision accuracy | M=31 scenarios (re-run fresh) | acc ≥ 0.95, M ≥ 30 | **0.968 (30/31)**; same single miss as run 2: `det_notfound_1` (chose abort over escalate — not in the scenario's Unsafe set) | **GREEN**; flip re-executed: `restart_gate_green=true agent_authority_enabled=true` |
| §5 eval-derived fuel capacity | from the 156 scored attempts | formula must cover p95 passing fuel | run confirmed the run-2 tension (floor 8 < p95 fuel 10) as structural, not sampling noise → **formula re-derived per the ADR §5 revisit rule: `ceil((p95_iter + 1) × 5 × 1.5)` — the `+1` commit landing term (BUILD-E R6 in ADR-12 §5)** → floor 15 ≥ 10, capacity **15** now formula-derived, `derived_from` carries no adjustment | **GREEN — covers by formula, R6 discharged** |

The §5 re-derivation was ADR-first: ADR-12 §5 (formula + red-path clause,
BUILD-E R6 marker) updated before the harness, then a zero-LLM-call resumable
recompute pass re-derived the gate rows from the persisted attempts — the same
capacity 15 both runs provisioned, now traceable to the formula alone.

## 5. Durability fences + drills

- **`migrate N`**: dry-run writes `migration_finding` rows (ok / needs-hold /
  undecodable) and mutates NOTHING (`TestMigrateDryRunFindingsNoMutation`);
  `--commit` re-runs the O4 scan INSIDE one SERIALIZABLE txn, all-or-nothing under
  a mid-migrate kill (`TestMigrateCommitAtomicityKill`); undecodable/needs-hold
  BLOCK commit fail-closed (`TestMigrateUndecodableBlocksCommit`,
  `TestMigrateO4NeedsHoldBlocksCommit`).
- **Golden-continuation corpus (B2)**: `internal/cfr/testdata/golden/` — 30 CFR
  blobs covering every frame kind @ CFR v1 + committed `coverage.json`; monotone
  floor: every blob must decode, the covered (frame-kind, version) set must never
  shrink (`TestGoldenCorpusRedPathCorruption` red-paths both corruption and
  removal); deliberate regeneration via `-regen`.
- **O1–O5**: O1 byte-immortality (canary encoder leg over ALL defs); O2 semantic
  stability (golden corpus + year-old + two-epoch resume); O3 readers-forever
  (decode floor + dry-run undecodable); O4 lattice-narrowing enumerates atomically
  with the flip (in-txn re-scan); O5 fleet coherence (`cfr.checkEpoch` — a kernel
  observing a newer catalog epoch than its binary ROLLS BACK, emits
  `epoch.fence_tripped`, drains; witnessed live in scenario c).
- **Bad-epoch revert drill** (`drill-bad-epoch-revert.sh`): bad epoch 2 deployed,
  dependent parks bound to it; `migrate 3 --revert-to 1` carries the prior-good
  pair; the dependent is HELD FAIL-CLOSED — `epoch_hold` row (the L1 DDL-backed
  state) + `condition` status, never resumed by the reverted fleet
  (`TestBadEpochRevertHoldsDependents`); time-to-recovered measured (1.3s in the
  captured run).
- **World-rehash canary** (`regel canary`): two legs (encoder over all definitions;
  parse→lower replay over product app defs); a tampered stored AST ⇒ structured
  `store.scrubber_tripped` + nonzero exit (`TestWorldRehashCanaryGreenThenTamper`).
  Nightly scheduling = one crontab line documented in the ADR (operator infra).

## 6. Hardening trio (REVIEW-PRE-E §5.5), red-path-first

- **D4 `RESIDUE_LOG_SINK`** (`e4b285d`): std/log.write joined V2's sink set per
  ADR-10 §3 — RED first (a Vault value routed into log.write ADMITTED), then
  V2 rejects (PII_ESCAPE family), corpus fixture added, nativetcb trusted_for row
  updated (not deleted — monotone harness rules). Non-pii log.write still admits.
- **L7 REPEATABLE-READ serve** (`f3999bb`): the whole mount/resync read phase runs
  in ONE `REPEATABLE READ, READ ONLY` txn (`serveReadSnapshot`) — a concurrent
  admission's name_pointer/derived-artifact flip can no longer split dispatch
  across serve reads. RED witness `TestL7ReadCommittedSplitsDispatch` (the hazard
  is real at READ COMMITTED); GREEN twin `TestL7RepeatableReadPinsSnapshot`.
  Writes/admission stay SERIALIZABLE.
- **C4 full-control-flow dataflow** (`b69cec8`): V2/V5 walkers previously handled
  only KVarDecl/KReturn/KExprStmt/KIf/KBlock — statements inside
  for/for-of/while/do-while/switch/try were NEVER WALKED. **Ten RED fixtures**
  (`c4_controlflow_test.go`) captured pii escapes and unserializable captures
  ADMITTED with zero diagnostics through every construct. Fix: all arms walked
  with printer-exact binder discipline; for-of elements inherit iterable taint;
  catch binders conservatively tainted on non-literal throws; V5 loops
  snapshotAtRisk AT ENTRY (loop-carried liveness). Clean twins prove no false
  positives; full admission pkg + CRM e2e green.

## 7. R1-14 stranger-review gate — built, then run for real

Mechanism (red-path-first): `stranger_review` table + `admission.StrangerReviewGate`
— the review having happened and its verdict being recorded IS the gate; a missing
row or a non-`finished` latest verdict reads RED like an un-run suite
(`TestStrangerReviewGateReadsRedWhenAbsent`). Runner `scripts/stranger-review.sh`
renders the reference dashboard/table/board and records a REAL outside reviewer:
a fresh-context `claude -p` with zero build context, honestly labeled in the
reviewer column (an operator can re-record with a human the same way).

**The gate then did its job.** First real review: **UNFINISHED** — "cards show
only industry plus the stage… no account name anywhere; repeating the stage on a
card inside a stage-grouped column is redundant; the table's column order is
plainly alphabetical rather than curated." All three fixed at the template
(presentation) layer only — physical/DDL order untouched: `boardTitleField`
prefers the human identifier (name/title/label); `curateFields` leads tables with
it; the card badge swaps the redundant states value for a non-states select (else
money). Re-review (fresh context): **`finished`** — "data is internally
consistent everywhere it can be checked: stage counts and tier counts sum to the
3 accounts, the ARR total matches the table, the kanban columns agree exactly…"
Recorded row: reviewer `llm-stranger:claude-cli (fresh context, no build
context)`, verdict `finished`.

## 8. ADR updates forced by build discoveries (`BUILD-E:` markers: 4)

ADR-10 ×3 (std/sql query surface; row-backed `user_account` identity reads;
board/dashboard/operatorPlane §7 concretization + dashboard read path),
ADR-11 ×1 (§1 hand-authored component lowering). The D11/L7/C4/M5 fixes and the
durability machinery implemented existing ADR law without deviation.

## 9. Named residues (nothing silent)

1. **R1 std/sql policy-predicate injection — DISCHARGED (Stage-F, 2026-07-17, `evidence-f/r1/`)**:
   the composition surface (a caller-authored SELECT + `$1` bind params, no
   auto-injected policy predicate) was proven unsubvertible by an adversarial
   fixture FAMILY of 25 hostile cases (`internal/kernel/r1_sql_injection_test.go`,
   `TestR1SQLInjectionFixtureFamily`) across five attack classes: param-injection
   (×6 — OR-bypass/UNION-exfil/stacked/comment-terminate params all bind as literal
   data, zero rows leaked), write/DDL text (×8 — UPDATE/DELETE/INSERT/DROP/ALTER/
   CREATE/TRUNCATE/GRANT refused `sql.write_refused` before PG), structural (×8 —
   stacked statements, `FOR UPDATE/SHARE` locks, data-modifying CTE, comment-hidden
   writes all refused), engine-enforced (×2), privilege (×1 — ungranted caller
   `capability.revoked`, no read). **Real bug found + fixed ADR-first (BUILD-F R1,
   ADR-10 §4):** `isReadOnlySQL` is a prefix check, so `SELECT nextval()`/`setval()`
   pass it yet are real writes; the non-as-of read path ran them in autocommit with
   NO transaction, so PG executed the write (red-path witnessed a derived `id`
   sequence mutated 2 → 999999 through `std/sql.query`). Fix: `dbReader.Query` now
   runs EVERY read inside a `READ ONLY` transaction (REPEATABLE READ when as-of,
   READ COMMITTED otherwise) — PG itself refuses the write (`cannot execute … in a
   read-only transaction` → resumable `sql.error`); `isReadOnlySQL` stays as
   defense-in-depth. RED `evidence-f/r1/red-path.txt` ("engine-write … got kind=0
   cond=<nil>" + "sequence changed 2 -> 999999"); GREEN `green-path.txt` (all 27
   subtests PASS); engine proof `engine-proof.txt` (`ERROR: cannot execute
   nextval() in a read-only transaction`); full uncached suite `full-suite.txt`
   EXIT=0. Trust boundary documented in ADR-10 §4 + `native_tcb_coverage.trusted_for`:
   the SQL text is author-trusted, `std/sql` does NOT auto-inject a tenant WHERE
   (a cross-org SELECT is bounded by the capability grant + engine-enforced
   SELECT-only, not a policy predicate) — policy-predicate injection into std/sql
   reads remains the named later increment; erf reads carry all policy-scoped
   surfaces in v1.
2. **R2 Settings form**: the tenant field-add drives the HTTP admission door;
   no point-and-click Settings FORM ships (the gate/scoping/re-derivation are the
   claim; the form is presentation).
3. **R3 as-of mount scope**: read-only first-paint; live steps track head; as-of
   observes SCHEMA/BEHAVIOR (template + code version), not historical row DATA —
   derived-table point-in-time reconstruction from history is unbuilt.
4. **R4 operatorPlane v1 is read-only** (no SSE/approval-delta/impersonation
   panels); hand-authored component lowering renders `props.<field>` depth-1.
5. **R5 M5 §3a suite size — DISCHARGED (run 3, 2026-07-17, §4a)**: corpus
   expanded 15 → 52 under a new pin (k=3 refrozen against the new hash);
   pass@1 = pass@3 = 1.00 on N=52 ≥ 50, non-partial — all three §3a legs GREEN.
   `TestCorpusInvariants` now enforces the N≥50 floor permanently.
6. **R6 §5 fuel formula — DISCHARGED (run 3, 2026-07-17, §4a)**: the N=52 run
   confirmed the under-coverage as structural (each iteration is a dry-run
   charge, the green iteration lands one commit charge on top); formula
   re-derived ADR-first to `ceil((p95_iter + 1) × cost × 1.5)` (ADR-12 §5
   BUILD-E R6) — floor 15 covers measured p95 fuel 10, capacity 15 now
   formula-derived with no adjustment in `derived_from`.
7. **R7 restart-flip depth — DISCHARGED (Stage-F, 2026-07-17, `evidence-f/r7/`)**:
   the flip's authority change is proven (refused `RESTART_DISABLED` while red →
   accepted after green); the harness's post-flip call then hit `INTERNAL` on its
   synthetic-frames continuation. Stage-F retires that synthetic evidence:
   `internal/kernel/r7_restart_flip_depth_test.go` (`TestR7AgentRestartRealParkedWorkflow`)
   admits a real `taak.signal` workflow, runs it on the REAL reactor/CEK machine to a
   REAL `durable_condition` with REAL CFR frames, then drives an AGENT-DRIVEN
   `condition.restart` over the real MCP door under the flipped authority — it runs to
   the correct final result (`resolved:approve`) with exactly-once effects (1 outbox
   row, 0 dupes) and an idempotent second-restart reject. RED witnessed first
   (`evidence-f/r7/red-path.txt`): with the green gate withheld the agent restart is
   REFUSED and the condition stays open with zero trace; GREEN in
   `evidence-f/r7/green-path.txt`. The deep path now rides REAL frames on the
   Stage-B/D restart machinery, not synthetic ones.
8. **R8 canary pipeline leg — DISCHARGED (Stage-F, 2026-07-17, `evidence-f/r8/`)**:
   the world-rehash canary's pipeline leg now re-lowers app defs at EVERY scope —
   product AND every overlay scope (org/team/user/package heads), std still excluded
   (un-relowerable native bodies). Before, the leg filtered `scope_kind=0 AND
   scope_id=''`, so an overlay-only def (an agent/tenant patch shadowing product for
   its own sandbox scope) was covered by the encoder leg alone and a text↔AST seam
   drift on it — stored AST hashing fine, canonical_text no longer re-lowering to
   that address — passed SILENTLY. The leg re-lowers with the SAME resolver admission
   uses (product-scope resolution, external caller — overlay import resolution is the
   Stage-B residue, so admission lowers overlay defs at product scope today and the
   canary matches). RED witnessed first (`cli-witness.txt` step 5): the OLD canary
   binary runs GREEN (exit 0) over a tampered overlay def — the blindness; step 6 the
   EXTENDED binary runs RED (exit 1) over the SAME state, naming the overlay hash +
   `scope:2:org1` on the pipeline leg (caught), green over healthy overlay in steps
   3/7. Permanent regression: `internal/admission/migrate_test.go`
   (`TestOverlayScopeCanaryReLower`, `go-test.txt`) proves the old product-only query
   never SELECTs the overlay def, the encoder leg stays silent on the untampered AST,
   and only the new pipeline coverage catches it. `regel admit --scope org.ID` added
   (the enabling CLI door for overlay admission; non-agent principals keep Stage-A
   scope semantics). No ADR change: ADR-02 §5 already mandates replay of "every
   historical definition" — this brings the implementation up to that law.
9. **R9 migrate-in-drill std pair — DISCHARGED (Stage-F, 2026-07-17, `evidence-f/r9-r11/`)**:
   the epoch-migrate drill now runs across a GENUINELY NEW std pair. The real std
   delta is `std/text.Slug` (`admission.BuildImageEpoch2`) — type-only, so it moves
   the std-manifest-root (`6b958652…` → `b2e0ac02…`) while holding the dispatch
   attestation constant, isolating the manifest root as the sole epoch discriminator.
   RED witnessed first (`r9-drill.txt`): the OLD machinery (`MigrateCommit`, which
   copies the current pair forward) migrates to epoch 2 carrying the stale root, and
   the epoch-2 binary refuses boot with the structured `epoch.boot_refused` /
   `manifest_root_mismatch`; separately, code importing `std/text` is refused
   admission under epoch 1 (`import "std/text.Slug" does not resolve`). The new
   `MigrateCommitImage` (`internal/admission/migrate.go`) slots the new pair through
   the real machinery — dry-run 3 findings-as-rows (epoch untouched) → all-or-nothing
   commit (new root slotted, `std/text/Slug` name-pointer catalogued) → epoch-2
   kernel boots (`NewWithImage`) while the stale epoch-1 binary is fenced
   (`catalog_manifest_root_mismatch`) → parked real workflows resume on epoch 2 with
   the correct result and exactly-once mail effect (outbox=1). Drill:
   `internal/kernel/r9_migrate_std_pair_test.go`. M5 eval pins untouched (the
   pure-compute corpus imports no std). Latent bug found + fixed: the delta must be
   keyed on the name-pointer, not the definition hash — every std TYPE shares the
   opaque `unknown` genesis body, so a new type reuses an existing hash but needs a
   fresh pointer.
10. **R10 hold fencing cost model — DISCHARGED (Stage-F, 2026-07-17, `evidence-f/r10/`)**:
    the bad-epoch-revert fence's dominant cost (holding dependents fail-closed via an
    `epoch_hold` audit row + a `condition` status flip) is now MEASURED under a
    dependents-heavy hold (N=5000 continuations bound to the bad epoch) and BUDGETED.
    `admission.RevertEpoch` was made SET-BASED (BUILD-F, ADR-08 §6a): one `INSERT …
    SELECT` + one `UPDATE` over the blast closure — O(1) round trips in N, replacing the
    per-row loop's 2N. Metric `epoch.hold_fence_ms` (perf_budget row, epoch 1, M6,
    registered ADR-13 §3): budget **120 ms**, real fence measured **≈36 ms** (best-of-3).
    RED witnessed through the SAME gate (`red-path.txt`): the un-batched runaway at
    identical N measures **~355 ms** and "crosses budget 120ms" → FAIL; GREEN
    (`green-path.txt`) the set-based fence is ~36 ms under budget and writes the row;
    the fail-closed drill is unchanged (`drill-unchanged.txt`). Permanent gate:
    `internal/kernel/r10_hold_fence_cost_test.go` (`TestR10HoldFenceCost`) — also asserts
    the budget is non-decorative (the runaway must exceed it). The 50k-storm step path
    still carries no per-claim `epoch_hold` read (that read-path exclusion stands).
11. **R11 golden corpus breadth — DISCHARGED (Stage-F, 2026-07-17, `evidence-f/r9-r11/`)**:
    the golden corpus grew from 30 synthetic single-frame blobs to 30 + 3 REAL
    multi-frame continuation shapes (`real_sleep_park`, `real_mail_park`,
    `real_capture_park`), captured byte-deterministically from the R9 migrate drill's
    parked workflows (`REGEL_CAPTURE_R11=1`). Since every production frame kind is
    already covered at CFR v1, the growth is on SHAPE: `real_coverage.json` lists the
    real blobs as NAMED floor obligations, so the monotone floor RATCHETS 30 → 33
    (`TestGoldenCorpusRealShapeFloorRatchets`). A regression below the new floor is
    refused — a removed real blob leaves its obligation uncovered and a corrupted one
    stops decoding (`TestGoldenCorpusRealShapeRedPath`, captured red in
    `r11-golden.txt`). The `-regen` generator was scoped to `k*.cfr` so a synthetic
    regen never wipes the real blobs. (`continuation_coverage` DB table still unused;
    the committed file manifests are the floor.)
12. **R12 V2 catch-binder taint is conservative** (non-literal throw ⇒ tainted);
    literal throws stay clean — a stated over-approximation, fail-closed
    direction.
13. **R13 std envelope**: `files`/`i18n` batteries are stubs-with-shape;
    `test.fake` remains a stub; board card title/badge heuristics are presentation
    defaults (stranger-approved for the reference CRM).
14. **R14 UX papercuts** carried in `docs/claim-evidence.md` §3 (CLI `--as-of`
    strictness, µs-precision boundaries, `--declare` token form + Stage-E
    additions).

15 DEFER-V2 why-safes and the claim map live in `docs/claim-evidence.md`
(31 claims → 27 test / 14 demo / 6 residue; C24 projectional-editors is the only
residue-only claim).

## 10. Discipline notes, stated

(i) Three session-limit cuts landed mid-chunk (D11, M5 harness, L7); every one was
salvaged by re-verifying the dirty tree red-path-first before committing — nothing
was committed unverified, nothing silently discarded. (ii) M5 run 1's wrong
numbers were kept as evidence and root-caused rather than re-rolled quietly; the
harness fixes have their own red-paths. (iii) The stranger-review reviewer is an
LLM, not a human — labeled in the row itself; the operator can re-record. (iv) The
whole stage ran strictly serial (one helper at a time, synchronous); the M5 eval
and final regression ran as isolated background captures with no concurrent
repo mutation.
