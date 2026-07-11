# BRUCE SCHNEIER — R1 targeted re-review (Phase 6)

Scope: rev 9 (mine) + boundary-verify rev 3 (C2) and rev 4 (C1). Every ruling cited.

## 1. REVISION 9 — native-TCB harness + parse ceiling + timing + attestation
**RULING: SATISFIED.** All four sub-parts present in substance, not name.
- **Native-TCB harness (§8, ADR-10).** `gate/native-tcb/`, "co-equal with ADR-07 §5's
  `gate/redpath/`", three seeded classes (vault-leak / contract / effect-order) each mapped to
  its catching control. Release-gating: a body "the surrounding machinery fails to catch …
  turns the release **red**." `verifier_coverage`-style **monotone** rows per class. Irreducible
  authority is honest: "an explicit **trusted-for** statement rather than a passing catch."
- **MAX_PARSE_DEPTH ahead of all budgets (§3, ADR-07).** "fixed syntactic nesting-depth limit",
  explicit depth counter so "the check fires *at* the depth boundary, never after the stack is
  already blown", deterministic across kernels, mints durable `PARSE_DEPTH refusal_id`. Precedes
  node ceiling, wall-clock, and fuel. Finding 2 met exactly.
- **Timing-indistinguishable resolution (§3, ADR-12).** Visibility predicate "evaluated first and
  identically", short-circuiting "the identical `NOT_FOUND` down the **same fast-fail path**,
  touching no row in either case" — the real fix (uniform work pre-fetch) plus a fixed latency
  floor, with a KS+p99-gap **release gate** ("can separate them **fails the release**").
- **H_dispatch attestation (§2, ADR-10).** SHA-256 over sorted (intrinsic, sig hash, body hash)
  triples "pinned in the epoch row", "**recomputes** … and compares … on every boot"; mismatch =
  structured `epoch.boot_refused`. "the gate never opens on an unattested table." Red-path present.

## 2. REVISION 3 boundary (C2) — no standing mutation privilege
**RULING: HOLDS.** "UPDATE/DELETE stay revoked from every database role, including the kernel's,
*permanently*" (ADR-02 §5.5). Restore is out-of-band audited break-glass superuser, "adds no
standing credential, no repair role, and no guard trigger." Self-certifying: accepted "only if
`SHA-256(domain ‖ candidate_ast) == hash` … **and** `hash(normalize…)==hash` … Wrong bytes cannot
verify" — fails closed, row quarantined. I9 codifies it; CI Gate 4 asserts the fail-closed leg
**and** the no-UPDATE invariant, release-blocking. Boundary intact.

## 3. REVISION 4 boundary (C1) — structural containment + no new surface
**RULING: HOLDS.** Containment unchanged: "no agent principal holds product-scope write", "blast
radius one scope", vault CHECK §4 — the corpus adds *detection*, not privilege. New machinery
adds no surface: the §7 delta is "computed from V1 capability-audit, V2 pii-flow, and V6
derivation-parity" over the content-addressed AST — machine-derived, never trusting agent claims.
Seeder metadata is structured provenance rendered into the ADR-11 no-`unsafeHtml` operator plane
(§4a corpus asserts seeded text renders "as **inert data**, never as instruction"), and "a seeder
outside the submitter's scope chain is unrepresentable … cannot be forged to blame another tenant."
§4a is M5-blocking with revert-to-P0 language. Holds.

## 4. NEW findings introduced by the revisions
- **[P2] Refusal-ledger retrieval is not caller-scoped.** `verdict.get {id}` / `catalog://
  verdict/{id}` "resolve `id` … against `gate_refusal`" (ADR-07 §6) with no predicate binding the
  *fetcher* to the refusal's principal. Leak discipline is enforced at *mint* vs the original
  submitter; a different principal holding a leaked/guessed `refusal_id` (they surface in logs, PR
  annotations, seeded condition text an injected agent reads) fetches that submitter's
  `scope_attempted`, `submitted_hashes`, diagnostic names — a cross-principal disclosure oracle.
  Fix: re-apply the §3 visibility predicate against the *calling* principal on every id-keyed fetch.
- **[P3] H_dispatch attestation is consistency, not provenance.** Genesis pins the builder's own
  `H_dispatch` and ships the matching binary, so a compromised build attests itself; "no longer a
  trust root taken on faith" (§2) overreaches — it catches only *post-genesis* divergence. Narrowed,
  not closed, by the §2 reproducibility kill-test (independent recompute). Carried-forward R7 root;
  noted, not re-litigated. Later fix: sign the epoch row / external attestation.
- **[P3] Timing indistinguishability is CI-proven only.** The KS gate runs in the harness; no
  ADR-13 production signal watches real-load cache/latency skew. Add a production timing-skew golden
  signal to keep the gate honest post-ship.

## Transitions on my original findings
- F1 (P1, boundary excludes TCB) → **RESOLVED** (§8 harness, gated, monotone, trusted-for).
- F2 (P2, parse-depth DoS) → **RESOLVED** (MAX_PARSE_DEPTH ahead of all budgets).
- F3 (P2, cross-tenant timing oracle) → **RESOLVED** (shared fast-fail + floor + KS gate).
- F4 (P2, one-human product gate) → **RESOLVED** (elevated into §7 machine delta, render a
  precondition of approve).
- F5 (P2, unattested binary) → **RESOLVED with residual** (§2 attestation + boot-refuse; genesis
  provenance residual re-noted as new P3).
