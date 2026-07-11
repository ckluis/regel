# regel — Phase 1 Risk Register

*Ordered deepest-bet-first. Every risk carries six fields: breaks / blast / signal /
mitigation / residual / kill-tests. The top four are the bets the design stands on.*

---

## R1 — Canonical printer and hash identity (ADR-02)

- **Breaks:** an encoder/normalizer bug splits identity (two renderings of one program
  hash differently — dedupe fails, deps dangle) or collides it (two programs, one hash —
  the wrong code evaluates); an uncontrolled AST-schema change re-addresses the world,
  orphaning every stored continuation, dep edge, pointer, and git SHA.
- **Blast:** total — `r<n>_` is the only code identity (ADR-03 PK, ADR-05 frames,
  ADR-09 catalog.lock, ADR-12 tokens). Nothing survives a moved hash.
- **Signal:** nightly world-rehash canary firing (ADR-02 §5); ADR-03 scrubber
  bytes/address mismatches; any round-trip property-fuzz failure in CI.
- **Mitigation:** identity on owned AST bytes, never text or tsgo output (ADR-02 §1);
  guarantees 1–4 enforced at every insert (ADR-02 §5, ADR-03 §5.3); mutation matrix +
  adversarial corpus + fuzz as release gates; `r<n>` in every address with append-only
  decoders — new schema versions re-hash nothing; re-addressing only via explicit
  re-admission with `supersedes` (ADR-02 §6). R1-INT: (R1-03) detection no longer
  dead-ends — scrubber-caught byte corruption has a rehearsed, release-gated
  self-certifying byte-restore (correct iff it rehashes to the address; fails closed; no
  role ever regains UPDATE — ADR-02 §5.5, ADR-03 §4a + CI Gate 4), so "nothing survives
  a moved hash" now names blast radius, not an unrecoverable end state. R1-INT: (R1-10)
  the world-rehash canary runs two legs — stored-AST encoder leg plus a load-bearing
  parse→lower replay from `canonical_text` — so it can see the printer-drift class it
  watches.
- **Residual:** a hash-stable *semantic* bug — normalize mapping two behaviorally
  different programs to one encoding — is invisible to the canary and round-trip suite;
  only finite corpus coverage stands against it. SHA-256 collision accepted as
  negligible.
- **Kill-tests:** mutation matrix (both halves); world-rehash canary; property fuzz of
  guarantees 1–3; NFC/NFD, `-0`/`0`, alpha-equivalence corpus; cross-epoch
  `r1`-untouched test (ADR-02 §5–6).

## R2 — Continuation serialization (ADR-05, ADR-04, ADR-01)

- **Breaks:** a CFR gap, frame-kind drift, or capture hole makes a parked program
  unresumable (workflow stuck forever) or resumable to a different result (silent state
  corruption); a semantics change without an `r<n>` bump makes year-old resumes compute
  new answers.
- **Blast:** every paused program — workflows, durable conditions, and (via ADR-11)
  every UI session: the highest-traffic surface shares the bet.
- **Signal:** `step.failed` conditions from CFR deserialization (alarmed, ~zero);
  golden-continuation corpus divergence at release; V5 rejection rate spiking after a
  std/type change.
- **Mitigation:** capture discipline as grammar (ADR-01 R1–R5) closed by V5, which
  shares one type table with the codec — encodable ≡ admitted, so pause-time
  serialization is total (ADR-05 §3); Control anchored to `(def_hash, node_path,
  phase)`, never bytecode or Go stacks (ADR-04 §2); readers append-only forever, resume
  always by content hash, lattice narrowing enumerated at epoch admission (ADR-05 §8,
  ADR-08 O1–O4); `step_seq` CAS + lease for exactly-once (ADR-05 §7). R1-INT: (R1-01)
  as-of soundness — one hash per (name, scope) instant — is enforced by the now-creatable
  I4 temporal exclusion on the unpartitioned history table, proven by the CI
  overlap-rejection kill-test (ADR-03), so a resume can no longer bind the wrong immortal
  code through a two-headed as-of. R1-INT: (R1-10) decode coverage is data with a
  monotone floor (`continuation_coverage`, ADR-05 §8.5) and resume determinism is probed
  cross-kernel under randomized scheduling (ADR-05 test 12).
- **Residual:** "stable for years" is proven only under simulated clocks until real
  years pass; the golden corpus covers captured fixtures, not every reachable state;
  per-`r<n>` semantics accretion is permanent kernel surface; `external` effects are
  effectively-once by dedup key, not exactly-once — the stated honest limit.
- **Kill-tests:** ADR-05 tests 1–10 (crash mid-await, year-old resume, as-of resume,
  poison-pill, double-resume race, fuel park, torn write, revoked capability, wake
  storm, join recovery); ADR-08 stranded-continuation and semantic-drift tests; the
  golden corpus per epoch.

## R3 — Admission correctness: verifier coverage IS the security boundary (ADR-07)

- **Breaks:** a V1–V6 gap admits code naming ungranted capabilities, leaking vault
  values, or immortalizing a PII literal in the INSERT-only store (undeletable —
  ADR-03's immortality/crypto-shred interaction). Trusted code runs on a shared heap:
  an admitted bypass is arbitrary behavior inside kernel authority. A hermeticity leak
  makes verdicts nondeterministic and the gate probeable.
- **Blast:** the trust model — one gate means one bypass class works from every door
  (CLI, Settings, agent, git merge).
- **Signal:** `verifier_coverage` mutation-score dips (monotonicity is a release
  blocker); surviving mutants in either dual-mutation direction; refusal-ledger probing
  patterns (ADR-12 §5); any field-reported bypass (each becomes a permanent fixture).
- **Mitigation:** six small pure verifiers, everything relocatable relocated to
  grammar/printer/resolver (ADR-07 Context); fail-closed stages over a frozen
  SERIALIZABLE snapshot; hermetic module host + deterministic typecheck budget
  (ADR-07 §2–3); hostile corpus, dual mutation testing, grammar fuzz, coverage as
  monotone queryable data (ADR-07 §5); runtime defense in depth — empty globals,
  sealed handles, absent capability slots (ADR-04 §5).
- **Residual:** coverage is enumerative, not proof — the suite catches named threat
  classes, and structural-variance holes survive in the trusted tier by explicit budget
  (BRIEF #5). Trusted-by-verification is a bet on the harness, permanently.
- **Kill-tests:** one fixture per verifier (CAP_UNGRANTED, PII_ESCAPE + PII_LITERAL,
  PARITY_UNWIRED, CONTRACT_EFFECTFUL, CAPTURE_UNSERIALIZABLE, DERIVE_PARTIAL +
  DDL_DESTRUCTIVE); hermeticity byte-identity; typecheck-DoS; info-leak probe; harness
  self-test with seeded mutants (ADR-07).

## R4 — Interpreter conformance and fuel fairness (ADR-04)

- **Breaks:** the owned machine diverges from TS semantics on an edge (`0.1+0.2`,
  coercions, sort stability, Unicode lengths) and admitted logic silently computes
  wrong values — tsgo checked types, not evaluation. Mis-charged fuel starves
  legitimate tenants or under-prices hostile code; a governor gap lets a trusted loop
  pin a kernel.
- **Blast:** every evaluated definition; corrupted values propagate into app rows and
  history, outliving the bug.
- **Signal:** differential-fuzz divergence corpus gaining entries; test262-subset
  regressions; `runaway` frequency; metering-tax benchmark drift.
- **Mitigation:** semantics never hand-reasoned — curated test262 subset + differential
  fuzzing against a dev-only `node` oracle, pinned to the epoch (ADR-04 §6); one
  machine, one value model, every ADR-01 ban is engine never built; two monomorphized
  meters — trusted pays no metering branch yet stays bounded (ADR-04 §4);
  park-not-panic exhaustion through the standard ADR-05 path.
- **Residual:** the fuzz generator's distribution defines findable divergence —
  conformance is corpus-strong, not proven; the interpreter tax is permanent in v1 (the
  AOT seam is reserved, not built), so a compute-bound surprise has no fast lane.
  R1-INT: (R1-02) Bach's P0-2 — the oracle structurally blind to regel-added semantics —
  is mitigated: harness 3's regel-native differential oracle (independent reference
  reducer; contract enforcement, derived boundary validators, effect-class ordering;
  seeded-mutant-validated) is release-gating (ADR-04 §6, ADR-07 §5). The residual
  narrows to oracle-corpus breadth: the oracle sees what its corpus exercises, so
  wrong-evaluation coverage is corpus-strong in the same sense as the base fuzz.
- **Kill-tests:** test262-subset green + empty divergence corpus per epoch;
  throw-across-await/finally vs oracle; deep-recursion bounded-stack serialize/resume;
  fuel park mid-expression; governor `while(true)`; metering-tax benchmark (ADR-04).

## R5 — One team, two build burdens (BRIEF #6)

R1-INT: R5's severity was materially reduced by R1-11 (the ladder is now a machine gate,
§5.1). It keeps this slot — the register is ordered deepest-bet-first, not by current
severity, and renumbering would break the "R5"/"R6" cross-references in ARCHITECTURE
§5.1 and ADR-13.

- **Breaks:** engine work (printer, machine, CFR, conformance) and substrate work
  (gate, catalog, world, surfaces) compete; feature pressure lands surfaces before the
  deep bets are proven, and a late R1–R4 failure forces rework of everything above.
- **Blast:** schedule and design credibility; a printer or CFR redesign after M3
  invalidates the stack built on it.
- **Signal:** milestone gates skipped or partially green; kill-test suites quarantined
  as flaky; feature branches landing ahead of their milestone's gate.
- **Mitigation:** the walking skeleton and its four kill-test families precede any
  feature (ARCHITECTURE §4–5); red-path-first test lists in every ADR; minimal v1
  surface via the deferral lists (ARCHITECTURE §6); and the ladder is now a **machine
  gate** — a `milestone-gates` manifest declares each milestone's gate-set and
  path/label attribution, and branch protection makes M(0..n)'s suites required checks
  so a branch classified M(n+1) cannot merge while any earlier suite is red, quarantined,
  or skipped (R1-11: ARCHITECTURE §5.1).
- **Residual:** staging is now mechanism, not process — gate-before-next-milestone is a
  forge required-check, not a human refusal (R1-11: former "no machine" residual closed).
  The residual narrows to three failure modes, each mitigated: **manifest rot** (the
  gate-set drifting from the ADRs) — the manifest self-test fails on undeclared suite
  removal; **misclassified work** (a diff attributed to a weaker gate-set) — attribution
  is mechanical and unmatched diffs default to the lowest open milestone; **override
  abuse** — the sole escape hatch is a signed, release-owner-only, append-only-audited,
  auto-expiring `gate-override`, and an unreconciled override blocks the v1 release
  suite. What remains is trust in the manifest's fidelity to the ADRs and in release-owner
  restraint on overrides — bounded and audited, no longer unbounded human discipline.
- **Kill-tests:** the milestone gates themselves; the release suite re-runs every prior
  gate; the **gate-of-the-gate** self-test — a seeded red M(n) kill-test must mechanically
  refuse an M(n+1) merge and name the red suite, and a manifest that drops a suite or
  misclassifies a diff must fail the self-test (R1-11: ARCHITECTURE §5.1 red path).

## R6 — Performance envelope: interpreted TS on one Postgres (ADR-04/06/03)

- **Breaks:** per-transition overhead, one transaction per effectful workflow await
  (ADR-10 §6), a SERIALIZABLE gate, and every queue/session/wake on one Postgres sum
  past the I/O-bound envelope under real tenant load.
- **Blast:** viability at scale, not correctness — mechanisms stay right, they get slow.
- **Signal:** step-throughput and metering-tax benchmarks trending down; Postgres
  lock/IO saturation on `task`/`continuation`; `tsgo_ms` and serialization-retry rates
  in the ledger; SSE patch latency percentiles. R1-INT: (R1-06) these are now *named*
  golden signals with SLOs and a Postgres-independent emission path (`cek.steps_total`
  stall alarm, `admission.tsgo_ms`, `task.ready_depth`, `reaper.lag_ms`,
  `sse.fanout_lag_ms`, `pg.*` — ADR-13 §2–§4), and the reaper retry-stampede failure
  mode is damped by bounded batches + the reap-rate breaker (ADR-13 §5, ADR-06 §5).
- **Mitigation:** heavy lifting in SQL by construction (ADR-06); slot diffing keeps the
  interpreter off tree reconciliation (ADR-11 §1); immortal-by-hash caches +
  affected-set typechecking (ADR-06 §3, ADR-07 §2); bounded pools and storm draining
  (ADR-11 §6); scale up = bigger Postgres (ADR-06 §5); AOT seam reserved (ADR-04 §7).
- **Residual:** R1-INT: (R1-07) "asserted, not yet measured" is discharged — the
  envelope carries benchmark-enforced `perf_budget` numbers from M0 (CEK-steps/sec
  floor, transitions/request ceiling, metering-tax, checkpoint-write; ADR-04 §8), the
  tsgo-in-txn budget under N=32 concurrency at M1 (ADR-07 §3), and the `wan-150`
  felt-latency gate at M4 (ADR-11 §9). What remains: the numbers are initial until
  calibrated on the reference product's load (M4/M6), and one Postgres is a hard
  ceiling accepted by design — vertical scaling only.
- **Kill-tests:** ADR-05 wake storm; ADR-11 50k-session storm within drain budget;
  metering-tax benchmark; ADR-07 typecheck-DoS latency isolation; reference-app load
  run at the M6 gate.

## R7 — std/ genesis and dual-representation drift (ADR-10, ADR-03, ADR-08)

- **Breaks:** std rows and native Go are two artifacts from one source; a build defect
  ships a binary disagreeing with the catalogued signatures/contracts, or genesis
  differs across databases — two "identical" deployments diverge.
- **Blast:** every app definition (everything imports std); reproducibility claims;
  the projected `std/` tree.
- **Signal:** boot-time manifest-root or dispatch-bijection failures in staging; std
  conformance-gate failures in CI; contract violations at std call boundaries.
- **Mitigation:** one source, four artifacts, canonicalized at build time by the real
  printer and gate-admitted in CI — nothing canonicalized at boot (ADR-10 §1–2); boot
  refusal on root mismatch + dispatch bijection (ADR-08 §2, ADR-10 §2); `NativeBody`
  structurally unwritable through the live gate.
- **Residual:** the bijection proves hash↔symbol coverage, not behavioral equivalence
  of native bodies to their catalogued contracts. R1-INT: (R1-09) "tests, not proofs"
  is downgraded, not erased — the native TCB is now *attacked* by the release-gating
  adversarial harness (seeded vault-leaking / contract-violating / effect-order-violating
  native bodies must be caught; ADR-10 §8), the binary is no longer an unattested trust
  root (`H_dispatch` pinned in the epoch row, recomputed every boot; ADR-10 §2, ADR-08
  §2), and the irreducible remainder is enumerated as explicit trusted-for rows rather
  than silently assumed.
- **Kill-tests:** genesis reproducibility across two fresh databases, byte-identical;
  mid-genesis kill ⇒ empty-or-complete; bijection boot refusal both directions;
  NativeBody rejection at lowering (ADR-10 §2).

## R8 — PII masking totality (ADR-10 §7, ADR-11 §8, ADR-07 V2, ADR-12 §4)

- **Breaks:** one unmasked path — a missed V2 sink, a seventh de-facto binding leaf,
  plaintext in a slot snapshot or CFR blob — leaks vault data; a PII literal reaching
  the immortal store is undeletable, defeating crypto-shred.
- **Blast:** regulatory and contractual; one leak class is systemic because derivation
  stamps the same pattern everywhere.
- **Signal:** PII-grep kill-test failures in CI; reveal-audit rows without matching
  grants; V2 corpus fixtures arriving from field reports.
- **Mitigation:** exactly six masking leaves, no raw-HTML, no `unsafeHtml` (ADR-10 §7);
  V2 taint analysis including `PII_LITERAL` before anything is immortalized (ADR-07,
  ADR-03); runtime leaf materialization under live reveal grants, plaintext never in
  durable state (ADR-11 §8); agents grant-ineligible by CHECK, three independent
  layers (ADR-12 §4); the projection structurally PII-free (ADR-09 §5).
- **Residual:** V2's sink set is enumerated — a novel channel (error shape, a new
  battery's output path) is outside it until named; every vocabulary or battery addition
  re-opens the totality proof. R1-INT: two formerly-open channels are now partially
  closed — kernel telemetry is structurally typed-fields-only and CI-swept for seeded
  PII (ADR-13 §6, R1-06), and the cross-tenant name-resolution timing oracle is closed
  by the shared fast-fail path + latency floor with a statistical release gate (ADR-12
  §3, R1-09); other timing channels remain outside the sink set until named.
- **Kill-tests:** no-plaintext-without-grant grep across session rows, CFR blobs,
  subscriptions, frames (ADR-11 §8); MCP exfiltration sweep incl. error paths
  (ADR-12 §4); PII-literal rejection (ADR-07 V2); projection-leak grep (ADR-09).

## R9 — Git projection determinism drift (ADR-09)

- **Breaks:** the fold stops reproducing (a field picks up wall clock; the path
  function moves) — mirror SHAs diverge, self-heal loops, and "the repo is a view"
  loses trust; an inbound timing hole lands unverified code on `main`.
- **Blast:** the trust surface and review workflow; the image is never corrupted (the
  fold is read-only over the ledger).
- **Signal:** SHA-reproducibility release-gate failure; force-restore audit rows
  without a forge-side cause; clone-and-resubmit yielding anything but
  already-admitted.
- **Mitigation:** every commit field a pure function of ledger data; projector-only
  `main`; merge-action-as-submission (the forge cannot merge without the gate); the
  name→path function shared with ADR-07's module host (ADR-09 §2–4).
- **Residual:** any projector change requires whole-history re-verification; forge
  outage degrades review (accepted); approved diffs and landed bytes differ in trivia
  the printer owns (stated in ADR-09).
- **Kill-tests:** byte-identical SHAs on two machines and from an empty mirror;
  merge-side-door impossibility; force-push mangle restore; rename fidelity;
  round-trip short-circuit (ADR-09).

## R10 — scherm invalidation storms (ADR-11)

- **Breaks:** a wide-horizon mutation fans out to tens of thousands of sessions;
  unbounded re-render bursts pin kernels and flood Postgres with checkpoints — the
  reactive layer becomes the system's own DoS.
- **Blast:** interactive UX fleet-wide during the storm; no correctness loss (sessions
  are rows; a late patch is still a correct patch).
- **Signal:** resync rate on the health surface; invalidation-queue depth and drain
  latency; coalescing ratios; session-cap truncation counts.
- **Mitigation:** bounded worker pool spreading fan-out across ticks, per-session
  coalescing (ADR-11 §6); horizon-keyed subscriptions — invalidation respects policy
  and never crosses tenants; slot-level diff bounds per-session work; the 256 KB CFR
  cap (ADR-11 §5).
- **Residual:** per-row/per-horizon granularity over-invalidates within a horizon
  (per-column deferred); the drain budget trades staleness for survival — patches
  arrive late, not never.
- **Kill-tests:** 50k-session storm within budget with kernel liveness; exactness
  (out-of-horizon sessions receive zero frames); double-event idempotence; size-cap
  truncation preserving drafts (ADR-11).

## R11 — Agent abuse of the gate (ADR-12)

- **Breaks:** an agent floods the gate (typecheck is the expensive stage), probes
  verdicts, escalates to product scope, or social-engineers an approval; the gate
  degrades for everyone or approved bytes drift. R1-INT: (R1-04) the second adversary
  class is named — the **confused deputy**: an attacker who cannot author but seeds
  content the trusted agent reads (resource rows, condition messages, audit rows,
  docstrings), steering it to author a verified-but-malicious patch with the victim's
  own grants.
- **Blast:** gate availability and product-scope integrity; overlay damage is already
  confined to one scope (ADR-03).
- **Signal:** refusal-ledger volume/pattern per principal; admission-fuel exhaustion
  rates; approval-token mismatch rejections.
- **Mitigation:** per-principal admission-fuel budgets charged by deepest stage,
  checked before `BEGIN` (ADR-12 §5); default-deny product scope, one-shot
  human-approved tokens bound to exact hashes (ADR-12 §6); verdict leak discipline and
  scope-filtered reads (ADR-07 §6, ADR-12 §3); deterministic verdicts make probing
  sterile; escalation attempts recorded as evidence. R1-INT: (R1-04) the confused-deputy
  class is gated — M5-blocking injection corpus co-equal with the PII sweep (ADR-12
  §4a, reverts to P0 if downgraded), content-seeder attribution names the third
  principal on every agent admission (ADR-12 §6), and the approval queue refuses
  approval of a surface-widening patch without the machine-computed capability/PII/DDL
  delta rendered (ADR-12 §7). R1-INT: (R1-13) agent *competence* is eval-backed, not
  assumed — authoring pass@k floor gates M5, fuel capacity derives from measured
  iterations-to-green P95, and restart-decision accuracy gates the agent restart
  authority (ships disabled until green).
- **Residual:** the approving human is the remaining product-scope attack surface — a
  persuasive wrong patch with a green dry-run Verdict is approvable, though the approver
  now judges a rendered blast-radius delta and seeder set, never a bare green (R1-INT,
  R1-04); budget tuning is operator judgment (agent-kind capacity excepted — it is
  eval-derived, R1-13); and the competence evals see only what their suites span —
  eval-suite breadth is the R1-13 residual.
- **Kill-tests:** spam flood with latency isolation; escalation with/without token;
  token replay and post-approval byte drift; unnameable-reads byte-identity;
  exfiltration sweep (ADR-12).

---

## Bets we are consciously making

1. **State-capture over replay** (ADR-05 §1): if CFR stability fails in the field,
   there is no replay log to fall back on.
2. **Trusted-by-verification on a shared heap** (ADR-07, BRIEF #5): six verifiers plus
   a harness stand in for memory isolation; coverage is enumerated, never complete.
3. **An owned interpreter with no fast lane in v1** (ADR-04): conformance by corpus,
   performance by envelope argument; the AOT seam is an interface, not an escape.
4. **One Postgres as the ceiling** (ADR-03, ADR-06): gate, queue, sessions, and
   history bound by one database; the scaling answer is vertical.
5. **Identity frozen in an owned encoder forever** (ADR-02): `canonEncode` and its
   decoders are kernel surface for life — a permanent, deliberately small covenant.
6. **A closed world and closed vocabulary** (ADR-01, ADR-10): corpus friction is the
   accepted price of total printing, total derivation, and the six-leaf masking proof.
   R1-INT: (R1-14) the one-app-closure risk inside this bet — a roster proven total
   against exactly one CRM — is mitigated by riders, not faith: rosters bias-to-defer
   under the reversibility asymmetry, product #2 must be analytics-shaped to attack
   closure at the known charts/aggregation gap, and the M6 stranger-review gate puts a
   human verdict on the chart-free reference dashboard before v1 (ADR-10 §5/§7,
   ARCHITECTURE §5/§5.1). Residual: closure remains unproven until product #2 actually
   runs.
