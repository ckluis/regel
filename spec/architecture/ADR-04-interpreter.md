# ADR-04: The owned interpreter

## Status

Accepted — Phase 1

## Context

BRIEF §4 requires the interpreter strategy: own-vs-bootstrap, the execution
representation, the tsgo relationship, fuel-metering semantics, capability environments,
conformance testing, and the reserved taal AOT-to-Go seam. Constraint #2 names the owned
interpreter as the tax regel pays for dropping SBCL; constraint #1 requires that any
program can pause with its state as data.

Cross-ADR dependencies, stated explicitly:
- The machine executes exactly ADR-01's admitted node kinds — nothing else exists to
  execute. Its one suspension surface is `await` (ADR-01 banned generators); `throw` is
  within-evaluation unwinding over frames the machine owns.
- The machine's Control register is a pointer into the ADR-02 canonical AST: a
  definition hash plus a structural node path. Local bindings are De Bruijn slot indices
  (ADR-02 alpha-normalization), so environments are slot arrays, never name maps.
- Definitions are loaded by hash from ADR-03's immortal `definition` table; std
  definitions dispatch to native Go through their ADR-03 §6 mirror rows.
- ADR-05 serializes the machine state this ADR defines; ADR-06 hosts and drives it.

## Decision

### 1. Own pure-Go interpreter from day one — no bootstrap engine

No goja, no otto, no embedded JS engine, ever. An embedded engine's pause state lives in
Go stack frames and VM internals that cannot be serialized to a row stably for years —
the continuation bet (constraint #1) is precisely the capability that cannot be
delegated, so bootstrapping would build it twice and prove nothing. An embedded engine
also implements the exact surface ADR-01 bans. What is staged over epochs is the
admitted grammar, never the interpreter technology.

### 2. Execution representation: a defunctionalized CEK machine over the canonical AST

All three proposals converged on a defunctionalized CEK-style machine; the convergence
is **confirmed**. The machine is a small-step abstract machine with three fully-reified,
serializable registers:

- **C (Control):** `(def_hash, node_path, phase)`. `def_hash` is an ADR-02 address;
  `node_path` is the child-index path into that definition's canonical AST; `phase` is a
  small per-node-kind enum naming the reduction stage. Suspension anchors only to these
  immortal facts — never to a bytecode offset, Go stack, or struct layout — so the eval
  loop can be rewritten in any release without invalidating a stored continuation.
- **E (Environment):** a chain of immutable-once-captured activation records; each
  record is a slot array indexed by De Bruijn binder index. Reassignable `let` locals
  live only in the current activation; ADR-01 R1 guarantees captured records are
  immutable snapshots. The root record is the capability table (§5).
- **K (Kontinuation):** an explicit heap stack of frames `{kind, node_path, vals[]}`.
  There is one frame kind per admitted composite node kind (ADR-01 §3); the kind set is
  closed, versioned with the ADR-05 wire format, and append-only. There is no `YieldK`:
  generators are banned; `Iter`/`AsyncIter` state is a std opaque handle per ADR-01 R2.

R1-INT: C's `def_hash` is also what supplies the ADR-03 §3 resolver's `:caller_module`
binding (`module_of(def's name)`) during evaluation — the visibility predicate's caller
identity is a register fact, never caller-asserted (R1-12); external entry points carry
`''` per ADR-06 §4.

There is no bytecode, no interpreter-internal IR, no whole-program CPS pass. Recursion
is reified into K, so the Go stack stays bounded regardless of program depth, and *every
transition boundary is a valid pause point*: pausing is "stop stepping and write C/E/K
down." ADR-01's "handler-frame stack" is realized inside K as `TryK`/`FinallyK` frames:
`throw` pops K to the nearest `TryK`, crossing `await` correctly, and `finally` frames
re-run on resume because they are ordinary frames in the serialized stack.

An `await` either completes inline (the kernel performs the I/O on the same goroutine
and stepping continues) or parks (C/E/K serialize to an ADR-05 row and the goroutine is
released). Values are one closed tagged union — exactly ADR-01 R2's serializable
lattice: `null`, `undefined`, `boolean`, f64, `bigint`, `string`, array, record,
closure `(def_hash, env_ptr)`, capability token `(grant_id)`, and std opaque handles
that declare their own serialization. Frame and value allocations use freelists; this is
the accepted interpreter tax, carried by the I/O-bound envelope and the §7 seam.

### 3. tsgo: typechecker only — no emit path exists

tsgo is vendored, pinned, and invoked once per admission inside the ADR-03 transaction
to typecheck the canonical text against the catalog graph under ADR-01's locked config.
Its output is diagnostics; any emit capability is unused and unreachable. The machine's
only executable input is the ADR-02 canonical AST. Zero JavaScript exists anywhere in
the system, at rest or in flight.

### 4. Fuel: monomorphized meter, park-not-panic exhaustion

- **Step** = one CEK transition. **Allocation** is charged in shallow bytes at the
  allocating transitions (array/record/closure construction, string concatenation,
  spread copy). Two `int64` budgets, charged in the dispatch loop.
- The step function is generic over a `Meter` type parameter with exactly two
  monomorphized instantiations (grafted from the red-path proposal): `fuelMeter`
  (sandbox tier: step + allocation budgets, checked at each charge) and `governorMeter`
  (trusted tier: no billing; a transition counter checked every 4096 transitions against
  ADR-01 §4's generous step ceiling and wall deadline). One machine, one semantics, two
  compiled loops — the trusted tier pays no per-step metering branch, and "unmetered"
  stays un-billed, not un-bounded.
- **Exhaustion never panics and never aborts silently.** Because every transition
  boundary is serializable, the machine parks C/E/K as an ADR-05 continuation and
  signals the durable condition `fuel.exhausted` with restarts `grant-fuel` (operator
  or agent capability) and `abort`. Governor breach signals `runaway` per ADR-01.
  Fuel is thereby the same park/wake machinery as every other pause — no second path.
- A trusted frame calling a sandbox closure re-enters through the metered instantiation
  at that call; tier is a property of the definition's admission, carried on the frame.

### 5. Capability environments: unnameable, twice

The machine has zero ambient bindings — no global object, no `Math`, no `Date`
(ADR-01 §3). The kernel builds the root environment record per evaluation as a table
mapping capability id → sealed handle for **exactly** the grant set:
`root = intersection(definition imports, principal grants)`. A reference to a
capability-bearing std definition evaluates by lookup in that table. An ungranted
capability is therefore unnameable twice: the capability-audit verifier rejected the
reference at admission (it never became code), and defensively the slot is absent at
runtime (evaluation faults closed if ever reached).

Representation: a **grant** is a row `(subject, capability, scope, expires_at,
granted_by, admission_id)`; a **role** and an **API key** are bundle rows referencing
grants — scoped, expiring, audit-rowed (grafted from the prior-art proposal's ocap
model). A live capability value is a sealed opaque handle carrying its `grant_id`; per
ADR-01 R2 it serializes as a token `(grant_id)` and is re-validated and re-bound to a
live handle by the kernel at resume (ADR-05 §4). No user code can construct, forge, or
introspect a handle.

### 6. Conformance: differential testing, versioned with the epoch

JS semantics (`0.1+0.2`, coercions, `NaN`, Unicode lengths, sort stability) are never
hand-reasoned (grafted from the red-path proposal). Three dev-time harnesses gate every
release and are pinned to the epoch:

1. A curated **test262 subset** filtered to the ADR-01 admitted grammar, run against the
   machine.
2. **Differential fuzzing (base-dialect)**: generated type-correct, capability-free
   subset programs are executed by the machine and, via a harness-owned type-stripping
   projection of the canonical text, by `node`; observable output and thrown-error shape
   must match. `node` is a dev-machine test oracle only — it never ships in or near the
   kernel. This harness covers only the vanilla-TS core on which the machine and `node`
   agree by construction; it is structurally blind to the semantics regel adds on top
   (contracts, derived boundary validators, effects), which is why harness 3 exists.
3. **regel-native differential oracle (R1-02: covers the regel-added semantics
   type-stripping cannot see)**: the same program **plus its inputs** is evaluated by the
   production CEK machine (§2) and by an **independent reference reducer**, and any
   divergence is red. This oracle exists precisely because harness 2, by stripping types
   and excluding capabilities, validates only the subset where the machine and `node`
   agree by construction — it never touches the layers regel puts *in* the hash and
   derives from types. The oracle covers exactly those three layers:
   - **(a) Contract enforcement semantics** — pre/postcondition combinators (ADR-07 §4
     V4) discharged as boundary validators: whether a contract clause holds or is
     violated for the given inputs, and the resulting verdict (pass / which clause
     rejected), must agree.
   - **(b) Derived boundary validators** — the validators derivation emits from a
     resource's types (ADR-07 §5a): the same input value must be accepted or rejected
     identically, with the identical rejection subject, by machine and reference reducer.
   - **(c) Effect-class ordering** — the observable order and classification of effects
     produced by capability-bearing code (an effect trace over the capability table,
     ADR-04 §5): the sequence of effect classes emitted must match exactly.

**Differential, defined precisely (R1-02: divergence-is-red comparison rule).** For each
corpus case the harness runs the *same* canonical program and the *same* input vector
through both the production evaluator and the reference reducer and compares four
observables: (i) contract/validator **verdicts** (accept/reject and rejecting clause or
subject), (ii) validator **outcomes** per input, (iii) the **effect-class order** trace,
and (iv) the **produced values** (the ADR-01 R2 serializable lattice). Any divergence in
any of the four — not merely a thrown-error mismatch — turns the corpus red and blocks
the release.

**Independence of the reference reducer (R1-02: independently authored, no shared code
path).** The reference reducer is an independently authored, deliberately minimal
evaluator that shares **no** code path with the production CEK machine: not the step
function, not the frame-kind dispatch, not the contract/validator/effect implementations,
not the `Meter`. It may be slow, non-serializable, and continuation-free (it never needs
to pause), because it exists only to be a second, disagreeing witness. A bug that lives
in a routine both evaluators call is invisible to any differential test; forbidding
shared code paths is what makes divergence meaningful. The reference reducer is a
dev-machine artifact only — like `node`, it never ships in or near the kernel.

**Seeded wrong-evaluation requirement (R1-02: mutant in each of the three layers must
turn the corpus red).** The oracle is validated against itself by mutation: a suite of
deliberately-broken production-evaluator **mutants**, at least one seeded in **each** of
the three covered layers — (a) a contract-enforcement mutant (e.g. a postcondition
evaluated as always-satisfied), (b) a derived-boundary-validator mutant (e.g. a range
validator off by one), and (c) an effect-class-ordering mutant (e.g. two effect classes
transposed) — **must** turn the corpus red. A mutant in any covered layer that survives
(corpus stays green) is a coverage hole in the oracle itself and is a release blocker,
exactly as ADR-07 §5's dual mutation testing treats a surviving verifier mutant. This is
mutation-style validation of the oracle, not of the code under test.

Divergences land in a golden corpus that is a versioned artifact of the epoch; the AOT
seam (§7) reuses the same corpus as its admission bar.

BUILD-C (increment C4 — harness 3 realized, red-path-first). The reference reducer is
`internal/oracle`: an independently authored big-step tree-walker over the canonical
rast sharing no production evaluation code — its own value union, its own environment
chain (the De Bruijn contract is the lowered AST's, shared as data), and its OWN
implementations of the three covered layers (contract enforcement, boundary-validator
outcomes, effect recording). The corpus (`internal/oracle/corpus.go`) grows from the
existing fixture shapes; the harness compares all four observables per (case, vector).
**Runtime discharge prerequisite:** the V4-derived boundary validators now RUN at the
kernel eval boundary — `std/contract.pre`/`post` are enforcing natives whose falsy
clause is a typed durable `contract.{pre,post}.violated` condition park (abort
restart), and a pre violation fires no effect (the ADR-05 condition discipline; proved
at the kernel eval door). The three seeded wrong-evaluation mutants are
`mutants.Evaluator` — `EVAL_PRE_ALWAYS_SATISFIED` (layer a), `EVAL_VALIDATOR_ZERO_ACCEPTS`
(layer b, the weakened-accept-set/off-by-one class), `EVAL_EFFECT_ORDER_TRANSPOSED`
(layer c) — compiled into the production natives/Host, default hard-off, armed
one-at-a-time by the oracle harness, each proven caught (a survivor fails the harness).
They are deliberately NOT in `mutants.All`: the admission corpus cannot witness an
evaluator weakening; the oracle is their killing harness. Stage-C RESIDUE: layer (b)'s
validator is the contract-clause validator artifact (C2 scope) — resource-type-derived
input validators join the same corpus when ADR-10's full derivation lands; the RED leg
(history) witnessed the machine evaluating straight past violated boundaries.

### 6.5 Machine determinism — the cross-kernel randomized hermeticity probe (R1-10: hermeticity probed across kernels + builds under randomized scheduling)

Conformance (§6) proves the machine computes the *right* value; determinism proves it
computes the *same* value on any kernel, forever — the property a year-old resume (ADR-05)
and a probeable verdict both rest on. The machine's result must be a pure function of
`(program, inputs, captured C/E/K)` and nothing else: **not** Go map-iteration order, **not**
checker/goroutine scheduling, **not** tsgo internal state carried across admissions, **not**
freelist/allocation order. "Submit twice on one warm process" perturbs none of those sources,
so it proves in-process determinism only — not the class that makes a verdict probeable.

The **cross-kernel randomized hermeticity probe** runs the same program — and the same
resumed continuation (ADR-05 red-path test 12 owns the resume-side probe; this section owns
the machine-level source) — across **≥ 2 independently-launched kernel instances**, and where
available **distinct kernel builds**, under **randomized Go map seeds, randomized goroutine/
checker scheduling, and a cold checker**, N times per case. All runs must yield **identical**
produced values (§2 R2 lattice), effect-class order, and thrown-error shape. **Any divergence
is red**: a result that changes with an ambient the machine must not observe would make some
future resume irreproducible. The probe is a **per-release CI gate** (nightly over the full
conformance + golden-continuation corpus), the cadence at which distinct builds exist. It is
validated by injecting a map-iteration-ordered diagnostic emission into the machine and
confirming the probe turns red — the same self-seeding discipline §6's oracle uses to prove it
can see the failure it gates.

### 7. Reserved taal AOT-to-Go seam — interface fixed, lane not built

A verified hot function may, in a future epoch, be dispatched to ahead-of-time-emitted
Go. The seam is fixed now so v1 forecloses nothing:

- **Signature:** `type AOTFn func(h *Host, args []Value) (Value, *Condition)`.
- **Value ABI:** the same tagged `Value` union as the machine — no marshalling boundary;
  a caller cannot observe which lane ran.
- **Suspension:** an AOT function must contain no pause point (`await` is banned in
  eligible functions). Anything reachable across a pause stays interpreted, so ADR-05
  never meets a Go stack.
- **Fuel:** publishes a static per-call cost or self-charges via `h.Meter`; metering
  semantics are unchanged. **Effects:** only through the `Host` capability table — no
  ambient Go I/O.
- **Verification:** the candidate must pass the §6 conformance corpus differentially
  against its own interpreted body before dispatch is permitted.
- **Dispatch:** a nullable `aot_symbol` on the definition's catalog metadata; when set
  and the pinned epoch binary exports the symbol, the machine calls the `AOTFn` instead
  of walking the AST. v1 ships with `aot_symbol` NULL everywhere.

### 8. Performance budgets — numbers with provenance, benchmark-enforced before M0 closes (R1-07: perf budgets exist and gate M0, not deferred to M6)

The envelope argument (§ Consequences: I/O-bound, heavy lifting in SQL) is a claim until
a number enforces it. This ADR states initial performance budgets **now**, as
numbers-with-provenance, and makes a benchmark that enforces them part of the **M0 gate** —
a budget regression is red exactly like a failed kill-test. The values are *initial*:
each is measured on the reference workload and re-tuned once, but the budget **exists and
is enforced before M0 closes**, not deferred to a later milestone.

- **CEK-steps/sec floor — ≥ 1,000,000 CEK transitions/sec per core** (trusted
  `governorMeter`, single goroutine, M0 reference-workload microbench). The fully-reified
  tree-walker is accepted to be well slower than a bytecode VM (§ Consequences); this
  floor is the line below which "fast enough" is false and the §7 AOT seam or a
  representation change is forced, not optional.
- **Transitions-per-request ceiling — ≤ 50,000 CEK transitions per typical reference-app
  request (p95)**. With the floor, a typical request spends ≤ ~50 ms in pure
  interpretation, leaving the rest of the request budget to the I/O it is supposed to be
  bound by. A request whose interpretation crosses the ceiling is a benchmark regression
  (red) surfacing an accidentally compute-bound path for the §7 seam.
- **Metering-tax ceiling — ≤ 10 % wall-time overhead of the sandbox `fuelMeter`
  instantiation vs. a meter-stripped build** on the same program; the trusted
  `governorMeter` per-step meter cost stays **0 %** by monomorphization (§4). Above 10 %
  the charge-point set (§4) is wrong, not the envelope.
- **Checkpoint-write budget — ≤ 1 step-transaction checkpoint write per interaction,
  CFR delta ≤ 64 KB/interaction (p95)**, well under ADR-11 §5's 256 KB session cap. The
  budget is set here against the CFR / step-transaction write path (built at M0); its
  end-to-end verification on the reactive layer — writes-per-interaction ≤ 1 across a
  reference-app load test — is an M4 gate seated in **ADR-11 §5**. *(Integrator pointer:
  the CFR row wire format itself is ADR-05's; this ADR states only the write-amplification
  budget, not the format.)*

**Provenance and enforcement.** Each number is carried as a `perf_budget` row
`(metric, tier, budget, measured, milestone)` versioned with the epoch, mirroring the
`verifier_coverage` shape (ADR-07 §5) — budgets are data, like coverage. The M0 gate runs
the benchmarks and **fails the milestone if any measured value crosses its budget**;
ADR-11 §9's WAN felt-latency gate and the ADR-11 §5 checkpoint-write end-to-end test
extend the same enforcement to M4. No budget is deferred to M6 — the stack is never built
on an unmeasured floor.

## Alternatives Considered

- **prior-art-faithful: compile-at-admission bytecode for a stackless CEK/CESK VM.**
  Rejected: a bytecode instruction pointer anchors every stored continuation to a
  mutable compiler artifact, and the rescue — deterministic epoch-keyed recompilation —
  makes every epoch's compiler immortal kernel surface. It is also a second artifact to
  emit, version, and verify, against ADR-02's anchor-to-owned-immortal-facts philosophy.
  Grafted from it: the ocap grant/role/bundle representation (§5), the shared-`Value`-ABI
  AOT contract (§7), and the one-semantics-two-loops metering insight (§4).
- **red-path-first: selective (two-colored) CPS — sync code on the native Go stack,
  heap frames only at `await`.** Rejected: mid-sync fuel exhaustion cannot park without
  unwind-and-replay machinery it never specified, deep sync recursion rides the Go
  stack, and two execution colors are a standing complexity tax on one team. Grafted
  from it: the `(def_hash, node_path, phase)` immortal anchor, the monomorphized meter,
  the charge points that bound both time and space, and the entire conformance strategy.
  Its sealed-handle rule survives except the capture ban, which ADR-05 overrules.
- **simplest-thing (winner):** the fully-reified CEK tree-walker is this ADR's machine.
  Corrections: its `YieldK` frame is deleted (ADR-01 bans generators), its name-keyed
  environment maps become De Bruijn slot arrays (ADR-02), its single metering branch is
  replaced by monomorphization, and it gains the conformance harness it lacked.

## Consequences

- One machine, one code path, two monomorphized meter instantiations; every ADR-01 ban
  is engine never built. The interpreter's surface is exactly the whitelist.
- Full reification costs real per-transition overhead for all tiers. This is the
  constraint-#2 tax, accepted deliberately: the envelope is I/O-bound, heavy lifting is
  SQL, and the §7 seam is the named escape valve, opened per-function by production
  profiling only. The envelope is no longer merely asserted — §8's budgets make it a
  benchmark-enforced number before M0 closes (R1-07: envelope measured, not asserted).
- Pause-anywhere is structural, not scheduled: fuel exhaustion, governor breach, and
  unsatisfiable awaits all park through the identical serialize-C/E/K path.
- The eval loop is a replaceable implementation detail; only the C/E/K shape, the
  `Value` union, the frame-kind set, and the node-path/phase anchoring are contracts
  (versioned in ADR-05's wire format).
- `node` and test262 exist only in the development loop; the kernel's dependency
  sentence is unchanged.

## Red-Path Tests Implied

- Deep recursion and large K stacks run with a bounded Go stack (no hidden native
  recursion); serialize/resume at maximal depth.
- Fuel exhaustion at an arbitrary mid-expression transition parks cleanly, signals
  `fuel.exhausted`, and resumes to the identical result after `grant-fuel` — never a
  panic, never a partial effect.
- Governor: type-correct `while(true)` in the trusted tier signals `runaway`, rolls the
  turn back, kernel stays live (ADR-01's test, executed here).
- Performance-budget gate (R1-07: budget regression is red): the M0 benchmark suite
  measures CEK-steps/sec (≥ 1M/core floor), transitions/typical-request (≤ 50k p95), and
  metering-tax; any measured value crossing its §8 budget fails the milestone. The trusted
  `governorMeter` instantiation shows zero per-step meter cost versus a meter-stripped
  build; the sandbox `fuelMeter` tax stays ≤ 10 %. The checkpoint-write budget's
  end-to-end proof (writes/interaction ≤ 1) is the ADR-11 §5 M4 gate.
- Unnameable: an ungranted capability fails capability-audit at admission; a hand-built
  root table omitting a grant makes the same reference fault closed at evaluation.
- `throw` across `await` with `finally`: park inside `try`, resume on another kernel,
  assert unwinding order and `finally` re-execution match the `node` oracle.
- Conformance: test262-subset green and base-dialect differential-fuzz divergence corpus
  empty are release gates per epoch.
- Regel-native oracle (R1-02: seeded-mutant red proof): the machine and the independent
  reference reducer agree on contract verdicts, derived-boundary-validator outcomes,
  effect-class order, and produced values across the corpus; and each of the three seeded
  wrong-evaluation mutants (contract, validator, effect-order) turns the corpus red. A
  surviving mutant blocks the release.
- AOT seam smoke test: a trivial `AOTFn` behind `aot_symbol` returns byte-identical
  `Value`s to the interpreted body across the conformance corpus.

## Constraints Discharged or Budgeted

1. **Mechanism discharged.** Every transition boundary is a serializable pause point
   anchored to immortal facts; ADR-05 owes only the format.
2. **Budgeted — this ADR is the tax.** Owned pure-Go machine, envelope argument for v1,
   AOT seam fixed and reserved (§7). The tax now carries enforced numbers — §8's
   steps/sec floor, transitions/request ceiling, metering-tax and checkpoint-write budgets
   are M0-gate benchmarks (budget regression is red), so the envelope is measured, not
   asserted (R1-07: tax has gate-enforced budgets).
3. **Budgeted (interface consumed).** `fuel.exhausted` and `runaway` are durable
   conditions with named restarts; schema and wake path land in ADR-05.
4. **Discharged.** The machine consumes the canonical AST directly — behavior is a
   tree-walk over the homoiconic artifact; no emit, no second representation.
5. **Budgeted.** Runtime enforcement (empty globals, sealed handles, meter, governor)
   backs the verifier boundary; conformance divergence is a tested, versioned artifact,
   and the harness is part of the stated coverage. The regel-native differential oracle
   (§6 harness 3) extends that coverage past the vanilla-TS core to the three semantic
   layers regel adds — contract enforcement, derived boundary validators, effect-class
   ordering — so no regel-added evaluation stays hand-reasoned and ungated (R1-02: coverage extends past the vanilla-TS core).
6. **Budgeted.** One machine for both tiers, no bootstrap engine to discard, staging
   lives in the grammar; the walking skeleton drives this machine end to end.
