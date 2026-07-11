# ADR-03: Catalog and definition-row schema

## Status

Accepted — Phase 1

## Context

BRIEF §4 requires the physical schema for "the code is rows": definition rows, name
pointers, scopes/overlays with a resolution order, history and as-of, integrity
constraints, and the one-transaction code+schema migration with its crash behavior.

Cross-ADR dependencies, stated explicitly:
- The primary key of `definition` is the ADR-02 address (`r<n>_` + base32 SHA-256 of
  the canonical AST bytes). The catalog has no other notion of code identity.
- ADR-01's admission pipeline runs entirely inside the transaction defined in §5 here;
  its capture discipline is why a serialized environment referencing these rows resumes
  cleanly years later — provided the rows are immortal, which §4 makes structural.
- Design stance (from the winning proposal): corruption states are made
  **unrepresentable by constraints** — FKs, composite PKs, a GiST exclusion,
  trigger-maintained history, and revoked privileges — not prevented by application
  discipline.

## Decision

### 1. Physical DDL

```sql
-- (1) Immortal content store. INSERT-only: UPDATE/DELETE privileges revoked from every
-- role, including the kernel's. Unpartitioned: content-addressed data has no time
-- dimension and dedup keeps it small (grafted from the simplest-thing proposal).
CREATE TABLE definition (
  hash            text PRIMARY KEY,                -- 'r<n>_<base32>' (ADR-02 §4)
  ast_schema_ver  smallint NOT NULL,               -- the <n> in the address
  kind            text NOT NULL CHECK (kind IN
                    ('resource','function','component','view','policy',
                     'workflow','prompt','translation','type')),
                    -- R1-12: 'module' removed from the kind set. A module is not a
                    -- definition: modules decompose into one row per top-level declaration
                    -- (§2, ADR-01 §2) and imports are stripped at lowering, so no row ever
                    -- holds — or hashes — a whole module. A 'module' kind was undefined and
                    -- uncreatable; the walking-skeleton admission emits only the kinds above.
  ast             bytea NOT NULL,                  -- canonEncode bytes (the hashed input)
  canonical_text  text  NOT NULL,                  -- printed projection: git, tsgo, display
  contracts       jsonb NOT NULL DEFAULT '[]',     -- mirror of in-hash contract nodes, for verifier queries
  deps            text[] NOT NULL DEFAULT '{}',    -- referenced addresses (Merkle edges)
  supersedes      text REFERENCES definition(hash),-- cross-epoch re-admission link
  admission_id    bigint NOT NULL REFERENCES admission(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT addr_shape CHECK (hash ~ '^r[0-9]+_[0-9a-z]+$')
);
-- hash == SHA-256(domain ‖ ast) is enforced by the KERNEL at insert (ADR-02 §5.4) and
-- re-verified by a periodic scrubber that alarms on mismatch. Honest edge: content
-- integrity is kernel-enforced + audit-scrubbed, not provable by a Postgres CHECK.
-- R1-03: scrubber detection has a self-certifying repair path (§4a) that NEVER grants
-- UPDATE. UPDATE/DELETE stay revoked from every role, including the kernel's, forever;
-- a scrubber-detected mismatch is repaired by out-of-band break-glass byte-restore whose
-- correctness is proven by rehashing to this row's own PK (ADR-02 §5.5), not by any grant.

-- (2) Metadata that is deliberately OUT of the hash (ADR-02 §3).
CREATE TABLE definition_meta (
  hash      text PRIMARY KEY REFERENCES definition(hash),
  docstring text,
  comments  jsonb NOT NULL DEFAULT '{}'            -- node-path-keyed sidecar
);

-- (3) Mutable scoped name pointer — the live catalog. The ONLY mutable code table.
CREATE TABLE name_pointer (
  name         text NOT NULL,                      -- 'app/crm/Deal'
  scope_kind   smallint NOT NULL CHECK (scope_kind BETWEEN 0 AND 4),
                                                   -- 0=product 1=package 2=org 3=team 4=user
  scope_id     text NOT NULL DEFAULT '',           -- '' at product scope
  kind         text NOT NULL,
  visibility   text NOT NULL DEFAULT 'exported' CHECK (visibility IN ('exported','private')),
                                                   -- 'private' = module-internal top-level decl
  hash         text NOT NULL REFERENCES definition(hash),
  overrides    text REFERENCES definition(hash),   -- base hash this overlay shadowed at admission
  admission_id bigint NOT NULL REFERENCES admission(id),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (name, scope_kind, scope_id)         -- at most one live winner per exact scope
);

-- (4) Append-only temporal history, written by trigger (never by application code).
-- R1-01: unpartitioned so the I4 temporal exclusion is actually creatable.
-- Requires btree_gist for the `=` gist operator class on name/scope_kind/scope_id.
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE TABLE name_pointer_history (
  name       text NOT NULL, scope_kind smallint NOT NULL, scope_id text NOT NULL,
  hash       text NOT NULL REFERENCES definition(hash),
  valid_from timestamptz NOT NULL,
  valid_to   timestamptz,                          -- NULL = current
  admission_id bigint NOT NULL REFERENCES admission(id),  -- R1-12: FK added — history was a
  -- bare bigint (unlike live name_pointer.admission_id, which has the FK), so the advertised
  -- "name_pointer_history JOIN admission" audit query (Consequences §2) could join to a
  -- missing ledger id. The FK makes that join total; a history write with an absent
  -- admission_id now aborts the transaction.
  EXCLUDE USING gist (name WITH =, scope_kind WITH =, scope_id WITH =,
                      tstzrange(valid_from, valid_to) WITH &&)
);  -- NOT partitioned: see rationale note below.
-- The exclusion's own GiST index (name, scope_kind, scope_id, tstzrange) also serves
-- as-of resolution; a supporting btree on (name, scope_kind, scope_id, valid_from DESC)
-- may be added for the point-in-time resolver if profiling warrants.
--
-- WHY UNPARTITIONED (was `PARTITION BY RANGE (valid_from)` — the P0-1 defect):
--   Postgres refuses ANY exclusion constraint on a partitioned table (verified on PG16:
--   ERROR "exclusion constraints are not supported on partitioned tables"), and even on
--   PG17+ — which allows table-level exclusions — a `tstzrange(valid_from,valid_to) &&`
--   over the partition-key column `valid_from` is illegal because partition-key columns
--   must be compared with `=`. Either way I4 was unenforceable as originally written:
--   overlapping windows would commit and as-of (§3, no LIMIT) would return two hashes.
--   HASH(name,scope_kind,scope_id) partitioning was also rejected: it is uncreatable on
--   PG16 (regel's target — same categorical error), and the table's own retention note
--   said partitions are "never dropped", so time-range partitioning bought NO drop/aging
--   benefit to begin with. History is off the hot path (Consequences §2) and as-of is a
--   per-(name,scope) point lookup the exclusion's index already serves, so a single
--   unpartitioned table costs nothing the design was relying on. If unbounded growth
--   ever forces sharding, revisit on PG17+ with HASH(name,scope_kind,scope_id) — whose
--   equality columns cover the partition key and keep the exclusion creatable.

-- (5) Admission ledger — one row per gate pass (engineer, tenant, agent: one gate).
CREATE TABLE admission (
  id               bigserial PRIMARY KEY,
  actor_kind       text NOT NULL CHECK (actor_kind IN ('engineer','tenant','agent','system')),
  actor_id         text NOT NULL,
  via              text NOT NULL CHECK (via IN ('cli','settings','mcp','git')),
  submitted_hashes text[] NOT NULL,
  verifier_report  jsonb NOT NULL,                 -- tsgo + catalog-parity + capability + pii + contracts verdicts
  tsgo_ms          int,
  migration_sql    text,                           -- derived DDL applied this tx, or NULL
  -- R1-INT: content-seeder + delta columns the R1-04 revision flagged here (ADR-07 §1/§6,
  -- ADR-12 §6). seeders = the third-principal provenance set, scope-chain-validated at
  -- step 2a, '[]' for human/PR submissions; verdict_delta = the machine-computed
  -- capability/PII/DDL blast-radius delta persisted with the row on commit.
  seeders          jsonb NOT NULL DEFAULT '[]',    -- [{source_kind, source_ref, scope, seeded_by|"unattributed"}]
  verdict_delta    jsonb,                          -- {capabilities, pii_surface, ddl_surface} (ADR-07 §6)
  created_at       timestamptz NOT NULL DEFAULT now()
);

-- (6) Gate and coverage ledgers. R1-INT: DDL the R1-04/07/08/10 revisions flagged to this
-- ADR ("DDL'd in ADR-03, flagged there") — authored here so every pointer resolves.
-- gate_refusal is written OUTSIDE the admission transaction (after rollback, or directly
-- for pre-BEGIN budget/busy refusals — ADR-12 §5); a rejected admission still leaves no
-- admission row (§5 rule stands).
CREATE TABLE gate_refusal (
  refusal_id       uuid PRIMARY KEY,               -- minted BEFORE the refusal returns (R1-08)
  principal        text NOT NULL,
  scope_attempted  text,
  submitted_hashes text[],                         -- NULL when a budget/busy refusal precedes parse (R1-08)
  outcome          text NOT NULL CHECK (outcome IN
                     ('rejected','stale-base','retry-exhausted','budget-exhausted','busy')),
                                                   -- the non-green ADR-07 §6 Verdict outcomes
  verdict          jsonb NOT NULL,                 -- the full Verdict served by verdict.get {refusal_id}
  created_at       timestamptz NOT NULL DEFAULT now()
);
-- Coverage/budget rows, versioned with the epoch, all monotone-gated by their owning ADRs:
CREATE TABLE verifier_coverage (                   -- ADR-07 §5 (component generalizes verifier, R1-10)
  epoch int NOT NULL, component text NOT NULL,     -- V1..V6 | 'grammar-gate' | 'resolver'
  threat_class_ids text[] NOT NULL, corpus_case_count int NOT NULL,
  mutation_score numeric NOT NULL,
  PRIMARY KEY (epoch, component)
);
CREATE TABLE perf_budget (                         -- ADR-04 §8 (R1-07): budgets are data, like coverage
  epoch int NOT NULL, metric text NOT NULL, tier text,
  budget numeric NOT NULL, measured numeric, milestone text NOT NULL,
  PRIMARY KEY (epoch, metric)
);
CREATE TABLE continuation_coverage (               -- ADR-05 §8.5 (R1-10): decode coverage, monotone floor
  epoch int NOT NULL, frame_kind text NOT NULL, cfr_version int NOT NULL,
  decoder text NOT NULL, covered bool NOT NULL,
  PRIMARY KEY (epoch, frame_kind, cfr_version, decoder)
);
```

### 2. Definition granularity and names

Each top-level declaration of a submitted module is one `definition` row (ADR-01 §1).
<!-- R1-12: definition rows are scope-free by construction -->
A `definition` row carries no scope: scope lives only on `name_pointer` (the I3 PK
`(name, scope_kind, scope_id)`). This is structural, not stylistic — a definition is
content-addressed, so putting scope inside it would make identical overlay bytes at two
orgs hash differently and break the step-3 `ON CONFLICT (hash)` dedup. **Scope is a
property of the pointer, never of the code.** Exported declarations get
`visibility='exported'` pointers; non-exported top-level helpers get `visibility='private'`
pointers under the same module path (resolvable only from within the module's own
definitions — enforced by the §3 resolver's visibility predicate, not by convention). A
rename is an UPDATE of `name_pointer` (trigger writes history); the hash never moves.

### 3. Scopes, overlays, resolution order

A request carries its principal chain `(user_id, team_id, org_id, package_id)`. **One**
resolver — no ad-hoc lookups exist anywhere in the kernel — walks most-specific-first:

```sql
SELECT hash FROM name_pointer
WHERE name = :name
  AND (scope_kind, scope_id) IN (VALUES
       (4, :user_id), (3, :team_id), (2, :org_id), (1, :package_id), (0, ''))
  -- R1-12: visibility predicate — the one resolver enforces 'private', not a second path.
  AND (visibility = 'exported'
       OR (visibility = 'private' AND module_of(name) = :caller_module))
ORDER BY scope_kind DESC
LIMIT 1;                                           -- user > team > org > package > product
```

First hit wins (total shadow). As-of resolution is the identical query against
`name_pointer_history` with `valid_from <= :t AND (valid_to IS NULL OR valid_to > :t)`;
the GiST exclusion guarantees exactly one hash per (name, scope) at any instant.

**Visibility (R1-12: `private` is resolved, not just documented).** §2 mints
`visibility='private'` pointers for non-exported top-level helpers, "resolvable only from
within the module." Without a predicate the single resolver — which by design is the
*only* lookup path (no ad-hoc lookups exist anywhere in the kernel) — would return those
pointers cross-module, or a private call would need a second, denied path that the
"one resolver" stance forbids. So the resolver carries the visibility leg above:

- `module_of(name)` is the module path of a `name` — every segment but the final
  declaration segment (e.g. `module_of('app/crm/deal/roundUp') = 'app/crm/deal'`); it is a
  pure function of the name, no extra column needed.
- `:caller_module` is the module of the definition that issued the resolution — derived
  from the C register's own `def_hash` (ADR-04), i.e. the module currently evaluating.
  External entry points (an inbound HTTP request, an operator query, an agent tool call)
  have no calling module, so `:caller_module` is `''`/NULL and only `exported` pointers
  match — `private` is unreachable from outside code by construction.
- Because the predicate lives inside the one resolver's `WHERE`, a `private` name the
  caller cannot see resolves to **zero rows on the same path** as a name that does not
  exist — coherent with ADR-12 §3's R1-09 timing-indistinguishability rule (visibility is
  evaluated before any row is fetched/decoded, so private-membership is not a latency
  oracle any more than existence is).

**Overlays.** An overlay row records in `overrides` the exact base hash it shadowed at
admission (audit + staleness detection); resolution itself is by name + scope alone.
When a base pointer moves, the same transaction re-runs the verifier suite over every
overlay of that name in every scope; a failing overlay rolls the base change back, and
the rejection report names each conflicting overlay. Upgrades re-verify every overlay —
streng's rule, enforced in the gate, not in a release checklist.

### 4. Immortality and integrity invariants

- **I1** FK `name_pointer.hash → definition.hash`: no dangling name.
- **I2** trigger validates every element of `deps` against `definition(hash)` within
  the transaction (dependencies inserted earlier in the same admission count): no
  dangling dependency edge can commit; ADR-01's acyclicity check ran at the gate.
- **I3** PK `(name, scope_kind, scope_id)`: at most one live winner per exact scope.
- **I4** GiST exclusion on the (unpartitioned) history table: as-of is sound — one hash
  per instant. R1-01: enforceability restored — the range-overlap exclusion is creatable
  only because the table is NOT partitioned (Postgres rejects such a constraint on any
  partitioned table); proven, not asserted, by the CI overlap-rejection kill-test in
  "CI Verification Gates" below, which fails the build if the constraint is absent,
  disabled, or uncreatable.
- **I5** kernel re-hash at insert + periodic scrubber + `addr_shape` CHECK: stored
  bytes match the address (honest edge stated in §1).
- **I6** `definition` is INSERT-only with privileges revoked: hashes never mutate or
  vanish; a paused workflow stores exact hashes, not names, and resumes against
  immortal rows regardless of any later pointer move.
- **I7** history written by a `BEFORE INSERT OR UPDATE` trigger on `name_pointer`
  (closes the prior window, opens the new one): code cannot forget to write history.
- **I8** SERIALIZABLE admission + optimistic CAS (§5): no lost updates.
- **I9** <!-- R1-03: recovery invariant — repair preserves I6, adds no UPDATE role -->
  immortal-store corruption detected by I5's scrubber has a *self-certifying byte-restore*
  recovery path (§4a) that repairs the affected row without ever granting UPDATE/DELETE to
  any role: I6's revoked-privilege posture is permanent and the restore is verified by
  rehash to the address (ADR-02 §5.5), so detection never dead-ends and no mutation door
  is opened. Enforced by the release-gated recovery drill in CI Verification Gates.

### 4a. Immortal-store recovery: scrubber-trip runbook

<!-- R1-03: authored scrubber-trip runbook — detection → quarantine → restore → verify → resume -->
The I5 scrubber and the ADR-02 world-rehash canary *detect* corruption of the sole code
identity; this runbook is the authored operational procedure for the minute after the
alarm. It repairs via ADR-02 §5.5 self-certifying byte-restore and **grants no role
UPDATE at any step** — the mutation matrix of §1 (UPDATE/DELETE revoked from every role,
including the kernel's) is never relaxed to recover.

1. **Detect.** Scrubber pass or nightly canary reports a row whose stored `ast` does not
   rehash to its `hash` PK (or whose address is otherwise corrupt). The finding names the
   exact address and emits to the out-of-band health surface (not only into the Postgres
   being diagnosed).
2. **Quarantine + alert.** Page the on-call operator; mark the address quarantined so the
   resolver/reactor treat continuations and dependents that bind it as *held* (fail-closed,
   not silently serving corrupt bytes). Blast is total for that identity — "Nothing
   survives a moved hash" — so containment precedes repair.
3. **Restore (out-of-band, no new privilege).** Obtain candidate `ast` bytes from, in
   order of preference: (a) rederivation from this row's own `canonical_text` via
   `hash(normalize(lower(parse(canonical_text))))`; (b) the git projection; (c) a physical
   backup/replica. The write is an **audited break-glass superuser** physical repair — the
   Postgres-layer access that exists regardless of any role grant — never an application
   role and never a standing repair role. No `GRANT UPDATE` is issued; no repair role is
   created; the change is logged to the incident record with operator identity.
4. **Verify (fails closed).** Accept the restored bytes **only if**
   `SHA-256(domain ‖ candidate_ast) == hash` AND
   `hash(normalize(lower(parse(canonical_text)))) == hash`. If no source rehashes to the
   address, the restore **refuses** — the row stays quarantined and the incident escalates;
   the operator never hand-edits bytes toward a passing hash. Wrong bytes cannot verify
   (ADR-02 §5.5), so this leg is what makes the break-glass write safe.
5. **Resume.** Run an on-demand scrubber pass over the repaired row; on clean, lift the
   quarantine so held continuations and dependents rebind. Record time-to-contained.

Note: for byte/address corruption the motion is **restore-to-hash only**. `supersedes`
re-admission is NOT a byte-repair (it mints a *new* address, cascades the Merkle closure,
and strands continuations on the old hash; and re-admitting identical bytes is a no-op
under step-3 `ON CONFLICT (hash) DO NOTHING`); it is reserved for genuine
*semantic*-corruption re-authoring, out of scope for this runbook.

### 5. One-transaction admission (code + schema together)

The kernel runs the entire gate in a single `SERIALIZABLE` transaction:

```
BEGIN ISOLATION LEVEL SERIALIZABLE;
  1. INSERT admission(...) RETURNING id;
  2. parse → lower → grammar gate → normalize → print → hash        (ADR-01 §4, ADR-02)
  3. INSERT INTO definition ... ON CONFLICT (hash) DO NOTHING;      -- dedup by content
     INSERT INTO definition_meta ... ON CONFLICT DO NOTHING;        -- existing row's meta wins
  4. tsgo typecheck of canonical text against this txn's catalog snapshot;
  5. verifier suite (catalog-parity, capability-audit, pii-flow, contracts)
     as queries on this connection — they see uncommitted rows; any failure ⇒ RAISE;
  6. apply derived migration_sql (Postgres DDL is transactional);
  7. UPDATE/INSERT name_pointer with CAS:
       ... ON CONFLICT (name, scope_kind, scope_id) DO UPDATE SET hash = :new
       WHERE name_pointer.hash = :base_hash_the_patch_saw;
     0 rows updated ⇒ a concurrent admission won ⇒ RAISE (client retries against the
     new head); the I7 trigger writes history;
  8. re-verify overlays of any moved base pointer (§3); failure ⇒ RAISE;
COMMIT;   -- deploy is this commit. Any RAISE ⇒ ROLLBACK ⇒ nothing happened:
          -- no definition row, no DDL, no pointer move, no audit row.
```

The "deploy window" where code and schema disagree has no representation: they commit
together or not at all.

**Crash behavior.** A kernel or Postgres crash mid-transaction is an ordinary aborted
transaction — Postgres crash recovery leaves no partial catalog state (steps 1–8 are
one atomic unit, DDL included). A crash after COMMIT but before the client hears the
verdict is durable; re-submission is idempotent: step 3 dedupes on the content hash and
step 7's CAS finds the pointer already at `:new` (reported as already-admitted).

### 6. std/ in v1 (decided; flagged for the world cluster)

std/ **evaluation is compiled into the kernel binary, and every std definition is
mirror-catalogued at bootstrap** as immortal product-scope rows (`scope_kind=0`) with
real ADR-02 hashes and real `deps`, whose evaluation dispatches to native Go. The
catalog is therefore complete: an app definition importing `std/taak` references a real
hash; I1/I2 hold with no synthetic-pointer carve-outs; as-of closes over std versions
across epochs. The substrate is not self-hosting in v1 — the interpreter does not
evaluate std from rows — and this dual representation is the named seam the world
cluster must keep coherent (kernel binary and mirror rows are generated from one
source, verified identical at boot).

## Alternatives Considered

- **simplest-thing:** four tables with live resolution served from the history
  partitions and integrity by application discipline. Rejected: history on the hot
  path, and no exclusion/FK machinery — its own red-path list is aspirational without
  constraints to enforce it. Adopted from it: the unpartitioned `definition` argument,
  the admission-ledger fields (`actor_kind`, `via`, `tsgo_ms`), and std synthetic
  pointers were rejected in favor of §6 mirror rows.
- **prior-art-faithful:** system-versioned `name` table (validity columns on the live
  table) with five nullable scope FKs and a sweep to history. Rejected: partitioning
  `definition` by `admitted_at` is incompatible with `code_hash` as sole PK (Postgres
  requires the partition key inside the PK), and the five-column scope encoding plus
  sweep job is more machinery than the resolution semantics need. Adopted from it: the
  `supersedes` cross-epoch link, and the PII-literal red-path below.
- **red-path-first (winner):** this schema is substantially its design — insert-only
  store, trigger history, GiST exclusion, CAS, invariants I1–I8, dual std
  representation. Corrections: `scope_id` is `text` (ids are not uniformly integral),
  `definition_meta` carries the docstring/comments split exactly as ADR-02 decided, and
  its overlay rule (`overrides` must name the shadowed base hash) is kept as recorded
  audit data while resolution stays purely name+scope, with base moves re-verifying
  overlays instead of hard-pinning them.

## Consequences

- Rollback is `AS OF`: point the resolver at `name_pointer_history` with `:t` — the
  previous app, forever. Deploy is one COMMIT.
- "Who changed this workflow" is `SELECT … FROM name_pointer_history JOIN admission` —
  the same query shape as business-row history (one machinery, both substances).
- The immortal store means a PII literal embedded in code would be undeletable: the
  pii-flow verifier runs **before** the insert becomes visible (step 5 precedes COMMIT)
  and rejects vault-typed literals in code — the named interaction between immortality
  and crypto-shred (grafted from the prior-art proposal).
- SERIALIZABLE admissions serialize concurrent same-name deploys; throughput of the
  gate is bounded by Postgres, which is the point — the gate is the database.
- The scrubber and boot-time std mirror check are standing operational duties, listed
  in the kernel's health surface.

## Red-Path Tests Implied

- Verifier RAISE at step 5 ⇒ assert: no `definition` row, no `definition_meta` row, no
  DDL applied (column absent), no pointer moved, no admission row.
- Kill the kernel between steps 6 and 7; assert Postgres rollback leaves zero trace;
  resubmit and assert idempotent success.
- Kill after COMMIT before response; resubmit; assert already-admitted verdict, no
  duplicate rows.
- As-of: admit v1 then v2; resolve at `:t` between ⇒ v1's hash. Admit an org overlay;
  resolve as-of before it ⇒ product hash.
- Overlay isolation: org A's overlay leaves org B's resolution byte-identical; user
  shadows team shadows org shadows package shadows product.
- Two concurrent admissions moving one name: exactly one wins; the loser's CAS updates
  0 rows and is rejected whole (its DDL rolled back with it).
- Dangling dep: submit a definition referencing an absent hash ⇒ I2 trigger rejects.
- Visibility: <!-- R1-12: private-visibility red path -->
  resolve a `visibility='private'` name with `:caller_module` set to a *different*
  module ⇒ zero rows (indistinguishable from a nonexistent name); resolve the same name
  with `:caller_module` equal to the name's own module ⇒ the private hash. An `exported`
  name resolves regardless of `:caller_module`.
- History audit FK: <!-- R1-12: history→admission FK red path -->
  force a `name_pointer_history` write whose `admission_id` names no `admission(id)` row
  ⇒ the transaction aborts on the FK (was silently accepted as a bare bigint).
- Tamper: flip one byte of `ast` via superuser ⇒ scrubber alarms on next pass.
- Recovery: <!-- R1-03: byte-restore red path — detection now has a verified exit -->
  after the tamper alarm, run the §4a runbook against a scratch/staging store ⇒ restore
  the row by rehash-verified byte-restore, assert scrubber-clean, and assert the restore
  **fails closed** when the candidate bytes do not hash to the address (no role gained
  UPDATE). This is the release-gated drill specified in CI Verification Gate 4 below.
- PII literal in submitted code ⇒ rejected at step 5, never immortalized.
- Rename: pointer UPDATE only — hash unchanged, old name resolvable as-of, history has
  both windows with no overlap (I4; the exclusion is exercised by the CI gate below).

## CI Verification Gates

**R1-01: I4 exclusion is executed and proven against a real Postgres, not asserted.**
These gates run in CI against a live Postgres of the same major version the kernel
deploys against (not a mock, not an in-memory shim). Any failure fails the build:

1. **DDL-creatable gate.** Apply the verbatim table-(4) DDL — the `CREATE EXTENSION
   btree_gist` and the `CREATE TABLE name_pointer_history ... EXCLUDE USING gist (...)`
   — against a fresh database. The build fails if `CREATE TABLE` errors. This is the
   guard that catches any regression reintroducing partitioning: Postgres answers
   `exclusion constraints are not supported on partitioned tables`, so a `PARTITION BY`
   sneaking back turns the build red here.
2. **Overlap-rejection kill-test (red path).** Against the real table, INSERT window
   `[t0, NULL)` for a `(name, scope_kind, scope_id)`, then INSERT a second window
   `[t1, NULL)` (t1 > t0, same name+scope) that overlaps it at every instant ≥ t1.
   Assert the second INSERT RAISES `exclusion_violation` (SQLSTATE `23P01`). This is a
   KILL-TEST: it must FAIL the build (not error-skip, not pass-by-absence) if the
   exclusion constraint is missing, dropped, disabled, or uncreatable — a green result
   requires the constraint to have actively rejected the overlap. (Verified locally on
   PG16.13: the overlapping INSERT is rejected with 23P01 against constraint
   `name_pointer_history_name_scope_kind_scope_id_tstzrange_excl`.)
3. **No-false-positive guard.** Assert that two *adjacent, non-overlapping* windows for
   one name+scope (e.g. `[t0,t1)` then `[t1,NULL)`) both COMMIT, and that one open
   window per *distinct* name+scope at the same instant COMMITs — proving the gate
   rejects only true overlaps and the exclusion is scoped per (name, scope), not global.

**R1-03: Immortal-store fault-injection recovery drill (release gate — red drill blocks
release).** This gate runs against a scratch/staging `definition` store of the same
Postgres major version the kernel deploys against. It proves the §4a runbook and ADR-02
§5.5 self-certifying byte-restore end-to-end. Any failure fails the build:

4. **Recovery kill-test.** Admit a definition; deliberately corrupt one immortal `ast`
   byte via superuser (the tamper motion). Assert, in order: (a) the scrubber/canary
   **detects** the mismatch and names the address; (b) the byte-restore runbook restores
   the correct bytes from `canonical_text`/git/backup and the row **rehashes to its
   address** (`SHA-256(domain ‖ ast) == hash`); (c) an on-demand scrubber pass reports
   **clean** and records time-to-contained; (d) **fail-closed leg** — feed the restore a
   candidate that does *not* hash to the address and assert the restore is **REJECTED**
   (row stays quarantined, no bytes written); (e) **no-UPDATE invariant** — assert that
   throughout, no database role holds UPDATE/DELETE on `definition` (grants unchanged from
   §1) and no repair role was created. A green result requires the restore to have
   actively repaired a corrupted row AND to have actively refused a non-matching
   candidate — pass-by-absence (drill skipped/erroring) fails the build.

   **This drill is a RELEASE GATE: a red drill blocks release.** Per the C2
   (Allspaw vs Schneier) compromise, the immortal-store recovery finding was withdrawn
   from P0 to P1 *only* on the condition that this recovery drill is a release-gate
   kill-test rather than an ops document. **If this gate is removed or downgraded from a
   release blocker, the finding reverts to P0.**

## Constraints Discharged or Budgeted

1. **Discharged (the storage half).** I6 immortality + exact-hash capture is what lets
   a continuation serialized years ago resume against precisely the code it started
   with; as-of for code deletes workflow versioning.
2. **Budgeted.** Resolution is one indexed lookup on the live table; history is off the
   hot path; the reactor caches over these tables.
3. **Budgeted (interface reserved).** Durable-condition and restart rows are
   continuation-cluster tables that reference `definition(hash)` and `admission(id)`
   defined here; nothing in this schema needs to change to receive them.
4. **Discharged.** The catalog is the storage format: being in `definition` +
   `name_pointer` is what "being code" means; every derivation reads these rows.
5. **Budgeted.** `admission.verifier_report` makes verifier coverage a stored,
   queryable, versioned fact per gate pass — the boundary is stated as data.
6. **Discharged for the skeleton.** Admit → row → evaluate → respond has its home in
   exactly these five tables plus the §5 transaction; the walking skeleton builds
   nothing else first.
