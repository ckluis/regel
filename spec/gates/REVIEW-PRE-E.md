# REVIEW-PRE-E — fresh-eyes re-verification of Stages A–D + Stage-E work list

*Author: PHASE R (fresh-eyes sub-orchestrator). Date: 2026-07-16. HEAD at start:
`1ce3293`. Real PostgreSQL 16.13 (Homebrew, aarch64-apple-darwin). Evidence captures
under `spec/gates/evidence-pre-e/`.*

**Verdict: BASELINE GREEN-WITH-FIXES.** Every Stage A–D green claim re-verified at HEAD
today: uncached `go test ./...` passes (exit 0), all six demo scripts exit 0, git
projection is two-fold byte-identical, genesis is two-fresh-DB byte-identical. Evidence
is CLEAN modulo minor perf percentile drift (within reason) plus **one flaky
liveness-attribution assertion found by re-execution and fixed** (the flagship kill-9
e2e's `reoffers>0` check, correctness invariants never affected). Driving the real
CLI/HTTP/MCP surface by hand surfaced **zero functional bugs** (a strong contrast to
Stage D, where use found two) and three benign UX papercuts recorded for Stage-E docs.

---

## §1 Baseline status — captured evidence at HEAD

### T1a — `go test ./... -count=1` (uncached, real PG 16.13)

```
$ go clean -testcache && go test ./... -count=1
ok  regel.dev/regel/internal/admission  16.700s   ok  internal/lower    1.836s
ok  regel.dev/regel/internal/catalog     1.787s   ok  internal/mcp      9.567s
ok  regel.dev/regel/internal/cek         2.862s   ok  internal/oracle   2.989s
ok  regel.dev/regel/internal/cfr         5.471s   ok  internal/pgwire   2.652s
ok  regel.dev/regel/internal/gitproj     6.259s   ok  internal/rast     1.354s
ok  regel.dev/regel/internal/kernel    416.413s   ok  internal/tsx      1.698s
                                                  ok  internal/ui       0.915s
(cmd/regel, gate/nativetcb, gate/redpath, internal/mutants: no test files)
=== EXIT CODE: 0  ELAPSED: 418s ===
```
All 12 test packages green. (Full capture: `evidence-pre-e/gotest-full.txt`.)

### T1b — all six demo scripts (fresh DB each, exit codes captured)

```
demo-admit-rollback   EXIT 0   "DEMO OK — all eight steps passed (admit → eval → rollback → park → restart)"
demo-kill9-resume     EXIT 0   "DEMO OK — kill -9 mid-step, cross-kernel resume, effect exactly once"
demo-mcp-session      EXIT 0   "DEMO OK — real MCP session: authoring loop + verdict.get + REFUSED escalation + fuel exhaustion"
demo-erf-derive       EXIT 0   "DEMO OK — erf Stage-D derivation: 11 passes, history live, pii vaulted + never leaked, crypto-shred verified"
demo-reactive         EXIT 0   "DEMO OK — reactive layer live: mount, SSE, event-driven mutation, live patch frame, resync"
demo-taak             EXIT 0   "DEMO OK — std/taak kill -9 mid-step: sleep+receive resume, effect once, delivered once"
```
(Per-demo captures: `evidence-pre-e/demo-*.txt`.)

### T1c — git-projection two-fold determinism (reused `internal/gitproj` release gate)

```
$ go test -count=1 -v -run 'TestDeterminismReleaseGate|TestGitOracleVerifiesRepo' ./internal/gitproj/
project_test.go:69: two-fold determinism: 4 commits, IDENTICAL head SHA on both machines
project_test.go:70:   machine A head SHA = c947ff2719c22d95a37a92b388f8ffde2849ebad
project_test.go:71:   machine B head SHA = c947ff2719c22d95a37a92b388f8ffde2849ebad
--- PASS: TestDeterminismReleaseGate (0.46s)
--- PASS: TestGitOracleVerifiesRepo (0.46s)   exit 0
```
The load-bearing property (A ≡ B, byte-identical) holds. The absolute head SHA differs
from STAGE-C.md's `90aa27e…` because the fold is a pure function of the freshly-seeded
ledger fixture (which grew across stages), not a pinned constant — this is expected, not
drift in the determinism guarantee.

### T1d — genesis two-fresh-DB reproducibility + boot battery (reused `internal/admission`)

```
$ go test -count=1 -v -run 'TestGateA_TwoFreshDBReproducibility|TestGateB_…|TestGateC_…|TestGateD_…|TestGateE_…' ./internal/admission/
genesis_gate_test.go:152: two-fresh-DB parity: 72 entries, IDENTICAL (hash,ast)+pointer+manifest-root on both
--- PASS: TestGateA_TwoFreshDBReproducibility (0.35s)
genesis_gate_test.go:216: mid-genesis kill: 219 statement boundaries swept, all empty-or-complete; retry completed
--- PASS: TestGateB_MidGenesisKillEmptyOrComplete (1.90s)
--- PASS: TestGateC_DispatchBijectionBootRefusal (0.00s)
--- PASS: TestGateD_AttestationRecomputeRefusesOnTamper (0.19s)
--- PASS: TestGateE_NativeBodyUnwritable (0.28s)   exit 0
```

---

## §2 Evidence spot-check — re-executed today (≥3 load-bearing claims per gate)

Claim → command → result → MATCHES / DRIFTED / MISREPRESENTED. Perf percentiles may
drift with machine load; only material drift is flagged.

| # | Gate claim (as written) | Re-executed result today | Verdict |
|---|---|---|---|
| A1 | CEK ≥1M steps/sec/core; measured 27.1M (27×) | `bench StepsPerSec` → **23.5M** transitions/sec (23×) | MATCHES (perf drift, still 23× floor) |
| A2 | transitions/request p95 ≤50k; measured 10 | `TestTransitionsPerRequestBudget` → "p95 = 10 over 20 requests (ceiling 50000)" | MATCHES (exact) |
| A3 | I4 overlap kill-test on PG16.13 (SQLSTATE 23P01) | `TestGate1/2/3` → all PASS, PG 16.13 asserted | MATCHES |
| A4 | printer round-trip: 13 perturbations ⇒ same hash | `TestMutationSameHash` (all subtests) → PASS | MATCHES |
| B1 | kill-9 cross-kernel exactly-once (result/outbox/trace identical) | `TestKill9CrossKernelExactlyOnce` → result 10000, outbox 4, trace identical | MATCHES (correctness); see §3 flaky-assertion fix |
| B2 | wake storm 10k, 0 dupes, abort ≤5% (was 0.89%) | `TestWakeStorm10k` → 10000 done, outbox 10000, **0 dupes**, abort_rate **0.76%** | MATCHES (abort drift, in budget) |
| B3 | exact-budget fuel, T=1022 park at exactly T-1 | `TestExactBudgetFuel` → T=1022, park at 1021, resumes to 1225 | MATCHES (exact) |
| B4 | hermeticity 6/6 byte-identical across processes | `TestHermeticityCrossKernel` → frames/result identical 6/6 | MATCHES (identical SHAs) |
| C1 | verdict.get caller-scoped (Schneier P2-3) | `TestVerdictGetCallerScoped` → PASS | MATCHES |
| C2 | MCP timing floor: ks 0.113 ON / 1.000 OFF+leak | `TestTimingIndistinguishable` → **ks 0.160 ON / 1.000 OFF+leak** | MATCHES (ks is a sample stat; separation holds) |
| C3 | dual mutation 13/13 killed + monotone gate | `TestAdversarialHarness` (4 subtests) → PASS | MATCHES |
| D1 | genesis two-fresh-DB 72 entries identical | (=T1d) PASS | MATCHES |
| D2 | erf roster totality + crypto-shred + history-excludes-PII | `TestRosterTotalityEndToEnd`/`TestVaultSealAndCryptoShred`/`TestHistoryExcludesPii` → PASS | MATCHES |
| D3 | incremental digest 36.8ns/edit vs full recompute | `bench Digest` → **37.4ns/edit** vs 20702ns full | MATCHES |
| D4 | checkpoint budget 1.00 writes/interaction, cfr_delta p95 53B | `TestCheckpointWriteBudget` → **1.00 writes, 53B p95** | MATCHES (exact) |
| D5 | 50k storm drain 33.6s/90s, exactly-once | `TestSessionStorm50k` → **42.8s/90s**, exactly-once (maxLat 5ms, kernel live) | MATCHES (drain drift, 47s under budget) |
| D6 | wan-150 RED 161ms → GREEN 0.0ms echo | `TestWanFeltLatencyGate` → RED 158.9ms → GREEN **0.0ms** echo / 161.3ms commit | MATCHES |

**No misrepresentations.** Numbers that moved (CEK 27→23.5M steps/s, storm10k abort
0.89→0.76%, timing ks 0.113→0.160, 50k drain 33.6→42.8s) are all machine-load / sample
variance within the stated budgets; every load-bearing property holds.

---

## §3 Found by driving the system (T3) + fixes applied

**Fix applied (FIX-NOW): de-flaked the flagship kill-9 e2e.** Re-executing
`TestKill9CrossKernelExactlyOnce` in isolation, it failed **1 of 6 runs** on
`t.Fatalf("kernel B healthz.reoffers = %d, want >0")` — while every correctness
invariant held (result 10000, outbox exactly 4, trace identical, zero dupes). Whether
the stranded task is resumed via the reaper's re-offer path (`reoffers>0`) or claimed
after a direct state transition depends on exactly where SIGKILL lands relative to the
task-claim commit — a legal race that does not affect exactly-once. The deterministic
reaper-re-offer property is *already* gated by `TestReaperReoffersStranded` (reliable
3/3). Fix: keep the exactly-once invariants hard, demote the racy `reoffers` read to an
informational log (commit `65580bf`). Re-ran the fixed test 3/3 green.

**Driving the system — zero functional bugs.** With a hand-built binary and a fresh DB
(`evidence-pre-e/t3-use-the-system.txt`):
- Authored a **new** definition not in `examples/` (`app/fin/tax`), admitted v1, evalled
  (24690), admitted v2 with `--base` (24590 live), and confirmed **rollback = as-of**
  end-to-end (`eval --as-of <boundary>` → v1's 24690).
- Rejection paths by hand: `class` → `BAN_CLASS` (+fix); a 3-arg `mail.send` →
  `TS2554` (tsgo fires **before** V1, correct ordering); a 2-arg ungranted `mail.send` →
  `CAP_UNGRANTED`. **Zero-trace confirmed**: 0 `app/bad/*` rows, refusals audited in
  `gate_refusal`.
- **Cross-door consistency**: an MCP `catalog.search {tax}` returned
  `app/fin/tax@product` — the CLI-admitted def is visible through the agent door.
- **Unexpected-but-legal MCP sequence**: `verdict.get {99999}` before any work →
  `NOT_FOUND` (caller-scoped, no oracle); `patch.submit {commit:true}` with **no prior
  dry-run** → admitted (no forced dry-run precondition).
- Live HTTP surface outside the demos: `/healthz` structured; HTTP `?as_of=` rollback
  works; unknown name and malformed body → clean JSON errors, no crash.

**Three benign UX papercuts (NOT bugs — behavior is correct; recorded for Stage-E
`docs/claim-evidence.md`):** (a) CLI `--as-of` requires strict RFC3339 offset (`Z` /
`±HH:MM`) and rejects Postgres's `-04` text form (HTTP `?as_of=` accepts it); (b) as-of
with whole-second timestamps near an admission boundary can miss by sub-second because
`valid_from` is microsecond-precise (inherent to point-in-time, correct); (c) `--declare`
expects the verifier's stripped capability name (`mail.send`), not the import path
(`std/mail.send`), and the mismatch message doesn't reveal the expected token form.

---

## §4 Consolidated residue table

Sweep of **all four stage reports' §residues + the luminary REPORT-R1 P2 backlog =
42 numbered residues.** Disposition ∈ {STAGE-E ITEM, DEFER-V2, DISCHARGED (already closed
by a later stage — re-verified this phase where noted), FIX-NOW}. For STAGE-E items, the
attached Stage-E deliverable (CRM = proof CRM; a–e = the five scenarios; M5 = real-LLM
gates; CE = claim-evidence doc; O = O1–O5 fences / revert drill).

| ID | Origin | Residue | Disposition | Stage-E attach |
|---|---|---|---|---|
| A1 | STAGE-A §5.1 | tsgo `MAX_PARSE_DEPTH` post-parse; `Diagnostic.Col` UTF-16 | DEFER-V2 (fork-internal edits forbidden; parse depth still enforced; col is display-only) | — |
| A2 | STAGE-A §5.2 | V1-only verifier roster | DISCHARGED (Stage C: V1–V6 live, re-verified) | — |
| A3 | STAGE-A §5.3 | continuation tests 1/2/3/8/9/10, wakes, joins, reaper | DISCHARGED (Stage B: 10/10 kill suite) | — |
| A4 | STAGE-A §5.4 | derivation 5a, migration DDL, overlay re-verify 8 | DISCHARGED (C/D); overlay re-verify → STAGE-E | a (tenant field-add) |
| A5 | STAGE-A §5.5 | `LOWER_UNSUPPORTED`: infer, call/construct sigs, variadic tuples, qualified names | DEFER-V2 (fail-closed, dialect-surface expansion) | — |
| A6 | STAGE-A §5.6 | regex opaque values (RE2), native-stub `unknown` sigs, H_dispatch intrinsic-symbol, UTF-16 collation | DEFER-V2 (std built at D; regex/H_dispatch are v2 hardening) | — |
| A7 | STAGE-A §5.7 | nightly world-rehash canary unscheduled; CI Gate-4 recovery drill; server-side SCRAM | STAGE-E (canary + bad-epoch revert drill); SCRAM → DEFER-V2 | O |
| B1 | STAGE-B §10.1 | V5 capture-verifier (test 4a) | DISCHARGED (Stage C, `TestV5CaptureUnserializable…`) | — |
| B2 | STAGE-B §10.2 | test-11 decode-coverage monotone floor (binds at first new frame/CFR version) | STAGE-E (golden-continuation corpus per epoch) | CE |
| B3 | STAGE-B §10.3 | test-7 full per-statement fault-injection sweep | DEFER-V2 (kill-9 + aborted-txn cover it; full injection is hardening) | — |
| B4 | STAGE-B §10.4 | outbox dispatcher effectively-once | DISCHARGED (Stage D) | — |
| B5 | STAGE-B §10.5 | message `match:` predicates + `event` wakes | DISCHARGED (Stage D taak) | — |
| B6 | STAGE-B §10.6 | reaper sliding-window breaker | DISCHARGED (Stage D) | — |
| B7 | STAGE-B §10.7 | epoch fence `--wait-for-epoch`, `migrate N`, O5 fleet drill | STAGE-E (deploy=commit under fleet) | c, O |
| B8 | STAGE-B §10.8 | cron/deliver task kinds (only resume/deliver driven) | STAGE-E (cron for scheduled workflows) | CRM |
| B9 | STAGE-B §10.9 | stated build deviations (thunk park, join-parent jsonb, best-of-3 microbench, in-proc storm) | DEFER-V2 (documented + accepted) | — |
| C1 | STAGE-C §10.1 | **OPEN M5 gates**: §3a pass@k, §7 restart accuracy, §5 eval-derived fuel; agent `condition.restart` DISABLED | STAGE-E (M5 real-LLM corpus) | M5 |
| C2 | STAGE-C §10.2 | I4 GiST predicate-lock coarseness caps admission semaphore at S=2 | DEFER-V2 (BUSY-shed protects correctness; finer lock = throughput opt) | — |
| C3 | STAGE-C §10.3 | `TYPECHECK_TIMEOUT` abandons checker goroutine | DEFER-V2 (rare secondary path; deterministic ceiling fires first) | — |
| C4 | STAGE-C §10.4 | V2/V5 dataflow over for-of/while/switch/try binder scopes | STAGE-E (CRM exercises full control flow) | CRM |
| C5 | STAGE-C §10.5 | `catalog.get {asOf}` not history-resolved; `resource.mutate` minimal policy; `resource.query` minimal shape | STAGE-E (CRM dashboard + operator desk) | b, CRM |
| C6 | STAGE-C §10.6 | hosted-forge wiring (webhook/push-creds/branch-protection) | DEFER-V2 (rides operator infra per ADR-09) | — |
| C7 | STAGE-C §10.7 | STAGE-B residues discharged at C | DISCHARGED | — |
| D1 | STAGE-D §13.1 | std/sql parameterized-query surface not landed | STAGE-E (CRM dashboard typed queries) | CRM |
| D2 | STAGE-D §13.2 | tier-2 `board`/`dashboard`/`operatorPlane` not derived | STAGE-E (CRM dashboard + operator desk) | CRM |
| D3 | STAGE-D §13.3 | hand-authored component→template lowering not built | STAGE-E (agent patches force it) | b, CRM |
| D4 | STAGE-D §13.4 | `RESIDUE_LOG_SINK` (std/log.write bears no capability) | STAGE-E (V2 admit/reject change needs a red-path) | CRM |
| D5 | STAGE-D §13.5 | H_dispatch Go-body-hash substitution (intrinsic symbol) | DEFER-V2 (attests build-consistency; boot-refusal catches image drift) | — |
| D6 | STAGE-D §13.6 | minimal natives: currentUser/currentOrg/test.fake stubs, KDF SHA-256 stand-in, http/mail record intents | STAGE-E (CRM needs real identity + `cfr.DeliverySink`) | CRM, e |
| D7 | STAGE-D §13.7 | derivation gaps: `pii(address)`→DERIVE_PARTIAL, `pii(relation/select)` unrepresentable, FK/hasMany deferred, multiselect | DEFER-V2 (guarded/fail-closed; multiselect unblocks when CRM has a tag field) | — |
| D8 | STAGE-D §13.8 | reactive minimalism: collapsed error slots, setValue first-paint, org-only policy | DEFER-V2 (named cuts) | — |
| D9 | STAGE-D §13.9 | 50k storm live-SSE subset = 100; single-machine numbers | DEFER-V2 (calibrated; cross-machine repro is a Stage-E carry) | — |
| D10 | STAGE-D §13.10 | cron task kind undriven; OTLP push exporter absent (stdout stands in) | DEFER-V2 (cron→CRM if needed; OTLP = L4) | — |
| D11 | STAGE-D §13.11 | lowerer quirk: multi-export modules sharing a type + identical-shaped object literals ⇒ runtime "definition not found" | STAGE-E (explicit "needs a lowerer look"; correctness sharp-edge) | CRM |
| D12 | STAGE-D §13.12 | `VaultPut` has no CLI door (demo hand-rolls via openssl) | STAGE-E (crypto-shred scenario seal path) | e |
| L1 | R1 P2.1 (Allspaw) | quarantine/hold-dependents has no DDL-backed state; Gate-4 never asserts a bound dependent held fail-closed | STAGE-E (revert drill asserts hold) | O |
| L2 | R1 P2.2 (Karpathy) | pass@k floor gameable via operator retry ceiling `k`; pin `k` per epoch | STAGE-E (M5 gate) | M5 |
| L3 | R1 P2.3 (Schneier) | `verdict.get` not caller-scoped | DISCHARGED (Stage C, `TestVerdictGetCallerScoped` re-verified) | — |
| L4 | R1 P2.4 (Majors) | owned OTLP exporter has no collector-round-trip conformance gate | DEFER-V2 (typed-stdout emitter stands in; no OTLP push yet) | — |
| L5 | R1 P2.5 (Majors) | ring-buffer drop-oldest can evict rare trip events; no event priority | DEFER-V2 (observability hardening) | — |
| L6 | R1 P2.6 (Kleppmann) | no SERIALIZABLE retry policy / abort-rate budget | DISCHARGED (Stage B, storm abort 0.76% re-verified) | — |
| L7 | R1 P2.7 (Kleppmann) | READ COMMITTED serve can dispatch-miss on std-pointers before the guard fires — serve txns should be REPEATABLE READ | STAGE-E (low-cost isolation hardening + test) | CE |

**Tally (42): STAGE-E 17 · DEFER-V2 15 · DISCHARGED 10.** FIX-NOW executed this phase =
1 (the flaky kill-9 assertion, §3 — a re-execution finding, not one of the 42 named
residues). Plus 3 T3 UX papercuts → `docs/claim-evidence.md` notes (not code changes).

---

## §5 Prioritized Stage-E work list (for the Stage-E builder; refs GATE-1 §4 Stage E)

Stage-E kill-tests (GATE-1 §4): reference app green end-to-end; stranded-continuation
impossibility across two epoch boundaries; reference-dashboard stranger-review gate
recorded (missing verdict reads red). **Build by DRIVING the real CLI/HTTP/MCP surface**
— Stage D's two worst bugs were found by use, and this phase's hand-driving already
exercises that muscle.

1. **Build the proof CRM entirely as admitted rows** — the forcing function for the
   deepest residues: std/sql typed queries (D1), dashboard/board derivation (D2),
   hand-authored component templates (D3), real identity + delivery natives (D6, `cfr.
   DeliverySink`), a `VaultPut` CLI door (D12), cron-driven workflows (B8). Resolve the
   **lowerer multi-export quirk (D11) early** — it is a latent correctness sharp-edge the
   CRM's multi-module code will hit.
2. **The five scenarios a–e** on the CRM: (a) tenant field-add [overlay re-verify A4,
   `resource.mutate` policy C5]; (b) agent patch over MCP [D3, C5]; (c) mid-flight
   workflow surviving deploy [epoch fence + `--wait-for-epoch` + `migrate N`, B7];
   (d) as-of rollback [already working — this phase drove it end-to-end]; (e) PII
   crypto-shred with attestation [D12; demo already green].
3. **Flip the OPEN M5 real-LLM gates** (C1, L2) — §3a authoring pass@k (**pin `k` per
   epoch**), §7 restart-decision accuracy, §5 eval-derived fuel capacity; enable the
   agent-facing `condition.restart` ONLY when its metric reads green.
4. **Durability fences + drills**: `migrate N` machinery, golden-continuation corpus per
   epoch [B2 decode-coverage floor], O1–O5 fences, bad-epoch revert drill that **asserts
   bound dependents are held fail-closed** [A7, L1], nightly world-rehash canary [A7].
5. **Small hardening with red-paths**: `RESIDUE_LOG_SINK` V2 behavior change (D4);
   REPEATABLE-READ serve transactions (L7); V2/V5 dataflow over full control flow (C4).
6. **`docs/claim-evidence.md`** — map every load-bearing claim → test / demo / residue;
   record the 15 DEFER-V2 items with their why-safe, and the three T3 UX papercuts.
7. **`spec/FINAL.md`** — v1 close-out.

DEFER-V2 (15) and DISCHARGED (10) are out of Stage-E scope except where a scenario
above happens to touch them; each DEFER-V2 carries a why-safe in §4.
