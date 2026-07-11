# regel — Integrated Architecture (Phase 1)

*The thirteen accepted ADRs tied into one buildable design (R1-INT: count updated —
ADR-13 observability was created by revision R1-06). This document adds no new
decisions; every mechanism cited here is normative in its owning ADR. Constraint numbers
refer to BRIEF §3.*

---

## 1. System overview

One Go kernel (stateless, N identical copies) over one Postgres. Code enters through
exactly one gate. Three surfaces face the world; all of them terminate in the same
admission transaction or the same evaluation loop.

```
      agents (MCP)      engineers (CLI / git PR)     tenants (Settings)     browsers
          │                     │                          │                  │
          ▼                     ▼                          ▼                  ▼
 ┌─────────────────────────────── surfaces ─────────────────────────────────────────┐
 │  MCP server (ADR-12)  │  git projection (ADR-09)  │  HTTP + SSE (ADR-06, ADR-11) │
 └───────────┬───────────────────────┬──────────────────────────┬──────────────────┘
             │ patch.submit          │ PR merge = admission     │ requests / events
             ▼                       ▼                          ▼
 ┌──────────────────────── kernel (Go binary, pinned epoch) ────────────────────────┐
 │                                                                                  │
 │  ADMISSION GATE (ADR-07) — the single write path for code                        │
 │    parse → lower → grammar gate (ADR-01) → normalize → print + hash (ADR-02)     │
 │    → tsgo typecheck (hermetic host) → derivation passes → six verifiers          │
 │    → additive DDL → name_pointer CAS       [one SERIALIZABLE transaction]        │
 │  ──────────────────────────────────────────────────────────────────────────────  │
 │  reactor: goroutine-per-request, netpoller (ADR-06)                              │
 │  CEK interpreter, fuel meter + governor (ADR-04)                                 │
 │  CFR codec, claim/lease protocol (ADR-05)                                        │
 │  vendored tsgo — typechecker only, no emit (ADR-04 §3)                           │
 │  owned Postgres wire client, destroy-on-desync (ADR-06 §2)                       │
 │  std/ native dispatch table (ADR-10)  │  scherm slot diff engine (ADR-11)        │
 └──────────────────────────────────────┬───────────────────────────────────────────┘
                                        │ one wire client; heavy lifting is SQL
                                        ▼
 ┌──────────────────────────────── one Postgres ────────────────────────────────────┐
 │ catalog:  definition · definition_meta · name_pointer (+history) · admission     │
 │           (ADR-03)                                                               │
 │ runtime:  continuation · durable_condition · restart (ADR-05) · task (ADR-06)    │
 │           subscription (ADR-11) · grants/roles/keys (ADR-04 §5) ·                │
 │           admission-fuel budgets + gate_refusal (ADR-12)                         │
 │ epochs:   epoch · std_manifest · epoch_current (ADR-08)                          │
 │ product:  app data · history tier · vault (keys in external KMS)                 │
 └──────────────────────────────────────────────────────────────────────────────────┘
```

Load-bearing identities: a definition's identity is `r<n>_` + base32 SHA-256 of its
canonical AST bytes (ADR-02); a paused program is a row whose frames reference only such
addresses (ADR-05); the catalog's `definition` table is INSERT-only with privileges
revoked (ADR-03 I6). Kernels hold zero authoritative state (ADR-06 §5).

## 2. Data-flow narratives

### (a) An admission, end to end (ADR-07, ADR-01, ADR-02, ADR-03)

1. A patch envelope arrives (CLI, Settings, MCP, or git merge). The kernel opens one
   `SERIALIZABLE` transaction and inserts the `admission` ledger row (ADR-03 §5.1).
2. Scope binds from the **authenticated** principal's chain, never from the body
   (ADR-07 step 2a).
3. Vendored tsgo parses; the default-deny lowering produces the owned regel-AST; the
   grammar gate enforces every ADR-01 §2 ban, switch discipline, floating promises,
   acyclicity, and capture rules R1–R5 (ADR-01 §4–5).
4. Normalize → canonical print → hash (ADR-02 §1–4). Identity is fixed here, before the
   typechecker runs: a tsgo bump can never move a hash.
5. No-op short-circuit: if every hash is catalogued and every pointer already resolves
   to it, return already-admitted (ADR-07 step 2d).
6. `definition` / `definition_meta` insert with content dedup; the kernel re-verifies
   `hash == SHA-256(domain ‖ ast)` before insert (ADR-02 §5 g4, ADR-03 §5.3).
7. tsgo typechecks the canonical text against the transaction's frozen catalog snapshot
   through the hermetic three-layer module host, affected-set only, under the
   deterministic node ceiling (ADR-07 §2–3).
8. Derivation passes run pure: proposed derived rows + `migration_sql` (ADR-07 step 5a,
   made exact by ADR-10 §4's ten artifacts).
9. The six verifiers run over base ⊕ patch ⊕ derived: V1 capability-audit, V2 pii-flow,
   V3 catalog-parity, V4 contracts, V5 capture, V6 derivation-parity (ADR-07 §4). Any
   failure RAISEs.
10. Additive DDL applies; `name_pointer` upserts with CAS against the base hash the
    patch saw; overlays of any moved base re-verify (ADR-03 §5.6–8).
11. COMMIT is the deploy. Any RAISE rolls back everything — no row, no DDL, no pointer
    move. The Verdict (ADR-07 §6) returns identically to every door.

### (b) A request → evaluate → respond (ADR-06, ADR-04)

1. HTTP request lands on a goroutine from the bounded pool (ADR-06 §1).
2. Route resolves as a catalog name (routes are handler rows): pointer cache,
   epoch-checked, reload-on-stale (ADR-06 §3–4).
3. Canonical AST loads by hash from the immortal cache (coherent fleet-wide with zero
   coordination).
4. The kernel builds the root capability table: `intersection(definition imports,
   principal grants)` — an ungranted capability has no slot (ADR-04 §5).
5. The CEK machine steps under `governorMeter` (trusted) or `fuelMeter` (sandbox)
   (ADR-04 §2, §4). Inline-satisfiable awaits run on the same goroutine.
6. Completion renders the response. A deferred wake parks C/E/K as an ADR-05 row via
   the step transaction and releases goroutine and connection; the synchronous caller
   receives `202` + continuation id (ADR-06 §4).

### (c) Workflow pause → wake → resume, durable condition + restart (ADR-05, ADR-10)

1. A workflow is a plain async function of `kind='workflow'`; every `await` of an
   effectful capability is a durable checkpoint per its declared effect class —
   `read` inline, `write` atomic with the checkpoint, `external` via outbox intent
   keyed `(continuation_id, step_seq)` (ADR-10 §6, ADR-05 §7).
2. `taak.sleep` parks with a `timer` wake; `taak.receive` with `message`;
   `taak.all/race` spawn children and park with `join` (ADR-05 §5).
3. A failing step calls `signal(condition, restarts)`: one transaction parks the
   continuation on its **durable condition** — `status='condition'` is the
   parked-on-condition status (the SQL literal is unchanged; R1-INT: three-term
   "condition" vocabulary adopted per R1-12/GLOSSARY — *wake condition* = trigger,
   *durable condition* = the resumable-error row, *`condition` status* = parked
   awaiting a restart choice) — with `wake=manual`, and inserts the `durable_condition`
   row and its `restart` rows (ADR-05 §6). Fuel exhaustion and governor breach park
   through the identical path (`fuel.exhausted`, `runaway`; ADR-04 §4).
4. Restarts render as operator-plane buttons and MCP choices — the same rows. Picking
   one checks `capability_required`, sets `resolved_*`, flips the continuation `ready`,
   inserts a resume task (ADR-05 §6, ADR-12 §7's `expectedHash` fence).
5. Any kernel claims via the `step_seq` CAS + lease, rebuilds E, re-validates and
   re-binds capability tokens (`grant_id` → live sealed handle; revocation signals
   `capability.revoked`), and re-enters the machine at the parked C (ADR-05 §4, §7).
   Resume is always by content hash against immortal rows — as-of, structurally.

### (d) UI session event → resume → diff → SSE patch (ADR-11)

1. A session is a `continuation` row, `kind='session'`; its CFR holds mount expression,
   UI-local state, last-sent slot snapshot + hash, principal chain; its subscriptions
   live in the `subscription` table (ADR-11 §5).
2. A browser event POSTs `{sessionId, slotId, event, value, eventSeq}`. Any kernel
   claims the row via the ADR-05 §7 CAS (double-fired clicks lose and drop).
3. The machine resumes with the event bound; the handler mutates or navigates; only
   slots whose readSet intersects the change re-evaluate (admission-time
   static/dynamic split; the diff unit is the slot, never a tree — ADR-11 §1).
4. Deltas frame as `[eventSeq, snapshotHash, ops[]]` and push on the session's SSE
   channel; the checkpoint (new CFR, snapshot, subscriptions) commits in the same step
   transaction (ADR-11 §2, §5).
5. The 15KB client applies by slotId, recomputes the snapshot hash, and POSTs `resync`
   on mismatch — divergence self-heals in one round trip (ADR-11 §3–4). Mutations
   elsewhere publish `NOTIFY (resource, rowId, horizon)`; matching sessions re-render
   through the bounded worker pool, coalesced per tick (ADR-11 §6). PII crosses only as
   mask tokens; plaintext never enters the slot snapshot (ADR-11 §8).

### (e) An agent patch conversation via MCP (ADR-12)

1. The agent authenticates with an API key that is a bundle of grant rows; its scope
   chain filters everything it can see (ADR-12 §1).
2. `catalog.search/get/deps` read scope-visible source — code, never data; another
   org's overlay is byte-indistinguishable from a name that never existed (ADR-12 §3).
3. `patch.submit {commit:false}` runs the full ADR-07 pipeline and rolls back — the
   same dry-run mechanism as PR checks. The structured Verdict returns with
   per-diagnostic `fix` fields; hermeticity makes the retry loop convergent.
4. The agent iterates, then `patch.submit {commit:true}`. Overlay scopes are
   self-serve through the full gate; product scope requires a one-shot human approval
   token bound to the exact content hashes (ADR-12 §6). Admission fuel prices the
   gate; refusals land in the `gate_refusal` ledger, never the admission ledger
   (ADR-12 §5). Vault plaintext is unreachable by three independent layers (ADR-12 §4).

### (f) An epoch migration (ADR-08)

1. `regel migrate N` dry-runs: the epoch-N binary mounts the catalog read-only and
   re-runs the full gate under epoch-N semantics over every definition and every
   overlay, memoized by `clean@(epoch, hash)` stamps. Findings are
   `migration_finding` rows — a work queue, served over MCP (ADR-08 §3).
2. Fixes valid under the current epoch land now as ordinary admissions; std-N-only
   fixes are prepared re-admissions held with the plan.
3. `--commit` is one SERIALIZABLE transaction: std-N mirror rows + manifest + epoch
   row + prepared re-admissions + the `epoch_current` flip with `NOTIFY epoch`. It
   re-hashes nothing (ADR-02 §6); it refuses while unresolved findings remain or a
   sleeping continuation holds a newly-banned lattice type — the O4 enumeration
   re-runs **inside this transaction**, so the check and the flip are atomic
   (R1-05: O4 TOCTOU closed; ADR-08 §4).
4. The fleet flips atomically *and coherently*: the binary refuses to boot against a
   mismatched manifest root, and every running kernel carries a per-transaction epoch
   fence — a kernel observing a newer `epoch_current` fail-closes (rolls back, emits
   the structured `epoch.fence_tripped` / `epoch.boot_refused` diagnostic, drains,
   exits for replacement; ADR-08 §2/§4a, ADR-06 §6). No work ever commits under a
   mismatched (binary, catalog) pair (R1-05: running kernels fenced, not only boot).
   A bad flip is recovered by the authored + drilled revert/roll-forward runbook —
   revert is a new epoch row carrying the prior pair (ADR-08 §6a). Failing overlays
   keep evaluating under their `r<n>` semantics and become
   `epoch.overlay_incompatible` durable conditions with `fix-overlay` /
   `drop-overlay` / `defer` restarts (ADR-08 §5). Obligations O1–O5 (byte
   immortality, `r<n>`-pinned semantics, CFR readers forever, narrowings enumerated
   atomically, fleet coherence) are release gates, proven by the golden-continuation
   corpus and the fence red paths (ADR-08 §4–4a).

### (g) A git clone / PR (ADR-09)

1. Outbound: the projection is a pure fold over the admission ledger — one commit per
   admission row, all metadata derived from ledger data, byte-identical SHAs on any
   machine (ADR-09 §2). One file per definition at the name→path shared with ADR-07's
   module host; `std/` projects read-only from mirror rows; `catalog.lock` anchors
   name → (hash, kind, epoch). Overlays, vault, grants, and runtime rows are never
   inputs to the fold; the tree is structurally PII-free via `PII_LITERAL` (ADR-09 §5).
2. The kernel pushes to a hosted mirror whose `main` only the projector identity can
   advance; any mirror/image SHA mismatch force-restores from the image (ADR-09 §3).
3. Inbound: PR open/update runs the gate as a rolled-back dry-run and posts the
   Verdict as the required check; the merge action submits the changed files through
   the **real** admission transaction (`via='git'`). On accept, `main` advances to the
   canonical commit — landed bytes are the printer's. On reject, `main` never moves
   (ADR-09 §4). No forge operation can land unverified code.

## 3. Subsystem seams

Each row is a contract: the exact artifact that crosses the boundary, owned by the
named ADR. Nothing else crosses.

| Producer → Consumer | Interface (the exact thing exchanged) | Owner |
|---|---|---|
| ADR-01 → ADR-02 | The node-kind whitelist: the printer/encoder's totality domain; every admitted kind has one canonical form | ADR-01 |
| ADR-01 → ADR-04 | The admitted surface: the machine executes exactly these node kinds; `await` is the sole suspension | ADR-01 |
| ADR-01 → ADR-05 | Capture rules R1–R5: what a serialized environment may contain (the R2 lattice) | ADR-01 |
| ADR-02 → ADR-03 | The `r<n>_<base32>` address: `definition.hash` PK; the only code identity anywhere | ADR-02 |
| ADR-02 → ADR-04 | Canonical-AST node paths + De Bruijn indices: the Control anchor `(def_hash, node_path, phase)` and slot-array environments | ADR-02 |
| ADR-02 → ADR-05 | Shared primitive encodings (f64 bit pattern, bigint, UTF-8) reused by the CFR codec; append-only `r<n>` decoders | ADR-02 |
| ADR-03 → ADR-04 | Definition load by hash from the immortal INSERT-only store; std mirror rows keying the native dispatch table | ADR-03 |
| ADR-03 → ADR-06 | `NOTIFY catalog/grants` + transactional epoch counters: the cache-invalidation contract | ADR-03 |
| ADR-03 → ADR-07 | The §5 transaction shape: the gate runs entirely inside it against a frozen snapshot | ADR-03 |
| ADR-03 → ADR-09 | The admission ledger: the fold's input; one commit per row | ADR-03 |
| ADR-04 → ADR-05 | The C/E/K registers + closed `Value` union: exactly what CFR serializes; frame kinds append-only | ADR-04 |
| ADR-04 → ADR-12 | Grant/role/API-key rows: the agent principal model | ADR-04 |
| ADR-05 → ADR-06 | The claim CAS (`step_seq`) + lease columns: the reactor's drain/heartbeat/reaper protocol operates on these | ADR-05 |
| ADR-05 → ADR-11 | The `continuation` row, `kind='session'`: sessions add no second store; `eventSeq` IS `step_seq` | ADR-05 |
| ADR-05 → ADR-12 | `durable_condition` + `restart` rows: the operator inbox and `condition.restart` render/consume the same rows | ADR-05 |
| ADR-06 → all evaluation | The owned wire client (eight-bullet scope, destroy-on-desync) and the single `task` table (resume/cron/deliver) | ADR-06 |
| ADR-07 → ADR-01/02 | Stage seating: bans/acyclicity/R1–R5 live in the grammar gate; printer idempotence at insert; only V1–V6 are suite members | ADR-07 |
| ADR-07 → ADR-05 | V5 capture verifier and the CFR codec share one type table: encodable ≡ admitted | ADR-07/05 |
| ADR-07 → ADR-09 | The shared name→path function: tsgo module host and git layout can never disagree | ADR-07 |
| ADR-07 → ADR-10 | Step 5a derivation slots: erf's ten artifacts run there; V6 checks totality | ADR-07 |
| ADR-07 → ADR-12 | The Verdict object + leak discipline, returned verbatim over MCP, PR checks, and the operator plane | ADR-07 |
| ADR-08 → ADR-02/05 | The epoch binds `r<n>`, CFR version, tsgo pin, verifier coverage; O1–O5 restated as release gates | ADR-08 |
| ADR-08 → ADR-06 | The `epoch_current` row + `NOTIFY epoch`: the per-transaction fence guard every kernel transaction reads; fail-close/terminal-drain contract and the structured refusal diagnostic (R1-05: fence seam) | ADR-08 |
| ADR-08 → ADR-10 | The epoch pair (binary version, std_manifest_root); boot refusal on mismatch; genesis populates both | ADR-08 |
| ADR-10 → ADR-11 | The 25-component vocabulary and six masking leaves; the horizon as both policy filter and subscription key | ADR-10 |
| ADR-10 → ADR-05 | `taak.*` mapping onto wakes/conditions; effect classes onto the step transaction and outbox | ADR-10 |
| ADR-11 → ADR-06 | SSE channels, hot-session cache-over-rows, invalidation drain: hosted by the reactor, no new machinery | ADR-11 |
| ADR-12 → ADR-07 | `patch.submit {commit:false}` = the rolled-back dry-run transaction — one implementation, two doors (MCP, PR) | ADR-12/09 |
| ADR-13 → all subsystems | The signal registry compiled into the epoch binary (ADR-08 §1): every operational duty emits only registered signals, over the §4 Postgres-independent paths, under the §6 PII policy — "health surface" resolves here and nowhere else (R1-INT: ADR-13 seam rows added, R1-06) | ADR-13 |
| ADR-06 → ADR-13 | Reaper pacing: bounded batches, token-bucket re-offers, and the reap-rate breaker — ADR-06 §5 implements ADR-13 §5's backpressure/breaker contract, safe because the ADR-05 §7 lease is liveness-only (R1-INT) | ADR-13 |

### Reconciliations

No contradiction between the thirteen accepted ADRs survives reading them together
(R1-INT: count updated for ADR-13). Three
places look like conflicts and are already resolved inside the later ADR, each in favor
of the earlier-numbered one, consistent with the resolution rule:

1. **Capability capture across pauses.** A proposal-level ban on capturing capability
   handles contradicted ADR-01 R2; ADR-05 §4 explicitly overrules the ban and adopts
   ADR-01's token model (`grant_id`, re-validated at resume). ADR-01 governs.
2. **Workflow step wrappers.** The proposal-era `wf.step` API contradicted ADR-04's
   everywhere-serializable machine and ADR-05's every-await capture verifier; ADR-10 §6
   overrules it — every effectful await is a checkpoint. ADR-04/05 govern.
3. **Old-epoch drain.** A resume-only N−1 engine with forced drain contradicted
   ADR-05 §8's readers-forever rule; ADR-08 rejects it and keeps O2/O3. ADR-05 governs.

One seam is dual-natured by design, not by accident, and is named in both owners:
std/ is rows in the catalog and native Go in the binary (ADR-03 §6, ADR-10 §1); the
coherence check is the boot-time manifest-root equality plus the dispatch-table
bijection (ADR-08 §2, ADR-10 §2).

## 4. The walking skeleton (constraint #6)

The first buildable slice, end to end, before any feature: **admit one pure function
through the full gate → definition row → evaluate it via HTTP → respond.** Concretely:
submit a module containing one exported async-free function of serializable arguments;
the gate runs parse → lower → grammar gate → print + hash → tsgo → verifiers → insert →
pointer CAS; an HTTP request resolves the name, loads the AST by hash, drives the CEK
machine under the governor, and returns the value. A second submission of a
fuel-metered sandbox function that exhausts its budget must park and signal.

The skeleton bootstraps against a micro-std: only the grammar-owed surface (ADR-01's
`Iter`/`keys`/`all`/`race`/`signal` signatures plus `std/contract`), admitted through
the same genesis mechanism ADR-10 §2 defines, so I1/I2 hold from the first boot.

**Kill-tests that gate the skeleton (all green before the world grows):**

- **Printer round-trip + idempotence:** ADR-02 §5 guarantees 1–4; the mutation matrix;
  the property fuzzer; the world-rehash canary running nightly from day one.
- **Continuation crash/resume suite:** ADR-05 tests 1, 2, 4, 5, 7 — crash mid-await
  with cross-kernel resume, clock-advanced resume, poison-pill rejection + corrupt-CFR
  fail-closed, double-resume CAS race, torn-write injection at every statement boundary.
- **Fuel exhaustion → durable condition:** mid-expression exhaustion parks cleanly,
  signals `fuel.exhausted` with `grant-fuel`/`abort`, resumes to the identical result
  (ADR-04, ADR-05 test 6); governor `runaway` on a trusted `while(true)`.
- **Admission rejection:** verifier RAISE leaves zero trace (no row, no DDL, no pointer,
  no admission row — ADR-03's red-path list); one rejection fixture per ADR-01 §2 ban
  with stable diagnostic codes; concurrent same-name admissions resolve to exactly one
  winner.

## 5. Build order: staged milestones

Each milestone's gate is a set of red-path tests already enumerated in the ADRs. The
ladder is a **machine gate, not a promise** (R1-11: staging is now mechanism, not
process): a milestone does not open until the previous gate is green, and CI — not
review discipline — enforces it (§5.1).

| Milestone | Builds | Gate |
|---|---|---|
| **M0 — walking skeleton** | ADR-02 printer/encoder/hash; ADR-01 lowering + grammar gate (full ban set); ADR-03 five tables + admission transaction; ADR-06 wire client (minimal scope) + bounded pool; ADR-04 CEK core with both meter instantiations; ADR-05 CFR codec + claim/lease + conditions/restarts (minimal); micro-std genesis; HTTP evaluate | §4's four kill-test families; ADR-04 §8 performance budgets benchmark-enforced before M0 closes — CEK-steps/sec floor (≥ 1M/core), transitions/request ceiling (≤ 50k p95), metering-tax (≤ 10%) and checkpoint-write budget, budget regression is red (R1-07: perf budgets gate M0) |
| **M1 — the gate, hardened** | ADR-07 complete: hermetic tsgo module host, typecheck budget, six verifiers, Verdict, no-op short-circuit; adversarial harness (hostile corpus, dual mutation testing, coverage rows) | ADR-07's red-path list: hermeticity, typecheck DoS, racing admissions, info-leak probe, harness self-test |
| **M2 — runtime complete** | ADR-05 full (all five wake kinds, joins, outbox); ADR-06 full (task table, drain, reaper, epoch-guarded caches, cron); ADR-04 conformance harnesses — three, not two (R1-INT: count updated, R1-02): test262 subset, base-dialect differential fuzz, and the regel-native differential oracle against the independent reference reducer | ADR-05 tests 1–10; ADR-06 red paths (dead kernel, mid-query poison, PG failover, stale cache, zombie fence, wake storm); conformance corpora green |
| **M3 — the world** | ADR-10: full genesis, the 14-battery roster, erf `resource(...)` + ten derivation artifacts, 13 field types, taak await-as-checkpoint, the 25 components (definitions) | Genesis reproducibility + mid-genesis kill; dispatch bijection; NativeBody unwritability; roster totality; exactly-once per effectful await; PII derivation rejects |
| **M4 — the reactive layer** | ADR-11: static/dynamic split pass, patch codec, 15KB client, sessions-as-rows, subscriptions + invalidation, forms/concurrent-edit, two-layer masking | ADR-11's red paths: exactness, kernel death mid-session, divergence resync, 50k-session storm, size cap, concurrent edit, PII grep; the WAN-throttled felt-latency machine gate (ADR-11 §9, `wan-150`: input→echo ≤ 50 ms, action→confirmed-commit ≤ 300 ms p95) and the checkpoint-write budget end-to-end (≤ 1 write/interaction) — budget regression is red (R1-07: felt-latency + checkpoint-write gate M4) |
| **M5 — the surfaces** | ADR-09: outbound fold, mirror + self-heal, PR dry-run checks, merge-as-admission; ADR-12: 11 tools + resources + prompts, admission fuel + refusal ledger, approval tokens, operator plane's four panels | SHA reproducibility on two machines; merge-side-door impossibility; force-push restore; projection-leak grep; MCP exfil sweep; spam flood; scope escalation; wrong-continuation restart fence |
| **M6 — epochs → v1** | ADR-08: epoch/std_manifest/`epoch_current` tables live from genesis (M0), now the `migrate N` machinery, findings-as-rows, prepared re-admissions, golden-continuation corpus, patch-epoch drill, the O5 per-transaction fence + terminal drain (ADR-06 §6), the bad-epoch revert/roll-forward runbook (ADR-08 §6a); the reference product (ADR-10 §3) built on everything above | O1–O5 as release gates; stranded-continuation impossibility across two epoch boundaries; boot refusal with structured-diagnostic shape asserted; running-kernel fence fail-close; O4 park-vs-commit race atomicity; 400-breaks drill; emergency `E.1` drill and bad-epoch revert drill under a measured clock (R1-05: fence + revert drill gate M6); reference app green end to end; reference-dashboard stranger-review gate — an outside reviewer's "does this look finished?" verdict on the chart-free stat-tile/table dashboard recorded as a gate entry (R1-14: M6 stranger-review gate on the reference dashboard) |

### 5.1 The milestone ladder is a machine gate (R1-11: manifest + mechanical CI refusal + audited escape hatch)

The ordering above is enforced by mechanism, not intention. R5's former residual —
"staging is process, not mechanism" — is closed here, and this gate is the substrate the
earlier milestone-gated obligations compile onto: R1-03's recovery drill, R1-05's epoch
fence/revert drill, R1-07's perf + felt-latency budgets, and R1-10's coverage floors are
only real if the ladder actually gates.

**The `milestone-gates` manifest.** A single in-repo, machine-readable manifest
(`spec/milestone-gates.toml`, versioned with this corpus) is the authority
(R1-11: declared machine-readable gate manifest). For each milestone M(n) it lists (i)
the gate suites that constitute M(n)'s gate-set — each by its exact CI job name — and
(ii) the path/label globs that classify a change as belonging to M(n). The manifest is
itself gate-checked (the self-test below), so it cannot silently drift from the ADRs it
mirrors.

**Gate-sets, by milestone** (R1-11: existing gates wired to their milestone gate-set).
Each row's suites are `required` status checks for any branch classified to that
milestone or higher:

| Milestone | Gate-set = §4/ADR red-paths + wired R1 gates |
|---|---|
| **M0** | §4's four kill-test families; R1-07 perf budgets (CEK-steps/sec floor, transitions/request ceiling, metering-tax, checkpoint-write); R1-01 I4 temporal-exclusion overlap kill-test executed against a real Postgres of the deployed major version — PG16+ pin is sufficient, the exclusion is verified creatable on PG16.13 (R1-INT: PG16+ note, R1-01); R1-03 immortal-store fault-injection recovery drill (ADR-03 §4a, ADR-02 self-certifying restore); R1-10 world-rehash canary replaying parse→lower from `canonical_text` (ADR-02); R1-06 signal registry compiled + stdout event emission + the stall alarm (ADR-13 §1/§4, test 7) (R1-INT: ADR-13 suites wired) |
| **M1** | ADR-07 red-path list (hermeticity, typecheck-DoS, racing admissions, info-leak, harness self-test); R1-02 regel-native differential oracle, seeded wrong-evaluation red (ADR-07 §5); R1-09 parse-depth ceiling ahead of all budgets (ADR-07 §3); R1-10 dual-mutation extended to grammar gate + resolver (ADR-07 §5); R1-07 tsgo-in-txn concurrency budget — N=32, p95 ≤ 40 ms / p99 ≤ 80 ms, retry ≤ 5%, overflow shed as `ADMISSION_BUSY` (ADR-07 §3; gate-enforced at M1, when the hermetic host first exists) (R1-INT: tsgo budget wired) |
| **M2** | ADR-05 tests 1–10; ADR-06 red paths; conformance corpora green; R1-02 conformance oracle (ADR-04 §6); R1-10 `continuation_coverage` monotone floor + cross-kernel randomized hermeticity probe (ADR-05 §8.5, ADR-04 §6.5); R1-06 push exporter + Postgres-loss visibility drill + reaper-saturation breaker drill (ADR-13 §4/§5, tests 1–2) (R1-INT: ADR-13 suites wired) |
| **M3** | genesis reproducibility/bijection/NativeBody/roster/effect/PII-derivation red paths; R1-09 native-TCB adversarial harness + boot-time dispatch-table attestation (ADR-10 §2/§8) |
| **M4** | ADR-11 red paths; R1-07 WAN-throttled felt-latency + checkpoint-write gate (`wan-150`, ADR-11 §9); R1-06 fan-out/resync SLOs calibrated — `sse.fanout_lag_ms` p95 ≤ 500 ms, `sse.resyncs_total` < 0.1% of frames (ADR-13 §3) (R1-INT: ADR-13 SLO calibration wired) |
| **M5** | SHA-reproducibility, merge-side-door, force-push restore, projection-leak, MCP exfil, scope-escalation, wrong-continuation restart fence; R1-04 confused-deputy injection corpus, M5-blocking (ADR-12 §4a); R1-09 timing-indistinguishable name resolution (ADR-12 §3); R1-13 authoring-eval pass@k floor — pass@1 ≥ 0.5 AND pass@k ≥ 0.9 against the real ADR-07 pipeline, M5-blocking (ADR-12 §3a) — plus agent fuel capacity traceable to the eval's iterations-to-green P95 (ADR-12 §5); the restart-decision accuracy floor (≥ 0.95) gates the agent-facing `condition.restart` *authority*, not M5 — red/absent metric ships the tool disabled (ADR-12 §7) (R1-INT: R1-13 gates wired; authority-narrowing policy restated, not widened) |
| **M6** | O1–O5 release gates; R1-05 running-kernel epoch fence + bad-epoch revert/roll-forward drill under a measured clock (ADR-08 §4a/§6a); reference app green end to end; R1-06 SLO recalibration on the reference workload + the epoch-flip drain SLO, with the Postgres-loss and reaper-saturation drills joining the release suite (ADR-13 §3, tests 1–2) (R1-INT: ADR-13 M6 wiring); R1-14: reference-dashboard stranger-review gate — an outside reviewer's recorded "does this look finished?" verdict is a `required` gate entry, mechanically: the review having happened and its verdict being recorded is the gate (a missing or absent-verdict review reads as red, like any un-run suite), ADR-10 §7 / C3 |

The M0/M4/M6 R1-07 and R1-05 gates keep their own markers in the table above; this
manifest ties them into the machine gate rather than restating them.

**Attribution is mechanical** (R1-11: how work is classified to a milestone). A change's
milestone is the highest M(n) whose manifest path/label globs it matches; a change
matching nothing defaults to the lowest still-open milestone, so nothing lands ungated.
CI computes this from the diff plus PR labels against the manifest — no human classifies
a branch into a weaker gate-set.

**CI refuses the merge** (R1-11: mechanical refusal via branch protection, not review
discipline). Branch protection makes every suite in the gate-set of every milestone
`≤` the branch's classified milestone a *required* check. A branch classified M(n+1)
cannot merge while any suite in M(0..n) is red, quarantined, or skipped — a quarantined
suite reads as red to the gate (flaky is not green). The refusal is the forge's
required-check mechanism; a reviewer cannot wave it through.

**Escape hatch — one, explicit, audited** (R1-11: no silent bypass). There is exactly
one override: a signed `gate-override` on the PR naming the specific red suite and a
justification, admittable only by a release owner, recorded in an append-only override
ledger, and auto-expiring at the next tagged release (it never persists to the next
branch). Absent a logged override, red M(n) ⇒ no M(n+1) merge, mechanically. An override
never reconciled is itself a release-blocker at v1's re-run-everything suite.

**Red path — the gate of the gate** (R1-11: seeded-red M(n) must mechanically block an
M(n+1) merge — a test of the gate, not the gated). Seed a red M(n) kill-test (disable
one M1 verifier fixture) and open an M2-classified feature branch: CI must refuse the
merge *and* the refusal must name the red M1 suite; flip the fixture green and the same
branch merges. Removing a suite from the manifest without a matching ADR change, or
misclassifying an M2 diff as M0, must fail the manifest self-test. (This is Torvalds'
verify step: "land a fake M2 feature branch with an M1 kill-test disabled — CI must
reject it.")

v1 is M6's gate plus every prior gate re-run as the release suite. The reference product
(orgs → users/roles → Deal/Company/Contact/Ticket → two workflows → operator desk) is
the acceptance harness: nothing ships that it and its red paths do not exercise.

**Post-v1 closure test — product #2 is analytics-shaped (R1-14: the second product tests roster closure at the known charts/aggregation gap).**
The reference product above is N=1: the std roster is a closed framework proven total
against exactly one CRM, so its closure claim is *untested at exactly the gap the roster's
own reviewers named* — charts and cross-resource aggregation (ADR-10 §4/§7). The **second
product built on regel is therefore required to be analytics-shaped** — deliberately
chart/aggregation-hungry — so closure is attacked at that known gap rather than declared
vacuously by a softball second CRM that never exercises it. This is a recorded commitment,
not a hope: the roster may not be declared closed until tier-1 composition (stat tiles +
tables over typed `std/sql`) is *measured* insufficient against a real analytics product,
or a chart epoch-addition is specced from *two* products' requirements (§6; ADR-10 §7). Its
v1-side counterpart — the M6 stranger-review gate, by which a human and not accretion
falsifies the charts gap before v1 ships — is wired into the M6 gate-set above (R1-14).

## 6. What v1 excludes

Collated from every ADR's deferrals. Each item names its trigger-to-build; "Rule of
Three" means the third product that needs it (ADR-10 §3).

| Exclusion | Owner | Trigger to build |
|---|---|---|
| AOT-to-Go lane (interface fixed, `aot_symbol` NULL everywhere) | ADR-04 §7 | Production profiling flags a verified, compute-bound hot function; candidate passes the conformance corpus differentially |
| std/ self-hosting (interpreting std bodies from rows) | ADR-10 §1 | Per-definition `supersedes` re-admission of a pure-logic battery in a future epoch, when interpretation cost is acceptable |
| Wire-client pipelining, COPY, binary result format | ADR-06 §2 | Measured latency or bulk-transfer need; each is additive behind the same client |
| Batteries: `std/mime`, `std/csv`, `std/files`, `std/i18n` translation rows, mail templates | ADR-10 §3 | Rule of Three; earlier needs arrive as framework-authored capability-gated bindings |
| Field types: `file`, `json`, `richtext`, `percent`, `duration`, `geo`, `computed` (`multiselect` is **not** deferred as a type — it ships as `relation`-desugaring, V6-parity-checked sugar iff the reference app exercises a tag field — R1-14: multiselect-as-sugar, not a deferred type) | ADR-10 §5 | Rule of Three; `json` additionally requires an answer to derivation totality before it is ever admitted |
| Cross-resource aggregates and computed fields | ADR-10 §4 | Rule of Three; dashboards ride typed `std/sql` until then — this is the known gap the analytics-shaped product #2 must test closure against (R1-14: known charts/aggregation gap, §5) |
| Components: charts, tabs, menus, toasts, date-range picker, file upload, maps, calendar | ADR-10 §7 | Epoch addition only, and only where the composition answer (tier-1 assembly) is measured insufficient **against the analytics-shaped product #2** (R1-14: analytics-shaped closure precondition, §5) and the M6 stranger-review gate has run; a chart vocabulary is its own project, specced from two products, never one |
| Per-column invalidation granularity | ADR-11 §6 | Measured over-invalidation cost exceeding the slot-diff savings on production traffic |
| Optimistic local echo; offline drafts | ADR-11 §3, §9 | Held to the WAN-throttled felt-latency **machine gate** on M4→v1 (ADR-11 §9, `wan-150`), not user complaints: a failed input→echo / action→commit budget makes echo behind the tested client state machine the release-blocking remedy (R1-07: felt-latency machine gate) |
| Per-tenant overlay repo export | ADR-09 §5 | A tenant with an audit requirement that catalog queries do not satisfy; ships opt-in, scoped to the tenant's principal |
| Owned in-kernel git remote (computed-on-fetch) | ADR-09 §5 | Forge-mirror operational cost or trust requirement exceeding the determinism-plus-restore guarantee |
| Signed projection commits; incremental/sparse projection; derived-artifact export | ADR-09 §5 | Signing: a consumer that verifies signatures; sparse: catalog size making full folds slow; derived export: a reviewer need that regeneration does not meet |
| Bulk condition operations, custom operator dashboards, approval delegation, extended metrics | ADR-12 §7 | Operator-plane usage data from the reference deployment |
| Eager epoch re-addressing (`--rewrite`) | ADR-08 | Never as a mode; a team wanting eager re-addressing submits a batch of ordinary re-admissions |
| Cohort / per-scope epoch rollouts (mixed-epoch fleet) | ADR-08 §5 | None — foreclosed by ADR-06 §5 (one fleet, one epoch); reopening it means superseding ADR-06, not extending it |
| Tree-hash sub-node addressing | ADR-02 | A consumer for sub-definition addressing; none exists in this design |

---

*This document plus RISKS.md plus ADR-01..13 are the complete Phase 1 output; the R1
revision ledger is REVISIONS-R1.md. (R1-INT: range updated for ADR-13.)*
