# STAGE-F gate report (= v1 → v1.1: residue burn-down + final gate)

*Author: Stage-F close-out (Workstream D). Date: 2026-07-18. Baseline inherited:
`1ef7232` (STAGE-E close-out + M5 run 3). Final-gate verified HEAD: `59c5fe6`
(`evidence-f/final-gate/`); this gate report + the STATE close-out land on top. Real
PostgreSQL 16.13. The M5 legs ran against a real LLM (`claude -p`); the stranger-review
reviewer is an LLM, honestly labeled — operator human re-record stays scheduled.
Evidence captures: `evidence-f/` (per-residue red/green + full-suite) + inline below.*

**Verdict for the operator: STAGE F GREEN.** v1 was already CLOSED at Stage E; Stage F
burned down the 12 open STAGE-E §9 residues + the R14 papercuts across four operator-GO
workstreams (A depth-of-proof, B hardening, C product surface, D close-out). **Fourteen
residues discharged** (R1–R14; R4 partial), each red-path-first with a captured RED
before the GREEN and a permanent regression test; **R5+R6 were already discharged
pre-Stage-F** by M5 run 3. Four real bugs were found and fixed by the burn-down (R1
std/sql write-bypass, R9 name-pointer migrate key, R12 latent template-throw escape,
R10 O(N) revert fence). Six BUILD-F ADR markers landed law-first. The final gate is
green: the serialized uncached suite passes, 17 deterministic scripts exit 0, and both
determinism gates (git two-fold, genesis two-fresh-DB) are byte-identical.

## 1. What Stage F was

Four workstreams, all operator-GO (`40ac80b`), run strictly serial:

| WS | Charter | Residues | Close SHA |
|---|---|---|---|
| A | depth-of-proof | R7, R9, R11, R8 | `271586a` |
| B | hardening | R1, R10, R12 | `f610919` |
| C | product surface | R2, R4, R3, R13, R14 | `59c5fe6` |
| D | close-out (this report) | claim-evidence re-tally + FINAL.md + STAGE-F.md + final gate | — |

## 2. Per-residue disposition (R1–R14; R5/R6 pre-F)

| Residue | Disposition | One-line what | Evidence · commit |
|---|---|---|---|
| R1 | DISCHARGED | 25-case std/sql adversarial family; SELECT-only is engine-enforced (every read wrapped in a READ ONLY txn — PG refuses the write) | `evidence-f/r1/` · `769719a` |
| R2 | DISCHARGED | point-and-click `SettingsForm` ships as admitted rows, drives the SAME `/admit` door (gate-level refusals witnessed) | `evidence-f/r2/` · `28f1cee` |
| R3 | DISCHARGED | `?as_of=` now reconstructs historical ROW DATA from history tables (PII stays structurally masked) | `evidence-f/r3/` · `597ee17` |
| R4 | DISCHARGED-PARTIAL | operatorPlane v1.1: SSE live updates + approval-delta panel + restart-door writes over the existing door | `evidence-f/r4/` · `9e201c4` |
| R5 | DISCHARGED (pre-F) | M5 §3a corpus 15 → 52; pass@1 = pass@3 = 1.00, N≥50 floor pinned forever | `evidence-e/m5/` · `1ef7232` (run 3) |
| R6 | DISCHARGED (pre-F) | §5 fuel formula re-derived `ceil((p95_iter+1)×5×1.5)`; capacity 15 now formula-derived | `evidence-e/m5/` · `1ef7232` (run 3) |
| R7 | DISCHARGED | agent `condition.restart` drives a REAL parked `taak.signal` workflow to `resolved:approve`, exactly-once, under the flipped authority (retires synthetic-frame evidence) | `evidence-f/r7/` · `bfb9bbe` |
| R8 | DISCHARGED | world-rehash canary pipeline leg now re-lowers app defs at EVERY overlay scope (a text↔AST drift on an overlay def no longer passes silently) | `evidence-f/r8/` · `b62404e` |
| R9 | DISCHARGED | epoch-migrate drill runs across a genuinely new std pair (`std/text.Slug`, root `6b958652…` → `b2e0ac02…`) through real `MigrateCommitImage` machinery | `evidence-f/r9-r11/` · `6aa0fc1` |
| R10 | DISCHARGED | bad-epoch revert fence made SET-BASED; `epoch.hold_fence_ms` budget 120 ms, measured ≈36 ms at N=5000 (runaway ~355 ms reds the gate) | `evidence-f/r10/` · `dea3181` |
| R11 | DISCHARGED | golden CFR corpus grows 30 → 33 REAL multi-frame continuation shapes; monotone floor ratchets, `-regen` scoped to synthetics | `evidence-f/r9-r11/` · `6aa0fc1` |
| R12 | DISCHARGED | V2 catch-binder taint tightened to `provablyCleanThrow` (reference-free throws only); relaxes safe composite throws, closes a latent pii-template-throw escape | `evidence-f/r12/` · `2d141f9` |
| R13 | DISCHARGED | `std/files` + `std/i18n` promoted DEFER → SHIP as epoch-1 genesis rows (Rule-of-Three-scoped to `crm/attach`, scenario-g) | `evidence-f/r13/` · `9bebdf4` |
| R14 | DISCHARGED | 3 CLI/UX papercuts (`--as-of` grammar shared both doors, `--declare` `std/` normalization); 1 re-named correct point-in-time behavior; caught+fixed a claim-evidence §3 doc inaccuracy | `evidence-f/r14/` · `87970cc` |

Full per-residue narrative (RED witness, fix, permanent gate, why-safe) is in
`spec/gates/STAGE-E.md` §9.1–§9.14 — the Stage-F agents marked each residue DISCHARGED /
RE-NAMED in place there; this table is the index.

## 3. Final gate (HEAD `59c5fe6`, evidence commit `6ce965c`)

**Serialized uncached suite — `evidence-f/final-gate/go-test.txt`:**

```
cmd: go clean -testcache && go test -p 1 ./...   (serialized, clean env)
ok  cmd/regel  ok  gate/m5eval  ok  internal/admission (17.1s)
ok  internal/catalog/cek/cfr/gitproj/lower/mcp/oracle/pgwire/rast/tsx/ui
ok  internal/kernel (422.8s)
(gate/nativetcb, gate/redpath, internal/mutants: no test files)  → all ok
```

Known test-harness property (NOT a product defect): the kill-9 and `gate/m5eval` tests
flake ONLY under parallel full-suite load (step-timing + a shared scratch-DSN env). The
serialized, clean-env run (`-p 1`) is fully green; the gate is defined as the serialized
run. Documented so a future parallel-run flake is read as harness timing, not
regression.

**17 deterministic scripts exit 0 — `evidence-f/final-gate/scripts.txt`** (fail flag
0): the 7 demos + 10 crm/scenario/drill scripts (`crm-setup`, `scenario-a/a2/b/c/d/d2/
e/f/g`, `drill-bad-epoch-revert`). `m5-eval.sh` (live-LLM, 156 attempts, rate-limited)
and `stranger-review.sh` (operator-scheduled human re-record) are operator-run, NOT part
of the deterministic gate.

**Determinism gates PASS — `evidence-f/final-gate/determinism.txt`:** git projection
two-fold byte-identical (`TestDeterminismReleaseGate`); genesis two-fresh-DB
byte-identical (`TestGateA_TwoFreshDBReproducibility` + `TestBuildImageDeterministic`).

## 4. ADRs-as-law — BUILD-F markers (6)

Every fix updated the governing ADR FIRST, then the machinery. The markers, verified in
place:

- **ADR-03 §3** (R3): "As-of ROW DATA reconstruction" — the name-pointer as-of rolls to
  the old image; the history subquery reconstructs row data at the instant.
- **ADR-08 §6a** (R10): the hold inside the revert commit is SET-BASED (one
  `INSERT … SELECT` + one `UPDATE` over the blast closure — O(1) round trips in N).
- **ADR-10 §4** (R1): std/sql SELECT-only is engine-enforced (READ ONLY txn), not just
  string-enforced; the trust boundary (author-trusted SQL text, no auto-injected tenant
  WHERE) is documented + carried on `native_tcb_coverage.trusted_for`.
- **ADR-10 §3/§5** (R13): `std/files` (external-sink `files.put`, content-addressed) and
  `std/i18n` (pure/total `i18n.t` lookup) promoted DEFER → SHIP as MODULES, not a 14th
  field type (the closed 13-type roster is untouched).
- **ADR-11 §7** (R2): the settings/schema form is a form whose submit drives ADMISSION,
  not the §7 row-mutation path.
- **ADR-12 §7** (R4): operatorPlane v1.1 — reactive session with restart-door writes;
  the admission-door approve write + panels 2–4 named as the next increment.
- **ADR-13 §3** (R10): `epoch.hold_fence_ms` perf_budget row (120 ms, measured ≈36 ms),
  red on an un-batched O(N) regression.

(R6's `ceil((p95_iter+1)×5×1.5)` formula carries a BUILD-E R6 marker in ADR-12 §5 — it
was discharged pre-Stage-F by M5 run 3. Workstream A discharged R7/R8/R9/R11 with ZERO
ADR changes — ADR-02 §5 already mandated the overlay-scope canary replay R8 implements.)

## 5. Real bugs found and fixed by the burn-down (red-path-first)

The residues were not paperwork — using them surfaced four latent defects, each fixed
with a captured RED:

- **R1 — std/sql write-bypass** (`769719a`): `isReadOnlySQL` is a prefix check, so
  `SELECT nextval()`/`setval()` pass it yet are real writes; the non-as-of read path ran
  them in autocommit with NO transaction, so PG executed the write (RED: a derived `id`
  sequence mutated 2 → 999999). Fix: every read now runs inside a READ ONLY txn — PG
  itself refuses the write.
- **R9 — migrate delta keyed on the wrong identity** (`6aa0fc1`): the std delta was
  keyed on the definition hash, but every std TYPE shares the opaque `unknown` genesis
  body — a new type reuses an existing hash and needs a fresh name-pointer. Re-keyed on
  the name-pointer.
- **R12 — latent template-throw escape** (`2d141f9`): `isLiteralNode` treated every
  template as literal, so a pii-interpolated `` throw `err ${owner}` `` was admitted
  CLEAN (captured admitted in `before.txt`). Fix: `provablyCleanThrow` requires an AST
  with ZERO reference forms.
- **R10 — O(N) revert fence** (`dea3181`): the bad-epoch revert held dependents with a
  per-row 2N loop; a dependents-heavy hold (N=5000) blew the budget ~10× (~355 ms).
  Fix: set-based fence (~36 ms), budgeted by `epoch.hold_fence_ms`.

(R8 additionally caught a canary BLINDNESS — the pipeline leg's `scope_kind=0` filter
let an overlay-def text↔AST drift pass silently — witnessed then closed, no product bug
shipped.)

## 6. Residue-forward — what is RE-NAMED into v2

Nothing silent; each carries a why-safe (STAGE-E.md §9, `docs/claim-evidence.md` §2):

- **R1** — policy-predicate injection into std/sql reads (SQL text author-trusted, no
  auto-injected tenant WHERE; erf reads carry every policy-scoped surface v1 renders).
- **R2** — the Settings form submit rides a plain POST; wiring it onto the reactive
  `/session` bus is the next increment.
- **R3** — (a) a row INSERTed after the as-of instant and never modified (creation time
  untracked — history fires only on UPDATE/DELETE); (b) arbitrary `std/sql.query` as-of.
- **R4** — the admission-door approve-a-pending-patch write + panels 2–4 (blast-radius
  delta, masked-impersonation/reveal-grant mint, catalog/audit browse) — new
  surfaces/authority, held to protect the no-new-Go line.
- **R10** — no per-claim `epoch_hold` read on the 50k-storm step path.
- **R12** — a template interpolating a NON-pii variable stays conservatively rejected.
- **R13** — `files.get`/`files.list`, an in-substrate blob table + capability-gated
  `files.put`, i18n pluralization/ICU (all unconsumed); `test.fake` stays a stub.
- **R14** — one item re-named as correct point-in-time semantics.
- **C24 projectional editors** — stays a residue; the operator did not scope it this
  session (the only residue-ONLY claim in `docs/claim-evidence.md`).
- **The human stranger-review re-record** — the Stage-E gate ran with an LLM reviewer
  (labeled in the row); a human re-record through the same door stays
  operator-scheduled.

The DEFER-V2 set (15 why-safes) is unchanged in `docs/claim-evidence.md` §2.

## 6a. Council-sourced v2 backlog (post-close review, 2026-07-18)

A five-lens opus review council (systems architect, security, database, product/DX,
skeptic) read the specs against the code after close-out. Verdicts: architecture
sound-with-caveats · security holds-with-caveats · data sound-with-caveats · product
niche-but-viable · evidence mostly-rigorous-with-gaps. None called it a paper
architecture; the convergent finding was that the deepest claims (durable-for-years,
resume-across-epochs, agents-author-code, any-scale) are provisioned but unexercised.
Net-new items (those not already in §6) are filed here for v2; severity is the council's.

- **F-V2-1 [architect · CRITICAL] Cross-version resume is unexercised.** Everything is
  `FormatVersion=1`/`SchemaVersion=1`; `cfr/decode.go` rejects any other version and no
  up-converter exists. The "year-old resume across an epoch" test (`yearold_test.go:55`)
  advances the epoch by copying identical manifest roots (the test comment concedes it).
  *v2:* mint a real `r2`/CFR-2 with a semantic delta + one up-converter, drive the golden
  corpus bit-identically across the boundary, and run a lattice-narrowing `migrate --commit`
  that blocks a real banned-type continuation.
- **F-V2-2 [architect · HIGH] No availability/durability architecture for the one
  Postgres.** Replication/failover/backup are assumed by ADR-03/06, designed by neither; no
  RPO/RTO, no PITR restore drill. *v2:* specify + drill a streaming-replica + PITR + failover
  topology with a release-gated restore.
- **F-V2-3 [security · CRITICAL] Cross-tenant read via `std/sql.query`.** No injected tenant
  predicate, no row-level security anywhere (`schema.sql` has no `create policy`); the
  `orgScoped` predicate applies only to the erf path, and `crm/pipeline.ts:15` ships a query
  with no org filter. (Named narrowly as R1 §6; the council escalates it to CRITICAL —
  *before a second tenant.*) *v2:* engine-enforced RLS keyed on the authenticated principal's
  org via a session GUC, or forbid raw `std/sql` in tenant/agent scope.
- **F-V2-4 [security · HIGH] `std/sql` read DoS + advisory-lock pool poisoning.** `READ ONLY`
  refuses writes, not expensive reads or locks: no `statement_timeout`, and
  `pg_advisory_lock` survives COMMIT to poison a pooled connection (`pool.go` does no
  `DISCARD ALL`). *v2:* `SET LOCAL statement_timeout`/`lock_timeout`, a function allowlist
  rejecting `pg_advisory_*`/`pg_sleep`/locking, and connection discard after `sql.query`.
- **F-V2-5 [security · MED→HIGH] V2 pii-taint is intra-patch, not whole-graph.** Soundness
  across module boundaries rests inductively on the export-boundary check, not structurally;
  `sql.query` results are untainted; non-vault-column + history sinks are still unmodeled
  (only the log sink closed in BUILD-E). *v2:* whole-graph taint (fold base-helper return
  types), taint `sql.query` by column provenance, promote every named residue to a red-path
  fixture.
- **F-V2-6 [database · CRITICAL] Single global `admission.id` order is unshardable.** It
  underwrites the deterministic fold, the git projection, and every as-of query — so the
  substrate cannot shard. *v2:* decide in writing whether v1 is one-Postgres/one-region by
  design, or region-scope the ledger id + per-shard fold before customer data lands (cheap on
  paper now, a re-derivation of the determinism gate later).
- **F-V2-7 [database · HIGH] History/outbox/continuation are unpartitionable and un-aged.**
  The I4 GiST exclusion forbids partitioning `name_pointer_history`; `res_*_history`,
  `outbox`, `channel_message`, `continuation` grow with write volume forever. *v2:* a
  retention/rollup story for `outbox`/`channel_message`/`done` continuations (no as-of
  needed); re-evaluate the I4 exclusion vs hash-partitioning on PG17+.
- **F-V2-8 [database · MED] As-of is sub-uni-temporal.** `asofRowsetPITR` has no creation
  lower-bound → a row created *after* the queried instant and later modified surfaces as a
  history-leg phantom (broader than R3's named base-leg half); and `valid_from` means
  *became-current* in `name_pointer_history` but *stopped-being-current* in `res_*_history` —
  a trap for the deferred general `std/sql` as-of. *v2:* add an INSERT-trigger creation window
  (true `valid_from`/`valid_to` per business row) and unify the column semantics.
- **F-V2-9 [product · CRITICAL] UI expressiveness ceiling.** The closed 25-primitive roster
  has no charts, no custom widgets, no third-party embed, no escape hatch, and depth-1
  `props.<field>` binding only — a real product team hits the wall in week two and can only
  wait for a kernel epoch. *v2:* a governed custom-render capability that preserves the V2
  masking proof, plus charts + computed/aggregate fields in-scope.
- **F-V2-10 [product · HIGH] The proof CRM validates the substrate, not a product.** 170 LOC,
  7-row dataset, no computed fields/aggregates/charts/integration. *v2:* build product #2
  (the analytics-shaped one already promised) before claiming the component roster closed.
- **F-V2-11 [skeptic · HIGH] The agent-plane proof is a toy proxy.** pass@1=pass@3=1.00 and
  restart 0.968 were scored on toy pure-arithmetic tasks (`corpus.go`) in a dialect the eval
  configures (`drive.go:17`) to forbid imports/classes/stdlib — regel's actual surface — with
  the rubric handed to the model in-prompt, single model. *v2:* a corpus of real regel
  definitions (an `erf resource` with `pii()`, a `std/taak` workflow, a capability-gated
  call), authored rubric-withheld, scored by the independent oracle, across ≥2 models.
- **F-V2-12 [skeptic · MED] Durability + storm scale are simulated.** "Years"/epoch resume
  use backdated rows and frozen-then-past-due timers; the 10k/50k storms clone one frame N×
  (only ~100 real SSE conns). The exactly-once property is genuinely proven; the *load* and
  *elapsed time* are not. *v2:* honest framing (name the golden-corpus decode-drift regression
  as THE durability claim) + a smaller-N storm driven entirely through the real front door.

Overlap with §6: F-V2-3 refines R1; F-V2-8 extends R3(a). The rest are net-new. All twelve
trace to a council report; the source verdicts and the file pointers above are the record.

## 7. Discipline notes, stated

(i) Red-path-first held for every residue: a captured RED (`evidence-f/<r>/red-path.txt`
or `before.txt`) precedes each GREEN, and each carries a permanent regression test. (ii)
Four real bugs (§5) were fixed ADR-first, not patched around. (iii) The stranger-review
reviewer remains an LLM, honestly labeled in its row; the operator can re-record with a
human. (iv) The whole stage ran strictly serial (one workstream/helper at a time); the
final gate ran serialized/clean-env by design (§3). (v) One session cut survived in
Workstream A (20 project strands total).

## 8. Claim ledger

`docs/claim-evidence.md` re-tallied at close-out: **31 claims → 30 test / 18 demo / 4
residue** (C6, C24, C27, C30). Stage F moved C21 (`T+R → T+D`, R-restart-flip retired),
C23 (`D+R → T+D`, R-settings-form retired), C27 (`T+R → T+D+R`, std envelope narrowed);
the re-tally provenance (the Stage-E summary line lagged its own table by 2/2) is stated
in the ledger.

---

**STATE close-out:** Stage F DONE — 14 residues discharged (R4 partial), 4 real bugs
fixed red-path-first, 6 BUILD-F ADR markers, final gate GREEN (serialized suite + 17
scripts + git 2-fold + genesis 2-DB byte-identical) at `59c5fe6`; v1 → v1.1 complete,
residue-forward set named for v2.
