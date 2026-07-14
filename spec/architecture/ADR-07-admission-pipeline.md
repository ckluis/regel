# ADR-07: The admission pipeline and verifier suite

## Status

Accepted — Phase 1

## Context

BRIEF §4 requires the verifier suite v1 roster (exact set, semantics, in-transaction
execution against tsgo output, adversarial harness, versioned coverage) and the
kernel-mediated mid-transaction tsgo invocation. Constraint #5 makes this ADR the
security boundary: trusted-tier code runs unverified-by-types on a shared heap, so the
suite's coverage — stated, versioned, adversarially tested — is what "trusted" means.

Cross-ADR dependencies, stated explicitly:
- The transaction shape is ADR-03 §5, verbatim: this ADR expands its steps 2, 4, and 5
  and adds nothing outside them. Isolation is ADR-03's SERIALIZABLE + CAS (I8).
- Stage order inside step 2 is ADR-01 §4 (parse → lower → grammar gate → print+hash);
  the grammar gate already owns the subset bans, floating-promise check, acyclicity,
  and capture rules R1–R5. Import resolution is ADR-01's closed resolver plus ADR-02's
  name→`Ref(hash)` substitution: an unresolvable or out-of-world import dies there.
- Printer idempotence is ADR-02 §5 guarantee 4, enforced by the kernel before insert.
- The capture verifier is defined in ADR-05 §3; this ADR seats it in the suite roster.
- tsgo is a typechecker only (ADR-04 §3); grants are ADR-04 §5 rows.

Where each check lives (the double-specification question, settled):

| Check | Lives in | Not a suite verifier because |
|---|---|---|
| Subset bans, floating promises, acyclicity, R1–R5 | ADR-01 grammar gate (step 2) | Grammar property, no catalog queries needed |
| Import closure | ADR-01 resolver + ADR-02 Ref substitution (step 2) | Resolution failure is a lowering error; also caught by tsgo's pinned resolver |
| Printer idempotence | ADR-02 §5 g4 kernel check (step 3) | Store-integrity invariant, runs on every insert unconditionally |
| Capture discipline | Verifier suite (ADR-05 §3) | Needs live-variable dataflow over the typed AST |

All of these fail closed inside the same transaction and report through the same §6
Verdict; only the last is a roster member.

## Decision

### 1. Pipeline: ADR-03 §5, expanded

One `SERIALIZABLE` transaction. Steps 1–8 are ADR-03's; sub-stages are this ADR's:

```
BEGIN ISOLATION LEVEL SERIALIZABLE;
 1  INSERT admission(...) RETURNING id;
 2a decode patch envelope; bind scope from the AUTHENTICATED principal's chain —
    never from the submission body (scope forgery is unrepresentable);
 2b parse (tsgo) → lower (default-deny) → grammar gate            [ADR-01 §4]
 2c normalize → canonical print → hash                            [ADR-02]
 2d no-op short-circuit: if every submitted hash is already catalogued AND every
    target pointer already resolves to it at the target scope, return an
    already-admitted verdict (audit-rowed); any pointer that would move continues;
 3  INSERT definition / definition_meta (ON CONFLICT DO NOTHING), after the kernel
    re-verifies hash == SHA-256(domain ‖ ast)                     [ADR-02 §5 g4]
 4  tsgo typecheck via the hermetic module host (§2), affected set, budgeted (§3);
 5a derivation passes: explicit ordered AST passes per resource →
    proposed derived rows (schema, history, policy wiring, forms, REST/OpenAPI/MCP,
    boundary validators, vault routing) + migration_sql — pure, nothing applied;
 5b verifier suite (§4): six verifiers over base ⊕ patch ⊕ derived, as queries on
    this connection (they see uncommitted rows); any failure ⇒ RAISE;
 6  apply migration_sql (additive only — §4 V6);
 7  name_pointer CAS upsert                                       [ADR-03 §5.7]
 8  re-verify overlays of any moved base pointer                  [ADR-03 §3]
COMMIT;
```

BUILD-C: step 5a's *seam* (explicit ordered pure passes over base ⊕ patch → proposed
derived rows + `migration_sql`, nothing applied) is built at Stage C; its v1 *pass
roster* over the full erf `resource(...)` vocabulary is ADR-10's and lands at Stage D
behind the same seam. The Stage-C pass set covers exactly the governance vocabulary the
Stage-C std slice exposes — capability/contract/policy declarations, vault-typed PII
fields, and a minimal `std/resource` field map deriving additive DDL — which is what
V3 (catalog-parity) and V6 (derivation-parity) verify at M1. Stage-D passes plug into
this seam without changing any verifier's semantics.

Ordering rationale: cheapest and most local first; identity (2c) is fixed by canonical
form alone before the checker runs, so a tsgo version bump can never move a hash;
derivation precedes verification because catalog-parity and derivation-parity check the
derived model; mutations that take locks (6–7) come last so a late rejection rolls back
near-nothing. Every stage is a pure function of the frozen snapshot plus the
submission, and fail-closed: a stage that cannot prove its property rejects — it never
warns and continues.

**Content-seeder attribution — the third principal in the admission row (R1-04: content-seeder provenance fields).**
For an agent-authored submission (ADR-12), step 1's admission row carries a
**content-seeder set** in addition to author and (for product scope) approver: the
provenance `{source_kind, source_ref, scope, seeded_by | "unattributed"}` of the catalog
/ resource / condition / audit rows the authoring session read that reach the submitted
patch. The kernel binds this set from the MCP read-log the agent submits with the patch,
validated against the **authenticated principal's scope chain** by the same step-2a rule
that binds patch scope — a seeder outside that chain is unrepresentable, so the set can
never be forged to blame another tenant. An external-effect source with no resolvable
principal (an upstream system's failure text in a `durable_condition.message`) is recorded
by source ref and marked `unattributed`. Human and PR submissions (ADR-09) carry an empty
seeder set. The set is immutable once written and is the substrate for the §6 Verdict
delta. (The new admission columns are DDL'd in ADR-03 §1 table (5) — flagged there, not
authored in this ADR. R1-INT: pointer corrected §5→§1; the columns now exist.)

### 2. tsgo invocation: hermetic in-memory module host

- **Vendored Go library, kernel-mediated.** The kernel calls the checker API directly
  (program construction + semantic/global diagnostics). No subprocess, no fork, no
  disk, no network, no emit (ADR-04 §3).
- **Three copy-on-write layers** back a kernel-implemented in-memory `CompilerHost`:
  L0 `std/` — the epoch's std type surface, generated from the ADR-03 §6 mirror rows
  and compiled into the binary; L1 `app/` — every catalogued definition at this
  transaction's snapshot, served at the path given by the **name→path function shared
  with ADR-09** (`readFile("app/crm/deal.ts")` returns that name's current
  `canonical_text`); L2 — the in-flight patch overlaying its target paths.
- **Hermeticity guarantee (testable):** diagnostics = `f(patch, snapshot hash-set,
  epoch)`. The host exposes no clock, no filesystem, no environment, no resolver
  outside L0–L2; the same three inputs yield byte-identical diagnostics on any kernel.
  Two identical submissions cannot receive different verdicts.
- **Affected set + memoization.** The checked graph is the patch units plus their
  transitive reverse-dependency closure over ADR-03 `deps` edges; the rest of the
  world is resolvable but not re-checked. A definition carries a `clean@(epoch, hash)`
  stamp from its own admission; unchanged definitions never re-typecheck
  (content-addressing's dividend). The double-digit-millisecond claim rests on this.

### 3. Typecheck budget

**Deterministic parse-depth ceiling — ahead of all budgets (R1-09: parse-depth ceiling before any budget/fuel engages).**
The type-graph node ceiling below guards step 4, but parse runs first at step 2b, so a
deeply-nested submission can fatally exhaust the Go stack — killing the process,
unrecoverable — *before* that ceiling or the wall-clock backstop ever engages. Ahead of
both, and ahead of any fuel charged by depth-of-stage (ADR-12 §5), the parser/lowering
pass enforces a **fixed syntactic nesting-depth limit** `MAX_PARSE_DEPTH`, checked as the
recursive descent proceeds (an explicit depth counter, so the check fires *at* the depth
boundary, never after the stack is already blown). It is **deterministic**: the same input
yields the same refusal on any kernel, independent of load, clock, or fuel state. Breach
rejects with a `PARSE_DEPTH` diagnostic naming the offending span and the depth reached;
because it is a non-green outcome it **mints a durable `refusal_id`** (§6, R1-08) — a
parse-depth refusal is as retrievable by `verdict.get {refusal_id}` as any other, never an
unrecoverable crash. The limit is a gate-fixed constant (M0/M1); lowering it can only
tighten what an accepted submission may nest, never loosen it.

Primary control: a **deterministic node ceiling** on the instantiated type graph.
Breach rejects with `TYPECHECK_BUDGET` naming the offending type site — same input,
same verdict, on any machine. Secondary backstop: a wall-clock deadline
(`TYPECHECK_TIMEOUT`), liveness only; breach aborts the transaction cleanly and serving
traffic is untouched (the heavy phase holds no locks — reads at a snapshot). A
conditional-type bomb is a rejected patch, never a stalled gate.

**tsgo-ms-in-transaction under concurrent admission — a measured, budgeted quantity (R1-07: measured under concurrency; backpressure, not silent stretch).**
tsgo is the expensive stage and it runs **inside** the SERIALIZABLE transaction (§1 step
4), so the milliseconds it holds the transaction open lengthen the SSI conflict window and
inflate the serialization-retry rate under concurrent admission. This is **measured**, not
assumed. A **concurrent-admission benchmark** — **N = 32 concurrent admissions** of
representative reference-catalog patches racing on a shared snapshot — records
**tsgo-ms-held-in-transaction** and the resulting retry rate, with an initial budget of
**p95 ≤ 40 ms, p99 ≤ 80 ms** tsgo-in-txn and a **serialization-retry rate ≤ 5 %** at
N = 32. **When the budget is exceeded the gate applies backpressure — it does not silently
stretch:** the kernel bounds concurrent in-transaction typechecks with an admission-control
semaphore sized from the benchmark, and an admission that would exceed it is refused
**before `BEGIN`** with an explicit `ADMISSION_BUSY` verdict carrying a `retry-after` — a
pre-BEGIN refusal recorded in the `gate_refusal` ledger (ADR-12 §5, never the admission
ledger), so a caller re-submits rather than a transaction piling onto the conflict window
with serving-latency-invisible cost. The budget and its measured value are `perf_budget`
rows (ADR-04 §8); they are enforced by the **M1 gate** that builds this hermetic host (the
milestone at which tsgo-in-txn first exists) — a regression is red.

### 4. Verifier suite v1 roster: exactly six

Run in this order at step 5b, fail-closed, each a pure query/walk over the typed AST +
the proposed catalog model. Each entry: semantics / failure shape / one red-path test.

- **V1 capability-audit** — the set of capabilities the definition can *name* (free
  references resolving to capability-bearing std bindings, per ADR-04 §5) ⊆ its
  declared capability set ⊆ the submitting principal's grants. No ambient authority.
  / `CAP_UNGRANTED{capability, subject}` / type-correct workflow declaring
  `["crm.read"]` calls `mail.send(...)` ⇒ reject.
- **V2 pii-flow** — taint analysis: vault/`pii`-typed sources reaching a boundary sink
  (response render, log, outbound http, non-vault column, history stream) without a
  masking or reveal-grant combinator reject; a vault-typed literal in code rejects
  (the ADR-03 immortality interaction — a PII literal must never be immortalized).
  / `PII_ESCAPE{field, sink}`, `PII_LITERAL{loc}` / view returns `deal.owner`
  unmasked in an HTTP response ⇒ reject.
- **V3 catalog-parity** — every declared governance artifact (policy, horizon,
  contract requirement, capability requirement) is reachable from at least one
  admitted or derived execution path in the proposed reference graph; nothing is
  inert. "Declared but unenforced" never becomes code.
  / `PARITY_UNWIRED{artifact, definition}` / `resource` declares `policy: orgScoped`
  and no read path consults it ⇒ reject.
- **V4 contracts** — pre/postcondition combinators are well-formed against the
  definition's derived types, **pure** (naming a capability inside a contract clause
  rejects — grafted from the prior-art proposal), reference only in-scope symbols, and
  every call site the catalog wires discharges them as boundary validators.
  / `CONTRACT_MALFORMED{def, clause}`, `CONTRACT_EFFECTFUL{clause}` / postcondition
  calls `mail.send` ⇒ reject.
- **V5 capture** — the ADR-05 §3 capture verifier: for every `await`, the
  live-variable set lies inside the R2 serializable lattice; the verifier and the CFR
  codec share one type table (encodable ≡ admitted).
  / `CAPTURE_UNSERIALIZABLE{binding, path}` / a connection-typed value live across
  `await` ⇒ reject (ADR-05 red-path test 4a, executed here).
- **V6 derivation-parity** — every derivation pass is total over the declared
  resource (no attribute yields a partial or undefined derivation; a semantic type
  feeding a PII field must carry a masking rule); derived artifacts are internally
  consistent (every form field ↔ a real column, every route ↔ a handler); emitted DDL
  is **additive-only** — destructive or rewriting DDL requires an explicit
  `intent=retire` envelope and routes to a staged maintenance lane (an additive
  admission plus ADR-06 task rows for backfill/drop), never inline.
  / `DERIVE_PARTIAL{attr}`, `DDL_DESTRUCTIVE{stmt}` / a derived `DROP COLUMN` without
  retire intent ⇒ reject.

BUILD-C (increment C2 — V2/V4/V5 realized as real, scoped verifiers over the typed
lowered rast, red-path-first; V1/V3/V6 already real from C1):
- **V2 pii-flow** taints `std/pii.Vault`-typed bindings (the vault route of a `pii(...)`
  field, §4 item 5) and propagates through local bindings and through a helper whose
  declared return type is a vault type (the multi-hop case); a tainted value reaching a
  served definition's return path or a capability-bearing outbound call (`std/mail.send`)
  without `std/pii.mask`/`reveal` ⇒ `PII_ESCAPE`; a literal given vault type ⇒
  `PII_LITERAL`. Scope-C RESIDUE: resource-field member-type inference, the log/history/
  non-vault-column sinks, and destructuring/loop binder scopes are Stage-D — the vault
  type is the field's admitted value form, so a `Vault`-typed flow is the faithful source.
- **V4 contracts** finds `std/contract.pre`/`post` clauses in the body; a clause naming a
  capability ⇒ `CONTRACT_EFFECTFUL`, a clause naming a governance/out-of-scope symbol
  (`std/policy`/`std/resource`/`std/sql`) ⇒ `CONTRACT_MALFORMED`. The derivation seam
  (step 5a) derives a `validator` boundary-artifact per contract-bearing def and mirrors
  the clauses to `definition.contracts` (ADR-02 §3); V3 parity then covers them.
- **V5 capture** runs the ADR-05 §3 live-variable walk over workflow-tier defs
  (`Patch.Tier == "workflow"`); a live host-resource binding (`std/sql.Conn`, or a value
  from a `NonSerial` native like `std/sql.connect`) live across an `await` ⇒
  `CAPTURE_UNSERIALIZABLE`. The R2 lattice is `cfr.EncodableTags()` — the SINGLE source of
  truth (ADR-05 §3, below); a host resource maps to no encodable tag. `std/sql` is added
  minimal (`Conn` + `connect`) as the fixture substrate (ADR-10 §3 BUILD-C).

### 5. Adversarial harness, versioned with the epoch

- **Hostile corpus** (`gate/redpath/`): one fixture per red-path test in ADR-01..07
  plus fuzz-grown variants (import squats, cast obfuscations, PII-through-N-hops,
  capability-via-alias, capture-through-iterator). Every fixture asserts a *specific*
  reject code; a green result on a hostile fixture fails the release. Every
  field-reported bypass becomes a permanent fixture before its fix ships.
- **Mutation testing, both directions, over the whole trust boundary — verifiers *and*
  the relocated gate/resolver (R1-10: dual mutation testing extended to grammar gate +
  resolver).** Direction (i): mutate admitted-clean definitions (drop a `mask()`, widen a
  grant, capture a handle across `await`, stub a policy path) and assert the owning check
  rejects the mutant. Direction (ii): mutate the **enforcement code itself** and assert the
  hostile corpus catches it. Direction (ii) covers not only the six suite verifiers (flip a
  comparison, drop a sink from the pii sink-set) but — because the Context relocation table
  moved the subset bans, floating-promise/acyclicity/capture R1–R5, and import closure *out*
  of the suite and *into* the ADR-01 grammar gate and the ADR-01/ADR-02 resolver — **the
  grammar gate and the resolver are mutation-tested as first-class targets.** A seeded mutant
  that **weakens a relocated ban** — widen a banned-syntax matcher so one forbidden form
  slips through, loosen the floating-promise or acyclicity check, admit an out-of-world
  import, weaken the R1–R5 capture predicates — **must be killed** by the hostile corpus;
  a surviving grammar-gate or resolver mutant is a coverage hole exactly like a surviving
  verifier mutant and **blocks the release**. This closes the gap Bach named: scoring only
  "the verifier code" left the bulk of the trust boundary — now sitting in the gate and
  resolver where the bans were relocated — mutation-untested, so a silently-weakened ban
  could survive; it can no longer. A surviving mutant anywhere in the three-part surface
  (verifiers, grammar gate, resolver) is a release blocker.
  BUILD-C: direction (ii)'s mechanism, realized — seeded mutants are **named weakenings
  compiled into the production enforcement code** (a mutant registry in the verifier /
  grammar-gate / resolver packages: each entry flips one comparison, drops one sink,
  widens one matcher; default-off, enabled one-at-a-time by the harness), never mocks —
  the same discipline REPORT-R1 rev-2 ratified for the ADR-04 harness-3 evaluator
  mutants. The harness enables each mutant, runs the hostile corpus, and asserts red;
  a mutant the corpus leaves green blocks the release.
- **Grammar fuzz invariants:** generated subset-valid ASTs through the real gate —
  never a panic, always terminates within budget, deterministic verdicts (same input,
  same verdict, any kernel).
- **Regel-native differential oracle as a release gate (R1-02: oracle-red or
  surviving-mutant blocks the pipeline).** The ADR-04 §6 harness-3 corpus run — the
  production CEK machine vs. an independent reference reducer over contract enforcement,
  derived boundary validators, and effect-class ordering — is part of this ADR's
  release/kill-test gate, alongside the hostile corpus and dual mutation runs. The
  release pipeline is **red** (a green pipeline is impossible) whenever either (i) any
  differential divergence exists between the machine and the reference reducer on
  verdicts, validator outcomes, effect-class order, or produced values, or (ii) any of
  the three seeded wrong-evaluation mutants (one per covered layer) survives — i.e. the
  corpus stays green under a deliberately-broken evaluator. This closes the gap that the
  base-dialect fuzz (ADR-04 §6 harness 2) leaves open: a wrong-evaluation bug in the
  regel-added layers can no longer stay green through admission and write corrupted
  values into the INSERT-only definition store or the history tier.
- **Coverage statement as data:** `verifier_coverage` rows
  `(epoch, component, threat_class_ids, corpus_case_count, mutation_score)` — the
  boundary is stated, queryable, and projected into docs. Coverage is **monotone**: an
  epoch may not shrink any component's threat inventory (grafted from the prior-art
  proposal). The `component` key (R1-10: gate/resolver carry coverage rows too) generalizes
  the former `verifier` key to name the enforcement *site* — each of the six suite verifiers,
  **plus `grammar-gate` and `resolver`** — so every relocated ban carries its own
  `mutation_score` and monotone threat inventory just as the verifiers do. A gate or resolver
  mutation score that regresses, or a threat class dropped from either component, fails the
  release exactly as a verifier regression does; the whole trust boundary — not just the
  suite — is stated as data. Per-admission verdicts are already stored in
  `admission.verifier_report` (ADR-03), so "which suite version passed this
  definition" is a SELECT.

BUILD-C (increment C3 — the §5 machinery realized, red-path-first). The mutant
registry is `internal/mutants`: ten named weakenings compiled into the REAL
production enforcement code — `V1_SKIP_DECLARED_CHECK`, `V2_DROP_LOG_SINK`,
`V3_SKIP_POLICY_PARITY`, `V4_ALLOW_EFFECTFUL`, `V5_ALLOW_ALL_TAGS`,
`V6_ALLOW_DESTRUCTIVE` (verifier.go/flow.go/contracts.go), `GATE_ALLOW_BANNED_SYNTAX`
/ `GATE_SKIP_FLOATING_PROMISE` / `GATE_WEAKEN_CAPTURE_R1` (internal/lower), and
`RESOLVER_ADMIT_OUT_OF_WORLD` (the pipeline.go resolver closure). Each is default
hard-off (the registry is disarmed until the harness arms it), switched one at a
time. The hostile corpus is `gate/redpath` (a data-only fixture set, no admission
import); one runner in `internal/admission/harness_test.go` drives every fixture
through the real pipeline on a fresh scratch DB. Direction (i) is a six-row
definition-mutation table (drop a mask ⇒ V2, widen a declared grant ⇒ V1, capture
across await ⇒ V5, unwire a policy ⇒ V3, effectful clause ⇒ V4, field-add→drop ⇒
V6). Direction (ii) arms each registry mutant, runs the corpus, and asserts ≥1
fixture flips green; a survivor fails the harness (the RED leg shipped a withheld
resolver fixture to witness this). Coverage rows are written for all eight
components (V1..V6 + grammar-gate + resolver) with `assertMonotone` refusing any
epoch that shrinks a threat inventory or regresses a `mutation_score`.
RESIDUE (Stage-C representability, ADR-07 §5 escape hatch): (a) the resolver
out-of-world witness uses the L0-stub/catalog gap (`std/resource`'s stub-only
`Resource` type) rather than a hallucinated module — a hallucinated import is
tsgo-redundant, so it cannot uniquely witness a resolver weakening; (b) the
capture-through-iterator fuzz variant is covered by its nearest representable form
(a host resource live across a straight-line await), the loop-binder scope being a
V5-walk residue (flow.go).

### 6. Concurrency and the structured Verdict

**Concurrency: SERIALIZABLE only — no advisory locks.** ADR-03's model stands
unmodified: SSI detects racing admissions as a serialization failure or a 0-row
pointer CAS; the kernel retries the whole pipeline against a fresh snapshot at most 3
times, then returns a `retry-exhausted` verdict; a patch whose declared base hashes no
longer match returns `stale-base` (client re-reads the head and resubmits) — `retry-exhausted`,
`stale-base`, and `already-admitted` are typed `outcome` enum values (§6 Verdict, R1-08), not
free strings a caller sniffs. A second
serialization mechanism would be redundant machinery guarding the same rows. The
heavy phase (typecheck + derive + verify) holds no locks; serving traffic reads
committed rows at its own MVCC snapshot and is never blocked by an in-flight
admission; additive DDL takes a brief ACCESS EXCLUSIVE bounded by `lock_timeout`.

**Verdict — one schema for humans and agents (gate parity):**

```
Verdict {
  outcome:                                         -- R1-08: typed outcome discriminant — the one field every door switches on
    "admitted" | "already-admitted" | "rejected"   --   in-/near-transaction doors: proceed / stop (no-op) / fix diagnostics & resubmit
    | "stale-base" | "retry-exhausted"             --   re-read head & resubmit / transient give-up
    | "budget-exhausted" | "busy",                 --   pre-BEGIN refusals: ADMISSION_BUDGET / ADMISSION_BUSY (§3, R1-07)
  admitted: bool,                                  -- retained, but == (outcome == "admitted"); no consumer string-sniffs a status
  patch_id?, admission_id?,                        -- patch_id once content is parsed; admission_id iff admitted
  refusal_id?,                                     -- R1-08: durable retrieval key iff outcome ∉ {admitted, already-admitted}; PK of gate_refusal (ADR-12 §5)
  retry_after?:                                    -- R1-08: schema'd (typed), never an ad-hoc key; set iff outcome ∈ {budget-exhausted, busy, retry-exhausted}
    { millis: uint32, cause: "budget-refill" | "admission-busy" | "serialization" },
  epoch, base_snapshot,
  hashes: {targetName → hash},                    -- computed even on reject: identity is free
  stages: [{stage, status: "pass"|"fail"|"skip", ms}],
  diagnostics: [{stage_or_verifier, code, severity, subject,
                 loc: {def_hash, span}, message, fix?: {kind, detail, suggested_patch?}}],
  delta: {                                        -- R1-04: machine-computed blast-radius delta
    capabilities: {requested: [...], granted: [...], added_vs_base: [...]},
    pii_surface:  {touched: [...], added_vs_base: [...]},   -- fields newly reaching a sink
    ddl_surface:  {statements: [...], additive: bool, added_vs_base: [...]}
  },
  seeders: [{source_kind, source_ref, scope, seeded_by | "unattributed"}]  -- R1-04: §1 content-seeder set (ADR-12 §6)
}
```

BUILD-C (increment C2 — delta + seeders + verdicts-as-rows realized). The `delta` is
computed on every run (green, red, and no-op): `capabilities` from V1 (requested/granted,
and `added_vs_base` = named caps of new-or-changed defs minus the base head def's named
caps), `pii_surface` from V2 (values reaching a sink, masked or not; `added_vs_base` when
the owning def changed), `ddl_surface` from the V6 plan (additive statements). A no-op adds
nothing. The content-seeder set is bound at step 2a from `Patch.ReadLog` and validated
against the principal's scope chain (out-of-chain ⇒ `SEEDER_OUT_OF_CHAIN`, rejected before
any row); an external/no-principal source is recorded `unattributed`; a non-MCP (human/CLI)
submission carries the empty set. On commit the full Verdict is written to
`admission.verifier_report` (verdicts-as-rows) and `delta`/`seeders` to their own columns.
Scope-C RESIDUE: `pii_surface.added_vs_base` is diffed at def granularity (a changed def
re-adds its sinks), not against the base's own per-field pii sinks — Stage-D refines it.

**Blast-radius delta — capability/PII/DDL delta attached to every Verdict (R1-04).** The
pipeline attaches a machine-computed `delta` to the Verdict on **every** run, green or
red: `capabilities` from V1 capability-audit (requested / granted, and added-vs-base),
`pii_surface` from V2 pii-flow (fields newly reaching a boundary sink vs. the base
snapshot), and `ddl_surface` from V6 derivation-parity (emitted statements, additive flag,
added-vs-base). It is a pure projection of what V1/V2/V6 already computed over the same
frozen snapshot — no new analysis — so a **green** Verdict now carries its blast-radius
*change*, not merely its pass. ADR-12 §7 renders this delta beside every green Verdict in
the approval queue and refuses the approve action on a surface-widening product patch
unless the delta is shown; the delta and the §1 content-seeder set are written to the
admission row on commit. Leak discipline (below) applies to `delta` and `seeders`
unchanged: they name only capabilities, fields, statements, and seeders resolvable in the
submitter's own scope chain.

**Typed outcome discriminant + refusal retrievability (R1-08).** `outcome` is the one
field a consumer switches on — the four in-/near-transaction doors (`admitted`,
`already-admitted`, `stale-base`, `retry-exhausted`) plus the verifier-`rejected` door and
the two pre-`BEGIN` refusal doors (`budget-exhausted` = `ADMISSION_BUDGET`, ADR-12 §5;
`busy` = `ADMISSION_BUSY`, §3, R1-07). No renderer (MCP, PR check, git, operator plane)
reads a status string outside this enum, so adding a door later is an enum extension the
compiler flags at every switch, never a silent four-surface break. `retry_after` is a typed
sub-object (`millis` + `cause`), not an out-of-schema field, and is set exactly on the three
retryable doors. **Refusal retrievability:** every non-green outcome — including the
pre-`BEGIN` `budget-exhausted`/`busy` refusals that open no transaction, and the
verifier-`rejected` patch whose transaction rolled back — mints a durable `refusal_id`, the
primary key of the `gate_refusal` ledger (ADR-12 §5; the table is DDL'd in ADR-03 §1
table (6) — R1-INT: DDL now authored there). The Verdict carries that id, so a refused caller always holds a key to fetch later.
Retrieval contract (surfaced through the agent plane, ADR-12 §2): `verdict.get {id}` and
`catalog://verdict/{id}` resolve `id` against `admission.verifier_report` when it is an
admitted patch's `patch_id` and against `gate_refusal` when it is a `refusal_id`. The id is
minted *before* the refusal is returned, so a pre-`BEGIN` budget or busy refusal is as
retrievable as an admitted verdict — never a refusal a client cannot address after the fact.

Rendered for the operator plane and PR annotations; identical JSON over MCP. Every
diagnostic carries a concrete `fix` when derivable (fix-in-the-error).
**Leak discipline (grafted from the red-path proposal):** diagnostics cite only names
and hashes resolvable in the submitter's own scope chain. capability-audit names the
ungranted capability (the submitter wrote it — no new information) but never
enumerates existing grants or their holders; pii-flow names field and sink, never a
value; a probe for another org's overlay returns the same resolution error as a
hallucinated name — and, per ADR-12 §3's timing-indistinguishable resolution (R1-09: leak discipline extends to latency, not just bytes), the
*same latency distribution*, not merely the same bytes: cross-tenant existence is never
confirmed or denied through the response or through the clock.

## Alternatives Considered

- **simplest-thing: 5 verifiers with capture folded into the subset gate; one global
  advisory lock; whole-graph typecheck.** Rejected: capture needs typed live-variable
  dataflow, which the grammar gate does not have and ADR-05 §3 already assigns to a
  first-class verifier; the advisory lock duplicates SERIALIZABLE; whole-graph
  re-checking forfeits the memoization content-addressing pays for. Adopted from it:
  the roster-minimality discipline (relocation table in Context), the no-op
  short-circuit, and the merged Verdict shape.
- **prior-art-faithful: 7 verifiers including import-closure.** Its module-host
  layers, affected-set + memoization, derive-before-verify ordering, mutations-last
  rationale, contract-purity rule, coverage monotonicity, and SERIALIZABLE-with-retry
  are all adopted. Rejected piece: a separate import-closure verifier — resolution is
  already total at ADR-01's resolver and ADR-02's Ref substitution; a second
  implementation of the same judgment is drift surface, not defense in depth.
- **red-path-first (winner): 8 verifiers including printer-idempotence and
  import-closure; per-scope advisory locks with a SERIALIZABLE backstop.** Its
  kill-test method, hermeticity equation, deterministic typecheck budget, scope-bind
  rule, leak discipline, dual mutation testing, and coverage rows carry this ADR.
  Corrections: printer-idempotence is ADR-02 §5 guarantee 4 (runs unconditionally at
  step 3, not as a suite member); import-closure relocated as above; advisory locks
  cut (ADR-03's model wins); its derivation-after-verification ordering is inverted so
  V3/V6 can see the derived model.

## Consequences

- The security boundary is six verifiers, each small, pure, and independently
  mutation-tested; everything relocatable to grammar, printer, or resolver has been
  relocated, so the suite carries only what needs the typed AST and the catalog.
- The gate's verdicts are deterministic and hermetic; an agent's retry loop converges
  because identical input yields identical, machine-actionable output.
- Admission throughput is bounded by SERIALIZABLE Postgres and the affected-set
  typecheck — deliberately: the gate is the database (ADR-03). tsgo-ms-held-in-transaction
  is a measured, budgeted quantity under a concurrent-admission benchmark (§3), and
  overflow is shed as `ADMISSION_BUSY` backpressure rather than a stretched conflict
  window (R1-07: tsgo-in-txn budgeted under concurrency).
- The harness is standing infrastructure: corpus, dual mutation runs, fuzz gates, and
  the ADR-04 §6 regel-native differential oracle run per release; `verifier_coverage`
  rows make coverage regressions diffable. A release cannot go green while the oracle is
  red or while any seeded wrong-evaluation mutant survives (R1-02: oracle in the per-release gate).
- The name→path function is shared with ADR-09, so typechecking and git projection
  can never disagree about where a definition lives.

## Red-Path Tests Implied

- **Ungranted capability** (V1): type-correct `mail.send` under `crm.read` ⇒
  `CAP_UNGRANTED`; the definition never becomes code.
- **PII exfil + PII literal** (V2): unmasked vault field in a response ⇒ `PII_ESCAPE`;
  a vault-typed literal ⇒ `PII_LITERAL`, never immortalized.
- **Declared-but-unenforced** (V3): unconsulted policy ⇒ `PARITY_UNWIRED`.
- **Effectful contract** (V4): postcondition calling a capability ⇒
  `CONTRACT_EFFECTFUL`.
- **Capture bomb** (V5): live unserializable binding across `await` ⇒
  `CAPTURE_UNSERIALIZABLE` (ADR-05 test 4a).
- **Partial derivation / destructive DDL** (V6): unmaskable semantic type on a PII
  field ⇒ `DERIVE_PARTIAL`; `DROP COLUMN` without retire intent ⇒ `DDL_DESTRUCTIVE`.
- **Hermeticity:** the same (patch, snapshot, epoch) submitted twice yields
  byte-identical diagnostics; the host build fails if any ambient read exists.
- **Typecheck DoS:** a 200-deep conditional-type bomb ⇒ deterministic
  `TYPECHECK_BUDGET` naming the site; serving latency unaffected during the attack.
- **Parse-depth bomb** (§3, R1-09: refused pre-budget, retrievable id): a 10⁵-deep nested
  submission ⇒ the parser rejects at `MAX_PARSE_DEPTH` with a deterministic `PARSE_DEPTH`
  diagnostic *before* the type-graph ceiling or the wall-clock backstop engages, the kernel
  process stays live throughout (liveness asserted during the attack), and the refusal
  carries a durable `refusal_id` fetchable by `verdict.get` — a nesting bomb is a
  retrievable refusal, never a crashed kernel.
- **tsgo-in-txn under concurrency** (R1-07: measured, backpressure-not-stretch): the
  N = 32 concurrent-admission benchmark holds tsgo-in-txn p95 ≤ 40 ms / p99 ≤ 80 ms and
  serialization-retry ≤ 5 %; an admission over the concurrency bound is refused pre-`BEGIN`
  with `ADMISSION_BUSY` + `retry-after` into `gate_refusal`, never a silently stretched
  conflict window; a budget regression is red at the M1 gate.
- **Racing admissions:** two agents move one name concurrently ⇒ exactly one commits;
  the loser gets `stale-base`/retry, never a two-headed catalog (ADR-03's test, with
  this ADR's verdict codes).
- **Info-leak probe:** a rejected submission referencing another org's overlay name
  receives the identical diagnostic as a hallucinated name, and (R1-09: timing-indistinguishable, per ADR-12 §3) is
  timing-indistinguishable from it per the ADR-12 §3 statistical test — the resolution
  clock leaks no existence signal the bytes already withhold.
- **Typed outcome + refusal retrievability** (R1-08): a consumer switches on `Verdict.outcome`
  across all seven doors with no string match on `message`/`code`; every non-`admitted`
  outcome carries a typed `retry_after` iff retryable and a `refusal_id` iff non-green; a
  pre-`BEGIN` `budget-exhausted` refusal and a pre-`BEGIN` `busy` refusal (each opening no
  transaction) are fetched back by `verdict.get {refusal_id}` and yield the same Verdict —
  proving the id was minted before the refusal returned; a renderer that omits a door fails
  to compile (the enum is exhaustive), so a post-ship door is not a silent four-surface break.
- **Harness self-test** (R1-10: a relocated-ban mutant must fail the release): a seeded
  verifier mutant (dropped pii sink) fails the release; a seeded code mutant (unmasked read)
  is rejected by V2; and a seeded **grammar-gate/resolver mutant that weakens a relocated
  ban** — a widened banned-syntax matcher, a loosened floating-promise/acyclicity check, a
  weakened R1–R5 capture predicate, or an admitted out-of-world import — is **killed by the
  hostile corpus (a survivor blocks the release)**, so a silently-weakened ban cannot ship
  where the bans actually live (§ Context relocation table).
- **Blast-radius delta present + honest** (R1-04): a patch that adds an egress capability
  and a newly-sunk PII field ⇒ the Verdict `delta` names both under `added_vs_base`, on
  green as well as red; a no-op re-admission ⇒ empty `added_vs_base`; ADR-12 §7 refuses
  approval of the widening patch unless the delta is rendered.
- **Content-seeder attribution** (R1-04): an agent-authored admission whose read-log
  includes a seeded in-scope row ⇒ the admission row and Verdict `seeders` name the source
  and seeding principal; a seeder outside the submitter's scope chain is rejected
  (unrepresentable, step 2a rule); an external-effect seeder is recorded `unattributed`,
  never dropped; a human/PR submission carries an empty seeder set.
- **Regel-native oracle self-test** (R1-02: seeded wrong-evaluation turns the corpus
  red): a seeded contract-enforcement mutant, a seeded derived-boundary-validator mutant,
  and a seeded effect-class-ordering mutant each drive a divergence between the machine
  and the independent reference reducer (ADR-04 §6 harness 3) and fail the release; a
  surviving mutant in any of the three layers is a release blocker, so no green pipeline
  can ship an unwitnessed wrong evaluation into the immortal store.

## Constraints Discharged or Budgeted

1. **Budgeted.** V5 seats ADR-05's capture verifier in the suite; the deepest bet's
   failure mode is moved to admission time and exercised by the hostile corpus.
2. **Budgeted.** The heavy phase holds no locks and is memoized by content hash; the
   deterministic budget keeps a hostile patch from touching serving latency.
   tsgo-ms-in-transaction is additionally budgeted and measured under concurrent admission
   (§3), shed as `ADMISSION_BUSY` backpressure when exceeded (R1-07: tsgo-in-txn budget).
3. **Not implicated** beyond verdict diagnostics sharing the fix-as-data shape that
   restarts use (ADR-05 §6).
4. **Budgeted.** Derivation is explicit ordered AST passes (step 5a), checked total by
   V6 — the derivation layer's obligations are verified, not assumed.
5. **Discharged — this ADR is constraint #5.** Six verifiers with stated semantics,
   hermetic execution, dual mutation testing, a hostile corpus, and monotone
   epoch-versioned coverage rows: the boundary is stated as data and attacked
   continuously.
6. **Budgeted.** The pipeline is the walking skeleton's spine (admit → row); the
   corpus and harness precede feature work — red-path-first staging.
