# JOE CELKO — SQL & Data Modeling · R1 RE-REVIEW (Phase 6 targeted re-audit)

## VERDICT: PASS (red flag CLEARED)

Round 1 was one P0 (uncreatable I4 exclusion) + four P2 integrity gaps. All five are
resolved in the revised corpus, and the integrator's authored DDL is sound bar one
latent key-width nit. My red flag is CLEARED.

## REVISION 1 (P0 — my red flag): **SATISFIED**
`name_pointer_history` is now unpartitioned — "unpartitioned so the I4 temporal exclusion
is actually creatable" (ADR-03 §1). `CREATE EXTENSION btree_gist` + `EXCLUDE USING gist
(… tstzrange(valid_from,valid_to) WITH &&)` is creatable; the rationale correctly records
that PG16 rejects exclusions on ANY partitioned table and HASH-partitioning fails the same
way. CI "Verification Gates" execute real DDL against a live Postgres of the deploy major:
gate 1 fails the build if `CREATE TABLE` errors (catches partitioning regression), gate 2
is a kill-test asserting the overlapping INSERT RAISES 23P01 — "must FAIL the build … if
the exclusion is missing, dropped, disabled, or uncreatable" (§CI) — gate 3 guards against
false positives. Proven by executed DDL + overlap-rejection, not asserted.
**RED FLAG: CLEARED** — resolved by ADR-03 §1 table-(4) unpartitioned DDL + CI gate 2
overlap-rejection kill-test. I4 rewritten: "enforceability restored … proven, not asserted"
(§4).

## REVISION 12 (coherence batch, 9 items): **SATISFIED**
1. scope → name_pointer: "A `definition` row carries no scope" (ADR-03 §2); GLOSSARY §9/§77. OK.
2. three "condition" senses: GLOSSARY §35 splits wake/durable/status; ADR-05 §1 drift site renamed. OK.
3. kind taxonomy vs CHECK: `CHECK (kind IN ('workflow','session','request'))` = GLOSSARY §33 verbatim. OK.
4. kind='module' removed: dropped from `definition` CHECK — "'module' … undefined and uncreatable" (ADR-03 §1). OK.
5. continuation.epoch purpose: "PROVENANCE stamp, NOT a resume selector" (ADR-05 §2). OK.
6. jsonb discriminator CHECKs: `wake_kind_shape` (ADR-05 §2), `payload_shape` (ADR-06 §5); ADR-03 has none — correctly noted. OK.
7. durable_condition FK + state CHECK: `resolved_restart_fk` ALTER + `resolved_consistency`
   `(status='resolved') = (resolved_restart/resolved_by/resolved_at NOT NULL)` + `class_shape` (ADR-05 §6). OK — iff is well-formed, resolved_args correctly excluded.
8. history→admission FK: "admission_id bigint NOT NULL REFERENCES admission(id)" (ADR-03 §1) — the join is now total. OK.
9. resolver visibility predicate: `visibility='exported' OR (private AND module_of(name)=:caller_module)` (ADR-03 §3); external entry `:caller_module=''` ⇒ private unreachable. OK.
All nine substantively satisfied; red-path tests added for items 7/8/9.

## DEVIATION (R1-12 kept SQL literal 'condition'): **ACCEPT**
The status literal is an opaque token whose set is already closed by the CHECK; the three-term
split is achieved in GLOSSARY + column comments. Renaming it buys zero integrity and would
drift ARCHITECTURE prose. "the status value 'condition' is the THIRD distinct sense" (ADR-05 §2)
is the right disambiguation site.

## EXTRA SCRUTINY
(a) **Integrator DDL, ADR-03 §1 table-(6).** Sound as a schema reviewer:
`gate_refusal` — uuid PK, `outcome CHECK IN ('rejected','stale-base','retry-exhausted,
'budget-exhausted','busy')` correctly excludes the two green Verdict outcomes so a refusal
row cannot record success; nullable `submitted_hashes`/`scope_attempted` match pre-parse
refusals; no FK is correct (rejected admissions leave no admission row). `verifier_coverage`
/`continuation_coverage` PKs are total and typed. NULL semantics clean (`perf_budget.measured`
nullable = unmeasured). One nit below.
(b) **ADR-06 task.payload discriminator CHECK.** Keys match the shapes ADR-06 uses:
resume⇒`continuation_id`+`step_seq`, cron⇒`schedule`+`target`, deliver⇒`intent_id`+`dedup_key`
— identical to the inline payload comment; no reader dereferences a key the CHECK omits. Correct.

## ORIGINAL FINDINGS
- #1 [P0] I4 exclusion uncreatable — **RESOLVED** (Rev 1).
- #2 [P2] unchecked jsonb discriminators — **RESOLVED** (Rev 12.6).
- #3 [P2] durable_condition FK/state integrity — **RESOLVED** (Rev 12.7).
- #4 [P2] history→admission FK missing — **RESOLVED** (Rev 12.8).
- #5 [P2] resolver can't enforce private — **RESOLVED** (Rev 12.9).

## NEW FINDINGS
- [P3] `perf_budget` PK is `(epoch, metric)` but ADR-04 §8 models the row as
  `(metric, tier, budget, …)` over two real tiers (sandbox/trusted); `tier` is a non-key
  column, so a metric with both a sandbox and a trusted budget in one epoch collides on the
  PK. No listed metric is dual-tier today, so latent — but the key should be
  `(epoch, metric, tier)` to match ADR-04's own row shape. Integrator-authored DDL.
No P0/P1/P2 introduced.
