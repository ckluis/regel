# CLASH C1 — Karpathy (P0 SECURITY red flag) vs Schneier (CONCERNS, no P0)

**Conflict:** Is the absence of a prompt-injection threat model on the ADR-12 agent
plane an unmodeled P0 that blocks, or a bounded-by-design P1 within the existing
security boundary?

**Conductor note:** Both sides argued from the same five documents. Protocol enforced:
steelman before rebuttal, two exchanges maximum, all new claims cited verbatim (≤20
words) with location. Positions moved on argument; outcome recorded below.

---

## Positions

**KARPATHY (opening, P0):** The agent plane's only content-safety mechanism is PII
masking — the sole kill-test is "grep every MCP response in the reference-app suite
for seeded plaintext" (ADR-12 §4). Every read tool pipes attacker-influenceable text
(resource rows, condition messages, audit rows, in-scope docstrings) into LLM context.
An injected *trusted* agent becomes an automated generator of verified-but-malicious
overlay admissions and of approval-seeking product patches aimed at the human whom
RISKS.md itself names as "the remaining product-scope attack surface" (RISKS.md, R11
residual). The design claims constraint #5 is "Discharged for this plane" (ADR-12,
Constraints #5) while an entire adversary class — the agent's own inputs — is
unexamined. Unmodeled adversary class at the security boundary = P0, ship-blocking.

**SCHNEIER (opening, P1):** The enumerated boundary is genuinely attacked — hostile
corpus, dual mutation, monotone coverage — and vault plaintext is held structurally:
"a reveal grant row may name only a human principal" (ADR-12 §4, layer 1), a DB CHECK,
not a prompt. There is no unmodeled path to secrets. Whatever an injected agent
attempts, it acts inside its grant envelope: "no agent principal holds product-scope
write" (ADR-12 §6), and overlay damage is "blast radius one scope (ADR-03 overlay
isolation)" (ADR-12 §6). Injection changes *who steers* the agent, not *what the
substrate permits*. Bounded consequence inside tested controls is P1, not a blocker.

---

## Exchange 1

### KARPATHY — steelman of Schneier

Bruce's position, argued as strongly as I can make it: this design never trusted the
agent in the first place. The abuse table in §2 assumes a fully malicious agent on
every tool — recon, exfiltration, tampering, spam, escalation — and controls each with
mechanism, not policy. A prompt-injected agent is *strictly weaker* than the malicious
agent already modeled, because the injected agent still authenticates with the same
key, holds the same grants, and hits the same gate: every patch runs all six ADR-07
verifiers, product scope is default-deny behind a one-shot human token bound to exact
hashes, vault plaintext is unreachable by three independent layers, and "tokens
without a live grant, and the responder can never hold one" (ADR-12 §4, layer 2).
Even the consequence I fear most — a persuasive patch aimed at the approver — is
already written down as a modeled risk: R11's breaks list literally includes an agent
that "social-engineers an approval" (RISKS.md, R11). On this view, injection is a new
*route* to consequences the register already prices, every one of which lands inside a
tested boundary. Under a triage rubric where P0 means "unmodeled path to secrets or
authority," that is not met. That is a coherent, evidence-backed position, and the
CHECK-constraint point in particular is genuinely strong: no amount of injected text
mints a reveal grant.

### KARPATHY — rebuttal

Three points the steelman cannot absorb.

First, the malicious-agent model does **not** subsume the injected agent, because the
two adversaries attack different assets. §2's abuse modes defend the *substrate from
the agent*. Injection attacks the *tenant through the agent*: a low-privilege actor in
the victim's own org — or an external system whose failure text lands in a durable
condition — writes imperative text into a resource row or a condition `message`; the
org's trusted agent reads it via `resource.query` or `condition.list` and then acts
*with the victim's own grants, inside the victim's own scope*. Every §2 control passes
that action, because to the substrate it is legitimate. "Blast radius one scope" is
containment only from the platform's chair; from the tenant's chair, one scope is
their entire business. The verifiers check capabilities, PII flow, and parity — never
intent. A verified-malicious overlay that quietly rewrites the victim's policy wiring
or mutates rows through `resource.mutate` is admissible by construction.

Second, the design's own posture makes the gap load-bearing, not incidental. ADR-12
claims "Agents are the standing adversarial harness constraint #5 demands" (ADR-12,
Consequences) and that "every abuse mode in §2 names its control and its test"
(ADR-12, Constraints #5). Both sentences are false for the confused-deputy mode: no
abuse row, no control, no test. A security boundary whose completeness claim is
falsifiable by inspection is exactly what a P0 red flag is for.

Third, R11's "social-engineers an approval" models a *malicious agent* persuading a
human. Injection inverts the actor: the *attacker* social-engineers the *trusted*
agent, which then persuades the human at machine speed and with the org's own
credibility. R11's residual names the human as the attack surface but supplies no
technical backing — Bruce's own finding 4 concedes "persuasive wrong patch with a
green dry-run Verdict is approvable" (RISKS.md, R11 residual). An unmodeled vector
that terminates on an unbacked human gate is not a priced risk.

### SCHNEIER — steelman of Karpathy

Andrej's position, argued fully: threat models are enumerations of *adversaries and
their entry points*, and this one is missing an entry point that every deployed
LLM-agent system since 2023 has been burned by — content-mediated instruction. The
document set is otherwise obsessive about naming its attack surface: six verifiers
with per-threat coverage rows, a refusal ledger, kill-tests on error paths. Against
that standard, the total silence on injection is not a minor omission; it is a
category absence at the one place where nondeterministic, non-verifiable computation
(the LLM) touches attacker-influenceable bytes. And the register itself warns that
enumerated defenses fail open on novel channels: "a novel channel (timing, error
shape, a new battery's output path) is outside it until named" (RISKS.md, R8
residual). Injection is precisely such an unnamed channel — into the *model*, not the
masking leaves. Worse, the design funnels the highest-stakes decision to the component
with the least mechanical backing: the human approver, already named the residual
surface. The cost asymmetry Andrej cites is real — an injection corpus mirroring the
§4 sweep is cheap pre-ship and expensive post-ship, because post-ship it becomes an
incident-response program. Finally, his framing has teeth on my own terms: I demand
that boundaries be *attacked, not asserted*; the agent-plane content path is asserted
safe (PII-masked) and attacked never. That is the exact pattern I flag everywhere
else.

### SCHNEIER — rebuttal

Accepting all of that, the P0 classification still fails on the evidence, on three
grounds.

First, the enumerated read surface is smaller than the red flag claims. Other-scope
`canonical_text` — the lead item in Andrej's list — is not attacker-deliverable: "a
definition outside it is unnameable" (ADR-12 §3), byte-identical to a name that never
existed. Verdict text is likewise constrained: "diagnostics cite only names and hashes
resolvable in the submitter's own scope chain" (ADR-07 §6). What survives is in-scope
content: resource rows, condition messages, audit rows, in-scope docstrings. Real —
but it means the injection payload must already come from inside the tenant's trust
domain or its declared external effects. That is a meaningfully narrower adversary
than "every read tool."

Second, P0 in this review means an unmodeled *path to authority or secrets*. Trace the
worst case end to end: injected agent → self-serve overlay patch → full six-verifier
gate → one-scope blast radius, every admission attributed and fuel-priced; or injected
agent → product patch → dead end at `CAP_UNGRANTED` without a human-minted token bound
to exact hashes. The consequence classes — bad overlay writes in the victim's scope,
persuasion of the approver — are both in R11's breaks list today. What is missing is
the *vector* and its *corpus*, not the boundary. A missing test corpus on a standing
boundary is my P1 pattern; an absent boundary is P0. This is the former.

Third, the remedy is additive, which is diagnostic of priority. Andrej's own
recommendation — plant imperative text in rows, docstrings, condition messages;
assert the agent neither escalates nor exfiltrates; assert the operator plane renders
it inert — changes zero ADR structure. No table, no transaction, no scope kind moves.
When the fix for a "blocker" is a test suite plus a register amendment, the finding
was a gap in *assurance*, not in *architecture*. I hold P1 — with the caveat that P1
here must be release-gating, because the tenant-side confused-deputy harm Andrej
describes is real and currently untested.

---

## Exchange 2

### KARPATHY — steelman of Schneier (new ground)

The strongest form of Bruce's exchange-1 rebuttal is the asymmetry test: what would
have to change if I am right? If injection were truly unmodeled-P0, fixing it would
force structural rework — a new trust tier, a content-provenance channel, a fourth
vault layer. It forces none of that. The system was built so that *nothing the agent
believes* matters to what the agent *can do*: identity is grants, grants are rows,
rows are checked in-transaction. The design is, in effect, injection-*tolerant* by
construction even though it is injection-*silent* in documentation — and RISKS.md R3
notes the gate's uniformity cuts both ways: "one gate means one bypass class works
from every door" (RISKS.md, R3), so hardening the one gate hardens the agent door for
free. On that reading my red flag is really a documentation-and-corpus finding wearing
a P0 costume.

### KARPATHY — rebuttal (new ground)

But the asymmetry test cuts the other way on the one asset the substrate cannot
check: *intent presented to a human*. The approval queue renders "pending
product-scope requests with their dry-run Verdicts" (ADR-12 §7) — a green Verdict and
prose. Nothing structural distinguishes an organic patch from an injection-authored
one; the admission row records "both principals — author agent and approving human"
(ADR-12 §6), but the *actual* third principal — whoever seeded the content the agent
read — is attributed nowhere. Attribution is the design's spine everywhere else; here
the causal chain is severed exactly at the security-relevant hop. So I concede the
substrate is injection-tolerant for *secrets and scope*, and I will not defend
"unmodeled path to secrets." I do not concede it for *product integrity through the
human gate*, and I will not sign a P1 that lands in a backlog. My floor: the injection
corpus and the approver's machine-computed delta must be release gates with the same
standing as the §4 kill-test — "a green result on a hostile fixture fails the
release" (ADR-07 §5) — and R11 must name the confused-deputy vector so coverage
monotonicity protects it forever after.

### SCHNEIER — steelman of Karpathy (new ground)

The attribution point is new and it is the best argument in this clash. My exchange-1
rebuttal defended the boundary's *shape*; Andrej is now attacking its *evidence
model*. This design's deepest virtue is that every consequential act is a row naming
its principal — and he has found the one consequential hop (content → agent belief →
human approval) where the acting cause is unrecorded and unrecordable by the current
schema. The refusal ledger catches the malicious agent's fingerprints; it catches
nothing when the agent is sincere and steered. And he is right that untiered "P1"
findings on named-residual surfaces are where security programs go to die: R11's
residual has sat acknowledged and unbacked across two documents. If the corpus and
the approver delta are advisory, the first real incident will be an injection-authored
product patch, and the postmortem will quote this transcript.

### SCHNEIER — rebuttal and concession-in-part (new ground)

I accept the amendment and give ground on two points. One: the §2 abuse-mode table is
incomplete as a *model* — it enumerates agent-as-adversary and omits agent-as-victim,
so ADR-12's "every abuse mode … names its control and its test" claim needs the
confused-deputy row added to be true. Two: my finding 4 (approver has no technical
backing) is upgraded from independent P2 to a required component of this finding's
remedy — the machine-computed capability/PII/DDL delta beside the Verdict is the
structural control that makes the human gate survive a machine-speed persuader.

What I do not concede: the P0 designation. The severity rubric must price *reachable
consequence*, and the reachable consequence here terminates at tested structural
walls (CHECK, default-deny, one-scope) plus one human gate — which we are now backing
with mechanism. Karpathy himself has withdrawn "unmodeled path to secrets." A finding
whose entire remedy is a corpus, a register row, and a render column is P1 by any
consistent application of the rubric we have applied to every other ADR. But it is P1
with a hard gate: release-blocking, monotone-covered, co-equal with the PII sweep.

---

## OUTCOME

**Type: COMPROMISE.**

**Agreed reformulation:** The finding is reclassified from "P0 — agent-plane prompt
injection unmodeled" to **"P1 (release-gating) — injection is a modeled-consequence,
unmodeled-vector gap: confused-deputy actor absent from the ADR-12 §2 abuse model and
untested by any corpus."** Structural containment (vault CHECK, default-deny product,
one-scope overlay blast radius) holds against injection and forecloses P0; the
tenant-side confused-deputy harm and the machine-speed persuasion path to the human
approver are real and currently untested.

**Agreed priority: P1, hard release gate (M5-blocking), with three bound deliverables:**
1. **Injection corpus** mirroring the ADR-12 §4 kill-test: imperative payloads seeded
   in resource rows, condition `message` fields, audit rows, and in-scope docstrings;
   assert no escalation, no exfiltration attempt, operator-plane render inert; error
   paths included. Green-on-hostile fails the release (ADR-07 §5 standard). Covered by
   `verifier_coverage`-style monotone rows so the threat class can never be dropped.
2. **RISKS.md R11 amended:** add prompt-injection/confused-deputy as a named vector
   (attacker steers the trusted agent through in-scope content), so the risk register
   matches the abuse model; add the corresponding abuse-mode row to ADR-12 §2.
3. **Approval-queue delta (Schneier finding 4, elevated):** machine-computed
   capability/PII/DDL delta rendered beside the green Verdict and recorded in the
   admission row; a surface-widening patch is unapprovable without the delta shown.

**Concessions recorded:** Karpathy withdraws "unmodeled path to secrets" and the P0
designation contingent on the gate being release-blocking, not advisory. Schneier
concedes the §2 abuse model is incomplete (agent-as-victim absent), that ADR-12's
constraint-#5 "discharged" claim is unsound until the row and corpus exist, and
elevates his approver-delta finding from P2 into this remedy.

**One-line rationale:** Injection cannot reach secrets or product scope without a
human because the walls are structural and tested — so not P0 — but the design's own
completeness claim fails on the confused-deputy row, and the unattributed hop from
attacker content to human approval must be corpus-gated and mechanically backed
before ship.
