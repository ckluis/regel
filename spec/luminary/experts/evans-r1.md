# ERIC EVANS — R1 targeted re-review (Phase 6, revision 12, DDD/language lens)

## REVISION 12 — RULING: **SATISFIED**

Per-item (language items only; Celko owns schema mechanics):

1. **scope → name_pointer — SATISFIED.** GLOSSARY "definition row" and "scope/overlay"
   both now read "a definition is **scope-free by construction**"; ADR-03 §2 note: "A
   `definition` row carries no scope: scope lives only on `name_pointer`." The dedup
   invariant is now stated as the reason, not contradicted. Zero "scope on definition" hits.
2. **"condition" split into three named senses — SATISFIED, and adopted in prose, not
   just glossary.** GLOSSARY "condition (disambiguation)" fixes the rule ("The bare word
   is never used alone"). ADR-05 §2 CHECK comment names all three; §6 uses
   "parked-on-condition status"; Context line 13 renames "durable-condition system."
   ARCHITECTURE §2(c): "*wake condition* = trigger, *durable condition* = the
   resumable-error row, *`condition` status* = parked awaiting a restart choice." The
   former drift sites both adopt the vocabulary substantively.
3. **continuation-kind taxonomy reconciled with the CHECK — SATISFIED.** GLOSSARY:
   "exactly one of three … **workflow**, **session** …, or **request**"; CHECK is
   `('workflow','session','request')`; both state "a durable condition is *not* a
   continuation kind." `request` is now a named term. String-for-string match.
4. **kind='module' — SATISFIED (removed).** ADR-03 CHECK drops it; comment: "A 'module'
   kind was undefined and uncreatable." Defined-or-removed clause met by removal.
5. **continuation.epoch purpose stated — SATISFIED.** ADR-05 §8: "provenance stamp, not a
   resume selector"; two named readers (lattice-narrowing enumeration + observability);
   fleet gating explicitly assigned to the R1-05 fence, not this column.

## DEVIATION — SQL literal 'condition' KEPT: **ACCEPT**

From the ubiquitous-language standpoint the literal does *not* name "none of the three" —
it *is* the canonical anchor of sense (3): the disambiguation rule defines "the
continuation **status `condition`**" as that third sense, so the persisted value maps 1:1
to a named term ("parked-on-condition"). Prose (ARCHITECTURE §2(c), ADR-05 §6) and the
adjacent CHECK comment disambiguate at every use; renaming a stable persisted enum would
manufacture the very schema/prose drift the batch closed. The language, not the byte,
carries the meaning — DDD is satisfied.

## NEW-DRIFT PROBE (R1 + R1-INT additions)

Additions qname, confused deputy, content seeder/third principal, reversibility asymmetry,
visibility, verifier-checked sugar, stranger-review gate read clean — each a distinct
noun phrase, cross-referenced, no collision. qname's `scope` reuses the scope/overlay
sense correctly. Two P3 nits:

- **[P3] "delta" has three surface names.** "blast-radius delta" (GLOSSARY headword) vs
  "approval delta" (confused-deputy entry) vs "capability/PII/DDL delta" (ADR-12 §6/§7).
  ADR-12 line 294 equates them explicitly, so it is reconciled, not broken — but the
  glossary headword should be the single spoken form. Cosmetic.
- **[P3] one bare "condition" survives the new rule.** ARCHITECTURE §6 exclusions,
  "Bulk condition operations" — a bare use the R1-12 rule ("never used alone") now
  forbids. Pre-existing line, no R1 marker; contextually operator-plane durable
  conditions. Rename to "Bulk durable-condition operations."

## ORIGINAL FINDINGS — TRANSITIONS

- F1 (scope on wrong aggregate, P2) — **RESOLVED**
- F2 ("condition" ×3, P2) — **RESOLVED**
- F3 (continuation taxonomy, P2) — **RESOLVED**
- F4 (epoch protects no invariant, P3) — **RESOLVED**
- F5 (kind='module' undefined, P3) — **RESOLVED** (removed)

All five RESOLVED; no regressions. Verdict lifts CONCERNS → **CLEAR** on the language axis.
