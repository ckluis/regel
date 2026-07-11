# regel — Glossary

Terms of art a Phase 1 architect needs. One line each. "The docs" = regel/kern/streng/taal/eigen concept studies.

## The gate and the substrate
- **admission / admit** — The single act by which code becomes code: TypeScript source is submitted, canonically printed, typechecked by tsgo, run through the verifier suite, and inserted as rows — all in one Postgres transaction. Rejected code never becomes code.
- **the gate** — The admission pipeline itself; the one path every change (engineer, tenant, agent) passes through. There is no privileged "CI" side door.
- **catalog** — The set of definition rows describing every piece of code (resources, functions, components, views, policies, workflows, prompts, translations). Being in the catalog is what "being code" means; the catalog is the storage format, not generated docs.
- **definition row** — A single content-addressed row holding one definition (its AST, canonical text, hash, catalog metadata, contracts/docstrings as fields). <!-- R1-12: scope removed — it lives on the name pointer, not the definition --> A definition is **scope-free by construction**: because identity is the hash of its bytes, scope cannot live on it (identical overlay code at two orgs would otherwise hash differently and break dedup). Scope is a column on the name pointer.
- **content-addressing** — The hash of a definition's canonical rendering is its identity. Names are catalog pointers to hashes; renames are metadata; drift is impossible; a patch references exact hashes.
- **canonical printer / canonical rendering** — The component that normalizes the strict subset to exactly one textual rendering per form, so the hash is stable and structural diffs are the only diffs. No formatting choices, minimal tokens.
- **name pointer** — A catalog entry mapping a human name to a content hash; renaming edits the pointer, not the code.
- **verifier suite** — The admission-time checks that run inside the insert transaction and form the real security boundary — exactly six (ADR-07 §4): capability audit, PII flow, catalog parity, contracts, capture, derivation parity. Versioned and adversarially tested. <!-- R1-INT: roster corrected four→six to match ADR-07 -->
- **blast-radius delta** — The machine-computed capability/PII/DDL *change* a patch makes vs. its base, attached to every Verdict green or red (ADR-07 §6) and rendered as a precondition of product-scope approval (ADR-12 §7). The approver sees what widened, not just green. <!-- R1-INT: term added, R1-04 -->
- **injection corpus** — The adversarial prompt-injection fixture suite: imperative payloads seeded in every attacker-influenceable read surface, driven through a real agent; co-equal with the PII sweep, M5-blocking, reverts to P0 if downgraded (ADR-12 §4a). <!-- R1-INT: term added, R1-04 -->
- **catalog parity** — Verifier ensuring every declared thing (e.g. a policy) is actually wired into an admitted path; "declared but unenforced" fails and never becomes code.
- **capability audit** — Verifier checking that code only names capabilities it was granted.
- **PII flow** — Verifier ensuring no vault value escapes unmasked across a boundary.
- **contracts** — Verifier checking machine-readable pre/postconditions declared on definitions.
- **gate parity / one gate** — Principle that engineer deploys, tenant Settings edits, and agent patches pass the same transaction, verifiers, and audit row.

## Evaluation
- **kernel** — The only compiled artifact: a reactor, the vendored tsgo checker, an owned pure-Go interpreter with a fuel meter, the verifier suite, an owned Postgres wire client, and the MCP server. Ships zero business logic; it is to the product what Postgres is to rows.
- **reactor** — The kernel's async event loop (epoll/io_uring) serving requests and driving evaluation; keeps hot state in memory as a cache over Postgres.
- **interpreter / owned engine** — The pure-Go evaluator sized to the strict subset: a defunctionalized CEK machine whose every transition boundary is a serializable pause point (ADR-04 §2 — the concept-era "CPS-transformed" phrasing is superseded; there is no whole-program CPS pass). Trusted code runs unmetered; untrusted (tenant/agent) code runs fuel-metered. <!-- R1-INT: aligned to ADR-04's CEK decision -->
- **trusted / admitted tier** — Code admitted by verification; runs unmetered; shares the kernel's heap (trusted-by-verification, no memory isolation).
- **sandbox tier** — Tenant and agent code; fuel-metered and capability-scoped in an environment.
- **fuel / fuel meter** — Exact step and allocation budgets charged per evaluation of untrusted code; untrusted code is "priced, not trusted." Cannot be escaped (no eval/new Function).
- **capability environment** — An evaluation environment in which only granted capabilities are bound; an unauthorized call is not rejected, it is *unnameable*. The environment is the policy — no ambient authority.
- **capability / capability grant** — A named permission (e.g. `crm.read`, `mail.send`); API keys and roles are bundles of capability grants, scoped, expiring, audit-rowed.

## Continuations & workflows
- **continuation** — A paused program serialized as a row: expression + captured environment, stored with a wake condition. Its `kind` is exactly one of three (ADR-05 §2 CHECK, string-for-string): **workflow**, **session** (a UI session), or **request**. <!-- R1-12: taxonomy matches the CHECK; a durable condition is not a continuation kind --> A durable condition is *not* a continuation kind — it is a separate row attached to a parked continuation. Any node resumes any continuation.
- **request continuation** — The `kind='request'` continuation: an in-flight HTTP request paused on a deferred wake (e.g. awaiting a workflow result or an event) and resumed like any other continuation. <!-- R1-12: 'request' named in the language -->
- **condition (disambiguation)** — The bare word is never used alone; it carries three distinct senses. <!-- R1-12: 'condition' split into three named senses --> (1) a **wake condition** — a trigger; (2) a **durable condition** — a resumable-error row with restarts; (3) the continuation **status `condition`** — "parked on an open durable condition awaiting a restart choice" (ADR-05 §2), also written *parked-on-condition*.
- **wake condition** — The stored trigger (timer, received message, event, join, manual) that revives a continuation. Sense (1) of *condition*; a trigger, never an error object.
- **durable condition** — A failed or fuel-exhausted step signals this instead of throwing; its restarts are data, rendered in the operator plane (or to an agent) as choices. Rebuilt in regel as rows, not inherited as a Lisp language feature. Sense (2) of *condition*; a row (`durable_condition`), not a continuation kind.
- **restart** — A named recovery option attached to a durable condition; picking one resumes the continuation. Renders as an operator button.
- **exactly-once** — A workflow step commits its effect and its checkpoint in one Postgres transaction, so recovery is guaranteed by Postgres, not by replay.
- **as-of** — Time-travel query over the history tier; a paused workflow resumes against the exact code hashes it started with, deleting the workflow-versioning problem. Rollback is an as-of query (a WHERE clause).

## The dialect and the world (from streng)
- **dialect** — regel's application language: closed-world strict TypeScript 7. "Subtract, never add" — no new syntax; the corpus is the spec.
- **strict subset** — The banned set: no `any`, no unchecked casts (`as`), no `eval`/`new Function`, no prototype mutation, no ambient declarations, no `with`/`Proxy`/sloppy mode, no imports outside the world. Every ban also shrinks the engine that must be owned.
- **closed world** — `import` resolves against `std/` and `app/` only, or fails to resolve; there is no registry to squat. A hallucinated import is a compile error.
- **the world / std/** — The batteries the closed world owes: identity, the two-tier component library, workflow engine, resources+vault+history, mail/files/i18n, test, crypto (vetted AEAD+KDF), http, sql, time, money. Complete for the B2B envelope, versioned with the epoch.
- **tsgo** — TypeScript 7's native-Go compiler, vendored as an Apache-licensed pinned library inside the kernel; typechecks the full graph in double-digit milliseconds, run inside the admission transaction.
- **epoch** — The atomic upgrade unit: dialect, engine, stdlib, component library, workflow engine, and gate move together as one artifact. Between epochs everything is frozen; an epoch is an offer, not an eviction. `streng migrate N` re-checks the whole app and lists every incompatibility at once.
- **Rule of Three** — Growth rule for `std/`: a battery ships when the third product needs it; earlier needs arrive as framework-authored, capability-gated bindings. The world is complete for its envelope (B2B SaaS), not every envelope.
- **component vocabulary** — The closed ~25 semantic components (page, form, table, card, badge, label, money, and kin), headless and CSS-var themed. Two tiers: primitives + a derived tier (forms from resources, data tables, boards, dashboards, operator plane). The closed set is what makes derived forms, PII masking, and a native-renderer lane possible.
- **semantic type / semantic field type** — A closed set of attribute types (money, email, phone, address, select, relation, pii, …), each carrying its own validation, rendering, masking, and locale behavior.
- **editor plane** — The IDE tooling (tsgo LSP, VS Code, syntax highlighting) inherited free because the dialect *is* TypeScript — the only family member with editors on day one.
- **runtime validator / boundary validation** — A validator derived from a type at every boundary; in the dialect types don't erase, they compile to validators (`Deal.parse`), never imported from a vendor.

## The family layers (streng's Dutch-named kernels)
- **grond** ("ground") — The stdlib: http, sql, crypto, time, money, mime, csv, test, and the component vocabulary.
- **erf** ("inheritance") — The declarative resource layer (ancestor: Ash); one declaration derives everything.
- **scherm** ("screen") — The server-diffed reactive UI layer (ancestor: LiveView); typed components, byte patches over SSE, real HTML first paint, no SPA/hydration.
- **taak** ("task") — The durable workflow engine (ancestor: Oban+DBOS); checkpoints as rows.
- **kooi** ("cage") — Governed evaluation of tenant/agent code: the same interpreter, fuel-metered and capability-scoped. In regel, governed evaluation is the *whole* substrate, not just this layer.

## Family invariants (from eigen)
- **resource / resource derivation** — One `resource(...)` declaration derives schema, migrations, history tables, sync horizons, org-scoped policy, PII vault routing, forms, REST+OpenAPI+MCP endpoints, and a catalog row — all admitted in the same transaction as the code. The single source of truth.
- **horizon** — The sync/visibility scope of a resource's rows — "who syncs / sees this row" (e.g. `by("assignment")`); the boundary the change stream and policy build on.
- **vault** — A mutable, per-subject-keyed store for PII fields; keys held in an external KMS outside the backup/WAL surface; everything downstream carries only a token. PII never enters the history stream.
- **masked by default** — Derived components render PII as tokens; plaintext materializes only behind an explicit, expiring, second-party-approved, audit-logged reveal grant.
- **crypto-shred / crypto-shredding** — Right-to-erasure by destroying a subject's vault key, rendering every copy (live, replica, history, backup) permanently undecryptable while history still replays perfectly. As final as backup retention allows.
- **reveal grant** — An explicit, expiring, second-party-approved, tamper-evident-logged permission to see a PII field in plaintext.
- **history tier** — One time-partitioned tier holding every version of every row *and* every definition; powers audit, as-of, and the change stream. "Who changed this workflow" is the same query as "who changed this record."
- **one Postgres** — The single datastore holding truth, queue, cron, pubsub, sessions, history, vault, catalog, and the application. The kernel is stateless; scale up = bigger Postgres, scale out = more identical kernels.
- **operator plane** — Support/admin surface derived from the same resources as the product; operators impersonate tenants with PII masked by default.
- **honest edges** — The family discipline of confessing each design's real costs plainly ("named, not waved away") rather than hiding them; regel keeps it.
- **envelope** — The stated scope a design is complete for (here: B2B SaaS, I/O-bound, heavy lifting in SQL); the honest boundary of the performance and completeness claims.

## The mutable surface
- **projectional editor** — A visual builder (workflow canvas, form designer, view editor) that reads and writes the same definitions engineers do; the canonical printer guarantees round-trip, deleting the "eject cliff."
- **scope / overlay** — A column on the *name pointer* (product · package · org · team · user); a definition is scope-free by construction (content-addressed). <!-- R1-12: scope attributed to the name pointer, not the definition --> The catalog resolves through the chain, so a tenant's custom field is a scoped overlay *pointer* no other tenant sees. Upgrades re-verify every overlay.
- **visibility (exported / private)** — A column on the name pointer. `exported` pointers resolve for any caller; `private` pointers (non-exported, module-internal top-level declarations) resolve only when the caller's module equals the pointer's module. <!-- R1-12: visibility predicate defined; 'private' enforced at resolution --> The single resolver enforces this with a visibility predicate in its `WHERE` (ADR-03 §3), so `private` is never a second, denied lookup path; a private name a caller cannot see resolves indistinguishably from a nonexistent one (ADR-12 §3, R1-09 timing rule).
- **Settings-as-admission** — The no-code surface is a thin admission client: a Settings edit is an `extendResource` form passing the same gate as any patch.
- **git projection** — A deterministic two-way projection of the code rows to a git repo so review/CI/editors keep working; the image is truth, the repo is a view. Mandatory in v1.

## The reserved lane (from taal)
- **taal** — A sibling family design (a strict ML that compiles to idiomatic Go). Inside regel only its compile-hot-functions-to-Go idea is borrowed and reserved — regel.html calls the AOT escape hatch "taal's lane."
- **AOT lane** — Verified hot functions ahead-of-time compiled to Go for speed; opened per-function, only when production says so, without changing semantics. v1 runs the interpreter only.
- **hot function** — A verified, compute-bound function that production profiling flags as worth AOT-compiling through the taal lane.

## Build discipline
- **walking skeleton** — The first thing built end-to-end: admit → row → evaluate → respond, before any feature exists; the deepest bets (printer, continuations, fuel) proven under kill-tests first.
- **red-path-first** — Building and testing the failure paths (crash, resync, exhaustion, mid-query error) before the happy path — where the real bootstrap cost lives.
- **MCP server** — Ships in the kernel; agents query the catalog, fetch definitions by hash, submit patches, and receive verifier verdicts as structured data. "The IDE is an API, and the API speaks TypeScript."
- **qname** — The one canonical scoped-name grammar on the agent plane: `qname := name "@" scope` (e.g. `deal@org.acme.crm`) — what search returns, what every name-addressing tool accepts, what `catalog://name/{qname}` embeds; the name-addressed twin of the content hash, and there is exactly one encoding of it (ADR-12 §2). <!-- R1-INT: term added, R1-08 -->
- **confused deputy** — The agent-as-victim adversary: an attacker who cannot author but seeds content the trusted agent reads, steering it to act with the victim's own grants; modeled in ADR-12 §2's abuse table, gated by the injection corpus, attribution, and the approval delta. <!-- R1-INT: term added, R1-04 -->
- **content seeder / third principal** — Whoever seeded the content an authoring agent read that reaches the submitted patch; recorded per admission as `{source_kind, source_ref, scope, seeded_by|"unattributed"}`, scope-chain-validated so it cannot be forged to blame another tenant (ADR-07 §1, ADR-12 §6). <!-- R1-INT: term added, R1-04 -->
- **reversibility asymmetry** — The roster-growth principle: a deferral is deletable, an addition is immortal epoch surface — so closed rosters bias to defer, with honesty riders where a deferral could hide a real gap (ADR-10 §5). <!-- R1-INT: term added, R1-14 -->
- **stranger-review gate** — An M6 mechanical gate entry: a reviewer outside the project records a "does this look finished?" verdict on the reference dashboard; a missing or unrecorded verdict reads as red (ADR-10 §7, ARCHITECTURE §5.1). <!-- R1-INT: term added, R1-14 -->
- **verifier-checked sugar** — Surface syntax that desugars at derivation to existing roster primitives with V6 checking the expansion byte-identical to the hand-written form — no new field-type row, mask bundle, totality pair, or native TCB (`multiselect` → `relation`+`select`-multi is the instance; ADR-10 §5). <!-- R1-INT: term added, R1-14 -->

## Observability (ADR-13)
- **health surface** — Not a gesture: the ADR-13 §2 signal registry, emitted over the §4 Postgres-independent paths (stdout JSON, owned push exporter, health port) under the §6 PII policy, judged against the §3 SLOs. Every use of the phrase in the corpus resolves to ADR-13. <!-- R1-INT: term added, R1-06 -->
- **golden signal** — One of the ~24 enumerated registry rows (plus 2 meta-signals) that *are* the health surface — named, typed, cardinality-bounded, each with an owner and (where operational) an SLO (ADR-13 §2). <!-- R1-INT: term added, R1-06 -->
- **signal registry** — The compiled-into-the-binary table of every legal signal (name, type, labels, units), versioned with the epoch; unregistered emission does not compile (ADR-13 §1). <!-- R1-INT: term added, R1-06 -->
- **reap-rate breaker** — The sliding-window circuit breaker on lease re-offers: it opens on excess re-offer rate or re-expiry ratio, halting reaping (safe — the lease is liveness-only) and emitting a structured trip event; half-opens on a probe batch (ADR-13 §5, ADR-06 §5). <!-- R1-INT: term added, R1-06 -->
- **re-expiry ratio** — The fraction of re-offered work whose fresh lease also expires uncommitted — the signature of a fleet that cannot keep up, where faster re-offering only amplifies load; >50% opens the breaker (ADR-13 §5). <!-- R1-INT: term added, R1-06 -->
- **wire protocol** — The kernel's owned byte-patch protocol to a ~15 KB browser client (scherm) and the sexp/structured channel to agents.
