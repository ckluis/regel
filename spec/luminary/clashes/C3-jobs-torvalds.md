# CLASH C3 — Jobs vs. Torvalds: v1 product-surface expansion vs. closed-roster discipline

Mandated by the Phase 3.5 convergence audit (8/12 identical CONCERNS verdicts — failure
signal). Conductor enforces protocol only; positions move on evidence or steelman-grade
argument. All new claims cite ADRs (quote ≤20 words + location).

**Disputed findings (Jobs 1–3):**
- F1 [P1] First-class chart family in the v1 vocabulary before the reference app ships.
- F2 [P1] Optimistic local echo for the two highest-frequency interactions in v1.
- F3 [P2] `multiselect` as a first-class field type; tenant never authors `relation` for a tag field.

## Positions

**JOBS (opening):** The customer never feels the six-verifier proof. They feel the
dashboard with no chart, the click that waits on a WAN round trip, and the tag field
they're told to model as a foreign key. The design's own remedy for latency is
"Measured WAN latency complaints on the reference envelope" (ARCHITECTURE.md §6) — you
shipped the felt-slow UI and made the customer file the ticket. Charts, echo,
multiselect enter v1. Priorities stand: P1, P1, P2.

**TORVALDS (opening):** The vocabulary is "a closed framework proven total against
exactly one app" (torvalds.md F4). "The set is closed because derivation must be total"
(ADR-10 §7) — and that proof is fit to a single CRM. Expanding the roster before a
second, structurally different product tests closure is textbook premature
generalization. Who calls a chart primitive today? One dashboard in one demo app. Hold
the roster; defer all three. F1→P3, F2→P3, F3→P3.

## Exchange 1

### JOBS — steelman of Torvalds

Let me state his case better than he did. This system's totality proof is real: "each
of the 13 field types maps to exactly one input primitive and one render primitive"
(ADR-10 §7) — that's why `form()` can never meet a field it can't render, why V2 can
prove masking at six leaves, why derivation is a property and not a review. Every
addition to that closed set widens the proof obligation. And the additions are not
free later-fixable experiments: every std definition is "an immortal product-scope
row" (ADR-10, Context), and "Every std behavior change is a new hash in a new
manifest (ADR-08), never a mutation" (ADR-10, Consequences). A chart API designed at
N=1 — fit to one CRM dashboard's needs — gets carried forever, wrong axes and all.
"Rule of Three governs (a battery ships when the third product needs it" (ADR-10 §3)
is not laziness; it is the only known defense against designing an API for a customer
you haven't met. His own F4 concedes the deferral rationale is thin at N=1 — which is
precisely why he wants product #2 to *test* closure before anyone touches the roster.
That is a disciplined, coherent position, and the exclusion table even pre-agrees with
me that the gap is real: "a closed chart vocabulary is its own project; dashboards use
stat tiles + tables" (ADR-10 §7). He isn't denying the ceiling exists; he's saying
don't pour concrete at N=1.

### JOBS — rebuttal

Three findings, three different structures — he's treating them as one.

**F2 is not vocabulary.** Optimistic echo touches zero primitives, zero field types,
zero masking leaves. The ADR itself says deferred items are "additive behind the same
slot/patch contract" (ADR-11, Consequences). His closure argument has no jurisdiction
here. What blocks echo is "an untested client state machine and a client-trust
boundary" (ADR-11, Alternatives) — an engineering-risk claim. Fine. But then the
trigger is *complaints*: reactive, unmeasured, customer-pays-first. Linus, your own F2
says the milestone discipline fails because "staging is process, not mechanism"
(RISKS.md R5 Residual, via your audit). A latency policy of "wait for tickets" is the
same disease: process, not mechanism. If v1 must feel instant, make *feel* a machine
gate — a WAN-throttled clickthrough with a hard budget in the M4/M6 release suite,
red until it passes. I'll trade "build echo now" for that gate, because the gate
forces the fix the moment it's actually needed, before a customer ever files.

**F3 is authoring surface, not roster.** I never needed a 14th semantic type — I need
the tenant to never type `relation` for tags. "`multiselect` (model as a `relation`)"
(ADR-10 §5) is the right *derivation* and the wrong *doorknob*. Ship it as
framework-authored sugar in `resource(...)` that expands to the relation + a
select-multi control: no new mask bundle, no new totality row, reuses `relation`'s
proof entirely. The reference app is the acceptance harness — "nothing ships that it
and its red paths do not exercise" (ARCHITECTURE.md §5) — so put a tag field on Deal
and let the harness exercise it.

**F1 I hold for now:** the reference dashboard is the demo. If a stranger looks at
stat tiles and says "unfinished," your acceptance harness passed while the product
failed.

### TORVALDS — steelman of Jobs

His case, honestly: the reference product is not a fixture, it's the *entire*
empirical basis of this architecture — "nothing ships that it and its red paths do
not exercise" (ARCHITECTURE.md §5). So if the reference app is embarrassing, the
proof proved the wrong thing. A dashboard defined as "grid of stat tiles + tables
over typed `std/sql` queries" (ADR-10 §7, tier-2) will read as a 2004 admin panel;
every click blocking on a round trip — "Optimistic local echo is not in v1"
(ADR-11 §3) — will read as lag the instant anyone demos over hotel wifi; and
telling a tenant to model tags as a foreign key is the framework's internals leaking
out of the abstraction, which is exactly the kind of leak I'd flag if it were a
syscall interface. His sharpest point is the trigger design: "Measured WAN latency
complaints" (ARCHITECTURE.md §6) makes the customer the instrument. I spent my whole
audit demanding machine gates over promises; a complaint-driven trigger is the
opposite of a machine gate. And he's right that none of his three asks is a P0
*because* the epoch mechanism makes additions cheap — he's arguing taste debt
compounds, not that the system breaks.

### TORVALDS — rebuttal

Taste debt compounds. So does API surface — and only one of them can be paid down.
He conceded the killing fact himself in his own red-flag note: "every ceiling here is
additive-later by the design's own epoch/deferral mechanism" (jobs.md, RED FLAG). The
asymmetry decides this: a *deferral* is reversible by construction — "Every exclusion
is deletable mask/validate surface" (ADR-10 §5) — while an *addition* is immortal
rows and permanent semantics. Deferring a chart costs one unimpressive demo quarter.
Shipping a wrong chart API costs every year the kernel lives.

**F1:** a chart family is not one primitive, it's scales, axes, legends, theming,
responsiveness — "a closed chart vocabulary is its own project" (ADR-10 §7) is not
an excuse, it's a scope estimate, and the exclusion table already sets the honest
bar: "Epoch addition only, and only where the composition answer (tier-1 assembly)
is measured insufficient" (ARCHITECTURE.md §6). Designed against one CRM it will be
the wrong API, and I'll maintain it at r40. No. But I'll give him this: my F4 remedy
— a second, structurally different reference product to test closure — should be
chosen *chart-hungry* (analytics-shaped), so the closure test attacks exactly the
gap he found instead of a softball second CRM.

**F2:** he moved to my ground, and on my ground he's right — I reject
complaint-driven triggers for the same reason I reject promise-driven milestone
gates. A WAN-throttled felt-latency budget as a required release gate is a machine
gate; I accept it. What I do not accept is pre-committing the *remedy*: the client
has five duties and "(e) nothing else" (ADR-11 §3), and echo "requires a tested
client state machine first" (ARCHITECTURE.md §6). If the gate fails, build whatever
passes it — echo included. The gate is the finding; the feature is contingent.

**F3:** sugar that lowers to `relation` adds no roster row, no mask bundle, no new
totality pair — that is not the generalization I police, and it has a live consumer
if the reference app grows a tag field. One condition: it must be verifier-visible
as pure expansion (the derived output byte-identical to the hand-written relation
form), so it can never fork semantics. Then it's fine. Conditional yes.

## Exchange 2

### TORVALDS — steelman of Jobs (remaining delta: F1's priority)

What's left between us is only F1's number. His best remaining argument: P3 means
"nice to have, nobody schedules it." But the chart gap is *load-bearing for my own
remedy* — if the closure test (product #2) is what validates the roster, and the
known gap is charts, then a P3 lets the team pick a chart-free second product and
declare closure vacuously proven. His P1 was never really "build charts"; it was
"don't let the system grade its own homework on the one gap we already know about."
That deserves better than P3.

### TORVALDS — rebuttal

So encode it, don't inflate it. The finding becomes: product #2 MUST be
analytics-shaped, and the roster may not be declared closed until tier-1 composition
is "measured insufficient" (ARCHITECTURE.md §6) or a chart epoch-addition is specced
from *two* products' requirements. That's a P3 with teeth — a named precondition on
my F4 remedy — not a P1 build item in v1. v1 scope holds.

### JOBS — steelman of Torvalds (remaining delta: F1's priority)

His position, at its best: priority means "what blocks v1," and nothing about charts
blocks v1 — the epoch door is open, the deferral is deletable, and the one thing that
would make a v1 chart *worse* than no chart is designing it against a single app. He
is not telling me the customer is wrong to want charts; he is telling me the fastest
route to a chart API that isn't garbage in year five runs through product #2. Given
"immortal product-scope row" (ADR-10, Context), rushing the API is the one move that
can't be taken back — and I'm the one who wrote that reversibility was why none of
this was a P0.

### JOBS — rebuttal (final)

Accepted, with two riders I won't drop. First: the stranger test — "does this look
finished?" on the reference dashboard — joins the M6 acceptance pass. Stat tiles and
tables can be made beautiful; make them beautiful, and let a human say so before v1
ships. Second: the chart-hungry product #2 is a commitment recorded here, not a hope.
With those riders, F1 rides at P3. F2 stays P1 as the felt-latency machine gate. F3
stays P2 as verified sugar, gated on the reference app exercising a tag field.

## OUTCOME

**COMPROMISE** — reached in exchange 2; no repetition; both sides moved on
steelman-grade argument.

**Pivotal argument:** the reversibility asymmetry — deferrals are "deletable
mask/validate surface" (ADR-10 §5) while additions are "immortal product-scope
row[s]" (ADR-10, Context) — decided the vocabulary questions for Torvalds; the
"staging is process, not mechanism" principle from Torvalds' own audit, turned
against the complaint-driven latency trigger, decided the felt-speed question for
Jobs.

**Final priorities:**

- **Finding 1 (charts): P1 → P3.** Charts stay out of the v1 vocabulary. Rationale:
  a chart API designed at N=1 becomes immortal kernel surface; the gap is instead
  encoded as two binding preconditions — product #2 (Torvalds F4 remedy) must be
  analytics-shaped so closure is tested against the known gap, and the reference
  dashboard must pass a "does this look finished?" stranger review in the M6
  acceptance pass.
- **Finding 2 (optimistic echo): P1 (held), reformulated.** v1 does not pre-build
  echo; instead the passive trigger ("measured WAN latency complaints") is replaced
  by a machine gate: a WAN-throttled reference-app clickthrough with a hard
  felt-latency budget joins the M4→v1 release suite, red until green. Rationale: the
  customer must never be the latency instrument; if the budget fails, the remedy
  (echo behind the tested client state machine, or better) blocks release.
- **Finding 3 (multiselect): P2 (held), reformulated.** No 14th semantic field type;
  ships as framework-authored `resource(...)` sugar that expands to `relation` +
  select-multi, verifier-checked byte-identical to the hand-written form, entering v1
  only if the reference app exercises a tag field (else it defers as the first
  epoch-addition candidate). Rationale: removes the tenant-visible abstraction leak
  without widening the totality/masking proof.

*Conductor's note: no escalation required; convergence-audit concern discharged for
C3 — the two verdicts now differ in substance, not only in label.*
