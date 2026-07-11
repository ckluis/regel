# ADR-08: Epochs and migration

## Status

Accepted — Phase 1

## Context

BRIEF §4 asks whether regel adopts streng's atomic epoch wholesale, what an epoch
physically is, the `migrate N` flow, the continuation compatibility contract, overlay
re-verification on upgrade, and the between-epochs freeze with its emergency path.
streng's chapter is the source: dialect, engine, stdlib, component library, workflow
engine, and gate move together as one artifact; the combination space has two points;
the gate re-checks the whole app and lists every incompatibility at once; an epoch is
an offer, not an eviction.

Cross-ADR dependencies, stated explicitly:
- ADR-02 §6: the AST schema, `normalize`, and `canonEncode` version as `r<n>`, bound to
  the epoch; **existing hashes are never recomputed**; a definition acquires a new
  address only by explicit re-admission with a `supersedes` link. Any migration design
  that re-hashes the world is foreclosed.
- ADR-03 §6: std/ is mirror-catalogued as immortal product-scope rows, generated from
  one source with the kernel binary and verified identical at boot. This ADR makes
  that boot check precise (§2).
- ADR-05 §8: CFR readers are append-only and kept **forever**; resume is always by
  content hash; a lattice-narrowing epoch must enumerate affected sleeping
  continuations at its own admission. Any design that drains or expires old
  continuations at an epoch boundary is foreclosed.
- ADR-06 §5: kernels are identical iff they run the same pinned epoch binary. A
  permanently mixed-epoch fleet is foreclosed.
- ADR-07: `migrate N`'s dry-run is that gate re-run under epoch-N semantics; the
  coverage-monotonicity rule binds epochs.

## Decision

### 1. streng's atomic epoch: adopted, confirmed

All three proposals said yes; confirmed. One epoch versions, as a single unit: the
dialect grammar (ADR-01), the AST schema + printer + `canonEncode` (`r<n>`, ADR-02),
the interpreter semantics and conformance corpus (ADR-04 §6), the CFR format version
(ADR-05), std/ and the component vocabulary, the workflow engine, the vendored tsgo
pin, and the entire gate — verifier suite, coverage contracts, budgets (ADR-07).
Nothing in this list moves alone. Between epochs, exactly one version of everything
exists; the combination space has two points: the epoch you are on and the one you are
moving to.

### 2. What an epoch physically is

An epoch `E` is a pair — **(kernel binary version, std manifest root)**:

```sql
CREATE TABLE epoch (
  n                 int PRIMARY KEY,
  binary_version    text NOT NULL,        -- pins tsgo, r<n>, CFR ver, verifier suite
  std_manifest_root text NOT NULL,        -- Merkle root over std_manifest hashes
  dispatch_attestation text NOT NULL,     -- R1-INT: H_dispatch (R1-09, ADR-10 §2) — SHA-256
                                          -- over the sorted (intrinsic, signature hash,
                                          -- Go body hash) triples of the binary's native
                                          -- dispatch table; recomputed and compared at
                                          -- every boot, mismatch = boot refusal below
  released_at       timestamptz NOT NULL,
  supersedes        int REFERENCES epoch(n)
);
CREATE TABLE std_manifest (epoch int REFERENCES epoch(n), hash text REFERENCES definition(hash),
                           PRIMARY KEY (epoch, hash));
```

The binary embeds its own manifest root and **refuses to boot** against a catalog
whose current epoch's `std_manifest_root` differs — the ADR-03 §6 "verified identical
at boot" check, made a hard equality on one hash. The live epoch is the max committed
`n`; every `admission` row stamps the epoch it passed under (ADR-03 ledger); every
definition already carries `ast_schema_ver`. Epoch identity is therefore checkable
from any replica: one row, one root.

**The live-epoch pointer (R1-05: fleet-coherence fence anchor).** The live epoch is
additionally materialized as one row, not left as a `max(n)` scan:

```sql
CREATE TABLE epoch_current (
  one bool PRIMARY KEY DEFAULT true CHECK (one),   -- at most one row, ever
  n   int NOT NULL REFERENCES epoch(n)
);
```

`epoch_current` is updated in exactly one place — inside the `migrate N --commit`
SERIALIZABLE transaction (§3) — which also publishes `NOTIFY epoch, '<N>'` at commit.
This row is what every kernel transaction reads as its epoch guard (§4a O5, ADR-06
§6): one PK read of one hot row, batched into round trips the kernel already makes.

**Structured refusal diagnostic (R1-05: boot-refuse and fence-trip are
machine-parseable).** Boot refusal — and its runtime twin, the O5 fence trip — emits
one structured event, as a JSON line on stdout and served on the kernel health port,
deliberately distinguishable from a crash loop:

```json
{ "event":                 "epoch.boot_refused" | "epoch.fence_tripped",
  "observed_epoch":        <epoch_current.n read from the catalog>,
  "required_epoch":        <the epoch this binary pins>,
  "binary_version":        "<epoch.binary_version of the running binary>",
  "binary_manifest_root":  "<the manifest root compiled into the binary>",
  "catalog_manifest_root": "<std_manifest_root of the catalog's current epoch row>",
  "pinned_h_dispatch":     "<epoch.dispatch_attestation of the current epoch row>",
  "computed_h_dispatch":   "<H_dispatch recomputed from the running binary's own table>",
  "kernel_id":             "<ephemeral boot uuid (ADR-06 §5)>",
  "ts":                    "<RFC 3339>",
  "action":                "refused_boot" | "parked_waiting" | "drained_and_exited",
  "in_flight_aborted":     <count>,
  "leases_released":       <count> }
```

R1-INT: the two `h_dispatch` fields are the attestation-mismatch cause of boot refusal
(R1-09, ADR-10 §2) — a tampered or swapped native dispatch table refuses boot with
pinned-vs-computed `H_dispatch` reported beside `binary_version`; they are absent from
fence-trip events, which fire on epoch mismatch alone.

A binary started with `--wait-for-epoch` refuses to *serve* rather than to *run*: it
emits the diagnostic with `action: "parked_waiting"`, polls `epoch_current`, and
begins serving the instant the flip commits — so the operator stages epoch-N binaries
beside the epoch-E fleet before `--commit`, and the flip's unavailability window is
the fence's drain deadline (ADR-06 §6), not a deploy.

Both events are golden signals in ADR-13's registry — `epoch.boot_refused` and
`epoch.fence_tripped`, adopted field-for-field — and ride ADR-13 §4's
Postgres-independent emission paths (stdout JSON lines, push exporter, health port),
so an epoch incident stays observable even when the catalog itself is the casualty
(R1-06: epoch diagnostics adopted as ADR-13 golden signals).

### 3. `migrate N`: dry-run findings as rows, commit as one transaction

```
regel migrate N            # DRY-RUN. The epoch-N binary mounts the catalog read-only,
                           # loads std-N, and re-runs the FULL ADR-07 gate under
                           # epoch-N semantics over every product/package definition
                           # AND every overlay in every scope. Commits nothing.
regel migrate N --commit   # ONE transaction: insert std-N mirror rows + std_manifest
                           # + epoch row; admit the prepared fix set under the epoch-N
                           # gate; re-run the O4 enumeration in-transaction (§4);
                           # update epoch_current + NOTIFY epoch. All-or-nothing.
```

- **Incremental by content-addressing:** a definition whose canonical form, deps, and
  governing checks are unchanged under N keeps its `clean@(epoch, hash)` stamp and is
  not re-checked (ADR-07 §2); only definitions the new epoch actually touches re-run.
- **The 400-incompatibilities operator story (grafted from the red-path proposal):**
  dry-run findings are **rows** — `migration_finding(epoch, scope, def_hash, rule,
  loc, message, fix)` — grouped by rule, sortable, queryable, and served over MCP, so
  an agent batch-fixes against them mechanically. Every incompatibility is listed at
  once with its fix in the error (streng's rule); the wall of 400 becomes a work
  queue. The old epoch serves the entire time; dry-run is repeatable and cheap.
- **Fixes are ordinary admissions.** A fix that is valid under the current epoch lands
  now through the normal gate. A fix that only typechecks against std-N (a new API)
  is submitted as a **prepared re-admission**: validated against the dry-run gate,
  held with the migration plan, and admitted inside the `--commit` transaction itself
  — so "the app and the framework land together" (streng) without a window where
  either is broken.
- **Hashes:** `--commit` re-hashes nothing (ADR-02 §6). Compatible definitions keep
  their addresses; only prepared re-admissions mint new addresses, each with a
  `supersedes` link. `--commit` refuses to run while any unresolved
  `migration_finding` lacks a prepared fix or an overlay condition decision (§5).
- **The flip is a signal, not just a row (R1-05: flip publishes epoch_current +
  NOTIFY).** The `--commit` transaction updates `epoch_current` (§2), publishes
  `NOTIFY epoch, '<N>'` at commit, and re-runs the O4 enumeration inside itself (§4).
  Because `--commit` is SERIALIZABLE, every kernel work transaction reads
  `epoch_current` as its guard (§4a), and `--commit` re-reads the tables concurrent
  work writes, a park or admission racing the flip forms an rw-cycle with it and SSI
  aborts one side — the check and the flip cannot be interleaved. It also asserts
  `epoch_current.n` still equals the epoch the plan was prepared from, so two
  concurrent migrates cannot both land.

### 4. Continuation compatibility contract: four testable obligations (a fifth, the fleet fence, in §4a)

An epoch never moves running or sleeping work. The obligations, each a release gate:

- **O1 — byte immortality.** A hash admitted under any epoch is fetchable forever and
  returns its original bytes; no epoch re-canonicalizes stored definitions.
  *Test:* fetch-by-hash under N equals the E-era bytes; the ADR-02 nightly
  world-rehash canary is the standing form.
- **O2 — semantic stability, keyed by `r<n>`.** A continuation resumes under the
  evaluation semantics of the `r<n>` its definitions were admitted under. Any
  behavior-visible change to evaluation semantics REQUIRES an `r<n>` bump; the kernel
  keeps per-`r<n>` semantics append-only, exactly as ADR-02 keeps decoders. There is
  no resume-only side engine and no drain requirement: old-`r` rows are evaluated by
  the current machine under their version's pinned semantics.
  *Test:* the **golden-continuation corpus** (grafted from the red-path proposal) —
  continuations captured under every prior epoch, resumed under the new kernel, must
  complete bit-identically; a semantic change that alters any golden resume without an
  `r<n>` bump is a release blocker. The corpus is a versioned artifact of the epoch,
  alongside ADR-04 §6's conformance corpus.
- **O3 — CFR readers forever.** Every CFR version ever written stays readable
  (ADR-05 §8, restated as an epoch obligation).
  *Test:* every historical CFR fixture deserializes and resumes under N.
- **O4 — lattice narrowing enumerates, atomically with the flip.** An epoch narrowing
  the serializable lattice fails to land until every sleeping continuation holding a
  newly-banned type is resolved (ADR-05 §8). The dry-run enumeration is the operator's
  preview; the **authoritative enumeration re-runs inside the `--commit` SERIALIZABLE
  transaction itself**, immediately before the flip, so the check and the flip are one
  atomic commit (R1-05: O4 TOCTOU closed — enumeration moved inside the commit).
  A continuation parked between dry-run and `--commit` is seen by the in-transaction
  scan; one parked *concurrently with* `--commit` cannot slip past it: the parking
  step transaction runs SERIALIZABLE and reads `epoch_current` (which `--commit`
  writes) while `--commit` reads `continuation` (which the park writes) — an rw-cycle
  SSI refuses, so either the park or the flip commits, never both. The fence (§4a)
  already mandates that guard read; O4's atomicity is a consequence, not extra
  machinery.
  *Test:* seed a sleeping continuation with a to-be-banned type; `migrate N --commit`
  refuses with the continuation listed. Race form: park a banned-type continuation
  concurrently with `--commit`; exactly one of the two transactions commits, under
  every interleaving.

### 4a. O5 — fleet coherence: no work commits under a mismatched pair (R1-05: per-request/resume epoch fence)

Boot fencing (§2) alone leaves running kernels unfenced: a kernel that booted under
epoch E would keep serving requests and resuming continuations after the catalog flips
to N — two kernels concurrently applying different epochs' semantics to the same data,
and an E-binary resolving `std` names to swapped N-hashes absent from its native
dispatch table. O5 closes this.

**Obligation.** No kernel transaction — request service, admission, continuation
step/claim/park, task claim — ever COMMITs having observed a catalog epoch newer than
the epoch its binary pins. "One fleet, one epoch" (ADR-06 §5) is thereby a
per-transaction invariant, not a boot-time hope: the older kernel's work cannot commit
once the flip is visible.

**Mechanism (the guard).** Every kernel transaction reads `epoch_current` (§2) as part
of its first batched round trip (fast fail) and again batched immediately before
COMMIT (authoritative: under READ COMMITTED the re-check sees a fresh snapshot;
admission and step transactions run SERIALIZABLE, where the guard read makes the
flip's `epoch_current` UPDATE an rw-conflict SSI resolves). Observed `n` newer than
the binary's epoch → ROLLBACK, then fail-close per ADR-06 §6: refuse new work, release
claims, emit `epoch.fence_tripped` (§2), drain, exit for replacement — the kernel
never limps along. Read-only serves are bounded by the same guard at snapshot
acquisition plus the `NOTIFY epoch` drain.

**Cost, priced.** One PK read of a one-row hot table, piggybacked on round trips the
kernel already makes (extended-protocol batching) — zero added network hops, zero row
locks; the standing overhead is one SSI read predicate on one hot page. The fence
concentrates its entire cost at the flip, where in-flight epoch-E transactions abort
and their work is re-offered to epoch-N kernels — which is precisely the intended
fail-closed behavior, and safe by the ADR-05 §7 exactly-once composition.

*Test:* `migrate N --commit` while an epoch-E kernel serves live traffic and resumes
continuations. Assert: the E-kernel's first post-flip commit attempt rolls back; the
structured diagnostic is emitted with every §2 field; the kernel drains and exits
within the lease TTL; released work is re-offered and completes exactly once on an
epoch-N kernel from the last committed checkpoint; at no instant is any request or
resume answered by a mismatched (binary, catalog) pair — the §2 boot-refusal
guarantee extended to a running binary.

### 5. Overlay re-verification: fleet-atomic binary, per-scope authorship gating

Dry-run fans the full gate across every overlay in every scope (product · package ·
org · team · user) and reports per-scope findings. The resolution model:

- **The binary and gate flip fleet-wide at `--commit`.** One fleet, one epoch
  (ADR-06 §5) — enforced for running kernels by the O5 fence (§4a), not merely
  asserted at boot (R1-05: flip is fenced, not hoped). Running a prior epoch's engine
  for a quarantined scope is rejected — it is a permanently mixed fleet and a second
  engine to carry.
- **A failing overlay blocks nothing already admitted.** Immortality (O1/O2) means
  the overlay's rows keep resolving and evaluating under their `r<n>` semantics after
  the flip. Commit is therefore not hostage to any tenant.
- **Each failing overlay becomes a durable condition** (grafted from the red-path
  proposal, on ADR-05 §6's machinery): `epoch.overlay_incompatible` with restarts
  `fix-overlay` (re-admit a corrected overlay through the gate), `drop-overlay`
  (capability-gated to the platform operator), and `defer{deadline}`. It renders as
  operator-plane buttons for the tenant's admins and as MCP choices for agents — the
  same rows.
- **Until resolved:** new admissions in that scope touching the failing overlay's
  name are rejected with the finding attached (the tenant authors nothing new on a
  base the epoch condemned), and ADR-03 §3's rule stands unmodified — a product-scope
  base move re-verifies overlays and rolls back naming the conflict. The operator's
  `drop-overlay` restart is the governance valve when a stale overlay blocks the
  product; exercising it is an audited admission like everything else.
- **Who is told what:** the tenant's admins see their scope's findings only; the
  platform operator sees counts, scopes, and deadlines; other tenants see nothing and
  notice nothing.

### 6. Between-epochs freeze and the emergency path

Within an epoch, the binary is immutable: no flag, no config toggle, and no hotfix
path can change the subset grammar, printer, interpreter semantics, verifier suite,
budgets, or std/. **The world is frozen; the app is not** — app and overlay
definitions admit freely through the normal gate the whole time. Models writing
next-epoch idioms at the current gate are rejected with fix-in-the-error until the
epoch ships; staying on an old epoch is a supported posture indefinitely (an offer,
not an eviction).

**Emergency fix (a CVE in a std battery): patch epoch `E.1`.** Same atomic mechanism,
minimal surface: a new manifest swapping only the affected std hashes, a dry-run
scoped by the reverse-dependency closure of the changed definitions, the same
`--commit` transaction, the same golden-corpus and coverage-monotonicity gates. Even a
Sev-1 ships as `regel migrate 8.1`. The freeze has no exception clause, because the
exception is precisely the side door this design exists to delete.

### 6a. Bad-epoch runbook: revert vs roll-forward, authored and drilled (R1-05: recovery for a bad flip)

The flip is fleet-atomic and fenced, so a defect the golden corpus missed surfaces
fleet-wide at once — and "chase it forward through the same gate that admitted it"
must not be the only rehearsed motion. This runbook is a normative artifact of this
ADR, and it is **drilled as a release gate** (below): an unrehearsed recovery is a
hypothesis, not a capability.

**Decide: roll forward or revert.** Classify from the §2 structured diagnostics, a
golden-continuation-corpus re-run, and the health surface (now specified: the ADR-13
§2 signal set, still emitting over its Postgres-independent paths mid-incident —
R1-06: health surface resolves to ADR-13):

- **Roll forward — patch epoch `N.1` (§6)** when the epoch-N binary evaluates
  correctly and the defect is content-shaped: a std battery bug, a wrong manifest, a
  gate rule with a bad threshold. The fix is expressible as admissions and the N gate
  can be trusted to admit it. This is the default, and it is the already-drilled
  `E.1` motion.
- **Revert — epoch `N+1` carrying E's pair** when the N *binary* misbehaves (an
  evaluator or verifier defect) or the N gate cannot be trusted to admit its own fix.
  Revert is not deletion — the `epoch` table is append-only like everything else. It
  is a new epoch row `N+1` with `supersedes = N` whose `(binary_version,
  std_manifest_root)` are **E's**, landed through the same one-transaction `--commit`:
  the fence trips fleet-wide, staged E binaries (`--wait-for-epoch`, §2) take over,
  and boot fencing now accepts exactly the E pair again. Rolling back is rolling
  forward to the previous binary.
- **Revert constraint, stated honestly:** a pure revert to E's binary is sound only
  while nothing depends on what only N can read. If N bumped `r<n>` and definitions
  or continuations were captured under the new `r`, E's binary has no decoder or
  semantics for them — O2/O3 would break from the other side. The blast query below
  checks this mechanically; if it is non-empty, the revert epoch must instead carry a
  binary built as E's semantics **plus** N's readers (previous behavior, new
  decoders), which the append-only-decoder rule (ADR-02 §6, ADR-05 §8) makes a small
  additive build.

**Steps.**

1. **Classify** from `epoch.fence_tripped` / `epoch.boot_refused` events, the golden
   corpus re-run, and O1–O5 gate re-runs, per the criteria above.
2. **Enumerate blast — rows, because everything stamps its epoch:** admissions with
   `epoch = N` (the ledger stamps it, §2), continuations parked or stepped since
   `epoch(N).released_at`, and any `r<N>`-stamped rows (the revert-constraint check).
3. **Execute** the chosen motion. Both are the standard §3 `--commit` shape: dry-run
   scoped to the blast closure, one SERIALIZABLE commit, fences trip, staged binaries
   take over. No bespoke emergency tooling exists, by design (§6).
4. **Reconcile.** Definitions admitted under N keep their addresses (O1) and their
   pinned semantics (O2). Any definition the defect may have wrongly admitted is
   superseded through the now-trusted gate; continuations a defective evaluator
   checkpointed are located by the blast query and resolved through their ADR-05 §6
   restarts — never by editing rows.
5. **Verify:** golden corpus green, O1–O5 green, world-rehash canary green, one epoch
   serving fleet-wide (O5).

**Drill — a release gate.** Every epoch release re-exercises this runbook in staging
under a measured clock: ship a deliberately-bad epoch (a seeded evaluator defect the
corpus is blinded to), observe the fleet fence, execute the revert to the prior pair,
and record time-to-recovered. The drill stands beside the §6 `E.1` emergency drill in
the M6 gate and every release suite thereafter; a release does not ship while the
drill is red or unrun.

## Alternatives Considered

- **simplest-thing: epoch = binary with std compiled-in only (no rows); `migrate
  --commit` re-admits the whole app to new hashes in one transaction; fleet-atomic
  overlay policy (any failing overlay anywhere aborts the commit).** Rejected on
  three counts: std-not-rows contradicts ADR-03 §6; whole-app re-hash contradicts
  ADR-02 §6's existing-hashes-never-recomputed rule; and fleet-atomic overlay abort
  hands any single tenant a veto over the platform's upgrade. Adopted from it: the
  frozen-binary statement of the freeze and the append-only format contracts (already
  ADR-02/05 law, restated here as O1/O3).
- **prior-art-faithful (winner):** the two-part epoch, boot-refusal on manifest
  mismatch, lazy keep-your-hashes commit, authorship-gated cohorts, and the
  point-release emergency path carry this ADR. Corrections: its "ABI support window"
  language is deleted — ADR-05 §8 keeps readers forever, so no window and no drain
  exist; its `--rewrite` eager mode is cut (one commit semantics; eager re-addressing
  is just a batch of ordinary re-admissions if a team wants one).
- **red-path-first:** its golden-continuation corpus, findings-as-rows operator story,
  overlay-failure-as-durable-condition, and the no-exception emergency framing are
  grafted. Rejected pieces: the resume-only N−1 evaluator with forced drain of older
  continuations (contradicts ADR-05 §8 and its own year-old-resume kill-test), and
  per-scope quarantine pinning a tenant to the prior epoch's binary surface
  (contradicts ADR-06 §5; carries two engines).

## Consequences

- Upgrades are priced once per epoch and paid by the framework team: dry-run, a work
  queue of findings, prepared re-admissions, one commit. No dependency matrix exists
  to solve.
- The kernel accretes per-`r<n>` decoders and semantics forever. This is the accepted
  cost of never stranding a continuation; it is bounded by how rarely `r<n>` bumps
  and enforced small by the golden corpus (a gratuitous semantic change is a visible
  release blocker, so semantics changes are deliberate and enumerated).
- A tenant can sit on a broken overlay indefinitely without breaking themselves; the
  pressure they feel is inability to author against the condemned name plus a visible
  condition with a deadline — governance by restart, not by outage.
- Every epoch artifact — manifest, findings, conditions, coverage rows, golden corpus
  verdicts — is rows, so "what did epoch 9 change and who is blocked" is a SELECT.
- Patch epochs make security response a rehearsed, gate-shaped motion rather than an
  exceptional bypass; response latency is bounded by dry-run time on the affected
  closure, which memoization keeps proportional to the change.
- A bad epoch is recoverable by the same mechanism that shipped it: revert is a new
  epoch row carrying the prior pair (§6a), drilled in staging every release —
  forward-only is a property of the ledger, not of operations (R1-05: revert path
  exists and is rehearsed).

## Red-Path Tests Implied

- **Stranded continuation (impossible):** capture under epoch E; upgrade through E+1
  and E+2; resume completes via O2/O3 with no drain step anywhere (extends ADR-05
  test 2 across two boundaries).
- **Semantic drift:** introduce an evaluation-semantics change without an `r<n>` bump;
  the golden-continuation corpus fails the release; with the bump, old-`r` rows still
  resume bit-identically.
- **400 breaks:** synthesize a catalog with hundreds of epoch-N incompatibilities;
  dry-run lists every one as `migration_finding` rows in one pass; an agent
  batch-fixes from the rows; `--commit` refuses until the queue is empty.
- **Overlay blocker:** org A's overlay fails epoch-N dry-run; commit proceeds; A's
  live traffic is byte-identical before/after the flip; A's next admission on that
  name is rejected with the finding; `fix-overlay` restart clears the condition;
  org B observes nothing throughout.
- **Boot refusal, with diagnostic shape (R1-05: diagnostic asserted, not just refusal):** point an epoch-N binary at a
  catalog whose manifest root is E's; the kernel refuses to serve and emits the §2
  structured diagnostic — the test parses it and asserts every named field
  (`observed_epoch`, `required_epoch`, `binary_version`, both manifest roots,
  `kernel_id`, `action`) and that it is machine-distinguishable from a crash loop;
  `--wait-for-epoch` parks, then serves the instant the flip commits. No request is
  ever answered by a mismatched pair.
- **Running-kernel fence (R1-05: fail-close red path):** `migrate N --commit` while an epoch-E kernel
  serves traffic and resumes continuations; the E-kernel's first post-flip commit
  attempt rolls back on the O5 guard, `epoch.fence_tripped` is emitted, the kernel
  drains and exits within the lease TTL, and the re-offered work completes exactly
  once on an epoch-N kernel — the boot-refusal guarantee extended to a running
  binary.
- **Bad-epoch revert drill (R1-05: runbook exercised as a gate):** ship a deliberately-bad epoch in staging; the
  fleet fences; execute §6a's revert as epoch `N+1` carrying E's pair under a
  measured clock; assert time-to-recovered is recorded and O1–O5 re-green. Variant:
  with an `r<N>` bump and one `r<N>`-captured continuation, assert the pure revert
  is refused and the fix-epoch path (E semantics + N readers) is taken instead.
- **Emergency drill:** ship a std hash swap as `E.1` end-to-end (dry-run on the
  affected closure, commit, golden corpus green) under a measured clock; verify no
  path shorter than the gate exists in the codebase.
- **Lattice narrowing:** a sleeping continuation holding a newly-banned type blocks
  `--commit` with the continuation enumerated (O4). Race form (R1-05: O4 inside the
  commit transaction): park a banned-type continuation concurrently with `--commit`;
  exactly one of the two transactions commits, under every interleaving.
- **Commit atomicity:** kill the kernel mid `--commit`; the epoch row, manifest, and
  prepared re-admissions are all present or all absent; the fleet never observes a
  half-epoch.

## Constraints Discharged or Budgeted

1. **Discharged (the years half).** O1–O4 are the tested statement of "serialized
   stably for years": immortal bytes, `r<n>`-pinned semantics, readers forever,
   narrowings enumerated. The golden corpus is the standing proof.
2. **Budgeted.** Per-`r<n>` semantics accretion is the named interpreter-tax line
   item at epoch scale; memoized dry-runs keep migration cost proportional to change.
3. **Discharged for upgrades.** Overlay failure is a durable condition with named
   restarts rendered to operators and agents — not an ops runbook.
4. **Consumed.** Migration findings and manifests are AST-schema-aware rows; the
   `r<n>` version in every address is what lets two epochs coexist in one catalog.
5. **Budgeted.** Coverage monotonicity (ADR-07 §5) binds every epoch; a patch epoch
   re-runs the same suite — the boundary cannot be thinned by an upgrade.
6. **Budgeted.** The epoch is the single staging lever: one binary, one commit, one
   work queue; no rollout matrix, no parallel engines, no drain choreography (the O5
   fence's terminal drain is automatic and mechanical, not an operator ceremony).
