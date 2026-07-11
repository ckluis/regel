# regel — orchestration state

## Status
- Phase 0 (ingest & brief): DONE — spec/BRIEF.md + spec/GLOSSARY.md, acceptance passed.
- Phase 1 (ARCH): DONE — 12 ADRs + ARCHITECTURE.md + RISKS.md + SUMMARY.md in spec/architecture/; hedge-grep clean; survived 2 session-limit hits (finished serial).
- Phase 2 (LUMINARY): DONE — verdict REVISE. P0×2, P1×17, P2×29, P3×6; all P0/P1 citations grep-verified; 14 mandated revisions in spec/luminary/REPORT.md (with targeted re-review map). Runbook v2.1 fetched live, 12-expert locked roster, clashes C1-C3 all COMPROMISE.
- Phase 3 iter 1/2 — ARCH-R1: DONE. 14/14 revisions applied + fable integration pass (5 cross-ADR contradictions fixed). ADR-13-observability created. Ledger: spec/architecture/REVISIONS-R1.md (incl. 4 documented deviations: R1-07 M1-not-M0 tsgo budget; R1-08 patch_id optional/enum 7; R1-12 SQL literal kept; R1-13 restart-authority disable). Markers: R1-<nn> + R1-INT grep-verified.
- Phase 3 iter 1/2 — LUMINARY-R1 re-review: DONE — **VERDICT: GO** (spec/luminary/REPORT-R1.md). 14/14 SATISFIED; both P0 red flags CLEARED (Celko, Bach); Allspaw+Karpathy stay withdrawn; all 4 deviations ACCEPTED. New findings 0 P0/P1, 7 P2, 16 P3 → tracked backlog with expert owners; 8 out-of-scope carries. No iteration 2 needed.
- GATE 1: IN PROGRESS — opus agent writing spec/GATE-1.md; operator GO/revise/stop decision pending. **No build before operator GO.**

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

## Next
- Phase 2 LUMINARY verdict → Phase 3 revision loop (if REVISE, max 2) → GATE 1 operator stop.
