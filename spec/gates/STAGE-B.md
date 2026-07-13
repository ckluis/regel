# STAGE-B gate report (= deepest bets under kill-tests, M0 CFR core → M2)

*Author: BUILD-B (fable sub-orchestrator). Date: 2026-07-13. HEAD: `8ef56e2`.*
*Verdict for the operator: **STAGE B GREEN with named residues** — the ADR-05 kill
suite is 10/10 green (plus tests 12/13/14); a real `kill -9` mid-workflow resumes
cross-process to the byte-identical result with effects exactly-once; wakes, joins,
sends/receives, and the reaper are catalog rows driven by a real reactor; fuel halts
at the exact budget; the capability resume path refuses smuggled tokens with zero
effects; the SERIALIZABLE retry policy + abort budget (REPORT-R1 P2-6) is implemented
and measured under the budget. Re-decide Stage C at this gate per STATE.md.*

## 1. What was built

Four increments, red-path-first (RED commits precede GREEN in history):

| Commit(s) | Content | ADR |
|---|---|---|
| `79e0732` | ADR-first Stage B pins (BUILD-B markers) before any machinery | ADR-05/13 |
| `602de62`→`cff923f` | cek wake machinery: `Wake`/`NativePark`/`Delivery` seam, `ParkWake`/`ParkFresh` (append-only ParkKinds), `Resume(ctx, st, Delivery, Principal)` (fixes the Stage-A dropped-principal gap), `InitialState` (CFR seed for fresh workflows/join children), `std/wf.sleep|receive|send|all|race` natives, runtime capability gate on `std/mail.send`, `cfr.EncodeValue`/`DecodeValue`, schema: `continuation.result`, `'cancelled'`, `channel_message`, `outbox`, `epoch_current` | ADR-05 §2/§5, ADR-04 |
| `e2a54b0`→`4297886` | Durable store + reactor: `ClaimAndStep` (epoch-fenced SERIALIZABLE step txn: guard read + pre-COMMIT re-check → claim CAS → live grant reload → fail-closed decode → capability-token re-validation BEFORE machine re-entry → delivery by ParkKind → fenced checkpoint), `ParkOutcome` (result column, outbox rows under the UNIQUE dedup key, channel claim + receiver flip, join quorum computed-not-counted, race winner + `'cancelled'` losers), `StartWorkflow`/`SendChannel`/`RetrySerializable`, reactor (timer scanner, SKIP LOCKED drain, heartbeat, paced reaper with fresh-step_seq re-offers, LISTEN task, fence trip → `epoch.fence_tripped` + terminal drain), HTTP `/workflow` `/channel/{c}/send` `/healthz`, `regel step-once` | ADR-05 §4-§8, ADR-06 §4-§6, ADR-08 §2 |
| `92d8bd9`…`8ef56e2` | Process-level: `-lease`/`-poll` serve flags, e2e harness (built binary, real processes), ADR-05 tests 1/2/9-at-10k/12, `scripts/demo-kill9-resume.sh`, perf evidence + `perf_budget` rows | ADR-05 tests, ADR-13 |

## 2. (a) ADR-05 kill suite — 10/10 (+12/13/14)

| # | Test | Status | Where |
|---|---|---|---|
| 1 | Crash mid-await + cross-kernel resume (real SIGKILL) | **GREEN** | `TestKill9CrossKernelExactlyOnce` (§3) |
| 2 | Year-old resume (+1 epoch, moved head) | **GREEN** | `TestYearOldResume` (below) |
| 3 | As-of resume (3× re-admit while parked) | **GREEN** | `TestReactorAsOfResume` |
| 4 | Poison-pill: (b) corrupt CFR fails closed | **GREEN** (Stage A) — (a) capture-verifier leg is the V5 Stage-C residue (§10) | `TestCorruptCFRFailsClosed`/`TestCorruptCFRDB` |
| 5 | Double-resume race, zombie CAS fence | **GREEN** | `TestDoubleResumeCAS`, `TestConcurrentClaimExactlyOne` |
| 6 | Fuel exhaustion mid-step, exact budget | **GREEN** | §4 |
| 7 | Torn write | **GREEN** (Stage-A subset: aborted park txn ⇒ zero rows; full per-statement injection sweep is a named residue §10) | `TestTornWriteRollsBack` |
| 8 | Capability-revoked resume (call leg + token-at-claim leg) | **GREEN** | §5 |
| 9 | Wake storm 10k, multi-kernel, exactly once | **GREEN** | below |
| 10 | Join: all/race quorum, cancelled losers, no double-decrement | **GREEN** | `TestReactorJoinAll`/`JoinRace` |
| 12 | Cross-kernel randomized hermeticity probe | **GREEN** | `TestHermeticityCrossKernel` (below) |
| 13/14 | Wake discriminator + condition-integrity CHECKs | **GREEN** | `catalog_test.go` (`wake_bogus_kind_rejected`, `resolved_consistency_*`, `class_shape`) |
| 11 | Decode-coverage monotone floor | **RESIDUE** (§10) — binds at the first epoch that adds a frame kind / CFR version | — |

Captured (test 2, year-old — real PG, 2026-07-13):

```
yearold_test.go:98: YEAR-OLD RESUME: id=e7c77dc4-76bf-433d-a439-ca55365abd9b rest_age=400 days,
    epoch_current advanced 1→2, head moved r1_j4whj201q→r1_0twz81t57, resumed against ORIGINAL
    def_hash, result=42, provenance epoch stamp stayed 1
--- PASS: TestYearOldResume (0.25s)
```

Captured (test 9, 10k storm — 3 reactor instances, one PG):

```
storm10k_test.go:121: WAKE STORM 10k: 10000 workflows done, outbox=10000 (0 dupes),
    elapsed=2.260629917s, aborts=90, abort_rate=0.0089 (<=0.05 budget), reoffers=0
--- PASS: TestWakeStorm10k (2.52s)
```

Captured (test 12, hermeticity — ONE parked continuation cloned 6×, each resumed by a
SEPARATE `regel step-once` process under a GOMAXPROCS {1,4} × GOGC {50,100,400} matrix;
distinct processes ⇒ distinct Go map seeds):

```
hermeticity_test.go:119: clone 0 ([GOMAXPROCS=1 GOGC=50]):  frames_sha256=2f4683246b53de9a…a551ad result_sha256=3ef3eb68f88793a9…4e621319
hermeticity_test.go:119: clone 1 ([GOMAXPROCS=4 GOGC=50]):  frames_sha256=2f4683246b53de9a…a551ad result_sha256=3ef3eb68f88793a9…4e621319
    … (clones 2-5 identical) …
hermeticity_test.go:145: HERMETICITY: 6/6 clones byte-identical across distinct processes
    (GOMAXPROCS 1/4 x GOGC 50/100/400):
    frames=2f4683246b53de9a2519076ea694aa2ca469833bceba670bdc585d61e1a551ad
    result=3ef3eb68f88793a9d2122aa534b980ddac9efa5287ae7dd2d7b8b0004e621319
--- PASS: TestHermeticityCrossKernel (1.51s)
```

## 3. (b) kill -9 mid-workflow → identical result (THE acceptance test)

Go e2e (real built binary, real SIGKILL, two independent kernel processes,
`TestKill9CrossKernelExactlyOnce`, real PG 16.13, 2026-07-13):

```
e2e_test.go:410: REFERENCE LEG: id=b2a044b7-419c-487b-ad97-6460179c409e result=10000 outbox=4
    trace="channel.send@1.0;channel.send@2.0;channel.send@3.0;channel.send@4.0;"
e2e_test.go:448: KILL: SIGKILL kernel A pid=76782 at moment [outbox=1 running_tasks=1
    continuation_status=ready step_seq=1]
e2e_test.go:455: STRANDED: continuation_status=ready running_tasks=1 (lease will expire →
    reaper re-offers)
e2e_test.go:468: RESUME (kernel B): id=776defe2-397b-48b7-8267-29a08aa54058 result=10000
    outbox=4 trace="channel.send@1.0;channel.send@2.0;channel.send@3.0;channel.send@4.0;"
    healthz.reoffers=1
e2e_test.go:484: EXACTLY-ONCE VERIFIED: result identical (10000), outbox exactly 4, trace
    identical, reoffers=1
--- PASS: TestKill9CrossKernelExactlyOnce (6.48s)
```

Runnable demo `scripts/demo-kill9-resume.sh` (fresh DB, exit 0):

```
kill-leg workflow: ae89d549-8e32-4fb8-8ef0-83a1ad1c318d
kill moment: [outbox=1 running_tasks=1 status=ready step_seq=1]
>>> kill -9 79733 (kernel A — no graceful shutdown, no lease release)
stranded: continuation status=ready step_seq=1 (running task lease expires in <=2s)
STEP 3: restart — kernel B (NEW process, same DB) reaps the lease and resumes
kernel B up (pid 79889): serve: regel kernel abc5db12-… listening on :8794 (epoch 1, lease 2s, poll 100ms)
kill-leg result: 10000
kill-leg outbox trace (4 rows): channel.send@1.0;channel.send@2.0;channel.send@3.0;channel.send@4.0
kernel B healthz reoffers: 1
PASS: result identical across the kill (10000 == 10000)
PASS: outbox exactly 4 rows (no double effect, no missing effect)
PASS: ordered effect trace identical
PASS: zero duplicate dedup keys in the outbox
PASS: kernel B re-offered the dead kernel's work (reoffers=1)
DEMO OK — kill -9 mid-step, cross-kernel resume, effect exactly once   (exit 0)
```

## 4. (c) Exact-budget fuel

```
fuel_test.go:32: EXACT-FUEL: total transitions T=1022, result=1225
fuel_test.go:39: EXACT-FUEL: Fuel=T(1022) → Done, transitions=1022
fuel_test.go:52: EXACT-FUEL: Fuel=T-1(1021) → Parked at transitions=1021, class=fuel.exhausted
fuel_test.go:61: EXACT-FUEL: T-1 park + grant-fuel(1) → Done, resumed transitions=1
fuel_test.go:69: EXACT-FUEL: Fuel=T-5(1017) → Parked at transitions=1017
fuel_test.go:80: EXACT-FUEL: +grant-fuel(4) → re-Parked, resumed-leg transitions=4
fuel_test.go:88: EXACT-FUEL: +grant-fuel(1) → Done, resumed-leg transitions=1, result=1225
--- PASS: TestExactBudgetFuel (0.00s)
```

T=1022 for the fixture: Fuel=1022 ⇒ OutDone; Fuel=1021 ⇒ parks with Transitions **exactly 1021**
(`fuel.exhausted`, restarts grant-fuel/abort as rows); +grant-fuel(1) ⇒ completes to the
identical result (1225). Fuel=1017 ⇒ parks at exactly 1017; +grant-fuel(4) ⇒ re-parks with the
resumed leg's Transitions **exactly 4**; +grant-fuel(1)… ⇒ completes, result identical. The
halt is at the exact transition, not approximate; the park is a durable condition with restarts.

## 5. (d) Capability smuggle refused with zero trace

```
--- PASS: TestReactorCapabilityRevokedAtCall (0.11s)          # grant deleted while parked;
    # resumed step's mail.send parks capability.revoked; ZERO new outbox rows;
    # re-grant restart (operator-gated) completes the workflow
--- PASS: TestReactorCapabilityTokenRefusedAtClaim (0.11s)    # the SMUGGLE test: a forged
    # TagCapToken("mail.send") injected into the parked CFR blob of a principal holding
    # no such grant; on resume the claim transaction refuses it BEFORE the machine
    # re-enters: status='condition', durable_condition class='capability.revoked' == 1,
    # outbox rows for the continuation == 0 (machine never re-entered — zero trace)
```

Two independent fences on the resume path:
- **Token-at-claim (ADR-05 §4):** a `TagCapToken` in the decoded CFR whose grant is revoked/
  foreign/expired is refused BEFORE the machine is re-entered — the step parks
  `capability.revoked` in the claim transaction; zero effects run, zero outbox rows.
- **Call-time gate:** `std/mail.send` under a principal without the grant parks
  `capability.revoked` with zero recorded effects; the `re-grant` restart (operator-gated)
  completes the workflow. Grants are reloaded LIVE from `grant_row` at every claim — revocation
  reaches sleeping workflows at their next step, auditably (ADR-05 §4).

## 6. (e) Wakes / joins / sends / reaper under restart

- `wf.sleep`: sleeping rows with fixed-width UTC ISO-8601 `due` (BUILD-A text-order contract),
  timer scanner flips + resume task, partial-index-served.
- `wf.receive`/`wf.send`: `channel_message` rows; send txn claims the message for the oldest
  matching sleeping receiver + flips + resume task in ONE transaction; send-before-receive
  claims immediately at park. (`TestReactorReceiveSend`)
- `wf.all`/`wf.race`: children materialized by the parent's park transaction; quorum computed
  by counting terminal siblings under a status CAS (idempotent — the duplicate-task crash leg
  asserts the parent flips exactly once); race losers → `'cancelled'`, their zombie steps die
  at the fenced checkpoint. (`TestReactorJoinAll`/`JoinRace`)
- Reaper: `TestReaperReoffersStranded` (abandoned 1s lease re-offered ≈2s, second kernel
  completes, outbox UNIQUE proves exactly-once) and the kill-9 e2e (healthz `reoffers=1` after
  restart).

```
--- PASS: TestReactorSleepEndToEnd (0.36s)  --- PASS: TestReactorReceiveSend (0.32s)
--- PASS: TestReactorJoinAll (0.65s)        --- PASS: TestReactorJoinRace (0.20s)
--- PASS: TestReaperReoffersStranded (1.21s)
--- PASS: TestEpochFenceStoreLevel (0.11s)  --- PASS: TestEpochFenceReactorDrains (0.15s)

Fence-trip structured event, captured from the reactor drain test (ADR-08 §2 shape):
{"action":"drained_and_exited","event":"epoch.fence_tripped","in_flight_aborted":true,
 "kernel_id":"6242d55b-4ffc-477b-b759-f0a1a68cd9a3","leases_released":true,
 "observed_epoch":2,"required_epoch":1,"ts":"2026-07-13T23:35:50.443588Z"}
```

## 7. (f) SERIALIZABLE retry/abort budget (REPORT-R1 P2-6, binds at M2)

Policy implemented per ADR-05 §7 BUILD-B: retry on 40001/40P01, 5 attempts, 10 ms base,
×2, 500 ms cap, full jitter; exhaustion falls back to the lease/reaper path (liveness
delayed, correctness untouched). Counters: `pg.serialization_aborts_total`,
`pg.serialization_retry_exhausted_total` (ADR-13 rows 25/26).

| Run | Steps | Aborts | Abort rate | Budget |
|---|---|---|---|---|
| Wake storm 2 000 × 3 kernels | 2 000+ | 52 | **2.53 %** | ≤ 5 % PASS |
| Wake storm 10 000 × 3 kernels | 10 000+ | 90 | **0.89 %** | ≤ 5 % PASS |

`TestRetrySerializableExhausts`: a persistent 40001 exhausts exactly 5 attempts and
increments the exhaustion counter once. `perf_budget` row `step.abort_rate` written
(epoch 1, milestone M2).

## 8. (g) Performance vs ADR budgets

| Budget | Required | Measured | Status |
|---|---|---|---|
| `continuation.resume_latency_ms` p95 (ADR-13 M2) | ≤ 5 s | **57.3-59.8 ms** (n=50) | PASS (~85×) |
| CFR blob per park p95 (ADR-04 §8 ≤64KB delta) | ≤ 64 KB | **199 bytes** (kill-9 workflow parks) | PASS (~330×) |
| `step.abort_rate` (ADR-05 §7 BUILD-B) | ≤ 5 % | 0.89 % (10k storm) | PASS |
| 10k wake storm drain | exactly-once | 10 000/10 000, 0 dupes, 2.26 s | PASS |

## 9. ADR updates forced by build discoveries (ADR-first, `BUILD-B:` markers)

1. **ADR-05 §2** — `continuation.result` column (durable terminal value, CFR value codec):
   kill-9 identical-result and 202-then-poll both need the produced value durable; no ADR
   stored it (STAGE-A residue "durable result storage").
2. **ADR-05 §2** — `'cancelled'` status: race losers were unrepresentable in the closed
   status set ('done' fabricates a result, 'failed' fabricates an error).
3. **ADR-05 §5** — `all`/`race` take THUNK arrays (the machine evaluates calls eagerly and
   inline, so promise-valued args would already have run); children are rows written by the
   parent's park transaction; quorum is COMPUTED, never counted (idempotent flip — the
   double-decrement of test 10 is structurally impossible); `channel_message` DDL authored.
4. **ADR-05 §7** — `outbox` DDL authored (was referenced by ADR-06 §5 `deliver` tasks but
   DDL'd nowhere); dedup key `UNIQUE (continuation_id, step_seq, ordinal)`.
5. **ADR-05 §7 + ADR-13 rows 25/26 + M2 SLO row** — the REPORT-R1 P2-6 (Kleppmann)
   retry-on-40001 policy and abort-rate budget, previously unspecified.

## 10. Named residues (nothing silent)

1. **ADR-05 test 4a** (capture verifier rejects a connection-typed live-across-await value
   at admission): V5 is Stage C's verifier-roster work; the CFR type table it must share
   lives in `internal/cfr` (encodable ≡ admitted). Runtime is already fail-closed (4b).
2. **ADR-05 test 11** (decode-coverage monotone floor): the required grid is computable but
   only CFR-1 and one decoder generation exist; the gate binds at the first epoch that
   appends a frame kind/CFR version. `continuation_coverage` table exists (Stage A).
3. **ADR-05 test 7 full sweep**: torn-write is covered by the aborted-txn leg + kill-9
   at process granularity; a per-statement fault-injection sweep inside the checkpoint
   transaction remains Stage-C hardening.
4. **Outbox dispatcher**: intent rows are written exactly-once and are the audit surface;
   the ADR-06 `deliver`-task dispatcher that pushes them across the process boundary
   (effectively-once with dedup keys) is not yet driven — no external sinks exist at M2.
5. **Message wakes**: `match:<pred>` predicates and `event` (record-change) wakes are not
   implemented — channel receive is FIFO-by-channel; `event` binds with the reactive layer
   (Stage D). `wake_kind_shape` already admits them.
6. **Reaper breaker** (ADR-13 §5 / ADR-06 §5): pacing + bounded batches + jittered re-offer
   exist; the sliding-window trip/half-open breaker state machine and `reaper.breaker_*`
   signals are Stage-C observability work.
7. **Epoch fence**: guard + pre-COMMIT re-check + terminal drain are real and tested at
   store and reactor level; `--wait-for-epoch` staging, `migrate N` machinery, and the O5
   fleet drill are Stage E per GATE-1.
8. **Cron/deliver task kinds**: table + CHECKs exist; only `resume` is driven.
9. **Build deviations, stated:** (i) a non-closure `wf.all`/`wf.race` thunk element fails
   closed as a durable-condition park of class `wf.thunk` (restart `abort`) rather than a
   raw machine fault — the `NativePark{Condition,Wake}` seam has no fault arm; (ii) join
   children carry their parent pointer as `join_parent`/`join_ordinal` keys in the child's
   `principal` jsonb (documented in code; the `wake_kind_shape` CHECK constrains only
   `"kind"`); (iii) the Stage-A M0 timing microbench was made best-of-3 under parallel
   load (`8ef56e2`, budgets unchanged); (iv) `perf_budget` numbers were measured on an
   otherwise-idle M4 — the 10k storm used 3 in-process reactor instances (distinct kernel
   ids, one PG), while CROSS-PROCESS multi-kernel is exercised by test 1 and the demo.

## 11. What Stage C should watch

- The `ClaimAndStep` step transaction is THE seam: epoch fence, claim CAS, grant reload,
  token re-validation, delivery, fenced checkpoint — verifiers (V2-V6) and MCP doors must
  compose with it, not around it.
- V5 must share `internal/cfr`'s type table (encodable ≡ admitted) — the codec is the
  single source of truth for the serializable lattice.
- `principal` jsonb carries `subject` (+ `operator` flag, + `join_parent`/`join_ordinal`
  for children); grants are NEVER persisted in it — they reload live per step. Keep it so.
- Timer `due` text-order contract binds every future scanner change (fixed-width UTC).
- The abort-rate budget is measured at storm scale; MCP fan-outs at Stage C should re-run
  the storm with their own transaction mix before trusting the 5% headroom.
