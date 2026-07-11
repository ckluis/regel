# ANDREJ KARPATHY — R1 targeted re-review (AI/LLM Integration)

## VERDICT: PASS (round-1 red flag stays withdrawn; two new findings, none blocking)

Scope: revisions 4/13 only; substance judged at R1-04:/R1-13: sites. Carries not re-litigated.

## REVISION 4 — injection defense: **SATISFIED**
All four bound deliverables landed with mechanism, not prose:
1. Confused-deputy adversary is a first-class abuse row: "an attacker who **cannot
   author** … but **can seed content the agent reads**" (ADR-12 §2) — abuse mode → three
   named controls. Constraint #5 now reads "agent-as-adversary and agent-as-victim" (§461).
2. Injection corpus is a mechanical M5 gate, not prose: "co-equal with the §4 PII sweep …
   **M5-BLOCKING**" (ADR-12 §4a) AND wired as a `required` status check in the manifest —
   ARCH §5.1 M5 row: "R1-04 confused-deputy injection corpus, M5-blocking (ADR-12 §4a)"
   under "Each row's suites are `required` status checks" (§328). Revert-to-P0 language
   preserved verbatim. The oracle is inverted correctly — "a green result on a hostile
   fixture fails the release" — so it *can* fail.
3. Content-seeder attribution (the third principal from C1): admission row records
   "`{source_kind, source_ref, scope, seeded_by | "unattributed"}`" (ADR-12 §6, ADR-07 §1/§6),
   scope-chain-validated so "the set cannot be forged to blame another tenant" (§281).
4. Delta is a **precondition of approve**, not decoration: a surface-widening patch
   "**cannot be approved without the delta shown** — the render is a precondition of the
   approve action" (ADR-12 §7). `unattributed` seeder counts as widening.

### RED FLAG: **STAYS WITHDRAWN.** The C1 revert condition (ship revision 4 ungated) did
not occur — all three deliverables are hard M5 gates with revert-to-P0 language intact.

## REVISION 13 — agent-competence evals: **SATISFIED**
1. Authoring pass@k runs the **real** pipeline: "the **real ADR-07 admission pipeline** …
   **no mocked verdict**" (§3a), N≥50 monotone tasks, "pass@1 ≥ 0.5 AND pass@k ≥ 0.9",
   M5-blocking and manifest-wired (ARCH §5.1 M5).
2. Fuel capacity is eval-traceable: "`capacity = ceil(P95_iterations_to_green ×
   cost_full_pipeline × margin)`" re-derived "every epoch and every dialect-version bump";
   "not traceable to a §3a P95 measurement is **red**" (§5).
3. Restart-decision accuracy ≥0.95 gates the `condition.restart` authority (M≥30 labeled
   scenarios, §7). **The documented narrowing honors the mandate.** The policy — red metric
   does not block M5, instead "the agent-facing `condition.restart` tool ships **disabled**"
   until green — is *fail-safe*: it withholds the unproven capability rather than blocking an
   unrelated substrate milestone. My r1 recommendation asked for a decision-accuracy metric
   gating restart selection; that is exactly what ships, and disabling-until-green is the
   stronger reading, not a dodge. Human operator buttons are unaffected. Honored.

## ADR-13 SCRUTINY — registered-extension vs golden signal: **ACCEPTABLE, not a finding**
The three gauges sit in ADR-13 §2 as "Registered extensions … not new golden rows." Fine
**because the gate's authority does not live in ADR-13.** M5-blocking force comes from ARCH
§5.1's `required` checks + ADR-12's floors; per-epoch re-derivation is itself gate-enforced
(`untraceable-to-P95 = red`). ADR-13 is display — "CI rows first, exported as per-epoch
gauges so a floor regression is visible on the same surface that alarms." Golden signals are
continuous kernel telemetry; these are per-epoch batch eval facts — a principled distinction,
not a demotion. (P3 nicety: bind them to an SLO/alarm in §3 so a *missed* re-run also alarms.)

## NEW FINDINGS
- **[P2] pass@k floor is gameable via the operator-set retry ceiling.** §3a reports pass@k
  "at k = 1 and **k = the operator-set retry ceiling**." k is a tunable knob: widening the
  ceiling inflates pass@k toward 1 without improving the agent, and the same widening also
  inflates the P95-derived fuel budget. The pass@1 ≥ 0.5 floor is the only ungameable leg.
  Fix: pin k per epoch in the manifest (spec-fixed, not operator-tunable) so the 0.9 floor
  measures competence, not budget generosity.
- **[P3] Injection corpus under-names the tenant-scope integrity harm.** §4a's assertion set
  leads with escalate/exfiltrate (the substrate-boundary harms already covered) and folds the
  C1 co-primary harm — a capability-clean, *same-scope* attacker-directed overlay mutation —
  into "render treats the seeded text as inert data." That harm trips neither "escalation"
  nor "exfiltration" as defined; give it an explicit named assertion (agent did not perform
  the seeded in-scope imperative) so corpus authors cannot under-test it.

## ORIGINAL FINDINGS — transitions
- F1 [P0] injection unmodeled → **RESOLVED** (§2 row + §4a M5 corpus + §6 attribution + §7 delta).
- F2 [P1] no agent-success eval → **RESOLVED** (§3a pass@k on the real pipeline, M5-blocking).
- F3 [P2] dry-run fuel self-throttles → **RESOLVED** (§5 eval-derived capacity; honest task must not hit `ADMISSION_BUDGET`).
- F4 [P2] restart decision unmeasured → **RESOLVED** (§7 accuracy ≥0.95 gating the authority).
