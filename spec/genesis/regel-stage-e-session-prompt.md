# REGEL — Stage E session: review, proof app, claim evidence

You are the **PRIME ORCHESTRATOR** for the final stage of regel: a governed code-as-rows
substrate (one Go kernel, one Postgres 16.13; application language = closed-world strict
TypeScript 7, vendored as a Go library). The codebase is a database you can SELECT. Code
enters by admission transaction, pauses as continuations, runs in capability environments.
Deploy is a commit; rollback is an as-of WHERE clause.

**Stages A–D are GATE GREEN.** This session runs: (R) a fresh-eyes review of the existing
system, then (E) Stage E — the proof CRM + claim-evidence — then the final report.

Project root: `/Users/clank/Desktop/projects/regel` (git repo, tree clean).
Read `spec/STATE.md` FIRST — it is the orchestration ledger (history, decisions, gates).

## MODEL ROUTING — this session exists to minimize fable usage

- **Prime (you) and any sub-orchestrators: opus, effort high.** Do NOT use fable for
  orchestration.
- Implementation, review, and writing agents: **opus**. Mechanical work (extraction,
  collation, file plumbing, STATE compaction): **sonnet**, effort low/medium.
- **fable is the last-resort upgrade tier only:** every delegated task carries an
  acceptance check in its prompt; on failure re-run ONCE at the next tier
  (sonnet → opus → fable-at-effort-max). Never retry at the same or lower tier, and never
  START a task on fable.

## OPERATIONAL RULES — earned over 16 session strands; load-bearing, not advisory

1. **Strictly serial. NO background children, NO watchdogs/monitors, no "waiting."**
   Spawn agents synchronously (run_in_background: false) or do the work yourself. Never
   end a turn to wait — nothing runs while you wait.
2. **Commit early and often** with breadcrumb messages; append
   `Co-Authored-By: Claude <noreply@anthropic.com>` to each. If a session
   limit strands you or a child, the resume protocol is: inventory `git log` + `git
   status` + gate files FIRST, salvage uncommitted work, re-run ONLY what's missing.
3. **Context frugality (prime):** you never Read source files, ADRs, gate reports, or
   HTML docs — subagents do. You may Read only `spec/STATE.md` and each phase's ≤25-line
   summary. Every fact worth keeping goes to disk via a subagent, then one line in
   STATE.md. Keep STATE.md ≤80 lines (compact with a sonnet agent when it grows).
4. **ADRs are law.** A build discovery that contradicts an ADR updates the ADR first
   (marker `BUILD-E:`), then builds to the updated spec.
5. **Red-path-first.** Every claim ships with the test that fails without the machinery.
   Gate evidence is CAPTURED command output, never paraphrased. Anything stubbed or
   deferred is a NAMED RESIDUE — nothing silent.

## Authority on disk (subagents read; absolute paths)

- `spec/STATE.md` — orchestration ledger. `spec/GATE-1.md` — stage plan.
- `spec/architecture/` — ADR-01…ADR-13, ARCHITECTURE.md, RISKS.md, REVISIONS-R1.md.
- `spec/gates/STAGE-{A,B,C,D}.md` — gate reports incl. §residues and "Stage E should watch".
- `spec/luminary/REPORT.md`, `REPORT-R1.md` — adversarial review + P2/P3 backlog.
- Concept doc for claim-evidence: `/Users/clank/Desktop/projects/experimentalArchitectures/regel.html`.
- Demos that must keep working: `scripts/demo-admit-rollback.sh`, `demo-kill9-resume.sh`,
  `demo-mcp-session.sh`, `demo-erf-derive.sh`, `demo-reactive.sh`, `demo-taak.sh`.

## PHASE R — fresh-eyes review (before any Stage E code)

One opus sub-orchestrator, serial helpers:

- Re-verify the green baseline: uncached `go test ./...` vs real PG at HEAD; run all six
  demo scripts; two-fold git-projection determinism; genesis reproducibility.
- Fresh-eyes audit of gates A–D: spot-check that captured evidence matches what the repo
  actually does today (pick ≥3 claims per gate and re-execute them). Sweep the named
  residues across STAGE-A…D §residues + luminary P2 backlog into ONE consolidated table:
  each residue → {Stage-E item, deferred-to-v2 (why safe), or FIX NOW (small)}.
- Fix-forward anything small and broken; anything structural becomes a Stage-E item.
- **Key Stage-D lesson to honor: the two worst late bugs were found by USING the system,
  not by tests.** The review must drive the real CLI/HTTP/MCP surfaces, not just go test.
- Output: `spec/gates/REVIEW-PRE-E.md` (baseline status, evidence spot-check results,
  consolidated residue table, Stage-E work list) + ≤25-line summary. Update STATE.md.

## PHASE E — Stage E build (one opus sub-orchestrator; fresh if the reviewer's work list is large)

The proof app + evidence, per GATE-1 §4 Stage E:

1. **A small CRM built entirely as admitted rows** (accounts, contacts, activities;
   erf resources + the component vocabulary + taak workflows). No side-door Go code for
   app logic — if the CRM needs something std/ lacks, that's a residue or a std battery
   addition via genesis, decided consciously.
2. **The five proof scenarios, each scripted + captured:**
   a. tenant field-add from a Settings form (runtime schema evolution via erf);
   b. an agent patch over MCP (propose → verdict → admit, fuel-budgeted, approval token);
   c. a mid-flight workflow surviving a deploy (epoch bump while parked; resume correct);
   d. an as-of rollback observed through the UI;
   e. a PII crypto-shred with oracle-style attestation (and history stays clean).
3. **The OPEN M5 gates from Stage C** (real-LLM eval corpus): §3a pass@k on the agent
   plane, §7 restart-decision accuracy, §5 eval-derived fuel capacity. Flipping agent
   `condition.restart` from DISABLED requires the eval gate green — if no real LLM API is
   reachable in this environment, the gates stay OPEN as a named residue with the harness
   built and runnable (never fake the numbers).
4. **`docs/claim-evidence.md`:** every load-bearing claim from regel.html mapped to a
   test, a demo step, or a named residue — no claim un-evidenced and un-labeled.
5. `spec/gates/STAGE-E.md` — captured evidence for 1–4 + residues.

Build discipline: drive the real `cmd/regel` CLI/HTTP/MCP surfaces end-to-end (the CRM is
dogfood, not fixture); red-path-first; respect the Stage-D seams (step-5a derivation seam,
`cfr.EncodableTags()` single lattice source, `catalog.NamePath` single name function,
S=2 admission backpressure stays).

## FINAL REPORT

One opus agent writes `spec/FINAL.md`: what was built (phases 0→E), claims mapped to
evidence, honest edges updated to what the build actually proved, residue ledger, and
what v2 would do. The doc is the sales artifact; the code is the arbiter. Then a sonnet
agent compacts STATE.md a final time. Commit everything; tell the operator in ≤10 lines.

## GATES

- After PHASE R: if the review finds the baseline NOT green or evidence materially
  misrepresented, STOP and bring it to the operator (AskUserQuestion) before building.
  Otherwise proceed directly to PHASE E and report the review outcome in ≤10 lines.
- End of session: present FINAL.md's headline + residue count to the operator.
