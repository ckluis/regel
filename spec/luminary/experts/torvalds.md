# LINUS TORVALDS — Architecture & Maintainability

## VERDICT: CONCERNS

I came here to mock speculative seams and framework-for-a-slide abstraction. I mostly
can't. This design is unusually disciplined about *collapsing* layers instead of
breeding them: one gate for four doors, one `task` table for queue/cron/resume/deliver,
one continuation store shared by workflows + conditions + UI sessions (`eventSeq` IS
`step_seq`), one `Value` union shared by interpreter and codec. That is the opposite of
premature generalization, and I won't pretend otherwise. No P0, no red flag. But there
are real maintainability liabilities that outlive the demo, so: CONCERNS.

## FINDINGS

1. **[P3] Two reserved seams with zero callers today.** The AOT lane fixes an `AOTFn`
   signature and a `Value`-ABI constraint, and the self-hosting lane is specced — yet
   nothing dispatches to either in v1. Who calls this today? Nobody; keep the NULL column
   and the paragraph, but do not build one line of `Host`/`Meter` plumbing until a
   profiled hot function exists. CITE: "v1 ships with `aot_symbol` NULL everywhere." (ADR-04, §7)

2. **[P2] The deepest-bet ordering is enforced by nothing.** The entire safety story for
   R1–R4 is "prove the walking skeleton before features," but the control is a promise, not
   a mechanism — a skipped or quarantined gate lets surfaces land on unproven identity/CFR
   and forces a redesign of everything above. Make the milestone gate a machine gate (CI
   refuses M(n+1) branches until M(n) kill-tests are green), not a human's good intentions.
   CITE: "staging is process, not mechanism" (RISKS.md, R5 Residual)

3. **[P2] The kernel accretes old-epoch surface forever, with no removal path.** Append-only
   `r<n>` decoders and per-epoch semantics are immortal by design; at r40 nobody runs r1, yet
   the binary still carries and must maintain r1's reader and semantics. That is an unbounded,
   permanent maintenance tax nobody signed up to pay in year five — at minimum, budget and
   measure the carried surface as a tracked release metric. CITE: "semantics accretion is
   permanent kernel surface" (RISKS.md, R2 Residual)

4. **[P3] A closed framework proven total against exactly one app.** 25 components, 13 field
   types, 14 batteries, "derivation must be total" — all fit to a single reference CRM, while
   "Rule of Three" gets invoked for deferrals when there is no first, second, or third real
   product yet. The totality proof is real but N=1; treat the second real product as the
   actual test of the vocabulary's closure. CITE: "nothing ships that it and its red paths do
   not exercise." (ARCHITECTURE.md, §5)

## RECOMMENDATIONS

- AOT/self-hosting: keep `aot_symbol` (nullable) and the ABI note; add a test asserting no
  non-NULL `aot_symbol` and no self-hosting dispatch path compiles in v1. Verify: grep the
  binary's dispatch table for any AOT entry — must be empty.
- Milestone gating: encode each milestone's kill-test family as a required CI gate keyed to
  the milestone; a feature branch above M(n) fails to merge if M(n)'s gate is red. Verify:
  land a fake M2 feature branch with an M1 kill-test disabled — CI must reject it.
- Epoch surface: add a release metric = count of live `r<n>` readers + native dispatch
  entries carried; alarm on unbounded growth. Verify: the metric exists in the health surface
  and the golden-continuation corpus runs against every carried `r<n>`, not just the newest.
- Vocabulary closure: before shipping the roster as "closed," build a second, structurally
  different reference product (not another CRM) and re-run roster-totality. Verify: every
  field type × form/table/detail pair still emits a render for the second app with zero new
  primitives.

## RED FLAG

NONE. My red-flag trigger is an abstraction *layer* with no second user; the AOT and
self-hosting seams qualify as "no second user" but they are a NULL column and a documented
type, not a layer in any call path — no concrete harm, so it files as P3, not a blocker.
Declaring a red flag here would be theater, and zero is fine.
