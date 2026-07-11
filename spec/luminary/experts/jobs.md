# STEVE JOBS — Product Quality & Customer Experience

## VERDICT: CONCERNS

The engine is beautiful. The *product* is where I stop clapping. This design keeps confusing "governed" with "great." A customer does not feel the six-verifier proof; they feel the missing chart, the lagging click, and the tag field they were told to model as a foreign key. Every one of those is reversible-later, so no P0 — but the taste debt is real and it lands on the person the whole thing is supposedly for.

## FINDINGS

1. **[P1] The closed vocabulary has no designer's door — only an engineer's.** The reference CRM ships with no charts (a dashboard is "stat tiles + tables"), no file upload, no rich text, and *permanently* no escape hatch — so any custom polish must be re-authored as a gated tier-1 composition, not drawn by a designer. That is engineering convenience sold as a masking proof; the customer sees a 25-primitive ceiling and an answer of "it's its own project." CITE: "there is no raw-HTML primitive and no `unsafeHtml`" (ADR-10, §7).

2. **[P1] You shipped the felt-slow UI and deferred the fix until people complain.** Every click, keystroke, and submit blocks on a server round trip before anything visibly moves; the remedy is postponed to "measured WAN latency complaints." Great products feel instant *first*; you don't ship latency and wait for the ticket. CITE: "Optimistic local echo is not in v1." (ADR-11, §3).

3. **[P2] The data-model abstraction leaks straight onto the tenant.** A customer who wants a tag field is told to think in foreign keys; `json` and `multiselect` are gone so the tool's derivation stays "total." The customer should never pay for your proof — that is exactly the abstraction a customer can see. CITE: "`multiselect` (model as a `relation`)" (ADR-10, §5).

4. **[P2] Collaboration feels adversarial.** With no optimistic echo, no presence, and no merge, two people on one Deal means the second is bounced with "review and resubmit" and told to redo it. Correct-by-CAS, yes; lovable, no — this is how 2009 software behaved. CITE: "this record changed — review and resubmit" (ADR-11, §7).

5. **[P3] Builder papercut branded as virtue.** The most universal idiom on earth — the counting `for` loop — is uncapturable, and the doc calls the stiffness a feature. Fluency claims for agents may hold; for the humans who read errors, this reads as the tool scolding them. CITE: "This is deliberate stiffness, not an oversight." (ADR-01, Consequences).

## RECOMMENDATIONS

- Build ONE first-class chart family (line/bar/area over `std/sql`) into the v1 vocabulary before the reference app ships; verify by demoing the reference dashboard and asking a stranger "does this look finished?" — a yes is the gate, not Rule of Three.
- Ship optimistic local echo for the two highest-frequency interactions (field edit, single-mutation button) in v1 behind the tested client state machine ADR-11 §3 already names; verify with a WAN-throttled reference-app clickthrough where the UI never visibly waits on the server for those two paths.
- Add `multiselect` as a first-class field type (deriving to `select` multi + a join under the hood) so the tenant never sees the relation; verify the reference app models a tag field without the author touching `relation`.
- For concurrent edit, add live presence + a field-level merge for non-conflicting fields; verify two operators editing different fields of one Deal both save with zero bounce.
- Rewrite the for-loop consequence as a diagnostic that offers the `for-of` rewrite inline (the "fix in the error" you already promise), then verify a `for (let i…)` capture rejection prints a copy-pasteable replacement.

## RED FLAG

NONE — every ceiling here is additive-later by the design's own epoch/deferral mechanism, so nothing is irreversible or unsafe enough to be a P0. The taste debt is a CONCERNS-grade P1, not a blocker.
