# LINUS TORVALDS — R1 targeted re-review (Phase 6)

## RE-VERDICT: two touched P2s discharged as mechanism; one new P3; no red flag.

I came back hunting the disease I filed — an enforcement story naming a machine it never builds. I mostly don't find it.

## 1. Rev 11 — milestone ladder machine gate: **SATISFIED**

Every piece I asked for is present and mechanical, not aspirational:
- Manifest + attribution globs: gate suites "each by its exact CI job name … path/label
  globs that classify a change" (ARCH §5.1).
- Mechanical refusal: "A branch classified M(n+1) cannot merge while any suite in M(0..n)
  is red, quarantined, or skipped" (ARCH §5.1) — quarantined-reads-as-red closes the
  flaky-is-green loophole I actually cared about.
- Gate-set table wiring the other revisions (R1-01/03/07/10→M0, R1-02/09/10→M1 …) is real
  and cites the owning ADR per cell.
- Escape hatch: "one override: a signed `gate-override` … append-only override ledger, and
  auto-expiring at the next tagged release" (ARCH §5.1). Single, signed, audited, expiring.
- Gate-of-the-gate: "Seed a red M(n) kill-test … CI must refuse the merge and … name the red
  M1 suite" (ARCH §5.1) — my exact verify step, encoded.
- Residual honest: rot / misclassification / override-abuse each named + mitigated; residual
  = "trust in the manifest's fidelity … and release-owner restraint" (RISKS R5).

## 2. spec/milestone-gates.toml — **ACCEPTABLE**

File does not exist (grep-confirmed repo-wide); ledger calls it an M0 CI deliverable. NOT
the spec-by-reference disease: the manifest's required *contents* are fully enumerated in
the §5.1 gate-set table and the self-test fails on a missing/drifted manifest — the .toml is
the drift-guarded serialization of a spec that exists. A CI artifact belongs at build stage.

## 3. Revision 14 (my slice) — closure discipline: **SATISFIED**

- Bias-to-defer via reversibility asymmetry, load-bearing: "an addition earns its place only
  against a measured consumer" (ADR-10 §5) — the exact clash-C3 pivot.
- Multiselect strictly desugars: "no new field-type row, no new mask bundle, no new totality
  pair, and no new native TCB" (ADR-10 §5), V6-parity byte-identical, admitted only if the
  reference app exercises a tag field. My "conditional yes" delivered in full.
- Charts deferred-with-riders, not smuggled: "a chart vocabulary is its own project, specced
  from two products, never one" (ARCH §6); §7 riders (analytics product #2 + M6 stranger-review) mechanized.
- Premature generalization through the riders? None — neither adds kernel surface (product #2
  is a recorded commitment, stranger-review a recorded human verdict wired as a gate entry).

## 4. Machinery probe (ADR-13, gate manifest, override ledger, attestation, breakers)

**No abstraction-without-a-second-user among them** — my red-flag trigger does not fire.
ADR-13 resolves a reference already dangling in all 12 ADRs (12 callers, not zero); the
manifest has 7 milestones of live gates. H_dispatch attestation and the reap-rate breaker
have one call site each (boot; reaper) but are SHA-compares / token buckets guarding named
threats, not layers awaiting a caller. The override ledger is ceremony for a hatch that may
never fire — but a signed annotation + append-only log, cheap, not a seam. Accretion of
*mechanism*, not *abstraction*. Zero red flag stands.

**NEW [P3] Triple-enumerated gate-set; only one edge drift-guarded.** The gate-set is
written three times — ADR red-path lists, the `milestone-gates.toml` manifest, and the
human-readable §5.1 table. The self-test guards manifest↔ADR; nothing guards the §5.1
markdown table against the manifest. Failure: an edit updates manifest + ADR but not the
table (or vice-versa) and the table — what a human reads to know the gate *is* — silently
lies while CI stays green. Same shape as the P2 glossary/schema drift cluster. Fix: generate
§5.1's table from the manifest, or add it to the self-test compare set.

## Original findings — transition

- **F1 [P3] AOT/self-hosting inert seams** — not in this slice — **UNCHANGED**.
- **F2 [P2] milestone gate is promise not mechanism** — **RESOLVED** (R1-11; RISKS R5:
  "a forge required-check, not a human refusal").
- **F3 [P2] kernel accretes old-epoch surface, no metric** — not in rev 11/14; no carried-
  surface count/alarm added (R1-10 gave a decode floor, not my metric) — **UNCHANGED**.
- **F4 [P3] closed framework proven at N=1** — **RESOLVED**. My remedy is now binding:
  "the second product … must be analytics-shaped … roster may not be declared closed until
  … measured insufficient" (ARCH §5, ADR-10 §7).
