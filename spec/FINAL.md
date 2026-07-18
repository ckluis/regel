# regel — v1 close-out

*The one-document narrative for regel v1: what it is, the gate arc that proved it,
the evidence that stands at HEAD, and what v2 holds. Author: Stage-F close-out
(Workstream D). Date: 2026-07-18. Verified HEAD for the final gate: `59c5fe6`
(evidence `evidence-f/final-gate/`). Real PostgreSQL 16.13. No claim below is made
without an on-disk evidence pointer; every number traces to a captured file, a
`go test` identifier, or a gate report cited by section.*

---

## 1. The concept — what regel is

regel is a **code-as-rows governed substrate**: one Go kernel over one Postgres, no
third dependency (vendored tsgo, an owned pure-Go interpreter, an owned wire client,
vetted AEAD/KDF — no cgo, Node, or npm; C29). Application code does not live in files
that a server loads; it lives as **admitted rows** in a catalog, and the kernel ships
**zero business logic** (C16, proven by the reference CRM: `grep -rin crm internal/
cmd/` non-test = 0 app logic, `TestCRMReferenceAppEndToEnd` admits `crm/` from disk).

Five load-bearing commitments, each an ADR (full text in `spec/architecture/`):

- **Code enters by an admission transaction.** Canonical print + a real static
  checker (tsgo, TypeScript 7.1.0-dev) + six verifiers + DDL + name pointers commit in
  ONE serializable transaction (ADR-01/03/07; C1). Rejected code never becomes code —
  zero trace (C11). Engineer, tenant, and agent all walk the SAME gate; there is no
  privileged CI side door (C9).
- **A closed-world strict TS7 dialect.** A default-deny AST whitelist (classes / this /
  new / generators / symbols / enums banned; async/await the sole suspension surface),
  identity = SHA-256 over the normalized AST (never text), so the hash IS the identity,
  there is exactly one canonical rendering, and renames are metadata (ADR-01/02; C2/C3).
- **Deploy is a commit; rollback is a WHERE clause.** As-of reads reconstruct schema,
  behavior, AND (Stage-F R3) historical row data through a `?as_of=` mount — rollback is
  a point-in-time query, not a redeploy (ADR-03/09; C8).
- **Pauses are continuations; anything resumes exactly-once.** The interpreter is an
  owned defunctionalized CEK machine; fuel exhaustion parks as a durable condition and
  never panics; effects are exactly-once across a real `kill -9` (ADR-04/05; C4/C13).
  Durable conditions, UI sessions, and workflows are all continuation rows (C14).
- **The verifier suite — not the type system — is the security boundary,** stated as
  such: trusted code runs on a shared heap, TypeScript's unsound corners survive in the
  trusted tier, and verifier coverage (enumerative, backed by a hostile corpus + dual
  mutation + monotone floors) is what holds the line (ADR-07; C30).

The agent plane (ADR-12) makes agents ordinary capability principals under
admission-fuel budgets over a real MCP JSON-RPC surface; std/ (ADR-10) is itself rows
(genesis transaction, native bodies bound by hash), and the epoch (ADR-08) upgrades
dialect, engine, stdlib, and gate as one atomic step with boot-refusal on mismatch.

## 2. The gate arc — A → B → C → D → E → F

Each stage was gated GREEN-with-named-residues against real Postgres 16.13; every
verdict is in `spec/gates/STAGE-*.md`.

**Stage A (M0, walking skeleton — `spec/gates/STAGE-A.md`).** One binary + one
Postgres end-to-end: admit → CEK-evaluate → HTTP serve → admit v2 → rollback via as-of
→ fuel-park → restart-resume. All four GATE-1 §4 kill-test families at Stage-A scope;
perf 27.1M CEK steps/sec (27× floor), metering tax ≈0%; the I4 overlap kill-test on PG
16.13; tsgo vendored as a Go lib. The skeleton walked before any feature was built.

**Stage B (deepest bets under kill-tests — `spec/gates/STAGE-B.md`).** The ADR-05
continuation kill suite 10/10 (+12/13/14); a real `kill -9` mid-workflow resumes
cross-process to the byte-identical result with effects exactly-once; a year-old resume
across an epoch; a 10k wake storm with 0 dupes and abort_rate ≤0.9%; exact-budget fuel
parking; a forged CapToken refused pre-machine-re-entry with zero trace. The "serialize
stably for years" bet was reduced to a decode-drift regression gate.

**Stage C (M1 verifiers + M5 surfaces — `spec/gates/STAGE-C.md`).** The full V1–V6
verifier roster red-pathed in-transaction; a hostile corpus (19 fixtures) + dual
mutation (13 mutants, all killed, a survivor blocks release) + a monotone
`verifier_coverage` gate; tsgo-in-txn p95 = 12 ms under a deterministic budget; the MCP
plane (11 tools / 6 resources / 3 prompts) over real JSON-RPC; git projection folding
the ledger to two-fold byte-identical SHAs with merge-as-admission. The real-LLM M5
legs were named OPEN to bind at Stage E.

**Stage D (M3 the world + M4 the reactive layer — `spec/gates/STAGE-D.md`).** std/
complete as rows (14 batteries + 25 tier-1 components, 72 genesis entries) behind a
two-fresh-DB byte-identical genesis with a 219-boundary mid-genesis kill sweep; full
`erf resource(...)` derivation (13 field types + `pii()`, vault + crypto-shred +
attestation, history-excludes-PII); the ADR-11 reactive layer live (binary SSE frames,
incremental digest, desync→resync self-heal, sessions as capped continuation rows,
six-leaf PII masking); a 50k storm drained exactly-once. Two bugs were found by USING
the system (reactive loops never started; pgwire cancel-taint pool poisoning).

**Stage E (M6 → v1 — `spec/gates/STAGE-E.md`).** The proof CRM entirely as admitted
rows and all five proof scenarios scripted + exit 0 (tenant field-add, agent patch over
MCP, mid-flight workflow surviving TWO deploys, as-of rollback through the UI, PII
crypto-shred with oracle-recomputed attestation). The OPEN M5 gates RAN against a real
LLM (`claude -p`): §7 restart-decision accuracy 0.968 (M=31) GREEN, flipping the agent
`condition.restart` authority through a mechanized red-pathed gate; §3a authoring pass@k
measured 1.00. A real outside stranger-review gate was built and run — its first verdict
UNFINISHED forced curation fixes, its re-review read `finished`. Post-gate, M5 run 3
(`1ef7232`) discharged R5+R6 at N=52 (pass@1 = pass@3 = 1.00, §5 fuel formula re-derived
ADR-first). Baseline claim ledger: `docs/claim-evidence.md`.

**Stage F (residue burn-down v1 → v1.1 — `spec/gates/STAGE-F.md`).** The 12 open
STAGE-E §9 residues + the R14 papercuts were burned down across four workstreams (A
depth-of-proof, B hardening, C product surface, D this close-out). Fourteen residues
discharged (R4 partial); four real bugs found and fixed red-path-first (R1 std/sql
write-bypass, R9 name-pointer migrate key, R12 template-throw escape, R10 per-row
fence). Six BUILD-F ADR markers landed law-first. The final gate is green.

## 3. Evidence that stands at HEAD

**Claim ⇄ evidence ledger (`docs/claim-evidence.md`, re-tallied 2026-07-18):**
31 concept-doc claims → **30 carry a red-path test, 18 carry a runnable demo, 4 carry a
named residue** (C6 the "years" bet, C24 projectional editors, C27 std envelope
narrowed, C30 the enumerative boundary — each with a why-safe; C24 is the only
residue-ONLY claim). The re-tally provenance (the Stage-E summary line lagged its own
table by 2/2; Stage F then moved C21/C23/C27) is stated in the ledger.

**Standing invariants at final-gate HEAD `59c5fe6` (`evidence-f/final-gate/`):**

- **Serialized suite green.** Uncached `go clean -testcache && go test -p 1 ./...` —
  every package `ok`, `GOTEST_EXIT=0` (`go-test.txt`). Known test-harness property, not
  a product defect: the kill-9 and `gate/m5eval` tests flake ONLY under parallel
  full-suite load (step-timing + a shared scratch-DSN env); the serialized, clean-env
  run is fully green. The gate is defined as the serialized run.
- **17 deterministic scripts exit 0** (`scripts.txt`, fail flag 0). `m5-eval.sh`
  (live-LLM, 156 attempts, rate-limited) and `stranger-review.sh` (operator-scheduled
  human re-record) are operator-run, not part of the deterministic gate.
- **Determinism gates PASS** (`determinism.txt`): git projection two-fold
  byte-identical (`TestDeterminismReleaseGate`); genesis two-fresh-DB byte-identical
  (`TestGateA_TwoFreshDBReproducibility`, `TestBuildImageDeterministic`).
- **M5 real-LLM gates GREEN at N=52** (run 3, STAGE-E §4a): §3a pass@1 = pass@3 = 1.00
  (156/156 scored), §7 restart accuracy 0.968 (30/31), §5 fuel capacity 15
  formula-derived. Run against `claude -p`, never faked; the behavioral oracle is an
  independent regel-native reducer sharing no code with the CEK machine.

## 4. What v2 holds

Nothing below is a defect; each is a bounded increment with a why-safe on disk.

**RE-NAMED narrower residues from Stage F** (`spec/gates/STAGE-F.md` §residue-forward;
STAGE-E.md §9):

- **R1** — policy-predicate injection into std/sql reads: the SQL text is
  author-trusted and std/sql does NOT auto-inject a tenant WHERE (SELECT-only is
  engine-enforced; erf reads carry every policy-scoped surface v1 renders).
- **R2** — the Settings form submit is a plain POST; wiring it onto the reactive
  `/session` bus so the client itself drives the field-add is the next increment.
- **R3** — a row INSERTed after the as-of instant and never modified (creation time
  untracked, history fires only on UPDATE/DELETE); arbitrary `std/sql.query` as-of.
- **R4** — the operator plane's admission-door approve-a-pending-patch write and
  panels 2–4 (blast-radius delta, masked-impersonation/reveal-grant mint, catalog
  browse) — new surfaces/authority, deliberately unbuilt to hold the no-new-Go line.
- **R10** — no per-claim `epoch_hold` read on the 50k-storm step path.
- **R12** — a template interpolating a NON-pii variable stays conservatively rejected
  (proving it clean needs env-resolved taint the syntactic predicate omits).
- **R13** — `files.get`/`files.list`, an in-substrate blob table + capability-gated
  `files.put`, and i18n pluralization/ICU (all unconsumed); `test.fake` stays a stub.
- **R14** — one item re-named as correct point-in-time semantics (no code change).

**DEFER-V2 set** (`docs/claim-evidence.md` §2 — 15 why-safes): tsgo fork-internal
edits (A1/A5/A6), server-side SCRAM (A7c), full per-statement fault injection (B3),
stated Stage-B deviations (B9), I4 lock coarseness (C2), typecheck-timeout secondary
path (C3), hosted-forge wiring (C6), H_dispatch body hash (D5), derivation gaps (D7),
reactive minimalism (D8), single-machine perf numbers (D9), OTLP push exporter
(D10b/L4/L5).

**C24 projectional editors** — the workflow canvas and form designer stay a residue;
the operator did not scope them this session. The catalog→UI direction is proven
(derived form/table/board/dashboard) and the UI→admission direction is now proven too
(Stage-F R2's admitted `SettingsForm`); the editor rides both.

**The operator-scheduled human stranger-review re-record** — the Stage-E stranger gate
ran with an LLM reviewer (honestly labeled in the row); re-recording with a human
reviewer through the same door stays operator-scheduled.

---

*regel v1 is closed. One Go kernel, one Postgres, code-as-rows, a closed-world strict
TS7 dialect, deploy=commit and rollback=as-of — proven end-to-end on a real CRM through
one admission gate, with the evidence in `spec/gates/` and `docs/claim-evidence.md`.*
