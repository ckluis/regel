# JOE CELKO — SQL & Data Modeling

## VERDICT: FAIL

One P0 red flag: the as-of temporal invariant (I4) rests on a Postgres constraint the
DDL cannot actually create. The rest of the schema is unusually disciplined, but three
tables let a row mean more than one thing.

## FINDINGS

1. [P0] **The history exclusion constraint cannot coexist with the partitioning; I4 is
   unenforced.** The table declares an overlap exclusion over `tstzrange(valid_from,
   valid_to)` yet is `PARTITION BY RANGE (valid_from)`; Postgres refuses an exclusion
   constraint whose partition-key column is compared by `&&` rather than `=`, so the
   DDL either fails or degrades to per-partition scope — a January row and a NULL-ended
   row in a later partition both commit. As-of resolution (§3, no `LIMIT`) then returns
   two hashes for one instant: the wrong code silently evaluates. CITE: "PARTITION BY
   RANGE (valid_from)" (ADR-03, §1) contra "the GiST exclusion guarantees exactly one
   hash per (name, scope) at any instant" (ADR-03, §3).

2. [P2] **Load-bearing discriminators live in unconstrained jsonb, contradicting the
   stated stance.** `wake`, `durable_condition.class`, and `task.payload` are the
   fields that drive wake dispatch and restart rendering, yet none carries a CHECK
   enumerating its variants — the very "application discipline" the ADR forswears.
   A row of `wake='{}'` or an unknown `class` is a valid row that means nothing.
   CITE: "unrepresentable by constraints" (ADR-03, Context) vs "wake          jsonb NOT
   NULL" (ADR-05, §2).

3. [P2] **`durable_condition` has no referential or state integrity on resolution.**
   `resolved_restart` is a bare uuid with no FK to `restart(id)`, and no CHECK ties
   `status='resolved'` to the four `resolved_*` columns being set (or `'open'` to their
   being NULL). A row can claim resolved by a nonexistent restart, or open while
   `resolved_by` is populated — the row does not mean exactly one thing. CITE:
   "resolved_restart uuid, resolved_args jsonb, resolved_by text, resolved_at
   timestamptz" (ADR-05, §6).

4. [P2] **The audit join the ADR advertises is not guaranteed.** `name_pointer_history
   .admission_id` is declared `bigint NOT NULL` with no `REFERENCES` — unlike the live
   `name_pointer.admission_id`, which has the FK — and `admission.submitted_hashes` is
   an unconstrained `text[]`. The "name_pointer_history JOIN admission" query can join
   to a missing ledger id. CITE: "admission_id bigint NOT NULL," (ADR-03, §1, table 4).

5. [P2] **The single resolver cannot enforce `private` visibility.** §3 asserts "no
   ad-hoc lookups exist anywhere in the kernel," but the one resolver selects on
   `(name, scope_kind, scope_id)` with no `visibility` predicate and no caller-module
   parameter — so a `private` pointer meant to be "resolvable only" in-module is either
   returned cross-module or requires a second, denied path. CITE: "resolvable only"
   (ADR-03, §2).

## RECOMMENDATIONS

- Do not partition `name_pointer_history`, or partition by `(name, scope_kind, scope_id)`
  hash so the exclusion's equality columns cover the partition key. Verify: run the exact
  `CREATE TABLE` against a real Postgres in CI and assert it succeeds; then insert two
  overlapping windows for one (name,scope) and assert the second is rejected.
- Add CHECK-validated shape to `wake`/`class`/`payload` (a `kind`/`class` column with an
  enumerating CHECK, or a jsonb `CHECK (wake ? 'kind' AND wake->>'kind' IN (...))`).
  Verify: attempt to insert `wake='{}'` and an unknown class; both must RAISE.
- Add `resolved_restart REFERENCES restart(id)` and a CHECK making
  `status='resolved' ⇔ resolved_restart IS NOT NULL`. Verify: red-path test resolving a
  condition with a foreign restart id and with status/columns disagreeing — both rejected.
- Add `name_pointer_history.admission_id REFERENCES admission(id)`. Verify: trigger a
  history write whose admission_id is absent; assert the transaction aborts.
- Give the resolver an explicit `visibility='exported'` predicate for external callers and
  a documented in-module path for private. Verify: resolve a private name from another
  module and assert zero rows.

## RED FLAG

CATEGORY: DATA INTEGRITY
CITE: "PARTITION BY RANGE (valid_from)" (ADR-03, §1) against "the GiST exclusion
guarantees exactly one hash per (name, scope) at any instant" (ADR-03, §3).
CONSEQUENCE: The temporal soundness invariant I4 ("as-of is sound — one hash per
instant") is claimed proven by a constraint Postgres will not create on a partitioned
table with a range-overlap key. Unenforced, two overlapping history windows for one
(name, scope) commit, and the unlimited as-of query returns two hashes — a rollback or
audit resolves to the wrong immortal definition, and a resuming continuation can bind
the wrong code. This must be proven by executing the DDL and an overlap-rejection test
before any table is trusted.
