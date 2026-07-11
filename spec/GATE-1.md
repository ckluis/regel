# regel — GATE-1 (design → build)

*Author: GATE-1. Date: 2026-07-10. Decision owner: operator. No build before operator GO.*

## 1. Design state

Phase 0–3 complete. What exists in `spec/`: **13 ADRs** (01 dialect · 02 printer · 03 catalog · 04 interpreter · 05 continuations · 06 kernel · 07 verifiers · 08 epochs · 09 git-projection · 10 std-world · 11 reactive · 12 agent-plane · 13 observability), **ARCHITECTURE.md** (diagram, 7 flow narratives, seam table, walking skeleton, machine-gated milestones M0–M6, v1 exclusions), **RISKS.md** (11 risks, deepest-bet-first), **SUMMARY.md**, **GLOSSARY.md**, and the R1 revision ledger **REVISIONS-R1.md**. Headline decisions:

- **Identity is owned bytes, not text**: SHA-256 over TLV of the normalized AST; types+contracts in the hash, comments/local names out; `r<n>_` schema-versioned addresses, immortal by append-only decoders + nightly world-rehash canary (ADR-02).
- **One admission gate, one SERIALIZABLE transaction**: code + DDL + name-pointers commit together into 5 catalog tables (INSERT-only `definition` keyed by hash) (ADR-03); the same gate serves CLI, Settings, agent, and git-merge — one bypass class works from every door.
- **State-capture continuations, no replay fallback**: C/E/K frames in versioned binary CFR; capture verifier makes encodable ≡ admitted; exactly-once via `step_seq` CAS + lease (ADR-05).
- **Owned pure-Go CEK interpreter, no fast lane in v1**: fuel parks as a durable condition (never panics); conformance by curated test262 + differential fuzz + a regel-native oracle; AOT-to-Go seam reserved, not built (ADR-04).
- **Verifier suite is the security boundary, not the type system**: six small pure verifiers over a hermetic tsgo host, coverage as monotone queryable rows, dual mutation testing (ADR-07).
- **Deploy = commit, rollback = as-of**: atomic epoch = (kernel binary, std-manifest-root) with boot-refusal on mismatch; git repo is a deterministic read-only fold over the ledger (ADR-08, ADR-09).

## 2. Luminary verdict

Adversarial review (12-expert locked roster, 12 lenses) returned **REVISE** — 2 P0, 17 P1, 29 P2, 6 P3. All **14 mandated revisions applied** + a fable integration pass fixing 5 cross-ADR contradictions; ADR-13 (observability) created to resolve a dangling health surface. Targeted re-review (REPORT-R1.md) returned **GO**: 14/14 SATISFIED, both P0 red flags CLEARED by their declarers (Celko I4 exclusion; Bach conformance oracle), both withdrawn-conditional flags STAY WITHDRAWN (Allspaw recovery, Karpathy injection — revert triggers did not fire; both boundaries HOLD), all 4 documented deviations ACCEPTED. Re-review introduced **0 P0 / 0 P1 / 7 P2 / 16 P3**, all hardening of the machinery the revisions added — tracked backlog with expert owners, none blocking. **8 out-of-scope carries** ride forward at prior priority. No iteration 2 required.

## 3. Residual risks (top 3 + notable new P2s)

- **R1 printer hash identity** (blast = total): a hash-stable *semantic* normalization bug is invisible to the canary and round-trip suite — only finite corpus coverage stands against it. Gate: mutation matrix + world-rehash canary (two-leg, incl. parse→lower replay) at M0.
- **R2 continuation serialization**: "stable for years" is simulation-proven only under simulated clocks; no replay log to fall back on. Gate: ADR-05 tests 1–10 + golden-continuation corpus per epoch + `continuation_coverage` monotone floor at M2.
- **R3 admission correctness**: verifier coverage is enumerative, not proof; trusted code runs on a shared heap. Gate: one red-path fixture per verifier + hostile corpus + dual-mutation monotonicity at M1.
- **New P2 (Schneier, ADR-07/12)**: `verdict.get` refusal retrieval is not caller-scoped — a guessed `refusal_id` is a cross-principal disclosure oracle. Mitigation: scope retrieval to the caller at M1/M5.
- **New P2 (Kleppmann, ADR-05/08)**: SERIALIZABLE-everywhere has no serialization-failure retry policy / abort-rate budget. Mitigation: define retry+abort budget as an M2 gate.
- **New P2 (Majors, ADR-13)**: owned OTLP exporter has no collector-round-trip conformance gate (silent wire-encoder bug). Mitigation: add round-trip conformance to the M2 exporter gate.

## 4. Build-stage plan (estimates in focused orchestrated build-sessions)

- **Stage A — walking skeleton (= M0). 2 sessions.** One Go binary + Postgres: admit a TS definition (canonical print → vendored tsgo check → one real verifier → content-addressed row + catalog entry, one SERIALIZABLE transaction) → evaluate on the CEK interpreter → serve an HTTP response; `admit!` / rollback / as-of demo. Kill-test: §4's four families green + ADR-04 §8 perf budgets (CEK ≥1M steps/sec/core, ≤50k transitions/req p95, ≤10% metering-tax) + R1-01 I4 overlap kill-test on real PG16.13.
- **Stage B — deepest bets under kill-tests (= M0 CFR core → M2). 4 sessions — the riskiest stage, budget for rework.** Continuation serialize/resume across process restart; sleep/receive as rows; exact-budget fuel halt (park-not-panic); capability environments (an ungranted call is unnameable, not merely denied). Kill-test: `kill -9` mid-workflow then clean resume to identical result — plus ADR-05 tests 1–10 (year-old resume, double-resume race, poison-pill, revoked capability, wake storm) and the cross-kernel randomized-scheduling determinism probe. This stage carries R1 and R2; a CFR or capture redesign here invalidates everything above it, so it is gated before any surface work.
- **Stage C — verifier suite + catalog + surfaces (= M1 + M5). 3 sessions.** Full v1 six-verifier roster red-pathed (hermetic tsgo host, typecheck-DoS isolation, dual mutation, coverage rows); MCP agent plane (11 tools, admission-fuel, refusal ledger, approval tokens); git projection outbound (deterministic fold, self-heal mirror, merge-as-admission). Kill-test: one fixture per verifier + harness self-test with seeded mutants; byte-identical SHAs on two machines + merge-side-door impossibility; MCP exfil sweep + confused-deputy injection corpus + spam-flood latency isolation.
- **Stage D — std/ v1 slice + reactive layer (= M3 + M4). 3 sessions.** erf `resource(...)` + derivation artifacts, 13 field types; the 25-component vocabulary (no raw-HTML escape hatch) with admission-time static/dynamic split + server-diffed rendering; taak await-as-checkpoint workflows; genesis transaction. Kill-test: genesis reproducibility across two fresh DBs byte-identical + mid-genesis kill (empty-or-complete); dispatch bijection boot-refusal; PII-derivation rejects; 50k-session invalidation storm within drain budget; `wan-150` felt-latency gate (input→echo ≤50ms, action→commit ≤300ms p95).
- **Stage E — proof app + evidence (= M6 → v1). 3 sessions.** A small CRM entirely as admitted rows; demonstrate tenant field-add, agent patch over MCP, a mid-flight workflow surviving deploy, as-of rollback, PII crypto-shred with attestation. `migrate N` machinery + golden-continuation corpus + O1–O5 fences + bad-epoch revert drill. Deliver `docs/claim-evidence.md` mapping every load-bearing claim to a test / demo / residue. Kill-test: reference app green end-to-end; stranded-continuation impossibility across two epoch boundaries; reference-dashboard stranger-review gate recorded (missing verdict reads red).

Total: **~15 build-sessions**, Stage B the single largest and highest-variance line.

## 5. The ask

**GO.** GO commits to **Stage A only** — stand up the one-binary + Postgres walking skeleton, admit → evaluate → serve, with the `admit!`/rollback/as-of demo and M0's four kill-test families plus perf budgets green. Stages B–E open sequentially, each behind its predecessor's machine gate; re-decide at the Stage A gate before opening the deep-bet stage.
