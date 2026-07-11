# STEVE JOBS — Phase 6 targeted re-review (R1-07 felt-latency slice + R1-14 C3 riders)

## VERDICT: CONCERNS → discharged on my slice. The compromise stayed honest.

I co-drove C3 by turning Torvalds' own principle — "staging is process, not
mechanism" — against a latency policy of wait-for-complaints. The revised corpus
converted every one of those words into a machine. I got the gate I traded for.

---

## 1. Revision 7 — WAN felt-latency machine gate → **SATISFIED**

ADR-11 §9 is a real mechanism a shipping team cannot weasel out of. Named profile
`wan-150` (150 ms RTT, 1.6/0.768 Mbps), budgets input→echo ≤ 50 ms and
action→confirmed-commit ≤ 300 ms p95, gating M4→v1, wired in ARCHITECTURE §5/§5.1
M4 rows and mechanically blocked by the R1-11 ladder (a red M4 stops M5/M6 merges).
The trigger flipped: "This replaces the prior complaint-driven trigger… the customer
is never the latency instrument" (ADR-11 §9).

The design detail that makes it un-gameable is the 50 ms echo budget: "Under
`wan-150` a pure server round trip cannot meet input→echo ≤ 50 ms" (§9). One RTT is
150 ms — so the current felt-slow UI **cannot pass**. Shipping lag turns the release
red; the only green is echo (or better). "The feature is contingent; the gate is the
finding" — exactly my C3 concession. The numbers are ones I'd ship: 50 ms is the
sub-100 ms instant threshold, and 300 ms confirmed commit over a deliberately bad
link is honest — the user feels the 50 ms echo, the truth lands by 300 ms invisibly.

## 2. Revision 14 — C3 riders → **SATISFIED**

- **Product #2 analytics-shaped — binding.** "the roster may not be declared closed
  until tier-1 composition… is *measured* insufficient against a real analytics
  product" (ARCH §5). The §6 charts row ties any epoch-addition to "measured
  insufficient **against the analytics-shaped product #2**." Roster is not "closed"
  until measured at the known gap — the system cannot grade its own homework.
- **Stranger-review at M6 — mechanical, not a promise.** "the review having happened
  and its verdict being recorded is the gate (a missing or absent-verdict review
  reads as red, like any un-run suite)" (ARCH §5.1 M6). A required gate entry that
  reads red if un-run. Correctly it forces the human judgment on record rather than
  hard-blocking CI on an aesthetic yes — which is precisely what I asked for.
- **multiselect sugar — honors the taste concern, widens nothing.** "no new
  field-type row, no new mask bundle, no new totality pair, and no new native TCB"
  (ADR-10 §5), V6 byte-identical to hand-written `relation`, conditioned on the
  reference app exercising a tag field (ADR-11 §7 render path confirms zero new slot
  kind / masking leaf). Deletes the "model tags as a foreign key" leak I flagged
  without touching the immortal epoch surface.

## 3. Worse-product probe → **NO.** Nothing in R1 makes the product worse.

Every R1 gate is server-side and invisible to the customer; the one customer-visible
change (multiselect sugar) *removes* visible machinery. Does 300 ms p95 bless 300 ms
as "good enough"? No — the felt experience is governed by the 50 ms echo budget; the
300 ms is a truth-arrival ceiling under the echo, not the felt floor. Two honest
residuals, neither a finding: (a) a p95 ceiling can drift into a team target — but
the 50 ms echo gate is the real feel bar; (b) the stranger gate records a verdict
without forcing action on a "no" — correct design (don't CI-block on taste), owner's
call thereafter.

## Original findings transitions
- F1 [P1] charts / no designer's door → **RESOLVED** (riders now binding + mechanized;
  no-`unsafeHtml` stands by masking-proof necessity, not a regression).
- F2 [P1] felt-slow UI deferred to complaints → **RESOLVED** (machine gate, §9).
- F3 [P2] multiselect data-model leak → **RESOLVED** (verifier-checked sugar).
- F4 [P2] collaboration adversarial (presence/field-merge) → **UNCHANGED** (outside
  R1-07/14 scope; concurrent-edit still reject-and-reconcile, ADR-11 §7).
- F5 [P3] for-loop diagnostic UX → **UNCHANGED** (outside scope).

## New findings: 0 (P0–P3).
