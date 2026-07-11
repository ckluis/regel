# CLASH C2 — Allspaw vs Schneier: repair path for the immortal `definition` store

Conductor: Luminary Phase 4. Protocol: steelman-before-rebuttal, max two exchanges,
movement on evidence only, all new claims cite ADRs (verbatim ≤20 words + location).

## Positions

**ALLSPAW (P0, DATA INTEGRITY red flag).** The scrubber and world-rehash canary can
detect corruption of the sole code identity, but the store is INSERT-only with
"UPDATE/DELETE privileges revoked from every role, including the kernel's" (ADR-03 §1),
so detection dead-ends: no runbook, quarantine, or supersede-around motion exists for
the minute after the alarm. Blast is total — "Nothing survives a moved hash."
(RISKS.md, R1 Blast). Detection without repair on a total-blast surface is P0.

**SCHNEIER (P1-with-runbook; no new mutation path).** The boundary's strength is
structural: "corruption states are made unrepresentable by constraints" (ADR-03,
Context) and "Genesis is the only gate bypass in the system" (ADR-10 §2, per audit).
Any repair door on the identity store is a new mutation path and attack surface that
could forge code identity. The cure — a standing write privilege on `definition` — is
worse than the disease. Absence of a repair door is the design working; what is missing
is a procedure, which is P1.

## Exchange 1

### ALLSPAW — steelman of Schneier

The revoked-privilege posture is not security theater; it is the load-bearing member.
Every durable guarantee downstream — "a paused workflow stores exact hashes, not names,
and resumes against immortal rows" (ADR-03 §4, I6) — is credible *because* no role can
mutate the store. A repair channel, however hedged, is a standing credential: it lives
in IAM, it can be phished, its guard trigger becomes attack surface, and its very
existence converts the auditable claim "nothing can rewrite code identity" into
"nothing can rewrite code identity except the repair role, when the check holds."
Schneier's own audit found the binary is an unattested trust root; adding a mutation
door beneath an unattested kernel hands a builder-of-the-binary a laundering path for
forged identity. Under his threat model, my remedy plausibly *creates* the P0 it
claims to fix. That is a complete, charitable statement of his position.

### ALLSPAW — rebuttal

But the mutation path already exists and is unrehearsed — that is the finding. The
design's own kill-test exercises it: "flip one byte of `ast` via superuser ⇒ scrubber
alarms on next pass" (ADR-03, Red-Path Tests Implied). Postgres superuser can write
those bytes today; revoking role privileges removes the *application's* door, not
physics. So when the scrubber fires at 03:00, the real repair will be an engineer with
superuser improvising surgery on the identity store under incident pressure — the
least constrained, least audited mutation path in the entire system. Reliability is
the presence of recovery: the choice is not "door vs no door," it is "rehearsed,
verified recovery vs improvised superuser access." And note what my remedy needs:
nothing new. Re-admission is already INSERT-shaped — "The only way a definition
acquires a new address is explicit re-admission" (ADR-02 §6) — and the corrupted
bytes' correct preimage is machine-checkable against the address the row already
carries. I am asking for a drilled motion, not a privilege. Its absence on a surface
where "Nothing survives a moved hash" (RISKS.md, R1) is P0.

### SCHNEIER — steelman of Allspaw

Charitably and completely: Allspaw is not asking to weaken I6. His discipline says a
catastrophic mode you can detect but not exit is worse than one you cannot detect,
because the alarm summons a human who has "nothing to do next" — and humans with
nothing to do next and superuser credentials do the worst possible thing. The scrubber
comment itself concedes the gap: "content integrity is kernel-enforced +
audit-scrubbed, not provable by a Postgres CHECK" (ADR-03 §1) — meaning the store's
correctness is a *maintained* property, and maintained properties fail. The design
rehearses every trip (fail-closed CFR, boot refusal, canary) and no recovery; on the
one surface whose blast is total, that asymmetry is exactly where P0s live. His
fault-injection drill — corrupt one immortal byte, measure time-to-contained — is the
same red-path-first ethos every ADR already follows, applied to operations. That is
his strongest form, and it is strong.

### SCHNEIER — rebuttal

Two corrections, both structural. First, his named motion is wrong for this failure:
"supersedes-re-admit a corrected row" does not repair byte corruption, because
re-admitting the correct program produces the *same* hash and step 3's
"ON CONFLICT (hash) DO NOTHING" (ADR-03 §5) silently keeps the corrupt row — while a
genuinely new address cascades, since "editing a leaf re-addresses its dependents
through their `Ref` nodes" (ADR-02 §2), stranding every continuation that stored the
exact old hash. Supersede-around is the *worst* repair here. Second, detection does
not dead-end, because the correct bytes are not lost: the same row's projection is
verified at insert — "the kernel verifies `hash(normalize(lower(parse(canonical_text))))`
equals the stored hash" (ADR-02 §5.4) — so `ast` is rederivable in-row, and again from
the git mirror and backups. Content addressing makes repair uniquely self-certifying:
a restore is correct iff the digest matches the immortal address; wrong bytes cannot
verify, so a *byte-restore* cannot forge identity. The gap is therefore procedural,
not structural — recovery data present, procedure absent. That is P1 with a mandatory
runbook, not P0. What stays non-negotiable: no standing repair role. The restore is
break-glass superuser (which exists regardless), audited, followed by an on-demand
scrubber pass over the repaired row.

## Exchange 2

### ALLSPAW — steelman of Schneier (new ground only)

His rebuttal is better than my remedy: he showed the repair *content* is pinned by the
address, so the only safe motion is byte-restore-to-hash, and he showed my
supersede-around would amplify the incident through the Merkle closure. He also keeps
the perimeter honest — a break-glass path that already exists plus an audit row grows
zero attack surface, whereas my drill, if it required a schema privilege, would grow
it. If the procedure is drilled and release-gated, his P1 preserves everything my P0
was protecting.

### ALLSPAW — rebuttal (priority only; no repetition)

I concede the mechanism; I contest the label's teeth. A P1 that lives in a backlog is
how "standing operational duties, listed in the kernel's health surface" (ADR-03,
Consequences) rot into wishes. RISKS R5 already admits the failure shape: "staging is
process, not mechanism — gate-before-next-milestone is a human refusal" (RISKS.md,
R5 Residual). So my condition for coming down from P0: the fault-injection drill —
corrupt one `ast` byte, execute the byte-restore runbook, assert scrubber-clean and
time-to-contained — must be a *release-gate kill-test* alongside the tamper test that
already exists, not an ops document. Gated P1: agreed. Ungated P1: red flag stands.

### SCHNEIER — steelman of Allspaw (new ground only)

His R5 citation is the sharpest thing said in this clash: this design trusts
mechanisms, not process, everywhere else — the history trigger exists because "code
cannot forget to write history" (ADR-03 §4, I7). Asking recovery to be enforced by the
same release machinery that enforces the mutation matrix is not P0 inflation; it is
the design's own philosophy applied to its one unrehearsed surface.

### SCHNEIER — rebuttal (closing)

Accepted without reservation. A release-gated drill is a test, and tests are not
attack surface; a scrubber that alarms into a rehearsed, self-certifying restore is
strictly stronger than one that alarms into silence. Agreed, with one recorded
boundary: the runbook must state that *no* standing role regains UPDATE on
`definition`, and the drill must assert the restore path fails closed when the
restored bytes do not hash to the address.

## OUTCOME

**COMPROMISE — agreed priority: P1, conditional on a release-gated recovery drill
(P0 red flag withdrawn under that condition; reverts to P0 if the gate is not adopted).**

**Agreed reformulation of the finding:** "The immortal store has recovery *data*
(hash-pinned, rederivable from `canonical_text`/git/backups) but no recovery
*procedure*. Remedy: a byte-restore-to-hash runbook using existing break-glass
superuser access (no new privilege, no standing repair role), self-certified by
digest match and an on-demand scrubber pass, plus a supersedes path only for the
semantic-corruption case — verified by a release-gate fault-injection kill-test that
corrupts one immortal `ast` byte, runs the runbook, and asserts scrubber-clean,
time-to-contained, and fail-closed on non-matching restore bytes."

**One-line rationale:** Content addressing makes repair self-certifying — wrong bytes
cannot hash to the immortal address, so a rehearsed byte-restore adds recovery without
adding a forgeable mutation path; the defect was procedural, not structural.

**Movement log:** Allspaw dropped supersede-around for byte corruption and conceded
P0→P1 on evidence (ADR-02 §5.4 rederivability; ON CONFLICT dedup defeating re-insert);
Schneier conceded the drill must be a release-gate mechanism, not an ops document, on
Allspaw's R5 process-vs-mechanism argument.
