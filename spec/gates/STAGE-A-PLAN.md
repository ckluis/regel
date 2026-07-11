# Stage A build plan — fixed contracts (BUILD-A)

*This file is the coordination contract for Stage A build agents. ADRs are law; this
file only pins choices the ADRs leave open. If anything here contradicts an ADR, the
ADR wins and this file is a bug. Scope: GATE-1 §4 Stage A (= milestone M0).*

## Environment (verified 2026-07-10)

- Go 1.26.1 darwin/arm64.
- PostgreSQL **16.13** (Homebrew) running on `localhost:5432`, local trust auth for OS
  user `clank` (`psql postgres` works; role `postgres` does NOT exist).
- Node v25.9.0 / npm 11.12.1 (dev-machine oracle only; never in the kernel).
- typescript-go (tsgo, the TS7 native compiler in Go) obtainable:
  `github.com/microsoft/typescript-go@v0.0.0-20260709225601-168e7015edf9` — everything
  under `internal/`, so it is vendored as a local fork with public shim packages (see
  Layout). npm `typescript@7.0.2` exists; `@typescript/native-preview@7.0.0-dev.20260707.2`.

## Module + layout

Module path: `regel.dev/regel` (go.mod at repo root).

```
cmd/regel/                 the one binary: serve | admit | genesis | eval | demo helpers
internal/rast/             owned regel-AST schema, normalize, canonEncode (TLV), SHA-256
                           addressing, canonical printer (ADR-02). Owner of family-1 tests.
internal/lower/            tsgo AST → regel-AST default-deny lowering + grammar gate
                           (ADR-01 §2 bans, switch discipline, floating promises,
                           acyclicity, capture rules R1–R5) with stable diagnostic codes.
internal/tsx/              checker seam (ADR-07 §2): Parse() and Typecheck() over an
                           in-memory 3-layer CompilerHost, locked strict config. The ONLY
                           package that imports the tsgo shim.
third_party/typescript-go/ vendored pinned fork of microsoft/typescript-go with added
                           public shim/ packages (type aliases over internal). Wired via
                           a `replace` directive in the root go.mod.
internal/pgwire/           owned Postgres wire client (ADR-06 §2 minimal scope: startup,
                           trust+SCRAM-SHA-256, extended query, transactions, text
                           results, destroy-on-desync). No third-party driver anywhere.
internal/catalog/          embedded substrate DDL + genesis, resolver (live + as-of),
                           micro-std mirror rows + native dispatch (ADR-03, ADR-10 §2).
internal/admission/        one-SERIALIZABLE-transaction pipeline (ADR-03 §5 / ADR-07 §1)
                           + Stage-A verifier V1 capability-audit + Verdict.
internal/cek/              CEK machine, Value union, De Bruijn envs, K frames incl.
                           TryK/FinallyK, monomorphized fuelMeter/governorMeter (ADR-04).
internal/cfr/              CFR-1 TLV codec, continuation/durable_condition/restart rows,
                           claim CAS + step transaction, park/resume (ADR-05 minimal).
internal/kernel/           HTTP reactor minimal: eval endpoint, as-of, 202-on-park,
                           restart endpoint (ADR-06 §4 subset).
scripts/demo-admit-rollback.sh   the admit → evaluate → serve → admit v2 → rollback/as-of demo.
examples/                  demo .ts sources (greet v1/v2, fuel burner, rejection fixtures).
spec/gates/STAGE-A.md      gate report (BUILD-A writes it).
```

## Pinned choices (ADR-open points, decided for Stage A)

1. **Address prefix:** `r1_` + lowercase Crockford base32 (alphabet
   `0123456789abcdefghjkmnpqrstvwxyz`), full 52-char untruncated digest.
   Domain string: `"regel-ast/1\n"`.
2. **The one Stage-A verifier: V1 capability-audit** (ADR-07 §4). Named capabilities
   (free references resolving to capability-bearing std bindings) ⊆ declared set ⊆
   principal grants. Declared capability set travels in the **patch envelope**
   (`declared_capabilities` per definition), not in source. Red-path fixture first:
   type-correct code calling `std/mail.send` under a principal granted only `crm.read`
   ⇒ `CAP_UNGRANTED{capability, subject}`, zero trace in the catalog.
3. **Micro-std roster (Stage A genesis):** `std/iter` (`Iter<T>`, `keys`), `std/taak`
   (`all`, `race`, `signal`, `sleep` signatures), `std/contract` (`requires`, `ensures`),
   `std/mail` (`send` — capability `mail.send`, the V1 fixture target). All are
   NativeBody rows with real hashes/deps, admitted by the genesis transaction
   (ADR-10 §2 steps 1–3; step-2 image computed at build time by the real printer).
   Stage A dispatch table: `std/mail.send` native = records an intent value (no real I/O).
4. **DB conventions:** runtime db `regel`, test db `regel_test`, DSN via env
   `REGEL_PG_DSN` (default `postgres://clank@localhost:5432/regel`). Tests create/drop
   schemas or databases as needed; every DB test runs against real PG 16.13.
5. **HTTP surface (Stage A):**
   - `POST /admit` — patch envelope JSON → Verdict JSON (same shape the CLI prints).
   - `POST /eval/{name}` — body = JSON array of args; resolves name (exported only,
     `:caller_module=''`), evaluates under governor (trusted) or fuel (sandbox tier per
     envelope), returns value JSON; parks ⇒ `202` + `{continuation_id}`.
   - `?as_of=<RFC3339>` on `/eval/*` — resolves via `name_pointer_history` (I4 path).
   - `GET /continuation/{id}` — status + open condition + restarts.
   - `POST /continuation/{id}/restart` — `{restart:"grant-fuel", args:{fuel:N}}` etc.;
     resumes synchronously for Stage A and returns the completed value.
6. **Verdict (Stage A subset of ADR-07 §6):** `outcome` (full 7-value enum), `admitted`,
   `hashes`, `stages[]`, `diagnostics[]` (code/severity/subject/loc/message/fix?),
   `refusal_id` on every non-green outcome (row in `gate_refusal`). `delta`/`seeders`
   deferred to Stage C (agent plane) — named residue.
7. **Tiers:** patch envelope carries `tier: "trusted"|"sandbox"` per definition;
   trusted ⇒ governorMeter (ceiling 50M transitions / 5s wall for Stage A), sandbox ⇒
   fuelMeter (per-eval budget from the eval request, default 100k steps / 10MB alloc).
8. **CFR-1 minimal:** full C/E/K + Value TLV codec (shares rast primitive encodings);
   park on fuel exhaustion / governor breach; durable_condition + restart rows;
   claim CAS + SERIALIZABLE step transaction; resume delivers restart (name,args) as
   the awaited value of the park point. Wake kinds implemented: `manual` (+ schema for
   all five). Stage-A tests: ADR-05 tests 4b (corrupt CFR fails closed), 5 (double-resume
   CAS), 6 (fuel exhaustion → identical result), 7 (torn write, statement-boundary
   subset), plus same-machine process-restart resume. Full cross-kernel kill-9 suite =
   Stage B (GATE-1 §4), named residue.
9. **Diagnostic codes:** every ADR-01 §2 ban row has a stable `BAN_*` code (one fixture
   per ban); grammar-gate extras: `SWITCH_FALLTHROUGH`, `FLOATING_PROMISE`, `DEP_CYCLE`,
   `CAPTURE_LET` (R1), `CAPTURE_UNSERIALIZABLE` (R2/R3), `PARSE_DEPTH`; lowering
   default-deny: `LOWER_UNSUPPORTED{kind}`; verifier: `CAP_UNGRANTED`.
10. **Genesis epoch row:** epoch table minimal `(n, std_manifest_root, dispatch_attestation,
    created_at)`; H_dispatch computed per ADR-10 §2; boot recompute-and-compare.
    `epoch_current` deferred to Stage B+ (single-kernel Stage A) — named residue.

## Build order (serial agents; 2||3 may run concurrently)

1. tsgo vendor + shim + `internal/tsx` (parse + hermetic typecheck proven by tests).
2. `internal/rast` + `internal/lower` (+ family-1 kill-tests, per-ban fixtures).
3. `internal/pgwire` + `internal/catalog` DDL/migrations (+ I4 CI gates 1–3 as Go tests).
4. `internal/cek` + `internal/cfr` (+ families 2-minimal/3, perf budget benchmarks).
5. `internal/admission` + `internal/kernel` + `cmd/regel` + genesis + demo script
   (+ family-4 zero-trace & concurrency tests).
6. Gate report `spec/gates/STAGE-A.md` (BUILD-A/fable).

## Red-path-first rule (non-negotiable)

Every verifier/gate check lands with its failing-input test written and failing (for
the right reason) before the pass path exists. Commit messages note the red test where
applicable.
