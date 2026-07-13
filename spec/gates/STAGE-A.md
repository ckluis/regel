# STAGE-A gate report (= milestone M0, walking skeleton)

*Author: BUILD-A (fable sub-orchestrator). Date: 2026-07-13. HEAD: `d4dd0b9`.*
*Verdict for the operator: **STAGE A GREEN with named residues** — the skeleton runs
end-to-end (admit → CEK evaluate → HTTP serve → admit v2 → rollback via as-of →
fuel-park → restart-resume) against real PostgreSQL 16.13; all four GATE-1 §4
kill-test families are exercised at Stage-A scope; all perf budgets pass with wide
margin; the I4 overlap kill-test passes on PG 16.13. Continuation family is
Stage-A-minimal by GATE-1's own staging (full crash/resume suite is Stage B's charter).*

## 1. What was built

One Go binary (`cmd/regel`: migrate-db | genesis | serve | admit | eval | grant) over
one Postgres, zero third-party Go dependencies except the vendored tsgo fork:

| Package | Content | ADR |
|---|---|---|
| `third_party/typescript-go` | pinned fork of microsoft/typescript-go @ v0.0.0-20260709225601 (TypeScript **7.1.0-dev**, the real TS7 native checker) + pure-alias `shim/` packages; zero edits to fork internals | ADR-04 §3 |
| `internal/tsx` | hermetic in-memory 3-layer CompilerHost, locked strict config, closed-world resolution (out-of-map import ⇒ TS2307); deterministic (same request ⇒ deep-equal diagnostics) | ADR-07 §2 |
| `internal/rast` | owned regel-AST (uniform Node, stable tag bytes), normalize, TLV canonEncode/Decode, SHA-256 `r1_` Crockford-base32 addressing, canonical printer | ADR-02 |
| `internal/lower` | tsgo→regel-AST default-deny lowering + full grammar gate: every ADR-01 §2 ban with stable `BAN_*` code + fix-in-the-error, switch discipline, floating-promise (syntactic), DEP_CYCLE, CAPTURE_LET (R1 free-variable analysis), PARSE_DEPTH | ADR-01 |
| `internal/pgwire` | owned wire client: startup, trust + SCRAM-SHA-256, extended protocol + statement cache, SERIALIZABLE txns, LISTEN/NOTIFY, destroy-on-desync, bounded pool | ADR-06 §2 |
| `internal/catalog` | substrate DDL (ADR-03 tables 1–6 + ADR-05/06 tables + grant_row + epoch), I2/I7 triggers, I6 revoked-role posture, single resolver (live + as-of, visibility predicate both legs), definition/pointer-CAS helpers | ADR-03 |
| `internal/cek` | owned defunctionalized CEK machine: C=(hash,path,phase), De Bruijn slot envs, closed frame set incl. TryK/FinallyK, monomorphized fuelMeter/governorMeter, park-not-panic, NativeBody dispatch registry | ADR-04 |
| `internal/cfr` | CFR-1 versioned TLV codec (fail-closed decode), Park/PickRestart/ClaimAndResume: durable_condition + restart rows, step_seq claim CAS, SERIALIZABLE step txn | ADR-05 |
| `internal/admission` | one-SERIALIZABLE-txn gate: lower → no-op short-circuit → insert (g4 re-hash) → hermetic tsgo → **V1 capability-audit** → pointer CAS; 7-outcome Verdict; gate_refusal ledger; micro-std genesis (NativeBody rows, std_manifest_root + H_dispatch attestation, boot re-proof) | ADR-07, ADR-10 §2 |
| `internal/kernel` | HTTP reactor minimal: /admit, /eval/{name} (+?as_of, +sandbox tier), 202-on-park, /continuation/{id}, /restart resume | ADR-06 §4 |

## 2. Runnable evidence

### (a) `go test ./...` — full uncached run (real PG 16.13, 2026-07-13)

```
$ go clean -testcache && go test ./...
?    regel.dev/regel/cmd/regel  [no test files]
ok   regel.dev/regel/internal/admission  1.388s
ok   regel.dev/regel/internal/catalog    1.129s
ok   regel.dev/regel/internal/cek        1.112s
ok   regel.dev/regel/internal/cfr        2.683s
ok   regel.dev/regel/internal/kernel     3.818s
ok   regel.dev/regel/internal/lower      1.380s
ok   regel.dev/regel/internal/pgwire     1.964s
ok   regel.dev/regel/internal/rast       0.925s
ok   regel.dev/regel/internal/tsx        1.681s
```

Red-path-first is in the history, not just the tests: `36f68b7` ("lower: RED per-ban
rejection fixtures … before lowering exists") and `dc86a31` ("cfr: RED — park-rows +
resume kill-tests before the store exists") precede their green counterparts.

### (b) admit → evaluate → serve and (c) admit!/rollback/as-of demo

`scripts/demo-admit-rollback.sh` (fresh `regel_demo` database, all eight steps, exit 0).
Captured transcript excerpts (full run 2026-07-13 14:11 UTC):

```
STEP 0b: genesis
genesis: epoch 1 pinned — std_manifest_root=056ef7b2169389ae dispatch_attestation=c65a0d24d0faf2ec

STEP 1: admit examples/greet_v1.ts
{ "outcome": "admitted", "admitted": true,
  "hashes": { "app/demo/greet": "r1_fvddarkc9zj0wzat5d5a8s6t97c0f1e1b4vtctgwrtbbesb62j90" },
  "stages": [ lower pass 0ms · insert pass 2ms · tsgo pass 20ms · V1 pass 0ms · cas pass 1ms ], ... }

STEP 2: eval app/demo/greet ["world"]            ⇒ response: "hello, world"
STEP 4: admit greet_v2.ts (--base v1-hash)       ⇒ outcome admitted, admission_id 3
STEP 5: eval app/demo/greet ["world"]            ⇒ response: "HELLO, world!"   (new behavior)
STEP 6: eval ?as_of=2026-07-13T14:11:44Z         ⇒ response: "hello, world"    (rollback = as-of WHERE clause)
STEP 7: admit examples/burn.ts; eval ?tier=sandbox&fuel=20000
        ⇒ 202 {"class":"fuel.exhausted", "restarts":[{"name":"grant-fuel","capability_required":"operator"},{"name":"abort"}]}
STEP 8: POST /continuation/{id}/restart grant-fuel {fuel:10000000}
        ⇒ response: 100000                        (resumed to completion)
DEMO OK — all eight steps passed (admit → eval → rollback → park → restart)
```

Deploy is the COMMIT of step 4's transaction; rollback is the `name_pointer_history`
as-of query of step 6 — no redeploy, no state migration.

### (d) I4 overlap kill-test on real PG 16.13 (ADR-03 CI gates 1–3)

```
$ go test -v -run 'TestGate1DDLCreatable|TestGate2OverlapKill|TestGate3NoFalsePositive' ./internal/catalog/
=== RUN   TestGate1DDLCreatable
    catalog_test.go:159: CI Gate 1 against real Postgres: PostgreSQL 16.13 (Homebrew)
                         on aarch64-apple-darwin25.2.0, ... 64-bit
--- PASS: TestGate1DDLCreatable (0.16s)
--- PASS: TestGate2OverlapKill (0.06s)      # overlapping window ⇒ SQLSTATE 23P01; asserts
--- PASS: TestGate3NoFalsePositive (0.07s)  # constraint present (no pass-by-absence)
```

### (e) Performance budgets vs ADR-04 §8 (Apple M4, single core)

```
$ go test -bench 'StepsPerSec|MeteringTax' -benchtime 2s ./internal/cek/
BenchmarkCEKStepsPerSec-10        855   2658438 ns/op   72022 transitions/op   27,091,844 transitions/sec
BenchmarkMeteringTaxGovernor-10   952   2515660 ns/op
BenchmarkMeteringTaxFuel-10       960   2498707 ns/op
```

| Budget (ADR-04 §8) | Required | Measured | Status |
|---|---|---|---|
| CEK transitions/sec/core (trusted) | ≥ 1,000,000 | **27,091,844** (27×) | PASS |
| Metering tax (fuel vs governor) | ≤ 10 % | **≈ 0 %** (fuel within noise of governor) | PASS |
| Transitions/request p95 (greet, 20 reqs) | ≤ 50,000 | **10** | PASS (`TestTransitionsPerRequestBudget`) |
| Checkpoint writes/interaction | ≤ 1 | park = 1 step txn by construction | PASS (structural; measured end-to-end at M4 per ADR-04 §8) |

Budgets are data: written to `perf_budget` rows (epoch 1, milestone M0) by the tests.

## 3. Kill-test families (GATE-1 §4 / ARCHITECTURE §4)

**Family 1 — printer round-trip + idempotence: GREEN.**
`TestMutationSameHash` (13 perturbations: whitespace/comments/docstring/local+typeparam
rename/quote style/number spelling ×3/type+union member order/import order ⇒ same hash),
`TestMutationDiffHash` (+`TestMutationDepHashDiff`) ⇒ different hash;
`TestPropertyFixedPoint` (guarantees 1–3, ≥260 random subset-valid ASTs);
`TestGuarantee1Deterministic`; `TestTokenFuzzNoPanic` (≥360 mutations, never a panic);
adversarial corpus: `TestNFCvsNFDDistinct`, `TestNegativeZeroDistinct`,
`TestBigIntEdges`, `TestDeepNestingRoundTrip`, `TestAlphaEquivalence`;
`TestEncodeDecodeByteRoundTrip`. Residue: the *nightly* world-rehash canary is not
scheduled (no world to re-hash yet); guarantee-4's equation runs at every insert (g4
hook) and in tests.

**Family 2 — continuation crash/resume: GREEN at Stage-A scope (minimal per plan pin #8).**
ADR-05 test 4b `TestCorruptCFRFailsClosed`/`TestCorruptCFRDB` (truncated + bit-flipped
blob fail closed); test 5 `TestDoubleResumeCAS` (sequential + concurrent — exactly one
claim); test 7 subset `TestTornWriteRollsBack` (aborted park txn ⇒ zero rows);
`TestProcessRestartResume` (fresh pool/kernel resumes from rows to identical result);
`TestPauseAnywhere` (park at *every* transition index ⇒ CFR encode/decode
byte-identical ⇒ resume result equals uninterrupted run). ADR-05 tests 1 (kill-9
cross-kernel), 2 (year-old clock), 3 (as-of resume), 8–10 are **Stage B's charter**
(GATE-1 §4 Stage B carries R1/R2) — named residue.

**Family 3 — fuel exhaustion → durable condition: GREEN.**
`TestParkRowsFuel` (RED-first: continuation `status='condition'`, `durable_condition
class='fuel.exhausted'`, restarts grant-fuel/abort as rows), resume-to-identical-result
with effect-fired-exactly-once (counting native), `TestGovernorRunaway` (type-correct
`while(true)` in trusted tier parks `runaway`, kernel stays live). Demo steps 7–8
exercise it over HTTP (202 + restart buttons as JSON).

**Family 4 — admission rejection: GREEN.**
`TestV1CapUngrantedZeroTrace` (RED-first: type-correct code calling `std/mail.send`
under grants `{crm.read}` ⇒ `CAP_UNGRANTED`; asserts **no definition row, no pointer,
no admission row**; `gate_refusal` row persists with retrievable verdict);
`TestBanClassRejectedZeroTrace`, `TestTypeErrorRejectedZeroTrace` (tsgo diagnostic,
zero trace); `TestConcurrentSameNameSingleWinner` (two racing admissions ⇒ one winner,
loser stale-base, one open history window); `TestIdempotentResubmission` ⇒
already-admitted, no duplicates. Grammar level: **35 per-ban fixtures** (one per
ADR-01 §2 row, incl. proxy/reflect/eval split, angle-assertions, regex
backreference/lookahead/lookbehind) each asserting its stable code + fix message, plus
admitted-twin fixtures proving the sibling legal form passes.

## 4. ADR updates forced by build discoveries (ADR-first, `BUILD-A:` markers)

1. **ADR-05 §2** — timer partial index `((wake->>'due')::timestamptz)` is uncreatable
   (text→timestamptz cast is STABLE, not IMMUTABLE; SQLSTATE 42P17 on PG 16.13). Index
   is now over raw text; `due` must serialize as fixed-width UTC ISO-8601.
2. **ADR-03 §5 step 7** — the single `INSERT … ON CONFLICT DO UPDATE` CAS fires both
   I7 trigger legs on the conflict path (two history windows per move ⇒ I4 trip);
   realized as an explicit two-arm insert/update with identical CAS semantics.
3. **ADR-03 §1/§3** — `name_pointer_history` gains a `visibility` column (snapshotted
   per window by I7) because §3's "identical query" as-of resolution was unstatable
   without it; as-of now enforces private-visibility identically to live (red-path
   test added).

## 5. Named residues (nothing silent; each is Stage-B/C work or a deliberate cut)

1. **tsgo**: real TS7 (7.1.0-dev) vendored as a Go library behind the ADR-07 seam — NOT
   a tsc stopgap. Residues: `MAX_PARSE_DEPTH` enforced post-parse over the tree (the
   at-descent guard needs fork-internal edits); `Diagnostic.Col` is UTF-16 units.
2. **Verifier suite**: V1 capability-audit only (per GATE-1 Stage A "one real
   verifier"); V2–V6 are Stage C. V1's named-capability set derives from resolved dep
   edges rather than a full free-variable walk. Declared capabilities travel in the
   patch envelope.
3. **Continuations**: ADR-05 tests 1/2/3/8/9/10, deferred wakes (timer/message/event/
   join), all/race joins, task-table drain/reaper, durable result storage — Stage B.
   `epoch_current` + running-kernel fence — Stage B+ (single-kernel Stage A).
4. **Admission**: derivation passes (5a), migration_sql DDL (6), overlay re-verify (8),
   Verdict `delta`/`seeders`, tsgo concurrency budget (`ADMISSION_BUSY`) — M1/Stage C.
   L1 module host serves one file per definition-name; L2 typechecks submitted source
   (canonical text is round-trip-verified separately by g4 + family 1).
5. **Lowering**: `infer` in conditional types, call/construct signatures in object
   types, variadic/optional tuple elements, qualified type names ⇒ `LOWER_UNSUPPORTED`
   (fail-closed, not silent); named function-expression self-name normalized to
   anonymous arrow; floating-promise check is syntactic (full type-driven check needs
   checker integration).
6. **CEK/std**: regex literals are opaque values (RE2 execution is std-battery work);
   micro-std NativeBody signature stubs are `unknown`-typed in rows (real signatures in
   the L0 type surface); `H_dispatch` uses the intrinsic symbol in place of a Go body
   hash; UTF-16 string-collation corners; tier is an eval-request property, not a
   durable per-definition column.
7. **Ops**: nightly world-rehash canary not scheduled (no scheduler yet); ADR-03 CI
   gate 4 (recovery drill) is Stage B+; SCRAM is vector-tested client-side (local
   pg_hba is trust — server-side SCRAM not exercised on this box).

## 6. What Stage B should watch

- The CFR/claim/lease core is real but its hard suite (kill-9 cross-kernel, year-old
  clock, as-of resume, wake storm, joins) is exactly Stage B's charter — build on
  `internal/cfr`'s Park/ClaimAndResume seams; do not re-plumb.
- The two-arm pointer CAS (ADR-03 BUILD-A marker) is the settled shape; keep it when
  adding overlay re-verification.
- `wake->>'due'` text ordering contract (ADR-05 BUILD-A marker) binds the Stage-B timer
  scanner: writes MUST be fixed-width UTC ISO-8601.
- V5 capture verifier must share the CFR codec's type table (encodable ≡ admitted) —
  the codec lives in `internal/cfr`, the R2 seam is documented in `internal/lower`.
- Perf headroom is large (27× steps/sec floor) but the bench is a microbench; keep the
  budget rows updated as programs grow.
