# regel — orchestration state

## Status
- Phase 0 (ingest & brief): DONE — spec/BRIEF.md + spec/GLOSSARY.md, acceptance passed.
- Phase 1 (ARCH): DONE — 12 ADRs + ARCHITECTURE.md + RISKS.md + SUMMARY.md in spec/architecture/; hedge-grep clean; survived 2 session-limit hits (finished serial).
- Phase 2 (LUMINARY): DONE — verdict REVISE. P0×2, P1×17, P2×29, P3×6; all P0/P1 citations grep-verified; 14 mandated revisions in spec/luminary/REPORT.md (with targeted re-review map). Runbook v2.1 fetched live, 12-expert locked roster, clashes C1-C3 all COMPROMISE.
- Phase 3 iter 1/2 — ARCH-R1: DONE. 14/14 revisions applied + fable integration pass (5 cross-ADR contradictions fixed). ADR-13-observability created. Ledger: spec/architecture/REVISIONS-R1.md (incl. 4 documented deviations: R1-07 M1-not-M0 tsgo budget; R1-08 patch_id optional/enum 7; R1-12 SQL literal kept; R1-13 restart-authority disable). Markers: R1-<nn> + R1-INT grep-verified.
- Phase 3 iter 1/2 — LUMINARY-R1 re-review: DONE — **VERDICT: GO** (spec/luminary/REPORT-R1.md). 14/14 SATISFIED; both P0 red flags CLEARED (Celko, Bach); Allspaw+Karpathy stay withdrawn; all 4 deviations ACCEPTED. New findings 0 P0/P1, 7 P2, 16 P3 → tracked backlog with expert owners; 8 out-of-scope carries. No iteration 2 needed.
- GATE 1: **OPERATOR GO (Stage A only)** 2026-07-10 — spec/GATE-1.md; design corpus committed (1cde410). Re-decide at Stage A gate before Stage B.
- Phase 4 Stage A (walking skeleton, =M0): **DONE — GATE GREEN with named residues** 2026-07-13 (spec/gates/STAGE-A.md). One binary + PG 16.13: admit→CEK-evaluate→serve + admit!/rollback/as-of demo (scripts/demo-admit-rollback.sh, 8/8 steps); go test ./... green (9 pkgs, real PG); 4 kill-test families at Stage-A scope; perf 27.1M CEK steps/sec (27× floor), metering tax ≈0%, 10 transitions/req; I4 overlap kill-test on PG 16.13; TS 7.1.0-dev vendored as Go lib; 3 ADR-first fixes (BUILD-A: markers in ADR-03/05). Survived 3 more session strands (serial finishers). **Operator re-decision required before Stage B** (GATE-1 §5).

## Facts
- Project root: /Users/clank/Desktop/projects/regel (git initialized 2026-07-09).
- Concept docs: /Users/clank/Desktop/projects/experimentalArchitectures/{regel,kern,streng,taal,eigen}.html
- Session prompt: /Users/clank/Desktop/projects/experimentalArchitectures/regel-session-prompt.md
- Essence: kern substrate (code-as-rows, admission, continuations, capabilities, one Postgres) + streng dialect (closed-world strict TS 7 via vendored tsgo); one Go kernel; owned pure-Go interpreter; deploy=commit, rollback=as-of.
- 6 edges as constraints: (1) continuations = deepest bet, kill-test first; (2) owned interpreter tax — thin reactor, SQL heavy-lifting, reserved AOT-to-Go seam; (3) durable-condition rows + restarts as operator buttons; (4) AST schema + canonical printer + hashing load-bearing; (5) verifier suite (not types) is the security boundary; (6) walking skeleton before any feature.
- Phase 0 flags: ~25-component vocabulary never enumerated in any doc (Phase 1 must roster it); kern says SBCL/Lisp — regel overrides to Go; taal borrowed only as reserved hot-function AOT lane.

## Decisions (Phase 1, one line each — full text in spec/architecture/)
- ADR-01 dialect: default-deny AST whitelist; classes/this/new/generators/symbols/enums banned; async/await sole suspension surface; 5 capture rules make serializability an admission property.
- ADR-02 printer: identity = SHA-256 over TLV of normalized AST (never text); types+contracts in hash, comments/local names out (De Bruijn); nightly world-rehash canary.
- ADR-03 catalog: 5 tables (INSERT-only definition by hash, meta, mutable name_pointer, trigger history, admission ledger); one SERIALIZABLE txn for code+DDL+pointers.
- ADR-04 interpreter: own pure-Go defunctionalized CEK machine day one (no goja/tsgo-emit); fuel parks as durable condition; differential conformance vs node; taal AOT seam reserved.
- ADR-05 continuations: state-capture, C/E/K frames in versioned binary TLV (CFR); capture verifier at admission; exactly-once via step_seq CAS + lease; 10-test kill suite gates M0.
- ADR-06 kernel: goroutine-per-request; owned PG wire client; immortal-by-hash cache; one SKIP LOCKED task table.
- ADR-07 verifiers v1 (6): capability-audit, pii-flow, catalog-parity, contracts, capture, derivation-parity; hermetic in-memory tsgo host; dual mutation testing.
- ADR-08 epochs: epoch = (kernel binary, std-manifest-root), boot-refusal on mismatch; migrate-N dry-run findings-as-rows then all-or-nothing commit; golden-continuation corpus.
- ADR-09 git projection: repo = deterministic fold over admission ledger (byte-identical SHAs = release gate); PR merge IS the admission txn.
- ADR-10 std/: std IS rows in v1 (genesis txn) but not self-hosting (NativeBody→Go by hash); 14 batteries; 25-component roster enumerated, no raw-HTML escape hatch.
- ADR-11 reactive: static/dynamic AST split at admission; SSE-down/POST-up; UI sessions = capped continuation rows.
- ADR-12 agent plane: 11 MCP tools; agents = ordinary capability principals; admission-fuel budgets; vault plaintext structurally unreachable.

## Top risks (RISKS.md order)
1. Printer hash identity (normalization bug invisible to canary). 2. Continuation serialization ("years" is simulation-proven only). 3. Admission correctness (verifier harness is the security boundary).

## Ops lessons
- Session limits killed wide fan-outs twice; run sub-orchestrators serial-to-2-concurrency; resume stragglers only.

- STAGE B GATE: **OPERATOR GO (Stage B only)** 2026-07-13. Re-decide at Stage B gate before Stage C.
- Phase 4 Stage B (deepest bets, =M0 CFR core→M2): **DONE — GATE GREEN with named residues** 2026-07-13 (spec/gates/STAGE-B.md). ADR-05 kill suite 10/10 (+12/13/14; 11 = residue, binds at first frame-kind/CFR-version epoch); real kill -9 cross-kernel resume to identical result, effects exactly-once (scripts/demo-kill9-resume.sh, exit 0); year-old resume across an epoch; 10k wake storm 0 dupes 2.3-2.9s abort_rate ≤0.9%; hermeticity 6/6 byte-identical across processes; exact-budget fuel (park at exactly T-1/T-5); capability smuggle (forged CapToken in CFR) refused pre-machine-re-entry with zero trace; P2-6 retry-on-40001 + ≤5% abort budget implemented+measured; resume p95 ~58ms, CFR blob p95 199B. 5 BUILD-B ADR updates (ADR-05 result/'cancelled'/thunk-joins/channel_message/outbox/retry-policy; ADR-13 rows 25-26). Survived 2 more session strands (9 total). **Operator re-decision required before Stage C** (GATE-1 §5).

## Next
- Operator re-decision at the Stage B gate → C (verifiers V2-V6 + MCP + git projection) → D (std/ slice) → E (proof CRM + claim-evidence).
- Stage C must compose with the ClaimAndStep step-txn seam (fence→CAS→grants→token-revalidation→delivery→fenced checkpoint), not around it; V5 shares internal/cfr's type table (encodable ≡ admitted); STAGE-B.md §10 residues are Stage-C/D work items (V5 4a leg, decode-coverage floor gate, torn-write statement sweep, outbox dispatcher, message match-predicates/event wakes, reaper breaker state machine).
