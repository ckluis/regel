# ADR-11: The reactive layer

## Status

Accepted — Phase 1

## Context

This cluster owes the scherm design: rendering model and diff unit, wire transport and
patch encoding, the ~15KB client's exact duties, divergence recovery, UI sessions as
rows, dependency-exact invalidation, forms/validation/concurrent-edit, and PII masking
on the render path. streng's chapter is the spec sentence: "server-diffed typed
components from the closed vocabulary — static parts sent once, dynamic bindings
tracked, byte-sized patches over SSE; real HTML first paint, no SPA, no hydration."

Cross-ADR dependencies, stated explicitly:
- Components are ADR-10 §7's closed vocabulary; the six masking leaves are the only
  value-binding sinks; there is no raw-HTML primitive.
- A UI session is an ADR-05 continuation row (`kind='session'`) — CFR frames, claim CAS
  + lease, `step_seq` fencing, one wake per row. This ADR adds no second session store.
- ADR-04's machine renders by evaluating component definitions; ADR-06 hosts the SSE
  channels, caches hot sessions as a cache over Postgres, and drains resume tasks.
- The static/dynamic split is an ADR-07 step-5a derivation pass; its output is cached
  forever by hash (ADR-06 §3, immutable half).
- ADR-07 V2 (pii-flow) is the static half of §7's masking; V1 (capability-audit) is what
  closes §5's read-path argument.

## Decision

### 1. Rendering model: admission-time static/dynamic split; the diff unit is the slot

A derivation pass at admission lowers every component-kind definition into a **render
template**: a static skeleton (constant markup, sent once) plus indexed **dynamic
binding slots**, each `{slotId, exprPath, readSet}` where `exprPath` is an ADR-02 node
path into the definition and `readSet` is the erf read-set the expression depends on.
The template is a derived artifact keyed by the component's hash — immutable, cached
forever. Slot ids are stable component-instance paths (mount path + slot index).

First paint is full server-rendered HTML walked from `template + data` — no hydration,
no client render. An update re-evaluates only slots whose readSet intersects the change,
compares each result to the last-sent value, and frames only the deltas. **The diff unit
is the dynamic binding slot** — never a DOM subtree, never a VDOM: the interpreter
(constraint #2) never reconciles a tree; it re-evaluates changed slot expressions only.

**BUILD-D (D2): the pass is concrete.** The static/dynamic split is emitted as an
inspectable `template` `derived_artifact` (a step-5a derivation), lowering each derived
`form`/`table`/`detail` component (and each hand-authored component-kind definition) to a
`ui.Template` = a static skeleton of ADR-10 §7 tier-1 nodes plus indexed dynamic `Slot`s
`{id, kind∈{setText,setAttr,setValue,spliceList}, exprPath|field, leaf, masked, maskLeaf,
readSet}`, keyed by the definition hash. Slot ids are `"<mount>.<index>"` (mount path +
slot index); a keyed-list cell is `"<colSlotId>#<rowKey>"`. The template artifact is
**parity-checked by V6 as an eleventh derivation pass** alongside ADR-10 §4's ten (a
resource that suppresses it fails `DERIVE_PARITY`). The template encoding is versioned
JSON (`ui.TemplateVersion`); the patch frame is the DISTINCT owned binary codec below.
First paint HTML-escapes every text value — there is no raw-HTML path and no render API
accepts pre-escaped markup (ADR-10 §7). ARIA rides the skeleton exactly where §7 names it:
`section`→`role=region`, `nav`, `alert`→`role=alert aria-live`, `dialog`→`aria-modal
tabindex=-1`, `spinner`→`role=status`.

### 2. Transport and patch encoding: SSE down, POST up — confirmed

The three-proposal convergence is confirmed, over WebSocket, for regel's own reason: a
WebSocket is a sticky, stateful connection to one node, and regel's sessions are rows
any kernel resumes — SSE-with-cursor + stateless POSTs map to any node; a sticky socket
does not. Secondary reasons: built-in browser reconnect with `Last-Event-ID`, no owned
framing/keepalive protocol, and up-traffic (clicks, inputs, submits) is low-volume and
rides plain HTTP POST. streng's chapter names SSE; faithfulness and mechanism agree.

**Patch frame** (owned binary, versioned with the epoch): `[eventSeq, snapshotHash,
ops[]]`, `op = [slotId, kind, payload]`, `kind ∈ {setText, setAttr, setValue,
spliceList}` — `spliceList` is keyed add/remove/move for `list`/`table` children.
`eventSeq` is the session's `step_seq`; it is the SSE event id, so the reconnect cursor
and the fencing counter are one number.

**Empty-diff / cursor invariant (R1-08: SSE empty-diff/cursor invariant specified).** The cursor identifies a **session checkpoint**,
not a DOM change: `Last-Event-ID = eventSeq = step_seq` of the checkpoint that produced the
frame the client last applied. The invariant is that **every checkpoint that advances
`step_seq` emits exactly one frame** — so the id stream is gapless and monotone and
`Last-Event-ID` replay is always complete. An **empty-diff checkpoint** (a UI-local-only
event, or an invalidation that re-evaluates every slot to byte-identical values) therefore
emits a **zero-op frame** `[eventSeq, snapshotHash, []]`: it advances the cursor and carries
the current `snapshotHash` (letting the client confirm convergence with no repaint), rather
than checkpointing silently and leaving a gap `Last-Event-ID` replay could not fill — the
exact hole Lauret flagged. A **heartbeat** is distinct: an SSE comment line (`:keepalive`)
carrying **no** `eventSeq`, advancing **no** cursor, existing only to keep the connection and
intermediaries warm; the client updates its cursor on a zero-op frame and never on a
heartbeat. Coalesced invalidations (§6) are one resume ⇒ one `step_seq` increment ⇒ one frame
(zero-op if the coalesced re-render changed nothing), so coalescing never skips an id; an
event dropped by the claim CAS (§5) does not resume, does not increment `step_seq`, and emits
no frame, so it never moves the cursor. **Stale/unknown cursor:** a reconnect whose
`Last-Event-ID` predates the session's retained frame buffer, names a session GC'd past TTL,
or exceeds the server's current `step_seq`, is resolved by a **full resync** — re-render +
fresh skeleton + freshly summed digest (§4) — not by a silent gap; this is the same recovery
path as a digest mismatch and is counted on `sse.resyncs_total` (ADR-13 §2). Within the
retained buffer the cursor replays exactly; beyond it, resync.

### 3. The ~15KB client: five duties, nothing more

(a) hold the SSE connection and reconnect with the `Last-Event-ID` cursor; (b) apply
patches by slotId — morph text/attr/value and splice keyed lists **while preserving
focus, selection, scroll, and IME state**; (c) capture DOM events on interactive
primitives and POST `{sessionId, slotId, event, value, eventSeq}`; (d) maintain the
slot-value map and its **incrementally-maintained snapshot digest** — updated in place per
changed slot, O(changed slots), never a full-view pass (§4, R1-07: client digest kept
incrementally); (e) nothing else. It does not render
components, hold app state, route, or validate. Optimistic local echo is not in v1.

### 4. Divergence detection and resync: an incremental scoped snapshot digest (R1-07: O(changed slots) per event, not O(view))

Every frame carries `snapshotHash`, a 64-bit digest of the **current** `(slotId → value)`
map — but it is **never** recomputed over the whole view per event. The digest is an
**order-independent sum**: `snapshotHash = Σ_slots h(slotId ‖ value) mod 2⁶⁴`, where `h`
is FNV-1a-64 keyed by the slotId. Because the combiner is a commutative group operation
and each slot contributes exactly once, the digest is a pure function of the current slot
map **regardless of the order or history of edits** — so it is maintained **incrementally**:
when a patch sets slot *s* from `v_old` to `v_new`, both server and client update
`snapshotHash ← snapshotHash − h(s ‖ v_old) + h(s ‖ v_new)  (mod 2⁶⁴)`; a `spliceList`
add/remove adjusts by the added/removed slots' contributions only. Per-event cost is
therefore **O(changed slots)**, not O(view size): a 2000-slot dashboard editing one field
pays a one-slot update, not a 2000-element pass. The server updates its digest over the
post-patch snapshot by the same incremental step; the client applies the frame and updates
its digest in place (§3 duty d).

**Consistency argument.** The incrementally-maintained digest equals a full recompute over
the same map by construction — it is a sum over exactly the same per-slot terms, and
commutativity of addition makes update order irrelevant — so a **mid-sequence value
change**, the exact case a position-ordered running hash could not update in place, is
handled correctly and cheaply. On mismatch the client POSTs `resync{sessionId}`; the
server re-renders the full view once and ships a fresh skeleton + snapshot + freshly
**summed** digest. Divergence — dropped frame, morph bug, extension interference — is
self-detecting and self-healing within one round trip; the client can never silently
drift. A resync is also counted on the kernel's health surface — the named signal
`sse.resyncs_total` (ADR-13 §2), with an initial SLO of < 0.1% of frames sent (R1-06:
resync counter named and SLO'd in ADR-13): a rising rate is a client bug alarm, not a
support ticket.

### 5. UI sessions are continuation rows

A session is one ADR-05 `continuation` row, `kind='session'`, whose CFR captures: the
mount expression (`detail(Deal, id)` + args), UI-local state (open dialog, form draft),
the last-sent slot snapshot + its hash, and the principal chain. Its **subscription
set** lives in a `subscription` table `(session_id, resource, key)` maintained by the
render transaction; its wake is `{kind:'message', channel:<session_id>}` — user events
and invalidations are both messages on that channel, honoring ADR-05's one-wake rule.

**Event loop:** POST arrives → any kernel claims the row via the ADR-05 §7 CAS (a
double-fired click or second tab loses the CAS and is dropped idempotently) → resume the
machine with the event bound → handler mutates/navigates → re-evaluate affected slots →
diff vs last-sent → frame patch → push on the session's SSE channel → checkpoint the row
(new CFR, snapshot, subscriptions) in the same step transaction. Hot sessions stay in
kernel memory strictly as a cache over the row (ADR-06 §3); a killed kernel loses
nothing — the next event resumes on any node.

**Checkpoint-write budget — writes-per-interaction ≤ 1, CFR delta bounded; end-to-end M4 gate (R1-07: per-interaction checkpoint write is budgeted, not open-ended).**
The per-interaction checkpoint is budgeted: **≤ 1 step-transaction checkpoint write per
interaction** — the claim→resume→diff→checkpoint loop above writes the row exactly once,
so a 20-field-form blur-validated is 20 interactions of one write each, not one interaction
of 20 writes, and a UI-local-only event is that same single write — and a **CFR delta
≤ 64 KB per interaction (p95)**, well under the 256 KB cap below. The budget number is set
in ADR-04 §8 against the CFR / step-transaction write path; its end-to-end verification —
a reference-app load test asserting writes-per-interaction ≤ 1 and bounded CFR delta under
a 20-field-form blur storm and the 50k-session mutation (§6) — is an **M4 gate**, red on
regression.

**Expiry/GC:** idle TTL 30 minutes from `updated_at`; an ADR-06 cron task sweeps expired
sessions (delete row + subscriptions, close channel). Reconnect within TTL resumes by
cursor; after TTL, a fresh mount.

**Size bounds:** a per-session CFR byte cap (256 KB v1) enforced at checkpoint. Breach
truncates the session to mount expression + form draft + subscription set (scroll and
derived UI state dropped, draft preserved); if still over, the session closes with an
`alert`. ADR-07 V5 already guarantees serializability; the cap guarantees size.

### 6. Dependency-exact invalidation; storms bounded

**The closure argument:** inside a component render, the erf read API is the only way to
read a resource — component-kind definitions are granted `erf.read` and not raw
`std/sql`, so a read that bypasses subscription registration is unnameable
(capability-audit V1) and therefore uncompilable. Every erf read records `(resource,
key)` into the session's subscription set: `key = rowId` for point reads, `key =
horizon` for list reads — the same horizon artifact the policy filter uses (ADR-10 §4),
so **invalidation respects policy for free**: a change outside a principal's horizon
never wakes their session. A missed dependency is impossible by construction, not by
test coverage.

**BUILD-D (D3): point-read keys are horizon-qualified.** A point read registers
`key = "rowId:<id>@<horizon>"` (not a bare `rowId:<id>`), and a mutation publishes
`NOTIFY (resource, rowId, horizon)` with the writer's horizon; the match is
`key=rowId:<id>@<horizon> OR key=horizon:<horizon>`. This makes the policy-respecting
guarantee hold for the *point-read* path too: a session that read the same id under an
excluding horizon (its render came back empty) subscribed under a different key, so the
cross-horizon mutation never wakes it. The D3 policy predicate is org-scoping — a resource
carrying an `org` text field filters reads `WHERE org = :horizon`; a resource without one
lives in a single global horizon `"*"` (a named residue: richer role predicates are later).
On an **invalidation** re-render the mount expression is unchanged, so the read-set (and
subscription set) is identical to what is stored — D3 SKIPS the subscription rewrite on
invalidations, relieving SSI write-contention on the shared `(resource,key)` index under a
fan-out storm; the set is rewritten only on an event (which may navigate). The bounded
drain additionally **re-enqueues a serialization-aborted drive** (bounded), so a single
NOTIFY never permanently strands a session.

Every admitted mutation's commit publishes `NOTIFY (resource, rowId, horizon)`. Each
kernel holds the in-memory index `subKey → set(session)` (rebuilt lazily from the
`subscription` table per ADR-06 cold-start rules), marks matches dirty, and enqueues an
invalidation message per session.

**Storm bounding (the 50k-session mutation):** dirty-marking is O(matches) set
insertion; actual re-render→diff→patch is drained by a bounded worker pool, spreading a
large horizon's fan-out across ticks instead of one burst; multiple invalidations for
one session within a tick coalesce to one re-render. Granularity v1 is
per-row/per-horizon — no per-column tracking (deferred, named); over-invalidation within
a component is harmless because the slot diff still ships only changed bytes. The
queue and its drain are instrumented as `sse.invalidation_depth` and
`sse.fanout_lag_ms` (ADR-13 §2), with the 50k-session storm's drain budget standing
as an ADR-13 §3 SLO calibrated at M4 (R1-06: fan-out lag is a named golden signal).

### 7. Forms, validation, concurrent edit

- `form(R)` is derived (ADR-10 §4 artifact 7): fields → `field` primitives by semantic
  type, constraints → the boundary validator, `pii` → masked leaves. R1-INT: a
  `multiselect` tag field renders through the existing `relation`(hasMany) +
  `select`-multi path — it is ADR-10 §5's verifier-checked sugar (R1-14), so the render
  path gains no new slot kind, no new masking leaf, and no new binding surface; V6
  checks the desugared output byte-identical to the hand-written form.
- **Validation is server-authoritative:** submit (or field blur) POSTs; the kernel runs
  the derived `R.parse`; failures return as slot-precise patches into each field's error
  slot and the form `alert`. Derived HTML hints (`required`, `maxlength`) ride the
  static skeleton for feel but are advisory — the 15KB client validates nothing.
- **Concurrent edit — reject-and-reconcile, decided:** every form draft carries the
  `rowVersion` it was opened against; the mutation is guarded `WHERE version = :base`.
  Zero rows ⇒ conflict: the server patches the current row values in with an `alert`
  ("this record changed — review and resubmit"), **preserving the user's draft** in
  unsaved fields. Last-writer-wins is rejected.

**BUILD-D (D3):** the derived `form` template gains ONE trailing form-level `alert` slot;
validation failures and the concurrent-edit reconcile patch target it (per-field error
slots are a named scope reduction for D3, not a per-field slot each). A schema pass adds an
additive `row_version bigint DEFAULT 0` to every derived base table; the mutation guards
`WHERE row_version = :base` and increments it, `rowVersion` is the version the form was
OPENED against (advanced only by a submit — success ⇒ base+1, conflict ⇒ reloaded current
— never by a re-render), and the D3 validator is an honest type-shape subset of the D1
`R.parse` bundle (full-rule parity + field optionality are named residues). The session
event loop is **composed over `cfr.ClaimAndStep`, never forked**: a session-specific arm-CAS
(sleeping/ready at `step_seq`) then ClaimAndStep, with the event/invalidation delivered via
the resume closure; a masked-40001 (25P02) inside the resume is retried at the driver, and
`admission`'s serialization classifier gains the same 25P02 case for its own retry.

### 8. PII masking on the render path: two layers, one kill-test

**Which layer, decided: both, with distinct jobs.** Static — ADR-07 V2 proves at
admission that no component path binds a vault value anywhere but the six masking
leaves (ADR-10 §7), so a non-masking sink never becomes code. Runtime — at slot
materialization inside the kernel, a masking leaf bound to a `pii` value emits the mask
token unless the session principal holds a live reveal grant for that (subject, field);
a revealed materialization is audit-rowed; grant expiry re-masks at the next render of
that slot.

**Invariant: plaintext never enters the slot snapshot** — the snapshot stores the mask
token (plus the grant id when revealed), so no session row, CFR blob, or resync replay
ever contains plaintext; revealed plaintext exists only in the transient SSE frame sent
under the live grant.

**BUILD-D (D3): two digests, resolved.** §4's divergence digest and §8's plaintext-free
snapshot are in tension — the durable snapshot stores the mask *token*, but the client's
slot map (and its DOM) holds the *display* value (plaintext under a live grant), so a
single digest cannot be summed over both. D3 splits them: the per-slot **Diff keys on the
SNAPSHOT (token) map** — so a grant flip (`token`→`token|scope`) or expiry still produces a
patch and the durable CFR/subscription rows/resync replay stay plaintext-free (the §8
kill-test) — while the **wire `snapshotHash` is summed over the DISPLAY map**, the exact
bytes the 15KB client holds and re-sums (§4). Divergence detection therefore compares
like-with-like; the plaintext-free invariant is unweakened because only the transient frame
and the client's in-memory map ever carry the revealed value.

**BUILD-D (D2): the token + grant encoding are concrete.** The mask token is
`"••••·" + <6 hex of FNV-1a-64(resource‖subject‖field)>` — it carries none of the
underlying value, yet distinct masked fields get distinct tokens so the §4 digest tells
them apart. A revealed slot's snapshot is `token + "|" + <grant scope>` (the grant
identity is the `grant_row.scope` = `resource|subject|field`; there is no surrogate id),
so a grant flip shifts the snapshot/digest and expiry re-masks by simply ceasing to
resolve the plaintext. The reveal grant is a `grant_row` with `capability='pii.reveal'`
(the `reveal_grant_human_only` CHECK forbids an agent subject) scoped to that triple with
`expires_at`; a live grant recovers plaintext via the vault (`VaultReveal`) into the frame
value only and writes one `reveal_audit` row per revealed materialization. The static half
(V2) is extended so a pii value bound at any component site OTHER than the six masking
leaves is `PII_NONLEAF_BIND` at admission — a non-leaf render sink never becomes code.

**The no-plaintext-without-grant kill-test:** render every reference-app view over
seeded PII with no grant; grep every session row, CFR blob, subscription row, and
captured SSE frame for the seeded values ⇒ absent. Mint a grant, reveal, expire it ⇒
plaintext appears only in frames timestamped inside the grant window, an audit row
exists per reveal, and the post-expiry re-mask patch is observed. ADR-13 §6 extends
this sweep to the kernel's own emissions — the stdout event stream, exporter batches,
and health-port responses are grepped for the same seeded values, so telemetry cannot
become the unmasked side channel durable rows are forbidden to be (R1-06: PII
kill-test extended to telemetry).

### 9. Felt-latency machine gate: WAN-throttled, M4→v1 (R1-07: felt-slow is a machine gate, not a wait-for-complaints trigger)

v1 ships without optimistic local echo (§3), so "does it feel instant?" is **not** left to
user complaints on the reference envelope — it is a **machine gate** on the M4→v1 release
suite, red until green. A harness drives the reference app end to end over a named
throttled WAN profile and asserts hard felt-latency budgets; exceeding any budget fails
the release.

- **Profile `wan-150`** (named, deterministic): 150 ms RTT, 1.6 Mbps down / 768 Kbps up,
  applied by the harness's throttle so every kernel measures the same link.
- **Felt-latency budgets** (p95 over the reference-app clickthrough):
  - **input→echo ≤ 50 ms** — from a keystroke/click to the first visible UI change.
  - **action→confirmed-commit render ≤ 300 ms** — from a single-mutation action to the
    committed server patch applied in the DOM.
- **Failure is backpressure on the release, not on the user.** If a budget is exceeded the
  release is red and the remedy blocks it: the deferred optimistic local echo behind the
  tested client state machine (§3's five client duties) — or a better fix — must land and
  turn the gate green. Under `wan-150` a pure server round trip cannot meet input→echo
  ≤ 50 ms, so the gate is the forcing function that lands echo the moment it is actually
  needed. The feature is contingent; the **gate** is the finding. This replaces the prior
  complaint-driven trigger ("measured WAN latency complaints"): the customer is never the
  latency instrument.

## Alternatives Considered

- **simplest-thing:** the same core (slot diff, SSE+POST, sessions-as-rows,
  subscription registration) with no divergence recovery, no storm bounding, no session
  size cap, and no concurrent-edit answer — four of this layer's five production wounds
  unaddressed. Its four-duty client framing and 30-minute TTL are adopted.
- **prior-art-faithful:** per-slot `fieldMask` subscription granularity (rejected for
  v1 — per-column tracking is machinery the harmless-over-render argument makes
  unnecessary; deferred, named); WAN-adaptive optimistic local echo and a client-side
  `no-persist` flag (rejected — an untested client state machine and a client-trust
  boundary; §8 keeps plaintext out of durable state server-side, trusting no client
  flag). Its WebSocket-is-sticky argument is adopted as §2's primary reason, and its
  policy-respecting-invalidation framing sharpened §6.
- **red-path-first (winner):** §§1–8 are substantially its design — snapshotHash
  resync, size caps, bounded fan-out, rowVersion reconcile, the read-path closure
  argument, the grep kill-test. Corrections: its admission-time session-size verifier is
  cut (statically bounding closure growth is not decidable and V5 already owns
  serializability — the runtime cap is the enforcement); its client hash duty is folded
  onto the slot map rather than DOM re-parsing; session wakes are unified onto ADR-05's
  one-wake `message` channel rather than a separate event path.

## Consequences

- Per-interaction server cost is bounded and legible: claim CAS + re-evaluate changed
  slots + one checkpoint transaction — no tree reconciliation ever enters the
  interpreter, which is the constraint-#2 defense on the hottest path.
- Sessions survive kernels, deploys, and reconnects because they are rows; the price is
  a checkpoint write per interaction, **budgeted at ≤ 1 write/interaction with CFR delta
  ≤ 64 KB p95** (§5, ADR-04 §8) and accepted inside the I/O-bound envelope (R1-07:
  checkpoint-write budget).
- The client is a commodity: five duties, no framework, no build pipeline; every
  correctness burden (validation, masking, routing, state) is server-side where the
  verifiers can see it.
- The incremental summed FNV-64 snapshot digest (§4) trades cryptographic strength for
  O(changed-slots) 15KB-client cheapness; a digest collision can delay divergence
  detection by one frame, and the next frame's digest catches it — accepted and stated
  (R1-07: incremental digest, one-frame-delay tradeoff retained).
- Per-column invalidation, optimistic echo, and offline drafts are deferred by name;
  each is additive behind the same slot/patch contract — and optimistic echo's deferral
  is held to the §9 WAN felt-latency machine gate, not to user complaints (R1-07).

## Red-Path Tests Implied

- **Exactness:** mutate row R ⇒ only sessions whose subscription set covers R receive
  frames; an unrelated session's channel shows zero frames; a session whose principal's
  horizon excludes R receives nothing (policy-respecting invalidation).
- **Kernel death mid-session:** kill the kernel between claim and checkpoint ⇒ the
  lease lapses, the next event resumes on another kernel from the prior checkpoint; no
  lost draft, no duplicate mutation (ADR-05 tests 1/5 on the UI path).
- **Divergence:** corrupt one applied frame client-side ⇒ next frame's snapshotHash
  mismatches ⇒ resync ⇒ client and server snapshots equal; assert exactly one full
  re-render.
- **Reconnect / empty-diff invariant** (§2, R1-08): drop SSE for 2 minutes across a
  **no-change invalidation** (which checkpointed a zero-op frame), reconnect with the cursor
  ⇒ delta replay from `eventSeq` yields **no gap and no full repaint**, and the replayed
  zero-op frame's `snapshotHash` matches; a reconnect whose `Last-Event-ID` predates the
  retained frame buffer (or names a session GC'd past TTL) ⇒ exactly one full resync
  (`sse.resyncs_total` increments), never a silent gap; reconnect after TTL ⇒ clean fresh
  mount; a heartbeat comment never advances the client cursor.
- **Storm:** one mutation in a horizon with 50k subscribed sessions ⇒ all sessions
  patched within the drain budget, kernel stays live, per-tick coalescing observed
  under a concurrent mutation burst.
- **Size cap:** a session accreting UI state past 256 KB ⇒ truncation preserving the
  form draft; assert the draft survives and derived state is rebuilt on next render.
- **Concurrent edit:** two sessions submit one row ⇒ first commits; second gets the
  conflict alert with its draft intact; no silent clobber.
- **Double event:** replay one event POST twice (retry) ⇒ one CAS wins, one mutation,
  one patch; `eventSeq` advances once.
- **PII grep** (§8 kill-test): seeded plaintext absent from every durable row and every
  no-grant frame; reveal audited; expiry re-masks.
- **Incremental digest** (§4, R1-07: O(changed slots)): editing one slot of a 2000-slot
  view updates both server and client digests by one term (a microbench asserts per-event
  hash cost is O(changed slots), not O(view)); a mid-sequence value change still yields a
  digest byte-equal to a full recompute; a seeded one-frame corruption still resyncs.
- **Checkpoint-write budget** (§5, R1-07: writes/interaction ≤ 1): a 20-field-form blur
  storm and the 50k-session mutation each write ≤ 1 checkpoint row per interaction with
  CFR delta ≤ 64 KB (p95); a regression is red at the M4 gate.
- **Felt-latency gate** (§9, R1-07: budget regression is red): the reference-app
  clickthrough over `wan-150` holds input→echo ≤ 50 ms and action→confirmed-commit render
  ≤ 300 ms (p95); a budget regression fails the M4→v1 release, and the echo remedy (or
  better) is what turns it green.

## Constraints Discharged or Budgeted

1. **Consumed and exercised.** UI sessions are ADR-05 rows on the highest-traffic
   surface — the deepest bet is kill-tested thousands of times a day by ordinary use;
   the size cap budgets its storage half.
2. **Discharged for the render path.** Slot re-evaluation only, no VDOM, bounded
   fan-out, reads through erf into SQL; the interpreter's per-interaction work is a
   handful of expression evaluations.
3. **Consumed.** Durable conditions surface as `alert` + restart `button`s in the same
   patch machinery (operator plane, ADR-12 §6).
4. **Discharged.** The static/dynamic split is an explicit derivation pass over the
   canonical AST — the render template is the homoiconicity payoff made operational.
5. **Budgeted.** Masking's static half is V2; the runtime leaf check and the grep
   kill-test state the render path's coverage; the client is untrusted by design.
6. **Budgeted.** SSE not WS, no optimistic machinery, no client framework, deferred
   per-column tracking: the layer is one derivation pass, one patch codec, one small
   client. The felt-latency budget is a WAN-throttled machine gate (§9) and the
   checkpoint-write budget is enforced end-to-end at M4 (§5), so "budgeted" here means
   numbers, not intentions (R1-07: felt-latency + checkpoint-write gates).
