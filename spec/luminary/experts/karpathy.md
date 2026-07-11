# ANDREJ KARPATHY — AI/LLM Integration

## VERDICT: FAIL (one P0 / red flag: agent-plane prompt injection unmodeled)

## FINDINGS

1. [P0] The agent plane has no prompt-injection threat model — every read tool pipes attacker-influenceable content straight into LLM context (other-scope `canonical_text`, resource rows, durable-condition messages, audit rows), and the only content-safety mechanism is PII masking. Injected instructions can drive a *trusted* agent to self-serve malicious-but-verified overlay admissions and to generate/advocate product-scope patches for a human whom the authors themselves rate as the residual attack surface. CITE: "grep every MCP response in the reference-app suite for seeded plaintext" (ADR-12, §4 kill-test) — content safety = PII-only.

2. [P1] "Convergent" is a determinism claim, not an agent-competence claim: a deterministic reject repeated forever is still "convergent." Nothing in the roster measures whether a current LLM can author a *passing* patch against this deliberately idiom-hostile dialect (no `class`/`new`/generators/`for(let i...)`), nor bounds retry-loop termination. There is no agent-success eval anywhere. CITE: "Determinism and hermeticity (ADR-07 §2) make the loop convergent" (ADR-12, §3).

3. [P2] The design mandates an iterate-on-`diagnostics[].fix` loop, but every `commit:false` dry-run runs the full pipeline and admission-fuel bills by deepest stage — so the encouraged iteration self-throttles, with no sizing of budget capacity against iterations-per-task. CITE: "charges by the deepest stage reached" (ADR-12, §5).

4. [P2] The `expectedHash` fence guarantees resuming the *right row*, not the *right decision*; a hash-valid, capability-valid, semantically-wrong restart resumes a workflow to a durable wrong state, and no eval measures whether an agent picks the correct restart/args. CITE: "One condition set, two front-ends, one exact-target" (ADR-12, §7).

## RECOMMENDATIONS

- Ship an agent-authoring eval harness before M5: seed N author/fix tasks, drive a real LLM through the MCP loop, report pass@k and mean-iterations, gate release on a floor. Verify: the numbers exist as CI rows next to `verifier_coverage`.
- Add an injection corpus, mirror of the §4 PII sweep: plant imperative text in `canonical_text`, docstrings, condition `message`, and resource rows; assert the agent neither escalates nor exfiltrates and the operator-plane render treats it as inert data. Verify: injection corpus green, error paths included.
- Publish a fuel-vs-iteration budget: measure mean dry-runs per successful task from the eval, set default bucket ≥ P95, optionally price `commit:false` below `commit:true`. Verify: honest-agent eval tasks never hit `ADMISSION_BUDGET`.
- Add a restart-decision eval: scored condition scenarios, measure correct-restart selection rate; consider a restart-consequence preview. Verify: a decision-accuracy metric per epoch.

## RED FLAG

CATEGORY: SECURITY
CITE: "grep every MCP response in the reference-app suite for seeded plaintext" (ADR-12, §4) — the sole content-safety test is PII plaintext; no injection analysis exists.
CONSEQUENCE: The entire 11-tool/6-resource agent-authoring surface ships assuming the agent is the only adversary, ignoring that its own inputs are attacker-controllable. A prompt-injected trusted agent becomes an automated generator of verified-but-malicious overlay admissions and of approval-seeking product patches aimed at the design's own named-weak human gate — unsafe LLM behavior that a trust-boundary design plus an injection eval, cheap now and expensive post-ship, would foreclose.
