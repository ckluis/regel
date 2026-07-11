# regel — Phase 0 Brief

*Ingest & synthesis of the five concept docs (regel, kern, streng, taal, eigen). This brief is the frozen input to Phase 1 architecture design. Terms in **bold** are defined in GLOSSARY.md.*

---

## 1. Core commitments — what regel IS

Regel is the **fusion of two sibling concept studies**: kern's *substrate* (the application as governed, content-addressed rows in one Postgres) and streng's *dialect* (closed-world strict TypeScript 7, the language every model already writes fluently). The one-sentence essence:

> Your app, schema, workflows, tenant customizations, and agents' patches are versioned **content-addressed** TypeScript rows in one Postgres, evaluated by one Go kernel — deploying, auditing, and governing them is one mechanism, in the one language every model on earth already speaks.

Precise commitments:

- **The code is rows.** Every definition (resource, function, component, view, policy, workflow, translation, prompt) is a versioned, content-addressed **definition row** in the same Postgres that holds the business data. The **catalog** is the storage format, not generated docs. Deploy is a commit; rollback is an **as-of** WHERE clause.
- **Code enters only by admission.** A patch arrives as TypeScript source; the **canonical printer** normalizes it to one rendering; **tsgo** typechecks it against the catalog; the **verifier suite** runs — all *inside the insert transaction*, with any schema migration. Rejected code never becomes code. There is **one gate** — engineers, tenants, and agents pass through it equally; no privileged "CI" side door.
- **One Go kernel, one Postgres.** The kernel — a **reactor**, vendored **tsgo**, an owned pure-Go **interpreter** with a **fuel meter**, the verifier suite, an owned Postgres wire client, and an **MCP server** — is the only compiled artifact and ships zero business logic. Its jobs: **admit · evaluate · resume · remember**. Substrate is Go + Postgres only: no cgo, no Node, no npm, no registry.
- **Governed evaluation is the whole substrate.** Trusted (admitted) code runs unmetered; tenant/agent code runs fuel-metered in **capability environments** where an unauthorized call is *unnameable*. Tenant customization and agent patches are the same primitive.
- **Departure from the parents (deliberate):** kern's Lisp/SBCL kernel and native compile-at-admission are gone — replaced by Go + the owned interpreter + vendored tsgo; homoiconicity is *approximated* via an AST schema + canonical printer, not native s-expressions.

## 2. Family invariants inherited (from eigen / kern / streng)

- **Resource derivation.** One `resource(...)` declaration derives schema + migrations, **history** tables, sync **horizons**, org-scoped policy, **vault** routing, forms, REST+OpenAPI+MCP, and its catalog row — a *closed derivation pipeline*, admitted in the same transaction as the code. The compiler (tsgo) is the first verifier; types compile to boundary validators (`Deal.parse`), they do not erase.
- **Closed ~25-component vocabulary.** Two tiers: the closed set of semantic primitives (page, form, table, card, badge, …) plus a derived tier (forms, data tables, boards, dashboards, operator plane). *The doc never enumerates the 25 — Phase 1 must fix the list.* The closed set is load-bearing: it enables derived forms, PII masking, dependency-exact invalidation, and a future native-renderer lane.
- **PII vault + crypto-shred.** PII fields route to a mutable, per-subject-keyed **vault** (keys in an external KMS outside the backup/WAL surface); never enter the history stream; render **masked by default**; plaintext only behind an expiring, second-party-approved, audit-logged **reveal grant**. Erasure = **crypto-shred** (destroy the key → all copies undecryptable, history still replays).
- **History tier.** One time-partitioned tier holds every version of every row *and* every definition; powers audit, as-of, and the change stream. "Who changed this workflow" is the same query as "who changed this record."
- **One Postgres.** Truth, queue, cron, pubsub, sessions, history, vault, catalog, and the app all live in one Postgres (LISTEN/NOTIFY bus, SKIP LOCKED queues, partitioned history, transactional DDL). Kernel is stateless — scale up = bigger PG, scale out = identical kernels.
- **Verifier gates.** Verifiers run *in* the insert transaction, fail-closed: **catalog parity**, **capability audit**, **PII flow**, **contracts**. The verifier suite *is* the type system for what tsgo can't see, and its coverage is the security boundary.
- **Honest-edges discipline.** Every family doc confesses its real costs plainly ("named, not waved away"). Regel keeps it — §3 restates its six edges as constraints.
- **Envelope + git projection.** The stated envelope is B2B SaaS (I/O-bound, heavy lifting in SQL). A deterministic two-way **git projection** (image is truth, repo is a view) ships in v1 or trust never arrives. Epoch upgrades (dialect+engine+stdlib+gate atomic) are inherited from streng.

## 3. The six honest edges as design constraints

Each of regel.html's honest edges is restated below as a constraint the architecture must satisfy or explicitly budget for, with its concrete architectural implication.

1. **Continuations over strict TS, serialized stably for years.**
   *Constraint:* the interpreter must CPS-transform the strict subset (async/await, closures, iterators) so any program can pause, and serialize captured environments stably across years, referencing immortal content hashes.
   *Implication:* **continuation representation + a stable environment-serialization format is the deepest bet and a foundational v1 deliverable**; capture discipline must be designed *into* the subset from day one and kill-tested (red-path-first) before any feature is built.

2. **No SBCL, no native compile-at-admission — the owned interpreter is the tax.**
   *Constraint:* v1 runs a pure-Go interpreter with no JIT/AOT; performance must live within the I/O-bound envelope.
   *Implication:* **keep the reactor thin and push heavy lifting into SQL; design an AOT-to-Go seam (taal's lane) that can be opened per verified hot function without changing semantics** — held in reserve, not built for v1.

3. **The condition system is rebuilt as data, not inherited from Lisp.**
   *Constraint:* a failed step must signal a **durable condition** row whose **restarts** are data, not rely on a host-language restart feature.
   *Implication:* **define the durable-condition + named-restart schema and its wake/resume path**; restarts must render as operator-plane buttons (or agent choices) that resume the exact continuation.

4. **Homoiconicity is approximated, not native.**
   *Constraint:* "code is data" must be delivered via an explicit **AST schema** for the subset plus the **canonical printer** (one rendering, hashable, structurally diffable) — there is no macro layer.
   *Implication:* **the AST schema, canonical-printer spec, and hashing scheme are load-bearing v1 artifacts**; every derivation (resource → schema/forms/API, static/dynamic split, etc.) must be an explicit pass over the AST, so the derivation layer works harder than kern's macroexpansion did.

5. **TypeScript's unsound corners survive in the trusted tier.**
   *Constraint:* the strict subset bans the visible lies (any/as/eval) and derives validators at every boundary, but structural-typing variance holes remain; **the verifier suite — not the type system — is the security boundary.**
   *Implication:* **the verifier suite roster + an adversarial test harness are a first-class, versioned deliverable**; trusted-tier admitted code is *trusted-by-verification* on a shared heap, so verifier coverage must be explicitly stated and continuously tested.

6. **Two parents, one team — both build burdens at once.**
   *Constraint:* the build must deliver both an owned engine *and* an admission substrate with one team.
   *Implication:* **ruthless staging — the walking skeleton (admit → row → evaluate → respond, end to end) precedes any feature**, and the deepest bets (printer, continuations, fuel) are proven under kill-tests before the world grows around them.

## 4. Open questions for Phase 1 (architecture design)

The genuinely unresolved decisions the concept docs leave open. Phase 1 must settle each.

- **Exact strict-subset grammar.** Precisely which TS7 constructs are admitted vs banned (beyond the stated any/as/eval/Proxy/with/ambient/prototype/outside-world bans) — the exact statement/expression/type surface the interpreter and printer must support. Async/await, generators/iterators, closures explicitly in-scope for continuations.
- **Canonical printer spec + hashing.** Normalization rules (whitespace, ordering, name→pointer substitution, literal canonicalization), the hash algorithm, and exactly what is included in / excluded from the hashed form (comments? contracts? type annotations?). This fixes identity for the whole system.
- **Continuation representation.** How async/await + closures + iterators are CPS-transformed in the Go interpreter; the on-disk environment-serialization format; capture discipline enforced by the subset; forward/backward compatibility across epochs.
- **Verifier suite v1 roster.** Exact set and semantics of catalog parity, capability audit, PII flow, contracts (and any additions); how each runs inside the insert transaction against tsgo output; the adversarial test harness and how coverage is versioned/stated.
- **Interpreter strategy.** Own interpreter day-one vs a staged bootstrap; how the pure-Go interpreter relates to vendored tsgo (tsgo typechecks; interpreter executes — confirm no tsgo emit path); fuel-metering semantics (how steps/allocations are counted in Go).
- **Catalog / definition-row schema.** The physical row schema for definitions: content-address column, AST/canonical-text storage, name-pointer table, scope/overlay column (product·package·org·team·user), contracts/docstrings as fields, history partitioning, and how code+schema migrate in one transaction.
- **Is std/ itself admitted as rows in v1?** Does the world (std/) bootstrap through the same gate as application code (rows in the catalog), or is it compiled into the kernel binary and referenced by import only? This decides whether the substrate is self-hosting on day one.
- **Secondary but load-bearing:** the 25-component vocabulary's actual roster; how tsgo is invoked mid-transaction (kernel-mediated admission architecture); capability grant/role model representation; git-projection determinism; the taal AOT-to-Go seam interface; regel's epoch mechanism (does it adopt streng's atomic epoch wholesale?).

---

*Files written by Phase 0: `spec/BRIEF.md`, `spec/GLOSSARY.md`. Source docs: `experimentalArchitectures/{regel,kern,streng,taal,eigen}.html`.*
