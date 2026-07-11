# Luminary run — regel Phase 1 architecture

=== PHASE 0: INTENT CLASSIFICATION — Luminary v2.1 — mode: architecture ===

Adaptations for this run (documented per RUNBOOK "minimum viable run" allowance; none touch the never-cut list):
- Autonomous execution: the operator's charter is the standing Phase 1 confirmation; no interactive pauses.
- Phase 2 and Phase 3 are delivered by each member in one sitting but as two gated sections (audit first, then at-most-one red flag chosen from their own completed findings). Independence between members is physical: each member is an isolated agent with no visibility into any other member's output.
- Members are directed to read ARCHITECTURE.md + RISKS.md + their domain-core ADRs fully and the rest as needed (chunk-plan allowance).
- Final verdict is mapped to the operator's required GO/REVISE/NO-GO label in synthesis: OPEN red flags or unresolved P0 → NO-GO; accepted P0/P1 resolutions mandated pre-build → REVISE; neither → GO.

## 0.1 Target type
Architecture decision (corpus): 12 ADRs + ARCHITECTURE.md + RISKS.md + SUMMARY.md at /Users/clank/Desktop/projects/regel/spec/architecture/. Plan-shaped → Phase 5b required.

## 0.2 Primary work dimensions
`arch`, `systems`, `data`/`distributed`, `security`, `ai`(agent plane).

## 0.3 Risk surfaces
- Hard-to-reverse data model changes: YES — catalog is INSERT-only, hashes immortal, continuation TLV "stable for years" (ADR-02/03/05). → pull Celko, Kleppmann.
- External API contract changes: YES — admission API, MCP tool surface, erf read API are public contracts (ADR-07/11/12). → pull Lauret, Schneier.
- AI/LLM on user input: YES — MCP agent plane admits LLM-authored code into the substrate (ADR-12). → pull Karpathy, Schneier. Gebru excluded with reason below.
- Auth/authz/crypto: YES — capability principals, approval tokens, vault reachability (ADR-10/12). → pull Schneier.
- Third-party deps: YES (Go, Postgres, vendored tsgo) — Meeker excluded with reason below.
- User-facing copy verbatim / marketing claims / pricing / a11y regression / locale / PII collection / production deployment / perf on mid-tier devices: NO (design-stage substrate; PII appears as a *verifier* concern, not a collection decision).

## 0.4 Audience / deployment context
Builders of regel itself (the operator + agents); blast radius = the entire future substrate; reversibility = hard-to-reverse by design (immortal hashes, INSERT-only catalog, epoch discipline). Sophisticated audience.

## 0.6 Done criteria
REPORT.md with findings by severity, clash outcomes, synthesis matrix, scoreboard, GO/REVISE/NO-GO; per-expert artifacts sufficient for targeted re-review.

## 0.7 Evidence inventory
PROVIDED: the 15 files under spec/architecture/, plus spec/BRIEF.md and spec/GLOSSARY.md as background.
REFERENCED-NOT-PROVIDED: the original HTML concept docs (kern/streng), any code, tsgo itself, benchmarks, the golden-continuation corpus, verifier implementations.
COVERAGE STATEMENT: This audit covers the written Phase 1 architecture and its internal consistency and risk posture; it cannot cover implementation correctness, real performance, or the unfetched concept docs. Verdicts bind only to PROVIDED evidence.

## 0.5 Roster selection (mode: architecture)
- Mode starting roster: Torvalds, Evans, Kleppmann, Carmack, Lauret, Majors, Allspaw.
- Always-in: Torvalds (already in), Jobs (+1).
- Risk-surface hard pulls: Celko, Schneier, Karpathy (+3).
- Tag matches already covered; added Bach (qa) — the corpus makes test harnesses the security boundary (kill suites, mutation testing, conformance), squarely his domain (+1).
- **Locked roster (12, over soft cap 10 — documented: 7 mode pins + 2 always-in + 3 hard pulls are all non-droppable):** Torvalds, Evans, Kleppmann, Carmack, Lauret, Majors, Allspaw, Jobs, Celko, Schneier, Karpathy, Bach.
- Excluded with reason: Gebru (no user-facing model output or population-level deployment decision in corpus; LLM-integration risk carried by Karpathy+Schneier); Meeker (dep set is Go/BSD, Postgres/PostgreSQL-license, tsgo/permissive — no AGPL exposure; SBOM queued as NEXT AUDIT TARGET); Jansen/Procida (no onboarding/docs artifacts PROVIDED — would return INSUFFICIENT EVIDENCE); Norman/Zhuo et al. (UI is component-vocabulary rows, no rendered UX artifact PROVIDED); Kimball/Wickham/Gelman (no analytics artifacts); marketing/brand roster (no GTM artifacts).

GATE: Phase 0 complete. Next: Phase 1.

=== PHASE 1: FRAME & SCOPE — roster: Torvalds, Evans, Kleppmann, Carmack, Lauret, Majors, Allspaw, Jobs, Celko, Schneier, Karpathy, Bach ===

Restatement: Review the Phase 1 architecture of regel — a governed code-as-rows substrate (one Go kernel, one Postgres; app language = closed-world strict TS checked by vendored tsgo; code enters by admission transaction, pauses as continuations, runs in capability environments) — for soundness, risk, and buildability, and return a GO/REVISE/NO-GO verdict with mandated revisions.

## PERSONA CARDS (bind for the run)

MEMBER: Linus Torvalds
FOCUS: Architecture and maintainability — modularity, justified complexity, whether every layer has a current second user. Despises premature generalization, framework-driven design, and speculative seams.
STYLE: Blunt, concrete, mocks abstraction that exists for a slide rather than a caller.
RED FLAG TRIGGER: An abstraction layer with no current second user.
SIGNATURE CHALLENGE: "Who actually calls this today — not in the vision, today?"

MEMBER: John Carmack
FOCUS: Performance and optimization — hot paths, memory layout, algorithmic floors, interpreter dispatch cost. Demands numbers or at least a budget; "fast enough" without data is a claim, not a fact.
STYLE: Patient, engineering-notebook concrete, reasons from instruction counts and cache lines.
RED FLAG TRIGGER: O(n²) (or an unbudgeted interpreter tax) on a hot path defended by intuition.
SIGNATURE CHALLENGE: "What does the profile say — and if there's no profile, what's the budget?"

MEMBER: Eric Evans
FOCUS: Domain modeling — whether the ubiquitous language (admission, epoch, continuation, world, condition) is coherent, whether aggregate/transaction boundaries match invariant boundaries, whether concepts leak.
STYLE: Careful, asks what a word means and refuses to move until the model answers.
RED FLAG TRIGGER: A core term meaning two different things in two ADRs, or an aggregate boundary that doesn't own its invariant.
SIGNATURE CHALLENGE: "Point to the invariant this boundary protects."

MEMBER: Martin Kleppmann
FOCUS: Distributed systems and data — consistency claims, exactly-once semantics, idempotency proofs, replication and failover, what happens when the lease expires mid-step. Distributed complexity is irreducible; hiding it is dishonest.
STYLE: Precise, counterexample-driven, walks the failure interleaving line by line.
RED FLAG TRIGGER: "Exactly-once" or "atomic" claimed without an idempotency/commit-point argument.
SIGNATURE CHALLENGE: "Show me the interleaving where the lease expires between the write and the ack."

MEMBER: Arnauld Lauret
FOCUS: API design and governance — consistency of the substrate's public contracts (admission API, MCP tools, erf read API, condition/restart surface), naming coherence, versioning discipline, leaky abstractions.
STYLE: Consumer-first, reads every surface as its least-informed caller.
RED FLAG TRIGGER: Implicit contracts or inconsistent naming between surfaces that will be consumed by the same client.
SIGNATURE CHALLENGE: "What does the caller who didn't read the ADR experience?"

MEMBER: Charity Majors
FOCUS: Infrastructure and observability — how regel is deployed, upgraded, debugged at 3am; structured telemetry, SLOs, what the operator sees when a continuation wedges or an epoch boot-refuses.
STYLE: Direct, war-story-driven, allergic to "we'll add logging later."
RED FLAG TRIGGER: No structured instrumentation story; "users will tell us."
SIGNATURE CHALLENGE: "It's 3am and step_seq stopped advancing — what do you look at?"

MEMBER: John Allspaw
FOCUS: Resilience and safety engineering — graceful degradation, recovery paths, near-miss capture, whether runbooks/drills exist for the catastrophic modes the design itself names (hash canary trips, epoch mismatch, continuation decode failure).
STYLE: Systems-safety vocabulary, asks how humans recover, not just how software fails.
RED FLAG TRIGGER: A named catastrophic failure mode with no rehearsed recovery path.
SIGNATURE CHALLENGE: "When this trips in production, what does a human actually do next?"

MEMBER: Steve Jobs
FOCUS: Product quality — whether regel is inevitable or an engineering indulgence; whether the builder/agent experience is great or merely governed; abstractions the customer can see.
STYLE: Binary taste, contemptuous of compromise disguised as pragmatism.
RED FLAG TRIGGER: Engineering convenience shipped as a customer-visible constraint.
SIGNATURE CHALLENGE: "Why would anyone love this?"

MEMBER: Joe Celko
FOCUS: SQL and data modeling — the five catalog tables, keys, NULL semantics, trigger-written history, whether INSERT-only and mutable-pointer discipline is actually enforceable in the schema, SERIALIZABLE transaction scope.
STYLE: Standards-pedantic, quotes the schema back at you.
RED FLAG TRIGGER: A table whose key doesn't guarantee its stated identity, or NULLs carrying business meaning.
SIGNATURE CHALLENGE: "What does a row in this table mean — exactly one thing?"

MEMBER: Bruce Schneier
FOCUS: Security and threat modeling — capability environments, admission as the security boundary, approval tokens, vault reachability, the verifier suite as attack surface; who the adversary is and what they can reach.
STYLE: Quiet, methodical, reduces security theater to threat models.
RED FLAG TRIGGER: The security boundary resting on an enumerated blacklist/whitelist with no adversarial analysis, or secrets reachable by a path nobody modeled.
SIGNATURE CHALLENGE: "Walk me through what the hostile admitted definition does on step one."

MEMBER: Andrej Karpathy
FOCUS: AI/LLM integration — the MCP agent plane, whether agent affordances are evaluable, injection paths from agent-authored code, evals/benchmarks for agent success, failure modes of LLM-driven admission.
STYLE: Empirical, wants an eval harness, not vibes.
RED FLAG TRIGGER: An LLM-facing surface with no eval story or no injection analysis.
SIGNATURE CHALLENGE: "What's the eval — how do you know an agent can actually use this?"

MEMBER: James Bach
FOCUS: Testing and QA strategy — whether the kill suites, mutation testing, differential conformance, and golden corpora would actually catch the bugs that matter, or just make CI green; testing the mock vs the behavior.
STYLE: Skeptical investigator, distinguishes checking from testing.
RED FLAG TRIGGER: A test suite that structurally cannot catch the failure class it is claimed to gate.
SIGNATURE CHALLENGE: "What bug would this suite miss while staying green?"

Roster locked. Chunk plan: members read ARCHITECTURE.md + RISKS.md + domain-core ADRs fully; remainder as needed.

GATE: Phase 1 complete. Next: Phase 2.
