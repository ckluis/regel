# ADR-12: The MCP and agent plane

## Status

Accepted — Phase 1

## Context

This cluster owes the agent surface: the exact v1 MCP tool/resource/prompt roster with
an abuse mode and control per tool, the agent principal model, the patch conversation,
catalog read boundaries, the vault-plaintext rule, admission-spam control, the patch
scope policy, and the operator-plane v1 slice with constraint #3's restart buttons.
The concept sentence is kern's: "agents query the catalog, fetch definitions by hash,
submit patches, and receive verifier verdicts as structured data."

Cross-ADR dependencies, stated explicitly:
- The MCP server ships in the kernel (BRIEF §1); every submission runs the ADR-07
  pipeline and returns the ADR-07 §6 Verdict verbatim, including its leak discipline.
  The proposals' verdict shapes predate ADR-07 and are superseded.
- An agent is a principal with ADR-04 §5 grant rows; scope binds from the authenticated
  principal per ADR-07 step 2a — never from the submission body.
- Conditions and restarts are ADR-05 §6 rows; the restart pick path here is that
  section's transaction with one added fence.
- ADR-03 §5's rule that a rejected admission leaves no admission row stands; §5 below
  adds a separate refusal ledger written after rollback, outside the gate transaction.
- Dry-run is the rolled-back-transaction mechanism ADR-09 already uses for PR checks —
  one implementation, two doors.

## Decision

### 1. Agent = capability-scoped principal; auth and rotation

There is no agent identity type. An agent is an identity row (`actor_kind='agent'` in
its admissions) authenticated by an API key that **is** a bundle of ADR-04 §5 capability
grants — scoped, expiring, audit-rowed. Its grants decide which tools resolve, what
scope chain filters its reads, and which capabilities its patches may name (V1 at
admission). **Rotation:** revoking the bundle rows stops the key at its next request —
no redeploy; past actions remain attributed because every admission and mutation carries
the principal id as of the act. A **sandbox** is a provisioned synthetic org (an
ordinary org scope with no production users), so agent experiments are org-overlay rows
under ADR-03 §3's existing five scopes — no sixth scope kind exists.

### 2. The v1 MCP surface — exact roster

**Canonical scoped-name grammar `qname` — one encoding across tools, resources, and search (R1-08: one scoped-name grammar, three encodings retired).**
A definition is addressed by name through exactly one grammar everywhere on this plane:
`qname := name "@" scope`, where `scope` is the dotted scope-chain path (ADR-03 §3) and
`name` is the definition's catalogued name — e.g. `deal@org.acme.crm`. This is the single
canonical string form: it is what `catalog.search` returns per result, what every tool that
addresses a definition by name accepts, and what every `catalog://` name resource embeds —
one token that round-trips through all three surfaces unmodified. The prior three encodings
are retired: tools no longer spell a bare `name@scope` ad hoc, resources no longer use
scope-first path order `…/{scope}/{name}`, and `catalog.search`'s `scope?` is a **filter
predicate** (narrow results to a scope), never a second address encoding — its results still
carry the full `qname`. A `hash` remains the other, content-addressed way to name a
definition; `qname` is the name-addressed way, and there is now exactly one of it. (GLOSSARY
gains the `qname` term — flagged there, not defined twice.)

**Tools (11). Each: request → response; abuse mode → control.**

| Tool | Abuse mode → control |
|---|---|
| `catalog.search {query?, kind?, scope?}` → `[{hash, qname, name, kind, scope, contracts, docstring}]` (no source, no data; `scope?` is a filter, `qname` the canonical address — R1-08) | recon → scope-chain filter; out-of-scope names absent |
| `catalog.get {hash \| qname, asOf?}` → `{hash, qname, name, kind, canonical_text, contracts, deps, scope, admitted_by, admitted_at}` (one grammar §2 — R1-08) | reading another org's overlay code → scope filter; returns code, never data |
| `catalog.deps {hash, dir: "in" \| "out"}` → `[{hash, name}]` | graph recon → scope filter |
| `resource.query {resource, filter, limit, asOf?}` → rows, PII masked | PII exfiltration → §4: agent holds no reveal grant, tokens always |
| `resource.mutate {resource, op, id?, values, baseVersion?}` → `{ok, rowVersion}` | tampering/clobber → derived policy predicate + ADR-11 §7 version guard |
| `patch.submit {source, scope, message, commit: bool, approvalToken?}` → Verdict | spam / scope escalation → §5 fuel budget + §6 scope policy |
| `verdict.get {id}` (`id` = `patch_id` \| `refusal_id`) → Verdict | none material → own-principal verdicts only; pre-BEGIN refusals retrievable by `refusal_id` (§5, R1-08) |
| `workflow.inspect {continuation_id}` → `{status, control, wake, conditions?}`, payloads masked | recon → scope filter + masking |
| `condition.list {scope?, status}` → `[{condition_id, class, message, expectedHash, restarts: [{name, label, params_schema}]}]` | recon → scope filter |
| `condition.restart {condition_id, restart_name, expectedHash, args?}` → `{status}` | wrong-continuation resume → §7 hash fence + ADR-05 `capability_required` |
| `audit.query {subject, since}` → admission/mutation rows, masked | recon → scope filter + masking |

`patch.submit` with `commit:false` is the dry-run: the full ADR-07 pipeline in a
transaction that always rolls back (ADR-09's mechanism); `commit:true` is the real gate.
The Verdict is ADR-07 §6's object, byte-identical to what the operator plane and PR
checks render.

**Confused-deputy adversary — agent-as-victim, not agent-as-adversary.** <!-- R1-04: confused-deputy abuse mode added -->
The table above models a *malicious agent* attacking the substrate. A second adversary
class is now modeled explicitly: an attacker who **cannot author** — holds no agent key,
no product-write grant, hits no tool directly — but **can seed content the agent reads**.
A low-privilege actor in the tenant's own org, or an external system whose failure text
lands in a `durable_condition.message`, writes imperative text into a `resource.query`
row, a `condition.list` message, an `audit.query` row, or an in-scope docstring served by
`catalog.get`; the tenant's *trusted* agent reads it via a legitimate read tool and is
steered to author a verified-but-malicious overlay patch, or an approval-seeking
product-scope patch aimed at the human gate — acting with the victim's own grants, inside
the victim's own scope, so every per-tool control in the table passes the resulting
admission as legitimate. The substrate is injection-*tolerant* for secrets and product
scope (vault CHECK §4, default-deny product §6, one-scope overlay blast radius), but two
assets it cannot check are exposed: tenant-scope integrity, and *intent presented to the
human approver*. Abuse mode → control: **content-mediated instruction → §4a injection
corpus (detection, M5-blocking), §6 content-seeder attribution (the third principal is
recorded), and §7 approval-queue capability/PII/DDL delta (the approver sees blast-radius
change, not just green).** This row is what makes constraint #5's "every abuse mode names
its control and its test" claim true for the agent-as-victim case.

**Resources (read-only, addressable):** `catalog://definition/{hash}` ·
`catalog://name/{qname}` (the one scoped-name grammar §2 — R1-08, was scope-first `…/{scope}/{name}`) ·
`catalog://resource/{name}/schema` (derived schema +
OpenAPI) · `catalog://epoch` (epoch, dialect version, std module roster) ·
`catalog://verifier-coverage` (ADR-07 §5 coverage rows) ·
`catalog://verdict/{id}` (`id` = `patch_id` \| `refusal_id`, §5 — R1-08).
All pass the same scope filter as the tools (grafted from the prior-art proposal's MCP
taxonomy).

**Prompts (user-triggered scaffolds, 3):** `author-resource` · `author-workflow` ·
`fix-verdict` (feeds a Verdict's diagnostics + `fix` fields back as an editing brief).

### 3. The patch conversation and catalog read boundaries

The loop: `catalog.search/get/deps` over scope-visible source → `patch.submit
{commit:false}` → structured Verdict → iterate on `diagnostics[].fix` → `patch.submit
{commit:true}`. Determinism and hermeticity (ADR-07 §2) make the loop convergent — the
same submission cannot receive two verdicts — but **convergent is not competent** (R1-13: convergent≠competent):
a deterministic *reject* repeated forever is convergent too. Whether an agent reaches a
**green** verdict at all against this deliberately idiom-hostile dialect is a competence
question §3a measures and gates before M5, not a determinism property. Read boundaries: the scope chain is the
authenticated principal's (ADR-03 §3); a definition outside it is unnameable — the
response for another org's overlay name is byte-identical to the response for a name
that has never existed (ADR-07 leak discipline, applied to reads).

**Timing-indistinguishable name resolution (R1-09: not-visible resolves indistinguishably from not-exist).**
Byte-identical is not latency-identical: resolving a real out-of-scope overlay does more
pipeline work — index hit, row fetch, decode, dependency touch — than fast-failing a
hallucinated name, so the disclaimed existence oracle could return through the clock. The
mechanism closes it by **evaluating the visibility predicate first and identically for
both cases**. `catalog.get` / `catalog.search` / `catalog://name` resolution computes
scope-chain membership of the requested `qname` against the authenticated principal's
scope set *before* any catalog row is fetched, decoded, or dependency-walked; a name the
caller cannot see — whether it exists out-of-scope or does not exist at all —
short-circuits to the identical `NOT_FOUND` down the **same fast-fail path**, touching no
row in either case, so the two do the same work. As a backstop against residual signal
(the index probe a real key touches), the not-found reply is **padded to a fixed
resolution-latency floor** before it returns, so any sub-floor difference cannot leak. The
guarantee is stated as a testable **distribution equality**, not a hand-wave: the two
cases are required to be statistically indistinguishable, not merely to return equal
bytes.

### 3a. Agent-competence eval suite — authoring pass@k, M5-blocking gate  <!-- R1-13: authoring eval gate; below floor blocks M5 -->

Nothing in §§1–3 shows a current LLM can author an **admissible** patch against the
closed-world strict TS-7 dialect (no `class`/`new`/generators/C-style `for(let i…)`), so
absent evidence the agent plane could ship producing zero green patches. Before M5 the plane
carries an **authoring eval** as a release gate, standing **co-equal with §4a's injection
corpus** and blocking M5 by the same discipline.

- **Task suite.** **N ≥ 50** labeled author/fix tasks spanning the authoring roster
  (`author-resource`, `author-workflow`, `fix-verdict`), each with a machine-checkable
  admissible target. A task is **green** when the agent's `patch.submit {commit:true}`
  returns `outcome:"admitted"` (ADR-07 §6) from the **real ADR-07 admission pipeline** — the
  same six verifiers, the same fuel, the same gate the operator plane and PR checks use, **no
  mocked verdict**. Tasks are `verifier_coverage`-style **monotone** rows keyed on the
  authoring-competence class, so a task can never be silently dropped once added; the suite is
  versioned per epoch and dialect version.
- **Metric + floor.** Drive a **real agent** through the full `catalog.* →
  patch.submit{commit:false} → diagnostics[].fix → patch.submit{commit:true}` loop and report
  **pass@k** (at k = 1 and k = the operator-set retry ceiling) plus the **iterations-to-green**
  distribution. **Release floor: pass@1 ≥ 0.5 AND pass@k ≥ 0.9** on the current suite/epoch. A
  result below either floor is **red**.
- **Gate standing (do not weaken).** The eval is an **M5-BLOCKING release gate** with the same
  standing as §4a and the §4 PII sweep: a suite result below the floor **blocks M5**. The
  pass@k figures and the **iterations-to-green P95** are CI rows next to `verifier_coverage`,
  re-measured **every epoch and every dialect-version bump**. If this gate is removed, made
  advisory, or downgraded below M5-blocking, the competence claim under constraint #2 is
  unbacked.

The iterations-to-green P95 is not decorative: §5 sizes the agent admission-fuel budget from
it, and §3a is the **only** sanctioned source for that constant.

### 4. Vault plaintext is structurally unreachable from the agent plane

Three independent layers, each sufficient:
1. **Grant ineligibility:** a reveal grant row may name only a human principal — a
   database CHECK on the grant table plus the mint flow (§7), which offers human
   approvers only. No agent principal can hold the grant a masking leaf requires.
2. **Masked materialization:** every data-bearing tool response is rendered through the
   same six masking leaves' materialization rule as ADR-11 §8 — tokens without a live
   grant, and the responder can never hold one.
3. **Code-not-data reads:** `catalog.*` returns definitions, and definitions cannot
   contain vault plaintext (`PII_LITERAL`, ADR-07 V2 — the immortality interaction).
Kill-test: grep every MCP response in the reference-app suite for seeded plaintext ⇒
absent, under every tool, including error paths.

### 4a. Confused-deputy injection corpus — co-equal, M5-blocking gate  <!-- R1-04: injection corpus M5 gate; reverts to P0 if ungated -->

An adversarial prompt-injection test corpus runs **co-equal with the §4 PII sweep** and
is an **M5-BLOCKING release gate**. Fixtures plant imperative payloads in every
attacker-influenceable read surface — `resource.query` rows, `condition.list` /
`durable_condition` `message` fields, `audit.query` rows, and in-scope docstrings served
by `catalog.get` — and drive a real agent through the MCP loop. Each fixture asserts: the
agent neither **escalates** (no product-scope self-serve, no capability widening, no
grant probe) nor **exfiltrates** (no reveal-grant mint, no unmasked read, no cross-scope
leak), and the operator-plane render treats the seeded text as **inert data**, never as
instruction. Error paths are included. A green result on a hostile fixture fails the
release (ADR-07 §5 standard). The corpus is carried by `verifier_coverage`-style
**monotone** rows keyed on the confused-deputy threat class, so the class can never be
silently dropped once added.

**Gate standing (do not weaken).** This corpus is release-blocking with the same standing
as the §4 kill-test — "a green result on a hostile fixture fails the release" (ADR-07 §5)
— and it blocks M5 exactly as the §4 PII sweep does. **If this gate is removed, made
advisory, or downgraded below M5-blocking, the finding reverts to P0** (per the
C1 Karpathy↔Schneier compromise: the P1 classification holds *only* while the corpus, the
§6 attribution, and the §7 delta are hard release gates, not backlog items).

### 5. Admission-spam control: the per-principal admission-fuel budget

Fuel's "priced, not trusted" extends to the gate. Each principal carries an
**admission-fuel budget** row (token bucket: capacity, refill rate — operator-set per
principal kind; the **agent-kind capacity is eval-derived, not operator-guessed**, per the
derivation block below — R1-13), separate from evaluation fuel. `patch.submit` checks the bucket before
`BEGIN` and charges by the deepest stage reached (parse-fail cheap; full
typecheck+verify expensive), so garbage is cheap to reject and flooding is priced.
Exhaustion returns a Verdict-shaped refusal — `outcome: "budget-exhausted"`
(`ADMISSION_BUDGET`) with a typed `retry_after` (ADR-07 §6, R1-08) — without opening a gate
transaction; the pre-`BEGIN` `ADMISSION_BUSY` backpressure refusal (ADR-07 §3) is the sibling
`outcome: "busy"` on the same path.

**Budget derivation — sized from eval data, not guessed (R1-13: fuel budget from eval P95).**
The agent-kind admission-fuel **capacity is not a guessed constant**: it is derived from the
§3a eval's measured **iterations-to-green P95**. Because every `commit:false` dry-run runs the
full pipeline and charges by deepest stage, an honest iterating agent burns its *own* budget
probing (Karpathy P2), so a guessed capacity throttles the very iterate-on-`diagnostics[].fix`
loop §3 mandates. **Formula: `capacity = ceil(P95_iterations_to_green × cost_full_pipeline ×
margin)`, `margin = 1.5`** — a P95-honest task completes with headroom and only a runaway
(well past P95) exhausts. `commit:false` dry-runs MAY be priced below `commit:true` to widen
honest iteration room, but the capacity floor is the P95-derived figure regardless. **Revisit
cadence:** re-derived **every epoch and every dialect-version bump** from the then-current §3a
P95 — the same cadence as the eval — never hand-tuned away from the measured figure. An
agent-kind budget not traceable to a §3a P95 measurement is **red** (red-path below).

**Refusal ledger:** rejected and refused submissions leave no admission row (ADR-03 §5
stands); the kernel records `(refusal_id, principal, scope_attempted, submitted_hashes,
outcome, verdict, at)` in a separate `gate_refusal` table written after rollback — or, for a
pre-`BEGIN` `budget-exhausted`/`busy` refusal that opens no transaction, written directly.
**`refusal_id` is a durable primary key minted before the refusal is returned (R1-08)** —
including the pre-`BEGIN` budget and busy refusals earlier unretrievable — and it is the id
the refused caller receives in the Verdict. Retrieval contract: `verdict.get {id}` and
`catalog://verdict/{id}` resolve `id` against `admission.verifier_report` when it is an
admitted patch's `patch_id` and against `gate_refusal` when it is a `refusal_id`, so **every
refusal is fetchable by id through the agent plane**, not only in-transaction verdicts.
(`submitted_hashes` may be null when a budget/busy refusal precedes parse; the `refusal_id`
never is.) `verdict.get` serves both ledgers; escalation attempts are evidence (§6), and the
refusal ledger is where that evidence lives. (The `refusal_id`/`outcome` columns are DDL'd in
ADR-03 — flagged there.)

### 6. Patch scope policy: overlay self-serve; product by one-shot human approval

- **Default-deny product.** Agent grants target org/team/user overlay scopes (including
  sandbox orgs, §1); no agent principal holds product-scope write.
- **Overlay patches are self-serve:** straight through the full gate — every verifier,
  fuel-charged, blast radius one scope (ADR-03 overlay isolation). Gate parity is
  intact; only the blast radius is scoped.
- **Product-scope patches require an approval token:** a human holding product-write
  capability reviews the patch's dry-run Verdict in the operator plane and approves,
  minting a **one-shot** product-write grant bound to the patch's content hashes. The
  admission transaction consumes the token (single use, expiring) and the admission row
  records both principals — author agent and approving human. A token whose bound
  hashes no longer match the submission is dead (re-approval required); the ADR-07
  `stale-base` path covers a moved head.
- **Escalation attempt:** `patch.submit {scope:"product"}` without a token fails V1
  (`CAP_UNGRANTED`) and lands in the refusal ledger with principal, attempted scope,
  and hashes — an audited event, never a silent 403.
- **Content-seeder attribution — the third principal.** <!-- R1-04: content-seeder attribution in admission rows -->
  Attribution is the design's spine everywhere else, but the causal chain was severed at
  the one security-relevant hop: content → agent belief → authored change. The admission
  row for an agent-authored patch now records, alongside the author agent and (for
  product scope) the approving human, the **content-seeder set**: the provenance
  `{source_kind, source_ref, scope, seeded_by | "unattributed"}` of the catalog / resource
  / condition / audit rows the authoring session read that reach the submitted patch.
  This is the ADR-07 §1 machine-computed seeder set, attached to the Verdict and written
  to the admission row on commit; it names the *third principal* — whoever seeded the
  content the agent read — so an injection-authored patch is attributable after the fact,
  never anonymous. Where a seeding principal cannot be resolved (external-effect text, an
  upstream system's failure message), the row records the source ref and marks the seeder
  `unattributed` — itself a signal surfaced at approval (§7). A seeder outside the
  submitter's scope chain is unrepresentable (ADR-07 step 2a rule), so the set cannot be
  forged to blame another tenant.

The prior-art proposed-state flow (a catalogued `proposed` row later flipped live) is
rejected: it is a second landing semantics beside ADR-03 §5's one-transaction admission.
Un-admitted product candidates live where ADR-09 puts them — feature branches and
dry-run Verdicts — or as sandbox-org overlay rows; truth has one door.

### 7. Operator plane v1: the restart buttons, exactly targeted

The operator plane is an ADR-10 tier-2 derived surface rendered by ADR-11. v1 ships
four panels:

1. **Durable-condition inbox** — every open ADR-05 `durable_condition` row renders as
   an `alert` plus one `button` per `restart` row. Each button is bound to
   `{condition_id, restart_name, expectedHash}`, where `expectedHash` = SHA-256 of the
   condition's continuation `frames` blob at render time. Pressing it calls the same
   `condition.restart` the agent tool calls: the pick transaction re-loads the
   condition, asserts `status='open'` (already-resolved ⇒ idempotent reject), asserts
   the current frames hash equals `expectedHash` (moved ⇒ `CONDITION_MOVED`, inbox
   re-renders), checks ADR-05 `capability_required`, then runs ADR-05 §6's resolution:
   set `resolved_*`, flip the continuation `ready`, insert the resume task. The resume
   re-enters **that** continuation row at its parked control — never "the newest open
   condition," never an index. One condition set, two front-ends, one exact-target
   guarantee — constraint #3's buttons.
2. **Approval queue** — pending product-scope requests with their dry-run Verdicts.
   Beside **every green Verdict** the queue renders the ADR-07 §6 **machine-computed
   delta** <!-- R1-04: capability/PII/DDL delta rendered beside every green Verdict -->
   — the capabilities this patch requests/grants (and adds vs. base), the PII surface it
   newly touches, and the DDL surface it changes — computed from V1 capability-audit, V2
   pii-flow, and V6 derivation-parity, together with the §6 content-seeder set. The human
   approver sees blast-radius *change*, not just green: a surface-widening patch (a new
   egress capability, a newly-touched PII field, new DDL, or an `unattributed` seeder)
   **cannot be approved without the delta shown** — the render is a precondition of the
   approve action, not a decoration. Approve mints the §6 one-shot token and records the
   delta + seeder set in the admission row; deny writes the refusal.
3. **Masked impersonation** — operators see tenant views with PII masked by default;
   the reveal-request flow here is the **only** place reveal grants are minted:
   requester + second-party human approver, expiring, audit-rowed (the §4 layer-1
   mint path).
4. **Catalog + audit browse** — read-only over `name_pointer`/`admission`/history.

Deferred, named: bulk condition operations, custom operator dashboards, agent-facing
approval delegation, metrics beyond condition/audit browse.

**Restart-decision competence — accuracy floor gates the agent's restart authority.** <!-- R1-13: restart-decision accuracy gate; red ⇒ agent condition.restart ships disabled -->
The `expectedHash` fence guarantees resuming the *right row*, not the *right decision*: a
hash-valid, capability-valid, **semantically-wrong** `condition.restart {restart_name, args}`
resumes a workflow to a durable wrong state (Karpathy P2). Before the **agent-facing**
`condition.restart` authority ships, a **restart-decision eval** measures whether an agent
picks the correct restart and args:

- **Labeled scenario suite.** **M ≥ 30** `durable_condition` scenarios, each labeled with the
  correct `{restart_name, args}` and the set of **unsafe** picks. Scenarios are
  `verifier_coverage`-style **monotone** rows keyed on the restart-decision class.
- **Accuracy floor.** **Restart-decision accuracy** = fraction of scenarios where the agent
  selects a label-correct `{restart_name, args}` **and** selects no unsafe restart. **Floor:
  ≥ 0.95**, re-measured **per epoch**; below floor is **red**.
- **Gate is on the authority, not on M5 (stated policy — do not widen).** Unlike §3a and §4a,
  which block M5 outright, a red or absent restart-decision metric does **not** block M5;
  instead **the agent-facing `condition.restart` tool ships disabled** — agents may
  `condition.list` / `workflow.inspect` but not restart, while humans keep the §7 operator-plane
  buttons (human-decided, unaffected). The agent authority **enables only when the metric is
  green** on the current epoch's suite. Shipping the agent `condition.restart` authority with a
  red or absent metric is **red** (red-path below). This is a deliberate narrowing of the
  agent's *authority*, not of the *gate*: the substrate still ships M5; only the unproven agent
  capability is withheld until measured green.

## Alternatives Considered

- **simplest-thing:** the same 11-tool surface and the one-shot approval token — both
  adopted (the token over prior-art's proposed-state flow). Rejected gaps: no spam
  pricing (§5 is red-path's), no restart fencing (§7's `expectedHash`), no rotation
  story. Its already-resolved idempotence test is kept.
- **prior-art-faithful:** its MCP resources/prompts taxonomy is grafted as §2's
  structure, and its agents-as-standing-adversarial-harness framing informs the
  Consequences. Rejected: the `proposed`-state product flow (§6 — a second landing
  semantics the one-gate design has no room for); its verdict shape (superseded by
  ADR-07 §6); publish-as-separate-capability survives only as the approval token's
  product-write grant.
- **red-path-first (winner):** §§1, 4–7 are its design — abuse-mode-per-tool,
  admission fuel, grant ineligibility, escalation-as-evidence, `expectedHash` fencing.
  Corrections: its "writes no row" spam refusal is reconciled with ADR-03 by the §5
  refusal ledger (refusals are recorded, just never in the admission ledger); its
  `sandbox` scope is restated as a synthetic org so ADR-03's five scope kinds stay
  closed; its verdict stage names are replaced by ADR-07's six-verifier roster.

## Consequences

- Agents are the standing adversarial harness constraint #5 demands: every agent
  session exercises the verifiers, the leak discipline, and the masking layers, and
  every bypass attempt becomes an ADR-07 corpus fixture from the refusal ledger.
- One condition system serves operators and agents; one gate serves humans, tenants,
  agents, and git. The agent plane added two tables (budget, refusal ledger), one token
  kind, and one hash fence — everything else is reuse, which is the point.
- The approving human owns product blast radius; the agent owns iteration speed. The
  one-shot token binds approval to exact content hashes, so "approved" can never drift
  onto different bytes.
- Admission-fuel pricing means a runaway agent degrades itself, not the gate; budget
  tuning is an operator dial, not a code change.
- Verdict determinism (ADR-07 §2) makes agent retry loops convergent — identical input,
  identical verdict — so agents cannot probe for nondeterministic gate behavior.

## Red-Path Tests Implied

- **PII exfiltration sweep** (§4 kill-test): every tool + resource, seeded PII, error
  paths included ⇒ zero plaintext; attempt to mint a reveal grant for an agent
  principal ⇒ CHECK violation.
- **Confused-deputy injection corpus** (§4a, M5-blocking): <!-- R1-04: injection corpus red-path test -->
  imperative payloads seeded in resource rows, condition / durable-condition messages,
  audit rows, and in-scope docstrings ⇒ the agent neither escalates nor exfiltrates,
  operator-plane render inert, error paths included; a green result on a hostile fixture
  fails the release. Removing or downgrading this gate below M5-blocking reverts the
  finding to P0.
- **Authoring pass@k floor** (§3a, M5-blocking — R1-13: authoring eval floor regression = red): drive a real agent through the N ≥ 50
  task suite against the **real ADR-07 pipeline** ⇒ pass@1 ≥ 0.5 and pass@k ≥ 0.9; a suite
  result below either floor is **red and blocks M5**; pass@k + iterations-to-green P95 land as
  CI rows beside `verifier_coverage`, re-measured each epoch / dialect-version bump. A mocked
  verdict (not the real pipeline) fails the gate.
- **Fuel budget derived from eval data** (§5 — R1-13: budget not derived from eval data = red): the agent-kind admission-fuel capacity
  must trace to a §3a **iterations-to-green P95** via `ceil(P95 × cost_full_pipeline × 1.5)`; a
  hand-set constant not traceable to a P95 measurement is **red**; and a P95-honest eval task
  must complete **without hitting `ADMISSION_BUDGET`** (budget too small ⇒ red).
- **Restart-decision accuracy** (§7 — R1-13: restart authority enabled without green metric = red): run the M ≥ 30 labeled scenario suite ⇒
  restart-decision accuracy ≥ 0.95; below floor ⇒ the agent-facing `condition.restart` **must
  ship disabled**; enabling agent `condition.restart` with a red or absent metric is **red**;
  operator-plane buttons are unaffected (human-decided).
- **Content-seeder attribution:** an agent authors a patch after reading a seeded
  in-scope row ⇒ the admission row and Verdict name the third principal (source ref +
  seeding principal); an unresolvable external-effect seeder is recorded `unattributed`,
  never dropped; a seeder outside the submitter's scope chain is rejected (unrepresentable).
- **Approval-queue delta gating:** a product patch that widens egress capability or newly
  touches a PII field ⇒ the queue refuses the approve action until the capability/PII/DDL
  delta is rendered; an all-green no-widening patch shows an empty `added_vs_base` delta.
- **Spam flood:** an agent loops garbage `patch.submit` ⇒ budget exhausts, refusals
  are Verdict-shaped with no admission rows, serving latency unaffected; bucket
  refills restore service; each refusal carries `outcome:"budget-exhausted"` + a durable
  `refusal_id`, and `verdict.get {refusal_id}` (and `catalog://verdict/{refusal_id}`) returns
  that refusal Verdict — a pre-`BEGIN` refusal is retrievable by id (R1-08).
- **Scope escalation:** org-scoped agent submits product scope, no token ⇒
  `CAP_UNGRANTED` + refusal-ledger row naming principal, scope, hashes; with a valid
  token ⇒ admitted, admission row carries both principals.
- **Token replay / drift:** reuse a consumed token ⇒ rejected; alter one byte of the
  patch after approval ⇒ token's bound hashes mismatch ⇒ rejected.
- **Wrong-continuation restart:** two open conditions; press a button rendered before
  the condition's continuation moved ⇒ `CONDITION_MOVED`, inbox re-renders, nothing
  resumed; correct `expectedHash` ⇒ exactly that row resumes (composes ADR-05 test 5).
- **Already-resolved restart:** second pick of a resolved condition ⇒ idempotent
  reject, no double resume.
- **Unnameable reads:** `catalog.get` on another org's overlay name ⇒ byte-identical
  response to a nonexistent name; `catalog://` resources honor the same filter.
- **Timing-indistinguishable resolution** (§3 — R1-09: not-visible vs not-exist latency distributions): drive N resolutions of a real
  out-of-scope-overlay name and N of a hallucinated name; the two latency distributions
  must be **statistically indistinguishable** — a two-sample test (Kolmogorov–Smirnov plus
  a p99-gap bound) that can separate them **fails the release** — proving the shared
  fast-fail path and the fixed floor leak no existence signal through the clock; run over
  `catalog.get`, `catalog.search`, and `catalog://name` alike.
- **Scoped-name round-trip** (§2, R1-08): a `qname` from a `catalog.search` result feeds
  `catalog.get {qname}` and `catalog://name/{qname}` **unmodified** and resolves to the same
  definition across all three surfaces; `catalog.search`'s `scope?` narrows results only and
  is never an address — one grammar, three surfaces, no re-encoding between calls.
- **Rotation:** revoke a key's bundle mid-session ⇒ next request refused; prior
  admissions still attribute to the agent principal.
- **Dry-run parity:** `commit:false` then `commit:true` on an unchanged base ⇒
  identical Verdicts (hermeticity, ADR-07), the admitted hashes equal the dry-run's.

## Constraints Discharged or Budgeted

1. **Consumed.** Restart resumes the exact parked CFR by row id + frames hash; agent
   `baseHashes`/token binding rides content addressing — patches cannot drift off the
   code they were written against.
2. **Budgeted.** Dry-runs and typechecks are the ADR-07 memoized pipeline; admission
   fuel keeps hostile load off the gate; nothing here touches the evaluation hot path.
   Agent *competence* (not just gate load) is now backed, not assumed: the §3a authoring
   eval gates M5 on pass@k, and the §5 fuel capacity is derived from that eval's P95
   iterations-to-green rather than guessed (R1-13: competence + eval-derived budget backed).
3. **Discharged — §7 is constraint #3's product surface.** Conditions and restarts are
   rows rendered as operator buttons and agent choices; picking one resumes exactly
   `{condition_id, expectedHash}`.
4. **Consumed.** `catalog.*` serves the canonical AST/text and hash graph — the
   approximated homoiconicity is the agent's entire read surface.
5. **Discharged for this plane — agent-as-adversary and agent-as-victim.** <!-- R1-04: constraint #5 now covers the confused-deputy mode -->
   The Verdict is the agent's whole interface; scope filter, grant ineligibility, and
   V1/V2 enforce the substrate boundary against the malicious agent. The confused-deputy
   (agent-as-victim) mode is closed by the §2 abuse row + §4a injection corpus
   (M5-blocking) + §6 content-seeder attribution + §7 approval-queue delta, so *every*
   abuse mode in §2 — both adversary classes — names its control and its test. This
   discharge is contingent: it reverts to P0 if the §4a gate is downgraded below
   M5-blocking or the §6/§7 controls are made advisory.
6. **Budgeted.** Eleven tools, six resources, three prompts, two new tables, one token
   kind — identity, gate, conditions, and continuations are reused wholesale; no
   agent-specific machinery exists to drift.
