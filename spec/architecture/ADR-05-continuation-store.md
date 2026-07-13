# ADR-05: The continuation store

## Status

Accepted — Phase 1

## Context

BRIEF §4 requires the continuation representation: capture-vs-replay, the exact captured
state and on-disk format, admission-enforced capture rules, the wake-condition and
durable-condition/restart schemas, exactly-once, concurrent-resume prevention, and epoch
compatibility. Constraint #1 names this the deepest bet; constraint #3 requires the
durable-condition system rebuilt as data. <!-- R1-12: "condition system" → "durable-condition system": the bare word carries three distinct senses (GLOSSARY); this site means the §6 resumable-error rows, not a wake trigger nor the 'condition' status. -->

Cross-ADR dependencies, stated explicitly:
- What is serialized is exactly the machine ADR-04 defines: the C/E/K registers, whose
  Control anchors to `(def_hash, node_path, phase)` and whose handler stack is realized
  as `TryK`/`FinallyK` frames inside K.
- Every `def_hash` in a stored frame is an ADR-02 address into ADR-03's immortal,
  INSERT-only `definition` table (invariant I6) — the anchor that makes year-old resume
  a WHERE clause, not a migration.
- ADR-01 R1–R5 are the grammar half of capture discipline; this ADR adds the
  admission-time dataflow verifier that closes it, and adopts R2's capability tokens.
- ADR-06 owns the drain/reaper machinery that claims the rows defined here.

## Decision

### 1. State-capture, not event-sourced replay

The store serializes the **actual machine state**, never an event log to re-execute.
Replay (the Temporal/Cadence model) forces two costs regel refuses: all workflow code
must stay replay-deterministic forever, and versioning a mid-flight workflow becomes the
problem that haunts the category. Owning a machine whose every transition boundary is
serializable (ADR-04 §2) is what buys the direct answer: capture C/E/K, anchor to
immortal hashes, and resume against the exact code that started — as-of, structurally.
Temporal's versioning API has no representation here because the problem it patches
cannot occur.

### 2. Captured state and on-disk encoding: CFR, an owned versioned binary TLV

A paused program is one `continuation` row whose `frames` column holds a **CFR**
("continuation frame representation") blob:

- **Control:** `(def_hash, node_path, phase)` per ADR-04 — never a code copy, never an
  offset into any compiled artifact.
- **K:** the frame stack `[{kind, node_path, vals[]}]`, innermost last, including
  `TryK`/`FinallyK` handler frames so unwinding and `finally` re-execution survive
  resume.
- **E:** an environment-node heap `[{parent_ptr, slots[]}]` — slot arrays indexed by
  De Bruijn binder index (ADR-02; there are no name maps), content-shared within the
  blob so closures over a common activation serialize it once (grafted from the
  prior-art proposal).
- **Values:** exactly the ADR-04 `Value` union. Closures serialize as
  `(def_hash, env_ptr)`; capability handles serialize as tokens `(grant_id)`;
  std opaque handles serialize by their declared codec (ADR-01 R2).

Encoding is an **owned, self-describing, versioned binary TLV in `bytea`** — CFR-1 —
sharing ADR-02 `canonEncode`'s primitive encodings (f64 as the 8-byte bit pattern,
bigint as sign + magnitude, length-prefixed UTF-8). jsonb is rejected as the storage
format: it cannot carry f64 bit patterns, bigint, or bytes with fidelity, and ADR-02
already committed the system to owned TLV. Operator inspectability survives as
`continuation_debug(id)`, a kernel function projecting CFR to JSON with display names
recovered from `canonical_text` (grafted from the simplest-thing proposal) — a view,
never the truth.

```sql
CREATE TABLE continuation (
  id            uuid PRIMARY KEY,
  kind          text NOT NULL CHECK (kind IN ('workflow','session','request')),
                -- R1-12: the closed continuation taxonomy is EXACTLY these three kinds.
                -- 'session' is a UI session; 'request' is a deferred-wake HTTP request
                -- continuation. A durable condition is NOT a kind — it is a separate
                -- durable_condition row (§6) attached to a parked continuation, so the
                -- GLOSSARY sentence must read "workflows, sessions, requests," never list
                -- durable conditions as a continuation kind.
  root_def_hash text NOT NULL REFERENCES definition(hash),
  epoch         int  NOT NULL,               -- R1-12: catalog epoch this row last
                -- checkpointed under. PROVENANCE stamp, NOT a resume selector — see §8:
                -- resume binds code by def_hash and decodes by format_ver; fleet-coherence
                -- gating is the ADR-08 R1-05 fence against epoch_current, not this column.
  format_ver    int  NOT NULL,               -- CFR version
  frames        bytea NOT NULL,              -- CFR blob (C + K + E heap)
  wake          jsonb NOT NULL,              -- §5
  status        text NOT NULL CHECK (status IN
                  ('sleeping','ready','running','condition','done','failed')),
                -- R1-12: the status value 'condition' is the THIRD distinct sense of the
                -- word (GLOSSARY disambiguation): "parked on an open durable_condition
                -- awaiting a restart choice" (§6). It is neither a wake condition (a
                -- trigger) nor the durable_condition row itself.
  step_seq      bigint NOT NULL DEFAULT 0,   -- monotonic; the claim fence (§7)
  lease_owner   uuid,                        -- claiming kernel, or NULL
  lease_until   timestamptz,                 -- heartbeat expiry
  principal     jsonb NOT NULL,              -- ADR-03 §3 scope chain for resume
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  -- R1-12: load-bearing jsonb discriminator gets CHECK-shaped validation. §5 dispatches
  -- on wake->>'kind'; a wake='{}' or an unknown kind is a row that means nothing (the
  -- "application discipline" the design forswears). Assert the discriminator exists and
  -- is in the closed §5 set.
  CONSTRAINT wake_kind_shape CHECK (
    wake ? 'kind' AND wake->>'kind' IN ('timer','message','event','join','manual'))
);
CREATE INDEX ON continuation ((wake->>'due'))
  WHERE status = 'sleeping' AND wake->>'kind' = 'timer';
-- BUILD-A: the original expression index `((wake->>'due')::timestamptz)` is
-- uncreatable — the text→timestamptz cast is STABLE, not IMMUTABLE (it reads the
-- session TimeZone), and Postgres rejects non-IMMUTABLE index expressions
-- (SQLSTATE 42P17; hit on PG 16.13 at Stage A bootstrap). The index is therefore
-- over the raw `wake->>'due'` text, and timer wakes MUST serialize `due` as a
-- fixed-width UTC ISO-8601 instant (`YYYY-MM-DDTHH:MM:SS.ssssssZ`), whose
-- lexicographic order equals timestamp order — the timer scanner's range scan
-- (`wake->>'due' <= :now_utc_iso`) stays index-served with identical semantics.
```

### 3. Capture discipline: enforced at admission, total at pause time

ADR-01 R1–R5 make illegal captures mostly unwritable. The remaining gap is closed by
the **capture verifier**, run inside the admission transaction: for every `await` in a
definition it computes the live-variable set at that point — exactly the set ADR-04
spills into frames — and rejects the definition if any live variable's type lies outside
the R2 serializable lattice, with a precise diagnostic ("`conn` is live across the await
at «path» and is not serializable"). The CFR codec and this verifier share one type
table: **encodable ≡ admitted**. Serialization at pause time is therefore a total
function; there is no pause-time failure mode by construction. Live host resources
(connections, sockets, in-flight promises) are never dialect values at all (R2), so the
codec has no tag for them — a poison-pill environment is structurally unrepresentable.

### 4. Capability tokens across pauses

A capability captured across a pause serializes as its token `(grant_id)` — the
prior-art model, consistent with ADR-01 R2 and required by the concept doc's flagship
workflow (`mail.send` held across `wf.sleep(days(3))`). The red-path proposal's blanket
capture ban is overruled. On resume the kernel re-validates the grant row — existing,
unexpired, unrevoked — and re-binds a live sealed handle into the rebuilt root table. A
failed re-validation signals the durable condition `capability.revoked` with restarts
`re-grant` (requires the granting capability) and `abort`. A revoked tenant grant
therefore stops a sleeping workflow at its next step, auditably — never silently.

### 5. Wake conditions: one per continuation, `join` for structured concurrency

The `wake` jsonb holds exactly one pending wake:

```
timer:   {kind:"timer",   due:<ts>}                          -- wf.sleep
message: {kind:"message", channel:<hash>, match:<pred>}      -- wf.receive(T)
event:   {kind:"event",   stream:<resource>, on:[...]}       -- record-change trigger
join:    {kind:"join",    children:[uuid...], quorum:<n>}    -- std all (n=len) / race (n=1)
manual:  {kind:"manual",  condition:<uuid>}                  -- awaiting a restart choice
```

ADR-01 admits `all`/`race` as std combinators, so they are not deferred: each spawns
child continuations and parks the parent with a `join` wake; a child's terminal commit
decrements the join in the same transaction, and quorum flips the parent to `ready`
(`race` cancels losers). Timer wakes are found by the partial index; message/event wakes
are flipped to `ready` in the same transaction as the triggering write, with a NOTIFY to
wake ADR-06's pollers.

### 6. Durable conditions and named restarts: rows, buttons, choices

A failed or fuel-exhausted step never throws outward (ADR-01 exceptions semantics); the
std `signal(condition, restarts)` API writes rows and parks:

```sql
CREATE TABLE durable_condition (
  id              uuid PRIMARY KEY,
  continuation_id uuid NOT NULL REFERENCES continuation(id),
  class           text NOT NULL,     -- 'fuel.exhausted' | 'runaway' | 'capability.revoked'
                                     -- | 'step.failed' | app-defined
  payload         jsonb NOT NULL,    -- context, PII-masked at write
  signaled_at     timestamptz NOT NULL DEFAULT now(),
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  resolved_restart uuid, resolved_args jsonb, resolved_by text, resolved_at timestamptz,
  -- R1-12: class is a namespaced, non-empty tag. The kernel classes above plus app-defined
  -- classes make the set intentionally OPEN (§6), so a CHECK asserts shape, not a closed
  -- enum — an empty or malformed class (a row that "means nothing") is still rejected.
  CONSTRAINT class_shape CHECK (class ~ '^[a-z][a-z0-9]*(\.[a-z0-9]+)*$'),
  -- R1-12: resolution-state integrity — the row means exactly one thing. 'resolved' iff
  -- the resolving restart and its provenance are recorded; 'open' iff they are NULL.
  -- Closes Celko's gaps: an 'open' row with resolved_by populated, or 'resolved' by a
  -- phantom restart. (resolved_args stays optional — a paramless restart carries none.)
  CONSTRAINT resolved_consistency CHECK (
    (status = 'resolved') =
      (resolved_restart IS NOT NULL AND resolved_by IS NOT NULL AND resolved_at IS NOT NULL))
);
CREATE TABLE restart (
  id            uuid PRIMARY KEY,
  condition_id  uuid NOT NULL REFERENCES durable_condition(id),
  name          text NOT NULL,       -- 'grant-fuel' | 'retry' | 'abort' | app-defined
  label         text NOT NULL,       -- operator-plane button text
  params_schema jsonb NOT NULL DEFAULT '{}',
  capability_required text           -- e.g. only operators may 'grant-fuel'
);
-- R1-12: durable_condition.resolved_restart FK. It cannot be inline because the two
-- tables reference each other (restart.condition_id → durable_condition(id), and
-- resolved_restart → restart(id)); the table created first would forward-reference one
-- that does not yet exist. So the FK is added after both exist — a resolution can no
-- longer name a nonexistent restart.
ALTER TABLE durable_condition
  ADD CONSTRAINT resolved_restart_fk
  FOREIGN KEY (resolved_restart) REFERENCES restart(id);
```

Signal path (one transaction): park the continuation (`status='condition'` — the
parked-on-condition status, §2), insert the `durable_condition` row and its `restart`
rows (`wake={kind:'manual'}`). Restarts render as
operator-plane buttons and as structured MCP choices to agents — the same rows. Picking
one (one transaction): check `capability_required`, set `resolved_*`, flip the
continuation to `ready`, insert a resume task (ADR-06 §5). Resume re-enters the machine
at the parked C with the restart's name and args delivered as the awaited value of the
`signal` call; kernel-signaled classes (`fuel.exhausted`) resume at the exact suspended
transition. Restart consumption is part of the resumed step's transaction, so an
abandoned resume leaves the choice intact for re-claim. This is Lisp's resumable-error
discipline as four columns and two tables — equivalent behavior, less linguistic
elegance, stated plainly.

### 7. Exactly-once and concurrent-resume prevention: claim CAS + lease

One step = one Postgres transaction containing all of — and the step transaction runs
**`ISOLATION LEVEL SERIALIZABLE`**, not READ COMMITTED (R1-INT: isolation stated
explicitly — the ADR-08 §4a/ADR-06 §6 epoch fence, R1-05, depends on it: the fence's
`epoch_current` guard read must form an SSI rw-conflict with the flip's UPDATE, which is
also what makes the O4 park-vs-commit race atomic):

```
BEGIN ISOLATION LEVEL SERIALIZABLE;
UPDATE continuation
   SET status='running', lease_owner=$me, lease_until=now()+interval '30 seconds',
       step_seq=step_seq+1, updated_at=now()
 WHERE id=$id AND status='ready' AND step_seq=$seen;     -- the CLAIM (CAS)
-- 0 rows ⇒ another kernel won or state moved: back off, no work done.
<business-effect writes>;                                 -- the step's SQL
<outbox intent rows>;                                     -- external effects, dedup key
                                                          --   (continuation_id, step_seq)
UPDATE continuation SET frames=$cfr', wake=$next, status=$s, updated_at=now()
 WHERE id=$id;                                            -- the CHECKPOINT
COMMIT;
```

Effect and checkpoint commit atomically: a crash at any instant leaves the row wholly
before or wholly after the step — no torn state, no double effect, no lost effect.
Double-resume is fenced twice: the `step_seq` CAS admits exactly one claimant, and a
zombie kernel returning from a network partition fails its commit-time CAS because the
sequence moved. The lease (`lease_owner`/`lease_until`, 10-second heartbeats) exists so
ADR-06's reaper can re-offer work whose kernel died; correctness never depends on the
lease, only liveness does. That asymmetry is what makes ADR-13 §5's reap-rate breaker
safe: under saturation the reaper pauses re-offers and recovery is delayed and visibly
measured (`reaper.lag_ms`), never corrupted (R1-06: breaker leans on
lease-is-liveness-only). External side effects (mail, webhooks) never fire inline:
the transaction writes an intent row and ADR-06's dispatcher delivers with the
idempotency key — exactly-once inside Postgres, effectively-once with dedup keys across
the process boundary. That is the honest limit, stated.

### 8. Epoch compatibility

- `format_ver` and `epoch` ride every row. **A kernel reads every CFR version ever
  written**: decoders and up-converters are append-only and never deleted (the ADR-02
  §6 rule, applied to continuations). Up-conversion runs lazily at resume and never
  rewrites sleeping rows in place.
- **What `epoch` is FOR (R1-12: provenance stamp, not a resume selector).** The `epoch`
  column records the catalog epoch under which the continuation last checkpointed. No
  resume path keys off it: code is bound by the frames' immortal `def_hash`es (I6) and
  the blob is decoded by `format_ver`. Its two readers are (a) the lattice-narrowing
  enumeration in the last bullet of this section, which prefilters candidate sleeping
  continuations by `epoch < N` before checking their held types, so a narrowing epoch N
  has a cheap, indexable "which pauses predate me" query; and (b) observability. Fleet
  coherence is enforced by the ADR-08 §2a **R1-05 fence** — a running kernel compares its
  *pinned* epoch against the live `epoch_current` row, never against this per-continuation
  stamp — so a stale `continuation.epoch` can neither select nor block a resume. Stating
  this closes the "what invariant does this column protect?" gap.
- Frame kinds, `Value` tags, and wake kinds are append-only within a CFR major version;
  no tag is ever repurposed.
- Resume is **always by content hash** — the frames' own `def_hash`es, which ADR-03 I6
  makes immortal. There is no follow-latest mode: moving a paused program onto newer
  code happens only through an explicit operator restart (an app-defined restart that
  aborts and re-launches against the new head). Silent migration is unrepresentable.
- An epoch that narrows the serializable lattice must enumerate every sleeping
  continuation holding a newly-banned type **at the epoch's own admission** and fails
  to land until each is resolved — streng's list-every-incompatibility-at-once rule
  applied to live pauses (grafted from the red-path proposal).

### 8.5 Continuation decode coverage as data: `continuation_coverage` rows with a monotone floor (R1-10: decode coverage is measured, monotone, and gated)

The golden-continuation and CFR corpora are otherwise unmeasured fixture bags: "stays
green" is achievable with a thin corpus that never exercises a rare decode path — a
`FinallyK` frame captured across an `await`, or an old `r<n>` up-converter leg — so a decode
regression in a rarely-hit path ships green. Decode coverage is therefore tracked as **data
with a monotone floor**, mirroring ADR-07 §5's `verifier_coverage` (data-as-coverage, one
discipline across the corpus):

- **Grain.** One `continuation_coverage` row per **(frame_kind, cfr_version, decoder)**
  triple: `frame_kind` ranges over the closed §2 K frame-kind set (`TryK`/`FinallyK`/… , the
  one-per-composite-node set, append-only), `cfr_version` over every CFR format version the
  kernel still reads (§8, append-only readers), and `decoder` names the specific decode/
  up-convert path exercised — the `r<n>` value-tag decoder and each per-version up-converter
  leg. A triple is **reachable** iff the closed grammar can produce that frame under that
  format version; because the frame-kind set and the format-version set are both enumerable
  and append-only, the full **required grid** of reachable triples is a *computed* set, not a
  guess.
- **Production.** Every corpus case — each golden continuation, each generated CFR fixture,
  and each cross-epoch / year-old / as-of resume test — emits at decode time the set of
  triples it touched; the harness unions them per run into `continuation_coverage`, exactly
  as the verifier harness accumulates `verifier_coverage`.
- **Monotone floor — coverage may only ratchet up.** The gate compares the run's covered
  triple-set against the stored floor for the epoch. A run whose covered set does **not**
  include every previously-covered triple is a **regression and a release blocker**: a
  decoder path exercised in a prior release and untouched now fails the gate, so coverage can
  never silently shrink. New reachable triples **raise** the floor; the floor never falls. A
  newly reachable triple (a new frame kind, a new CFR version) is **reachable-but-uncovered =
  red** and must be covered before the epoch that introduces it can land — no decode path
  enters the kernel unexercised.
- **DDL pointer.** The `continuation_coverage` table is DDL'd in ADR-03 alongside
  `verifier_coverage` and `perf_budget` (flagged there, not authored here); its shape mirrors
  those coverage tables.

## Alternatives Considered

- **Event-sourced replay (Temporal/Cadence model, surveyed by the prior-art proposal
  and rejected by all three):** rejected for replay-determinism-forever and the
  reintroduced mid-flight-versioning problem (§1). Unanimity across all three proposals;
  the prior-art proposal's defense is adopted as the canonical argument.
- **simplest-thing: canonical JSON in `jsonb`; single wake with `all`/`race` deferred;
  a `step_done` idempotency table.** Rejected: jsonb loses f64/bigint/bytes fidelity
  and contradicts ADR-02's owned-TLV philosophy; deferring `all`/`race` contradicts
  ADR-01, which admits them; `step_done` is replay-era machinery — under state-capture
  the checkpoint *is* the position, so the table is dead weight. Grafted: the
  two-halved poison-pill test and operator inspectability (as the debug projection).
- **prior-art-faithful: TLV frames carrying bytecode `ip`, a separate `choice` table,
  COPY-era env heap.** Its TLV-in-`bytea` decision, env-node heap, capability-token
  re-binding, and transactional outbox are adopted. Rejected pieces: `ip` anchoring
  falls with ADR-04's bytecode rejection; the separate `choice` table folds into
  `resolved_*` columns (history tier already audits the update).
- **red-path-first (winner):** CFR anchoring, admission-time live-variable capture
  verifier, `step_seq` CAS + lease, lattice-narrowing epoch rule, and the kill-test
  method carry this ADR. Overruled: its ban on capability capture (contradicts ADR-01
  R2 and the concept doc's own workflow; replaced by §4 tokens) and its `follow-latest`
  opt-in (cut — one resume semantics, explicit restart for migration).

## Consequences

- The deepest bet is now three small owned artifacts: the CFR codec, the capture
  verifier, and the claim/lease protocol — each kill-tested before any feature exists.
- A parked program costs one row: no goroutine, no connection, no memory (ADR-06).
- Capability revocation propagates to sleeping workflows at their next step, as a
  visible durable condition — governance reaches paused code.
- The CFR codec and capture verifier share a type table; extending the serializable
  lattice is one change in one place, versioned with the epoch.
- Conditions and restarts being ordinary rows means the operator plane and the MCP
  agent surface are derived views over the same data — constraint #3's product truth.
- The store's health is named signals, not a gesture (R1-06: health surface specified
  in ADR-13): `continuation.parked`, `continuation.resume_latency_ms`,
  `continuation.cas_losses_total`, and `condition.open_age_ms` are ADR-13 §2 golden
  signals with SLOs; `continuation_debug` is bound by ADR-13 §6's PII policy —
  operator-plane, capability-gated, never exported over any telemetry channel.

## Red-Path Tests Implied

All are release gates built before any feature (red-path-first):

1. **Crash mid-await + cross-kernel resume:** kill kernel A between claim and commit;
   lease expires; kernel B resumes from the prior checkpoint; effect fires exactly once.
2. **Year-old resume:** serialize; advance clock past a year and one epoch; resume to
   completion via the append-only CFR reader against immortal hashes.
3. **As-of resume:** re-admit the workflow's definition three times while parked; the
   continuation resumes against its original `def_hash`es, never the new head.
4. **Poison-pill environment:** (a) a definition holding a connection-typed value
   across `await` is rejected at admission by the capture verifier; (b) a truncated or
   bit-flipped CFR blob fails deserialization closed into a `step.failed` condition —
   never a crash, never a partial resume.
5. **Double-resume race:** two kernels claim one `ready` continuation concurrently;
   exactly one CAS wins; a partitioned zombie's later commit fails the fence; the
   effect exists once.
6. **Fuel exhaustion mid-step:** budget burns at an arbitrary transition; park +
   `fuel.exhausted` + `grant-fuel` restart; resumed run completes with no checkpoint
   corruption and no re-fired effect.
7. **Torn write:** crash injected during the checkpoint transaction at every statement
   boundary; the row is always wholly-before or wholly-after.
8. **Capability-revoked resume:** revoke a grant while parked; resume signals
   `capability.revoked`; `re-grant` restart completes the workflow.
9. **Wake storm:** 10k simultaneous due timers drain exactly once each across multiple
   kernels (with ADR-06's SKIP LOCKED drain).
10. **Join:** `all`/`race` parents wake at quorum; `race` losers are cancelled; crash
    between child commit and parent flip recovers without double-decrement.
11. **Decode-coverage floor** (R1-10: an untouched decoder path fails the gate): a run whose
    corpus omits a previously-covered `(frame_kind, cfr_version, decoder)` triple — e.g. a
    `FinallyK`-across-`await` decode under an older `cfr_version` — fails the
    `continuation_coverage` (§8.5) monotone-floor gate; a newly reachable triple left
    uncovered blocks the epoch that introduces it.
12. **Cross-kernel randomized hermeticity probe** (R1-10: same continuation, distinct
    kernels + builds + randomized scheduling → identical result): the *same* continuation is
    resumed to completion on **≥ 2 independently-launched kernel instances** — and, where
    available, on **distinct kernel builds** that each carry every appended CFR decoder (§8)
    — under **randomized Go map seeds, randomized goroutine/checker scheduling, and
    randomized admissible await-interleaving**, N times. All runs must produce **identical**
    observables: the same produced values (ADR-04 R2 lattice), the same effect-class order,
    the same terminal `status`, and a byte-identical re-checkpointed CFR blob. **Any
    divergence is red** — it exposes a hidden dependence on map-iteration order, scheduler
    nondeterminism, or build-carried decoder state that would make a year-old resume
    irreproducible. The probe runs in CI **per release** (and nightly over the full
    golden-continuation corpus), the cadence at which distinct builds first exist; it is
    validated by injecting a map-iteration-ordered emission on the resume path and confirming
    the probe turns red. This is the resume-side half of the machine-determinism probe whose
    machine-level source (map order, scheduling, tsgo state) is owned by ADR-04 §6.5.

13. **Wake discriminator** (R1-12: the load-bearing jsonb discriminator is CHECK-guarded):
    insert a continuation with `wake='{}'` and one with `wake='{"kind":"bogus"}'` ⇒ both
    RAISE on `wake_kind_shape`; a well-formed `{"kind":"timer",...}` commits.
14. **Durable-condition integrity** (R1-12: resolution can't lie): (a) set
    `resolved_restart` to a uuid naming no `restart` row ⇒ `resolved_restart_fk` rejects;
    (b) set `status='resolved'` with any of `resolved_restart`/`resolved_by`/`resolved_at`
    NULL — and the mirror, `status='open'` with `resolved_by` populated — ⇒ both RAISE on
    `resolved_consistency`; (c) `class=''` or a malformed class ⇒ `class_shape` rejects.

## Constraints Discharged or Budgeted

1. **Discharged — this ADR is constraint #1.** CFR + immortal-hash anchoring +
   admission-enforced capture + append-only readers is "serialized stably for years,"
   kill-tested first (tests 1–4).
2. **Budgeted.** Serialization cost is paid only at real pause points; inline awaits
   never touch the store (ADR-04 §2).
3. **Discharged — §6 is constraint #3.** Durable conditions and named restarts are
   rows; restarts render as operator buttons and agent choices; picking one resumes the
   exact continuation.
4. **Consumed.** Frames anchor to canonical-AST node paths — the AST schema is the
   serialization's coordinate system.
5. **Budgeted.** The capture verifier is a first-class, versioned member of the
   verifier suite; its coverage statement is part of the security boundary.
6. **Budgeted.** Three small artifacts, ten kill-tests, all preceding features — the
   staging discipline is this ADR's test list.
