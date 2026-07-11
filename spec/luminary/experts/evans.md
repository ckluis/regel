# ERIC EVANS — Domain Modeling & DDD

## VERDICT: CONCERNS

The twelve ADRs are internally coherent and the reconciliation table is real work. But
the ubiquitous language drifts from the schema in ways that will mislead
implementers, and one core term ("condition") carries three unrelated meanings. No P0:
the normative aggregates own their invariants; the defects are in the language, not the
design.

## FINDINGS

1. **[P2] "scope/overlay" is attributed to the wrong aggregate.** The language places
   scope on the definition, but the normative schema owns scope on the name pointer —
   and it MUST, because the definition is content-addressed: if scope entered a
   definition, identical overlay code at two orgs would hash differently and step-3 dedup
   would break. The invariant this boundary protects (one hash per byte-sequence) is
   silently contradicted by the glossary. CITE: "A column on the definition row"
   (GLOSSARY.md, scope/overlay) vs "PRIMARY KEY (name, scope_kind, scope_id)"
   (ADR-03, §1).

2. **[P2] "condition" means three different things with no disambiguating rule.** A
   *wake condition* is a trigger; a *durable condition* is a resumable-error object with
   restarts; and `condition` is a continuation *status*. A reader cannot tell which "the
   condition" denotes without context the corpus never supplies. CITE: "The stored
   trigger (timer, received message, event)" (GLOSSARY.md, wake condition) vs "A failed
   step signals this instead of throwing" (GLOSSARY.md, durable condition) vs
   "'sleeping','ready','running','condition'" (ADR-05, §2).

3. **[P2] The continuation taxonomy disagrees with itself.** The language enumerates
   continuations as workflows, sessions, and durable conditions; the schema enumerates
   workflow, session, and request. A durable condition is a *separate table* attached to
   a parked continuation, not a continuation kind, and `request` has no name in the
   language. CITE: "durable conditions are all continuations" (GLOSSARY.md, continuation)
   vs "kind IN ('workflow','session','request')" (ADR-05, §2).

4. **[P3] `continuation.epoch` protects no stated invariant.** Resume semantics are
   keyed by `r<n>`, never by this column, so its role (provenance? a resume guard?) is
   undefined — point the invariant it enforces. CITE: "epoch int NOT NULL" (ADR-05, §2)
   vs "evaluation semantics of the `r<n>`" (ADR-08, O2).

5. **[P3] `kind='module'` is an undefined definition kind.** Modules decompose into
   per-declaration rows and imports are stripped, so what a module-kind row holds or
   hashes is unspecified. CITE: "'translation','type','module'" (ADR-03, §1) vs "modules
   are files, files become rows" (ADR-01, §2).

## RECOMMENDATIONS

- Rewrite the glossary "scope/overlay" entry to read "a column on the name pointer,"
  and add one sentence: "a definition is scope-free by construction." Verify: grep the
  corpus for "scope" attributed to `definition`; expect zero hits after the fix.
- Split "condition" in the language: keep "wake trigger," "durable condition," and
  rename the status value discussion to "parked-on-condition." Verify: each of the three
  senses has a distinct noun phrase used consistently in ARCHITECTURE §2(c).
- Reconcile the continuation-kinds sentence with the CHECK constraint verbatim; add a
  glossary line for `request` (the deferred-wake HTTP continuation). Verify: the three
  kinds in GLOSSARY equal the three in ADR-05's enum, string-for-string.
- Add one clause to ADR-05 §2 stating what `continuation.epoch` is FOR (provenance
  stamp, not a resume selector) or drop the column. Verify: no resume path reads it.
- Add a kind='module' definition to ADR-03 §2 (what it holds post-import-strip, how it
  hashes) or remove 'module' from the enum. Verify: the walking-skeleton admission emits
  exactly the declared kinds.

## RED FLAG

NONE. Every finding is a language/documentation incoherence over a design whose
normative aggregates own their invariants correctly; none is irreversible, unsafe, or
produces incorrect output, so none rises to a blocking flag.
