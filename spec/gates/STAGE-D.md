# STAGE-D gate report (= M3 the world + M4 the reactive layer)

*Author: BUILD-D (fable sub-orchestrator). Date: 2026-07-16. HEAD: `1042a4d`.*
*Verdict for the operator: **STAGE D GREEN with named residues** — the std/ world
is complete as rows (14 SHIP batteries + the 25-component tier-1 roster, 72 genesis
entries) behind a genesis transaction proven reproducible across two fresh databases
and empty-or-complete under a 219-boundary mid-genesis kill sweep; the full erf
`resource(...)` derivation runs behind the existing step-5a seam (13 field types +
`pii()` modifier, eleven artifact passes incl. vault routing with working
crypto-shred + attestation, trigger-written history that excludes PII, V6
derivation-parity red-pathed); the ADR-11 reactive layer is live end-to-end
(admission-time static/dynamic split, binary patch frames over SSE with POST-up,
incremental order-independent snapshot digest, snapshotHash desync→resync
self-healing, sessions as capped/TTL'd continuation rows, dependency-exact
policy-respecting invalidation, six-leaf PII masking with reveal grants + audit +
the no-plaintext grep kill-test extended to telemetry); taak workflows author
against std/taak with await-as-checkpoint and effect-class conformance, and the
STAGE-B §10 residues are discharged (outbox dispatcher effectively-once, message
match-predicates, event wakes, reaper breaker); the native TCB is attacked by a
release-gating adversarial harness with monotone coverage + trusted-for rows.
The 50k-session storm drains in 33.6s (budget 90s, calibrated + pinned) with
exactly-once patching and a live kernel throughout; the `wan-150` felt-latency
gate ran RED (161.3ms input→echo) exactly as ADR-11 §9 predicted, forced the
optimistic-local-echo remedy, and reads GREEN (0.0ms input→echo / 160.9ms
action→commit, both under budget). Uncached `go test ./...` is green vs real
PostgreSQL 16.13 at `1042a4d`. **Operator re-decision required before Stage E**
(GATE-1 §5).*

## 1. What was built (30 commits, `39110b4`..`1042a4d`)

| Commit(s) | Content | ADR |
|---|---|---|
| `7e0bbe5`→`d6370e6` | **D0** std/ roster complete: +9 modules, +45 entries (24→69; 72 by gate-run) — identity/http/time/money/crypto/test/log/erf/ui incl. the 25 tier-1 components as native record constructors; effect classes on 9 natives; genesis gate battery (RED `ab2d150` witnesses the two missing controls: reverse-bijection refusal + structured attestation refusal) | ADR-10 §2/§3/§7 |
| `c5e753f`→`c31170b` | **D1** full erf derivation: 13 field types + `pii()` (closed bundle table `fieldtypes.go`), eleven passes (schema, history, validator, policy, vault, horizon, components, openapi, mcptools, catalog, template⁺), vault/vault_key/shred_attestation + `regel shred`, V6 `DERIVE_PARITY` + KT-A3 arms | ADR-10 §4/§5 |
| `206ad5f`→`3c7016b` | **D2** pure render machinery (`internal/ui`: template/render/diff/digest/codec/mask) + template derivation pass + render-path masking (reveal grants on `grant_row`, `reveal_audit`); V2 `PII_NONLEAF_BIND` (RED `088d528` → GREEN `f1c0cde`); corpus fixture `v2-pii-nonleaf-component` | ADR-11 §1/§4/§8, ADR-10 §7 |
| `819428d`→`02d7ecf` | **D3** reactive runtime: sessions as `continuation kind='session'` rows, `subscription` table, SSE + `Last-Event-ID` + zero-op frames + heartbeats, POST-up event loop composed over `ClaimAndStep`, forms (server validation + rowVersion reject-and-reconcile), NOTIFY invalidation + coalescing + bounded drain, 30-min TTL sweep, 256KB cap, 7.8KB five-duty client; ten ADR-11 red-paths | ADR-11 §2/§3/§5/§6/§7 |
| `8f5cded`→`37e4b36` | **D4** std/taak surface (sleep/receive/send/all/race/signal/onChange), effect-class conformance gate at `performNative`, message match-predicates, event wakes (`cfr.WakeEvents`), outbox dispatcher (deliver tasks, pluggable sink, effectively-once), reaper breaker (closed/open/half-open + `reaper.breaker_*`), kill-9 taak restart test + `demo-taak.sh`, session↔workflow bridge | ADR-10 §6, ADR-05 §5/§7, ADR-13 §5 |
| `22a7dda`→`1cc8b66` | **D5a** native-TCB adversarial harness `gate/nativetcb` (vault-leak / contract-violation / effect-order seeded evil natives), `native_tcb_coverage` monotone rows + trusted-for inventory, shipped-image purity | ADR-10 §8 |
| `c49373b`→`4483efb` | **D5b** optimistic local echo (landed by the §9 forcing function, digest-neutral, originating-slot-only), 50k storm + wan-150 + checkpoint-write-budget + PII telemetry sweep gates, perf_budget rows | ADR-11 §3/§5/§6/§9, ADR-13 §3/§6 |
| `a269a44` | demo-erf-derive.sh + demo-reactive.sh (both exit 0) | — |
| `ff21ab4` | **fix found by demo-reactive step 9:** `cmdServe` never started the reactive loops — `StartSessions` (invalidation LISTEN + TTL sweeper) now runs in the serving kernel; step 9 is a hard cross-session assertion | ADR-11 §5/§6 |
| `1042a4d` | **fix found by the whole-suite run:** pgwire pooled-conn 57014 poisoning — a fired out-of-band CancelRequest taints the conn; Release destroys tainted conns (RED witnessed on the unpatched pool) | — |

⁺ the ten ADR-10 §4 artifacts plus the ADR-11 §1 render-template pass; V6 asserts the emitted set exactly.

## 2. Acceptance — uncached full suite (real PG 16.13, 2026-07-16, HEAD `1042a4d`)

```
$ go clean -testcache && go test ./...
ok  regel.dev/regel/internal/admission  19.6s     ok  internal/lower    0.8s
ok  regel.dev/regel/internal/catalog     6.1s     ok  internal/mcp     11.0s
ok  regel.dev/regel/internal/cek         2.0s     ok  internal/oracle   1.8s
ok  regel.dev/regel/internal/cfr         7.9s     ok  internal/pgwire   2.6s
ok  regel.dev/regel/internal/gitproj     7.0s     ok  internal/rast     1.7s
ok  regel.dev/regel/internal/kernel    431.9s     ok  internal/tsx      1.2s
                                                  ok  internal/ui       1.0s
(cmd/regel, gate/nativetcb, gate/redpath, internal/mutants: no test files)
```
The kernel package includes the long gates (50k storm 74s, wan-150 44s) unguarded —
the full suite runs them every time. An earlier full run failed
`TestOutboxDispatcherEffectivelyOnce` once with SQLSTATE 57014; root-caused (not
flake-waved) to pgwire pooling a conn with an in-flight cancel and fixed at
`1042a4d` with a RED-witnessed regression test (`TestPoolNeverReusesCancelTaintedConn`).

## 3. (a) erf derivation — red + green per field type, vault routing, crypto-shred

```
--- PASS: TestFieldTypeBundleTotality        (closed 13-type bundle: every base has
--- PASS: TestRosterTotalityEndToEnd          validator/input/render/mask; mask leaf ∈
--- PASS: TestPiiWrapTotality                 cek.MaskingLeaves; render ∈ UITier1)
--- PASS: TestHistoryExcludesPii
--- PASS: TestVaultSealAndCryptoShred
```
`TestRosterTotalityEndToEnd`: one resource exercising ALL 13 types + 2 pii wraps ⇒
eleven artifacts, live table + history trigger + vault, V1–V6 green.
`TestPiiWrapTotality`: 11 scalar pii wraps admit; `pii(address)` (composite, no
single mask leaf) ⇒ `DERIVE_PARTIAL` — the row never exists. `pii(relation)`/
`pii(select)` are unrepresentable at the L0 surface (cannot be declared at all).
`TestHistoryExcludesPii`: UPDATE writes a history row; pii plaintext absent from
base AND history.

Runnable transcript (`scripts/demo-erf-derive.sh`, exit 0):
```
derived_artifact passes for app/derive/Contact:
  catalog, components, history, horizon, mcptools, openapi, policy, schema,
  template, validator, vault
grep 'ada.lovelace@acme.example' in base table:    ABSENT (pass)
grep 'ada.lovelace@acme.example' in history table: ABSENT (pass)
vault.ciphertext = efbe9bdd432185eed0259587880fd88e1f76…  (≠ plaintext)
shred: app/derive/Contact subject 1 — 1 key(s) destroyed, attestation #1
vault_key rows for subject 1: 0 (gone); ciphertext blob remains, permanently
undecryptable; reads return the mask token
```

## 4. (b) V6 derivation-parity red-path

```
--- PASS: TestDeriveParityTamperRejects       "must be DERIVE_PARITY, got admitted"
--- PASS: TestKTA3VaultRouteSuppressedRejects "must be DERIVE_PARTIAL, got admitted"
```
Both REDs were captured by disabling the control (mutants pattern) before wiring it
GREEN in `c31170b`. V6's pre-existing checks are untouched: the redpath corpus twins
`v6-derive-partial` / `v6-ddl-destructive` and the Stage-C derive tests still pass;
V6 now additionally asserts the emitted pass set ≡ the declaration (`DERIVE_PARITY`)
and the KT-A3 vault-route-presence arm.

## 5. (c) Component render + server-diff transcript (real SSE frames)

`scripts/demo-reactive.sh` (exit 0) against a real `regel serve` process:
```
STEP 5: POST /session/{id}/event  input   → {"applied":true,"eventSeq":1,"ops":1}
STEP 6: POST /session/{id}/event  submit  → {"applied":true,"eventSeq":2,"ops":0}
        (rowVersion-guarded write; res_app_rx_widget row 1 = 'ALPHA-LIVE')
STEP 7: captured SSE stream:
  id: 1   data: AQAAAAAAAAABB1BsQCDiMrwBAwZmb3JtLjEKQUxQSEEtTElWRQ==
          (CodecVersion=1 frame; decodes → setText form.1 "ALPHA-LIVE")
  id: 2   data: AQAAAAAAAAACB1BsQCDiMrwA        (zero-op frame — cursor advances)
STEP 9: cross-session fan-out — the SEPARATE table-viewer session's SSE stream:
  id: 1   data: AQAAAAAAAAAB…dGFibGUuMiMx…QUxQSEEtTElWRQ  (setText table.2#1)
```
The zero-op frame is the ADR-11 §2 empty-diff/cursor invariant live: every
checkpoint that advances `step_seq` emits exactly one frame. Step 9 initially
SKIPPED and exposed that `regel serve` never started the invalidation LISTEN loop
(test-harness-only) — fixed at `ff21ab4`; the step is now a hard assertion.
Masking on the same wire: the resync snapshot carries `"form.0":"••••·76b063"`
(pii email) beside plaintext non-pii slots.

## 6. (d) snapshotHash desync → resync

```
--- PASS: TestSessionDivergenceResync   (corrupt one applied frame client-side ⇒
                                         next frame's digest mismatches ⇒ client
                                         POSTs resync ⇒ exactly one full re-render,
                                         snapshots equal, sse.resyncs_total++)
--- PASS: TestSessionReconnectEmptyDiff (drop SSE across a no-change invalidation ⇒
                                         gapless Last-Event-ID replay, zero-op frame
                                         hash matches, no full repaint; heartbeats
                                         never advance the cursor)
--- PASS: TestSessionStaleCursorResync  (cursor beyond retained ring ⇒ exactly one
                                         full resync, never a silent gap)
```
Digest: order-independent Σ FNV-1a-64(slotId‖value) mod 2⁶⁴, maintained
incrementally on both ends — microbench: one-edit **36.8 ns/op** vs 2000-slot full
recompute **18,820 ns/op** (O(changed slots) proven; incremental ≡ full recompute
after arbitrary edit sequences incl. mid-sequence changes).

## 7. (e) taak workflow (sleep + receive) surviving restart

```
e2e_test.go:597: KILL: SIGKILL taak kernel A pid=36936 at [outbox=1 running_tasks=1 status=ready]
e2e_test.go:627: RESUME (kernel B): id=ad74d1e4-… result=10042 outbox=4 delivered=4 reoffers=1
e2e_test.go:645: TAAK KILL-9 VERIFIED: sleep+receive survived restart, result=10042,
                 4 effects delivered once
--- PASS: TestKill9TaakWorkflowRestart (5.06s)
```
`scripts/demo-taak.sh` (exit 0) shows the same end-to-end with an admitted
`std/taak` TS workflow (`examples/taak-kill.ts`). Supporting red-paths:
`TestTaakMatchPredicate` (disjoint predicates, exactly-once to the right receiver),
`TestTaakOnChangeEventWake` (unrelated mutation does not wake; matching wakes once),
`TestTaakSignalDurableCondition` (condition + restart rows, manual park),
`TestSessionMutationWakesWorkflowAndPatchesSession` (one write → workflow wake AND
session patch), effect-class conformance (`read`-declared native recording any
effect fails closed — `TestEffectClassConformanceRedPath`), outbox dispatcher
effectively-once incl. crash-between-sink-and-mark and concurrent dispatchers.

## 8. (f) Genesis reproducibility + the ADR-10 §2 boot battery

```
genesis_gate_test.go:152: two-fresh-DB parity: 72 entries, IDENTICAL
                          (hash,ast)+pointer+manifest-root on both
--- PASS: TestGateA_TwoFreshDBReproducibility
genesis_gate_test.go:216: mid-genesis kill: 219 statement boundaries swept,
                          all empty-or-complete; retry completed
--- PASS: TestGateB_MidGenesisKillEmptyOrComplete
--- PASS: TestGateC_DispatchBijectionBootRefusal   (orphan hash AND orphan
                                                    implementation both refuse boot)
--- PASS: TestGateD_AttestationRecomputeRefusesOnTamper
          ("boot refused: dispatch attestation mismatch (pinned 0000…, computed 3652eb…)"
           — structured BootRefusal, epoch.boot_refused, pinned vs computed)
genesis_gate_test.go:359: native-stub rejected by tsgo / TS2304:
                          Cannot find name 'regelNative'
--- PASS: TestGateE_NativeBodyUnwritable
```
The projected-tree byte-equality leg is covered by the standing Stage-C gitproj
two-fold gate (byte-identical SHAs), which now folds the 72-entry std/ tree.

## 9. (g) 50k storm + wan-150 vs budgets

```
STORM 50k: N=50000 workers=16 liveSSE=100  drain=33647ms (budget 90000)
           fanout_lag p50=16646ms p95=31547ms (n=50000)
           healthz 1334/1334 ok maxLat=0ms (kernel live throughout)
           exactly-once: max=min step_seq advance=1
           BURST K=20 avgAdvance=1.137 (coalescing 17.6×)
--- PASS: TestSessionStorm50k (74.2s)

WAN-150 (150ms RTT, 1.6Mbps↓/768Kbps↑ injected, 30 iters):
  RED  (no echo): input→echo p95=161.3ms  action→commit p95=161.3ms   ← the §9
  GREEN (echo)  : input→echo p95=  0.0ms  action→commit p95=160.9ms     forcing
  budgets       : input→echo ≤ 50ms       action→commit ≤ 300ms         function
--- PASS: TestWanFeltLatencyGate (44.2s)
```
All 50,000 sessions do real re-render + checkpoint work; 100 hold live SSE streams
(named residue §12.9). The RED run is the ADR-11 §9 gate doing its job — a pure
server round trip cannot beat a 150ms RTT — and the remedy is the minimal
optimistic local echo pinned in ADR-11 §3 (input-class events, originating slot
only, digest-neutral, server-authoritative reconcile, byte-identical client-JS/Go
harness state machines). The ADR-13 §3 500ms fanout p95 SLO binds interactive
fan-out; the one-shot 50k full fan-out is governed by the calibrated 90s drain
budget (pinned with a BUILD-D marker — measured physics of 50k real checkpoint
transactions on one node, p95 tail ≈ 31.5s).

## 10. (h) Perf vs ADR budgets (perf_budget rows, epoch 1, M4)

```
CHECKPOINT BUDGET: 20-field blur storm  writes/interaction=1.00 (budget 1)
                   cfr_delta_p95=53 bytes (budget 65,536)
--- PASS: TestCheckpointWriteBudget
```
Rows written: `sse.storm50k.drain_ms`, `sse.fanout_lag_ms.p50/p95`,
`sse.storm50k.burst_avg_advance`, `session.writes_per_interaction`,
`session.cfr_delta_bytes_p95` (+ storm leg), `sse.wan150.input_echo_ms_p95`,
`sse.wan150.action_commit_ms_p95`, + 2 record-only `.noecho` RED witnesses.
PII telemetry sweep (ADR-13 §6): seeded plaintext absent from stdout event stream,
/healthz, and sse/cfr metric snapshots during a reveal+expire session
(grep self-tested). Stage-B/C standing gates re-ran green in the same suite
(storm10k, storm-MCP-mix, perf M2, N=32 admission gate).

## 11. Native-TCB adversarial harness (M3, ADR-10 §8)

`gate/nativetcb` + `TestNativeTCBHarness` (all subtests PASS): **vault-leak**
(caller routing Vault into an egress sink ⇒ `PII_ESCAPE`; storage leg
ciphertext-only + grant-gated + shred; counterfactual `EvilExfilSink` proves the
contained authority), **contract-violation** (`EvilPostSkipsPostcondition`,
`EvilMailReturnsOutOfType` ⇒ differential-oracle divergence; honest baseline
agrees), **effect-order** (`EvilReadRecordsExternal` fails closed; RED-first via
the armed `TCB_SKIP_EFFECT_CONFORMANCE` mutant). Coverage as monotone
`native_tcb_coverage` rows (PK (epoch, threat_class); dropping a class, a fixture,
or a trusted-for statement refuses — proven in-test). Trusted-for inventory walked
against the full roster: post-reveal egress (mail/http), vetted AEAD, vault KDF,
derivation-pass content, `native.unrecordedIO` — stated as data, never silent.
Shipped-image purity: no fixture hash exists in the genesis image.

## 12. ADR updates forced by build discoveries (ADR-first, `BUILD-D:` markers)

17 markers: **ADR-10** ×6 (§4 eleven-pass concretization + vault/shred + relation
note; §5 pii-leaf refinement; §6 taak reuse/onChange/conformance-gate location;
§8 gate/nativetcb path + coverage table + `RESIDUE_LOG_SINK`), **ADR-11** ×8
(§1 concrete template pass/slot shape; §3 echo landed via the §9 forcing function
+ its state-machine contract; §4/§8 two-digest resolution + token/grant/audit
encoding; §6 horizon-qualified point-read keys + re-drive; §7 form alert slot,
row_version, ClaimAndStep composition; §9 RED/GREEN table), **ADR-05** ×2
(§5 concrete match/event wake shapes + WakeEvents adjacency; §7 dispatcher),
**ADR-13** ×1 (§3 fanout-lag SLO split: interactive vs one-shot storm).

## 13. Named residues (nothing silent)

1. **std/sql parameterized-query surface not landed** — `Conn`/`connect` remain the
   V5 fixture slice; the erf read path serves all derived-resource reads. First
   consumer is the Stage-E CRM dashboard (typed `std/sql` queries per ADR-10 §4).
2. **Tier-2 `board(R)` / `dashboard` / `operatorPlane` components not derived** —
   form/table/detail ship; `states()` columns carry the board-derivability flag;
   operatorPlane is ADR-12 §6 surface tied to the Stage-E operator desk.
3. **Hand-authored component→template lowering not built** — only derived
   form/table/detail are split into templates; V2 six-leaf enforcement covers
   hand-authored component ASTs regardless; `EvalSlotExpr` is unit-tested but no
   admission pass lowers arbitrary component bodies. CEK erf natives for
   hand-authored components remain stubs (session read path is the Go seam).
4. **`RESIDUE_LOG_SINK`** — std/log.write bears no capability so V2's
   capability-keyed sink set omits it; recorded as a trusted-for row (fix changes
   V2 admit/reject behavior → Stage E with a red-path).
5. **H_dispatch Go-body-hash substitution** (pre-existing Stage-A residue) — the
   attestation triple uses the intrinsic symbol, not a true Go body hash.
6. **Minimal natives**: identity.currentUser/currentOrg + test.fake are stubs;
   crypto KDF is SHA-256(token) as stand-in (keys live outside the dialect);
   http/mail record outbox intents — real sinks are the Stage-E
   `cfr.DeliverySink` implementations.
7. **Derivation gaps, guarded**: `pii(address)` ⇒ `DERIVE_PARTIAL` (composite has
   no single mask leaf); `pii(relation)`/`pii(select)` unrepresentable;
   `id`-as-declared-field ⇒ `DERIVE_PARTIAL`; hard cross-resource FK + hasMany
   inverse deferred (relation records the target-horizon predicate note);
   select/states enum-member evolution deferred; `multiselect` sugar not admitted
   (defers per R1-14 until a reference-app tag field exists).
8. **Reactive minimalism, named**: per-field error slots collapse to the form
   alert; the D3 write-path validator enforces type-shape (full R.parse optionality
   at the eval door); `setValue` first-paint emits no value attribute; org-scoping
   is the only policy predicate shape; resync does not re-seed the SSE ring
   (client repaints from the resync payload).
9. **50k storm live-SSE subset = 100** (all 50k do real re-render/checkpoint
   work); the 500ms fanout p95 SLO is split per ADR-13 §3 marker (one-shot 50k
   full fan-out ≈ 31.5s p95 — pinned, not hidden).
10. **cron task kind still undriven** (resume + deliver are); ADR-13 §4 OTLP push
    exporter has no artifact (typed-fields-only stdout emitter stands in).
11. **Lowerer quirk**: multi-export modules sharing a type + identical-shaped
    object literals can yield "definition not found" at runtime; D4 sidestepped
    with single-export modules — needs a lowerer look at Stage E.
12. **VaultPut has no CLI door** — demo-erf-derive.sh hand-rolls the equivalent
    seal via openssl (same AEAD shape); `regel shred` is the real CLI path.

## 14. Discipline deviations, stated

(i) D3 committed its red-path suite GREEN-batched (controls composed from
already-landed D1/D2 machinery) rather than strict per-control RED→GREEN; D0/D1/D2,
D5a/D5b and the two late fixes carry RED witnesses (separate commits or armed
mutants/quoted control-disabled output). (ii) The 50k storm and wan-150 numbers are
single-machine (idle M4, one PG); cross-machine reproduction is a Stage-E carry.
(iii) Stage D consumed 4 session strands (16 total project-wide); all work landed
via strictly-serial synchronous subagents, one side-branch was created by an agent
and fast-forwarded into main with nothing lost.

## 15. What Stage E should watch

- **The two late fixes were found by *using* the system, not by its tests**:
  demo-reactive step 9 caught the never-started reactive loops in `cmdServe`;
  the whole-suite run caught the pgwire cancel-taint poisoning. The Stage-E CRM
  must be built by driving the real CLI/HTTP surface, which is where the remaining
  wiring gaps (if any) will surface.
- Residues 1–3 are Stage-E charter collateral: the CRM dashboard forces std/sql
  typed queries + dashboard/board derivation; the operator desk forces
  operatorPlane; agent patches force hand-authored component templates.
- The Stage-C OPEN M5 gates (real-LLM eval corpus: §3a pass@k, §7 restart
  accuracy, §5 eval-derived fuel; agent `condition.restart` DISABLED) bind at
  Stage E per STAGE-C.md §10.1.
- Genesis roster changes bump `std_manifest_root` + `H_dispatch` — any Stage-E
  battery addition is an epoch change; the two-fresh-DB + kill-sweep gates re-run
  as-is (they build the image from the binary).
- The `wan-150` GREEN depends on the echo state machines staying byte-identical
  (client JS ↔ Go harness); a divergence shows up as spurious resyncs
  (`sse.resyncs_total` SLO < 0.1% of frames is the alarm).
- perf_budget rows for M4 are pinned on an idle M4 host; re-calibrate before any
  CI-host move, per the Stage-C two-mode precedent.
