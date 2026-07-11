# JOHN ALLSPAW — Phase 6 targeted re-review (regel R1)

Scope: revision 3 (my conditional flag) + revision 5 (operability slice). I checked
substance at the marked sites and, critically, whether the drills are *mechanical* gates.

## Revision 3 — immortal-store recovery — RULING: **SATISFIED**

The four things I conditioned on are all present and load-bearing, not prose:
- **Byte-restore, fails closed.** ADR-02 §5.5: "a restored byte sequence is correct if
  and only if it rehashes to the content address it claims"; accepted only if
  `SHA-256(domain ‖ candidate_ast) == hash` — "Wrong bytes cannot verify."
- **No role ever regains UPDATE.** ADR-02 §5.5 / ADR-03 §1 (I9): "UPDATE/DELETE stay
  revoked from every database role, including the kernel's, permanently" — out-of-band
  audited break-glass, "adds no standing credential."
- **Scrubber-trip runbook.** ADR-03 §4a is authored detect→quarantine→restore→verify→
  resume, restore-to-hash only (supersede-around explicitly rejected for byte corruption).
- **Release-gated drill.** ADR-03 CI Gate 4: corrupt one `ast` byte → run runbook →
  assert scrubber-clean + fail-closed on non-matching bytes + no-UPDATE invariant.

**Is the drill a MECHANICAL gate?** YES. Not merely promised in ADR-03 prose — it is
wired into ARCHITECTURE §5.1's M0 gate-set row: "R1-03 immortal-store fault-injection
recovery drill (ADR-03 §4a, ADR-02 self-certifying restore)", and §5.1 makes every
gate-set suite a `required` branch-protection check via `milestone-gates.toml` — "A
branch classified M(n+1) cannot merge while any suite in M(0..n) is red, quarantined, or
skipped." ADR-03 §4a's gate also carries the teeth I demanded: "If this gate is removed
or downgraded from a release blocker, the finding reverts to P0." The condition from C2
is met as a mechanism, not a wish.

### → **RED FLAG STAYS WITHDRAWN.**

## Revision 5 — epoch operability slice — RULING: **SATISFIED**

- **Authored + drilled revert/roll-forward runbook.** ADR-08 §6a: revert = a new epoch
  row "`N+1` with `supersedes = N`" carrying E's pair; the `r<n>`-bump revert constraint
  is stated honestly and checked by a blast query. "**Drill — a release gate**": ship a
  seeded-bad epoch in staging, execute the revert, "record time-to-recovered."
- **Staging drill is a mechanical gate.** ARCHITECTURE §5.1 M6 gate-set: "R1-05
  running-kernel epoch fence + bad-epoch revert/roll-forward drill under a measured clock."
- **Structured, actionable boot-refuse diagnostic.** ADR-08 §2 JSON event
  (`epoch.boot_refused`/`epoch.fence_tripped`) carries observed/required epoch,
  binary_version, both manifest roots, kernel_id, ts, action — "deliberately
  distinguishable from a crash loop," rides ADR-13's Postgres-independent paths so it
  survives the catalog being the casualty.
- **Fail-close degrades gracefully.** §4a O5: rollback → refuse new work → release
  claims → drain → exit; re-offered work "completes exactly once on an epoch-N kernel
  from the last committed checkpoint." Terminal drain, not data loss. Confirmed.

## Probe — runbooks-never-executed (my signature trigger)

Both new runbooks have real rehearsal mechanisms, not paper: ADR-03 §4a → CI Gate 4
(M0-gated); ADR-08 §6a → the release-gate drill + red-path "Bad-epoch revert drill"
(M6-gated). The design applied its own mechanism-not-process ethos to recovery. Good.

### NEW finding (P2, operability)
**The §4a "quarantine/hold-dependents" containment leg is named but unmechanized and
unrehearsed.** Step 2 says "mark the address quarantined so the resolver/reactor treat
continuations and dependents that bind it as held" — but ADR-03 §1 DDL has no quarantine
state column/table, and CI Gate 4 asserts detect/restore/rehash/clean/fail-closed/
no-UPDATE while asserting *nothing* about dependents actually being held during the
incident. "Contain before repair" is the one runbook step with neither a schema
mechanism nor a drill assertion — exactly the seam where a detected-but-still-served
corrupt identity leaks. Remedy: back quarantine with a real held-state and add a Gate 4
leg asserting a bound dependent is refused (fail-closed) while the address is quarantined.

## Original findings — transitions
- **F1 [P0] immortal-store detection-without-repair** → **RESOLVED** (byte-restore + gated drill).
- **F3 [P1] bad epoch flip forward-only, no rollback drill** → **RESOLVED** (§6a + M6 drill).
- **F2 [P1] git self-heal no circuit breaker** → **UNCHANGED** (ADR-09; not touched by rev 3/5).
- **F4 [P2] corrupt-CFR fail-closed no restart set** → **UNCHANGED** (ADR-05; out of this re-review's scope).
