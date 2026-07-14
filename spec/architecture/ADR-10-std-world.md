# ADR-10: The std/ world

## Status

Accepted — Phase 1

## Context

BRIEF §4 assigns this cluster two open questions: whether std/ is admitted as rows in v1,
and the ~25-component vocabulary's actual roster. This ADR also fixes the v1 battery
roster, the erf `resource(...)` surface and its exact derivation outputs, the closed
semantic field-type roster, and the taak authoring surface.

Cross-ADR dependencies, stated explicitly:
- ADR-03 §6 already decided the frame: std evaluation is compiled into the kernel binary;
  every std definition is mirror-catalogued as an immortal product-scope row with a real
  ADR-02 hash and real deps; **the interpreter does not evaluate std from rows in v1**.
  This ADR specifies the row form, the genesis sequence, and the coherence seam ADR-03
  named.
- ADR-08 §2 defines the epoch as (kernel binary, std-manifest-root) with boot refusal on
  mismatch; genesis below is what first populates `epoch` and `std_manifest`.
- ADR-01 names std surfaces the grammar owes (`Iter`/`AsyncIter`, `keys`, `all`, `race`,
  `signal`) and bans ambient declarations — the native-body form must not reopen it.
- ADR-05 defines the continuation rows, wakes, step transaction, and condition schema
  the taak surface maps onto; ADR-04 §2 makes every transition serializable, and
  ADR-05 §3 / ADR-07 V5 run the capture verifier at every `await`.
- ADR-07 step 5a lists the derivation passes made exact in §4 below (V6 checks them
  total); ADR-09 projects `std/` read-only from the mirror rows, so a native-bodied
  definition's printed form must typecheck in an editor.

## Decision

### 1. std/ is admitted as rows in v1: YES as catalog rows, NO as interpreted code

**Verdict, grep-able:** std/ IS rows in v1 — every std definition is a real,
content-addressed, immortal product-scope row entering through one deterministic genesis
transaction — and std/ is NOT self-hosting in v1: no std body is evaluated by the
interpreter; every std definition dispatches to native Go keyed by its ADR-02 hash.

The row form: a std definition is a full dialect declaration — exported name, complete
type signature, contracts, docstring, real `deps` edges — whose body is a single
**`NativeBody` regel-AST node** carrying the intrinsic symbol name. `NativeBody` has a
canonical encoding and prints as a typecheckable stub (`regelNative<T>("std/mail.send")`),
but **the ADR-01 lowering has no production for it**: no source submitted through the
live gate can construct one, so a user- or agent-authored native body is structurally
unwritable, not merely rejected. This delivers prior-art's `native` floor without a new
ambient-declaration exception.

One source, four artifacts, verified equal: the std source tree (dialect signatures +
contracts + docstrings, Go implementations beside them) is built once per epoch into
(i) the genesis image (canonical AST bytes + hashes, computed at **build time** by the
real ADR-02 printer/encoder), (ii) the `std_manifest` + Merkle root ADR-08 pins,
(iii) ADR-07's L0 type surface, and (iv) the hash-keyed native dispatch table. CI admits
the entire image through the full ADR-07 pipeline in a build sandbox; the shipped binary
carries only the verified output. Nothing is canonicalized at boot — the red-path
proposal's KT-A1 discharge, adopted: two fresh databases cannot disagree unless the
binaries differ.

**Reserved seam (not built):** a future epoch may re-admit a pure-logic std definition
with a dialect body replacing its `NativeBody` (a `supersedes` re-admission per ADR-02
§6), moving it into interpreted evaluation per-definition — the self-hosting lane, the
mirror image of ADR-04 §7's AOT lane. v1 opens neither.

### 2. Genesis: the bootstrap sequence and its reproducibility kill-test

Genesis is the only gate bypass in the system, and it bypasses the gate only in time —
the gate ran at build, in CI, over the same bytes.

Fixed sequence against a fresh Postgres:
1. Kernel applies the embedded substrate DDL: ADR-03's five tables, ADR-05's
   `continuation`/`durable_condition`/`restart`, ADR-06's `task`, ADR-08's
   `epoch`/`std_manifest`, and this cluster's `subscription` (ADR-11 §5).
2. **One genesis transaction:** insert one `admission` row (`actor_kind='system'`,
   `via='cli'`); insert every std `definition` + `definition_meta` row from the embedded
   image, the kernel re-verifying `hash == SHA-256(domain ‖ ast)` per ADR-02 §5 g4 on
   each; insert product-scope `name_pointer` rows; insert `std_manifest` rows and the
   `epoch` row (n=1) — the epoch row now also pins the **native dispatch-table attestation
   hash** `H_dispatch = SHA-256` over the sorted `(intrinsic name, dialect signature hash,
   Go body hash)` triples of every native function in the binary's dispatch table (R1-09:
   dispatch-table attestation hash pinned in the epoch row; the `epoch.dispatch_attestation`
   column is DDL'd beside the ADR-08 §2 epoch pair, where the epoch table lives — flagged
   there, not authored here. R1-INT: pointer corrected — the epoch DDL is ADR-08's, not
   ADR-03's; the column now exists in ADR-08 §2). Any mismatch ⇒ ROLLBACK ⇒ refuse to serve. All-or-nothing: a crash
   mid-genesis leaves an empty catalog and the next boot retries identically.
3. Kernel registers the in-memory native dispatch table: hash → Go function, and asserts
   every catalogued `NativeBody` hash has a registered implementation (and vice versa).
   It then **recomputes `H_dispatch` from the running binary's own dispatch table and
   compares it to the value pinned in the current `epoch` row** (R1-09: boot-time
   attestation of the binary dispatch table). A mismatch — a shipped kernel whose native
   names, signatures, or Go body hashes differ from what the epoch attested, i.e. a
   swapped or tampered dispatch table — is a **structured boot-refuse**: the
   attestation-mismatch cause of the R1-05 boot-refuse diagnostic (ADR-08 §2 `event:
   "epoch.boot_refused"`, ADR-06 boot sequence), which extends that diagnostic with the
   pinned-vs-computed `H_dispatch` beside its existing `binary_version` field (the new
   diagnostic field is flagged in ADR-08, not authored here). The kernel does not open the
   gate on an unattested dispatch table — the binary is no longer a trust root taken on
   faith.
4. ADR-08 §2 boot check: embedded manifest root equals the catalog's current epoch root.
   Then the live gate opens.

Against a populated catalog, step 2 is skipped and steps 3–4 run as the standing boot
parity check — ADR-03 §6's "verified identical at boot," made an equality on one root
plus a dispatch-table bijection **and the `H_dispatch` attestation equality** (R1-09:
attestation re-proven at every boot): which native authority a running kernel actually
carries is pinned in the epoch row and recomputed-and-compared on every boot, so a
kernel serving a dispatch table the epoch never attested is impossible to bring live.

**Reproducibility kill-test (release gate):** boot the same binary against two fresh
Postgres instances; `SELECT hash, ast FROM definition ORDER BY hash` and the projected
`std/` tree (ADR-09) must be byte-identical across both, and byte-identical to the CI
sandbox's admitted output. Then kill the kernel mid-genesis at every statement boundary
and re-boot: the catalog is empty-or-complete, never partial.

### 3. The v1 battery roster

Rule of Three governs (a battery ships when the third product needs it; earlier needs
arrive as framework-authored, capability-gated bindings). The reference product that
fixes the envelope: orgs → users/roles → Deal/Company/Contact/Ticket → a follow-up and a
triage workflow → an operator desk. Nothing ships that this app and its red paths do not
exercise. Each SHIP names its worst failure → containment (grafted from red-path).

| Battery | Verdict | Worst failure → containment |
|---|---|---|
| `std/identity` | SHIP | privilege forgery → capability-audit (V1) + grants as rows (ADR-04 §5); orgs/users/roles/sessions/API keys ship in core — policy, horizons, audit build on it |
| `std/erf` | SHIP | derived artifact leaks PII or mis-scopes → V2 pii-flow + V3 catalog-parity + V6 derivation-parity in the same transaction |
| `std/taak` | SHIP | unserializable capture in production → V5 at admission; double effect → ADR-05 §7 step transaction |
| `std/ui` | SHIP | a component sinks a vault token to text → V2 over every component AST + the six masking leaves (§7) |
| `std/sql` | SHIP | injection / cross-org read → parameterized-only surface (no string SQL is expressible) + policy predicate injected by derivation, never by the author |
| `std/http` | SHIP (minimal) | exfiltration via outbound call → outbound client is a capability; V2 treats it as a sink; inbound routes are catalog rows (ADR-06 §4) |
| `std/time` | SHIP | timer never fires / fires twice → wakes are rows, exactly-once by ADR-05 §7 |
| `std/money` | SHIP | rounding error on a displayed amount → decimal intrinsic (no float), currency in the type, no implicit coercion (ADR-01) |
| `std/crypto` | SHIP (intrinsic-only) | key reuse / weak nonce → vetted AEAD+KDF only; no key material is a dialect value; keys live in external KMS |
| `std/mail` | SHIP (minimal: `mail.send`, plain body) | PII in a mail body → V2 treats the mail sink as a boundary (mask or reveal-grant, else reject) |
| `std/test` | SHIP | a fake diverges from its intrinsic → fakes are admitted rows checked against the intrinsic's contracts |
| `std/log` | SHIP (tiny) | PII in a log line → the log sink is in V2's sink set |
| `std/iter` | SHIP | (grammar-owed: `Iter`/`AsyncIter`/`keys`, ADR-01) kernel-owned serializable state per R2 |
| `std/contract` | SHIP | (grammar-owed: pre/post combinators, ADR-02 §3) purity enforced by V4 |
| `std/mime` | DEFER | arrives as a capability-gated binding until the third product |
| `std/csv` | DEFER | import-as-dry-run is v2; first Rule-of-Three candidate |
| `std/files` | DEFER | no reference-app consumer; widens the mask/validate surface |
| `std/i18n` | DEFER (translation rows) | locale **formatting** still ships, carried inside the `money`/`date`/`datetime` semantic types — not as a module |
| mail templates | DEFER | v1 sends plain bodies |

`all`/`race` live in `std/taak` with ADR-05 §5 join semantics; outside workflows they may
complete inline when every branch resolves without a deferred wake (ADR-04 §2).

BUILD-C (increment C2 — the governance-vocabulary slice the V2/V4/V5 verifiers need is
added minimal, behind the same seam, mirroring not inventing):
- **`std/pii`** (§4 item 5, §5 modifier): `Vault<T>` is the pii/vault-routed value type
  (V2's taint source — the admitted value form of a `pii(...)` field); `mask()` and
  `reveal()` are the masking + reveal-grant combinators (V2's only sanitizers).
- **`std/contract`** (§137): `pre`/`post` are the pre/postcondition combinators
  attachable to a definition (ADR-02 §3 — contracts are subset code in the body, mirrored
  to `definition.contracts`); V4 enforces their purity, the derivation seam derives a
  boundary-validator artifact per contract-bearing def.
- **`std/sql`** (§3 `std/sql` SHIP; §Red-Path "socket-typed value live across await"): a
  MINIMAL host-resource slice — `Conn` (a live connection handle with no encodable value
  tag) + `connect()` — is added now purely as the V5 capture-fixture substrate. The full
  parameterized-query surface (§3 worst-failure: injection/cross-org) lands at Stage D.

### 4. erf: `resource(...)` v1 surface and exact derivation outputs

Surface: `resource(name, fields, options)` with `options ⊆ {horizon, policy, actions}`;
actions v1 = the CRUD five plus named custom actions as ordinary dialect functions over
the row. Overlays arrive via `extendResource(name, patch)` — the Settings-as-admission
path, an ordinary scoped admission (ADR-03 §3).

The derivation (ADR-07 step 5a) emits **exactly ten artifacts, all inside the one
admission transaction**, each an explicit pass over the canonical AST:
1. schema DDL + forward migration (additive-only, V6);
2. history-partition wiring;
3. the boundary validator (`Deal.parse`) — types compile to validators, never erase;
4. the org/role policy predicate, injected into every derived read path (V3-checked);
5. a vault route per `pii(...)` field (V2-checked);
6. the **horizon**, as read-scope + invalidation key only — regel is server-side B2B, not
   local-first; eigen's device-sync machinery is out of envelope; the horizon survives as
   the policy visibility filter and the ADR-11 subscription key, one artifact serving
   both roles;
7. derived `form(R)` and `table(R)` components (tier-2, §7);
8. REST + OpenAPI;
9. per-resource MCP tools (ADR-12 §2);
10. the catalog row itself.

Cross-resource aggregates and computed fields are deferred (Rule of Three); dashboards
ride typed `std/sql` queries in v1.

### 5. The closed semantic field-type roster: 13 base types + 1 modifier

Each base type carries its validator, input control, renderer, mask behavior, and locale
rule — the closed bundle is what makes derivation total (V6).

1. `text` 2. `longtext` 3. `number` 4. `money` (decimal + currency, locale render)
5. `boolean` 6. `date` 7. `timestamp` 8. `email` 9. `phone` 10. `url`
11. `address` (composite) 12. `select`/`states` (closed enum; `states` adds
ordered-history semantics and drives `badge`/`board`) 13. `relation`
(belongsTo/hasMany; FK + target-horizon policy predicate).
**Modifier:** `pii(<base>)` — wraps any base type; routes the value to the vault, masks
by default, never enters the history stream.

**Excluded from v1, each with its reason:** `multiselect` (not a 14th semantic type —
ships as verifier-checked sugar over `relation` when the reference app earns it; see
below), `file`/`attachment` (std/files deferred), `json` (an escape hatch from derivation
totality), `richtext`/`markdown` (rendering surface with no v1 consumer), `percent`,
`duration`, `geo`, `id`-as-declared-field (every resource derives its typed key
automatically), `computed` (deferred with aggregates). Every exclusion is deletable
mask/validate surface.

**Vocabulary-addition policy — the reversibility asymmetry (R1-14: bias-to-defer principle governing every closed roster).**
The two operations on a closed roster are not symmetric. Every *exclusion* above is
deletable mask/validate surface — a deferral is reversible by construction and costs at
most one unimpressive demo quarter, added the moment a real consumer appears. Every
*addition* — a field type, a battery (§3), a component (§7) — is an immortal product-scope
row and permanent semantics (Context; "every std behavior change is a new hash in a new
manifest, never a mutation", Consequences): it widens the totality/masking proof for as
long as the kernel lives, and a primitive designed at N=1 is carried forever, wrong axes
and all. The rosters therefore **bias to defer**: an addition earns its place only against
a *measured* consumer, never a hypothesized one. Because that bias could silently hide a
real gap, it carries **honesty riders** rather than blanket deferral — the one gap the
roster's own reviewers named as load-bearing (charts/aggregation, §4/§7) is validated or
falsified *early*, by a deliberately analytics-shaped second product and a stranger-review
gate on the reference dashboard (both mechanized at ARCHITECTURE §5/§5.1, M6), not left to
be proven by accretion.

**`multiselect` as verifier-checked sugar (R1-14: desugars to `relation`, no new epoch surface, no new native TCB).**
`multiselect` is **not** admitted as a 14th semantic field type — a new type would add an
immortal roster row, a new mask bundle, and a new totality pair (§7). It ships instead as
framework-authored sugar in `resource(...)` that **desugars, at derivation time, to the
existing `relation` base type (hasMany) plus a `select`-multi control** — the exact
derivation the excluded-type note always pointed to ("model as a `relation`"), now the
right *doorknob* for it. The desugared output is **byte-identical to the hand-written
`relation` form** a tenant would otherwise author, and V6 derivation-parity checks that
equality, so the sugar can never fork semantics; it reuses `relation`'s validator, policy
predicate, and V2/V6 proof entirely and adds **no new field-type row, no new mask bundle,
no new totality pair, and no new native TCB**. It enters v1 **only if the reference product
(§3) actually exercises a tag field** (e.g. a `multiselect` on Deal, exercised by the
acceptance harness); absent that live consumer it defers as the first epoch-addition
candidate. This deletes the tenant-visible abstraction leak — being told to model tags as a
foreign key — without widening any proof.

### 6. taak: the v1 authoring surface — await-as-checkpoint

A workflow is a definition of `kind='workflow'`: a plain async function. There is no
step-wrapper API. **Every `await` of an effectful capability call in a workflow is a
durable checkpoint**, executed as one ADR-05 §7 step transaction (claim CAS →
effect/intent → CFR checkpoint → COMMIT). The mapping:

- Each capability declares an **effect class** in its std signature, verifier-visible:
  `read` (kernel performs the call inline, no checkpoint — re-execution after a crash is
  safe and cheap), `write` (the call's SQL and the checkpoint commit in one step
  transaction — exactly-once by Postgres), `external` (the step transaction writes an
  ADR-05 outbox intent keyed `(continuation_id, step_seq)`; the await resolves on commit
  as queued; ADR-06's dispatcher delivers — effectively-once, the stated honest limit).
- `taak.sleep(dur)` parks with a `timer` wake; `taak.receive(T, match?)` with a `message`
  wake; `taak.signal(condition, restarts)` writes ADR-05 §6 rows and parks `manual`;
  `taak.all/race` spawn children and park with a `join` wake. All are ordinary std
  functions — suspension remains `await`, ADR-01's sole surface.
- In non-workflow kinds (handlers, components), awaits complete inline or park as
  `kind='request'` continuations per ADR-04/06; no durable checkpoints.

The three proposals' `wf.step`-wrapper design is overruled (see Alternatives): with
ADR-04's everywhere-serializable machine and ADR-05's every-await capture verifier, the
wrapper's boundedness rationale is void, and an awaited-but-unwrapped effect would
re-fire on crash recovery — the wound exactly-once exists to delete. Honest cost, stated:
every write/external await in a workflow costs one Postgres transaction; the envelope is
I/O-bound and this is the price of the guarantee.

### 7. THE COMPONENT VOCABULARY

**Tier-1 — the closed 25 semantic primitives** (headless, CSS-var themed; the six
value-binding **masking leaves** are marked ◆ — masking lives at exactly these six and
nowhere else):

1. **page** — document/session root; owns title, chrome, the patch root and session binding.
2. **section** — labeled region; grouping + ARIA landmark.
3. **stack** — one-axis flow layout (horizontal/vertical).
4. **grid** — two-dimensional responsive layout.
5. **nav** — navigation region between views; ARIA nav.
6. **heading** — semantic title with levels; document outline.
7. **text** ◆ — prose run; value-binding leaf, masking-aware when bound to a `pii` value.
8. **label** — static caption; the static half of a bound field.
9. **badge** ◆ — enum/status chip rendering a `states()` value with intent color.
10. **money** ◆ — renders a money value, currency + locale.
11. **datetime** — renders date/timestamp with locale; `pii`-wrapped temporal values are
    routed by derivation to `text`'s masked binding, keeping the leaf count at six.
12. **avatar** ◆ — person/org identity token; masking-aware for `pii` names.
13. **icon** — semantic pictograph from a closed set; no free value binding.
14. **link** — navigation trigger (href to a view).
15. **button** — action trigger; the event source of the reactive loop; renders restart choices.
16. **field** ◆ — polymorphic single input bound to a resource attribute; the control is
    chosen by semantic type — the derivation atom on which `form`, validation, and
    masking hinge.
17. **select** — closed-choice control rendering `states()`/`relation` options.
18. **checkbox** — boolean control (switch is a themed variant).
19. **dialog** — modal surface with focus trap; hosts modal forms and the reveal-grant prompt.
20. **card** — bounded composition unit; the record surface.
21. **list** — semantic vertical keyed collection.
22. **table** ◆ — columnar collection (header/rows/cells); cells are the sixth masking leaf.
23. **alert** — inline status/error/notice; surfaces validation errors and durable conditions; ARIA live.
24. **spinner** — pending indicator.
25. **empty** — empty-state placeholder.

**Tier-2 — derived surfaces** (composed from tier-1, derived from resources):
`form(R)` (fields + validation + masking from the declaration; submit = mutation or
admission); `dataTable(R, view)` (columns/sort/filter/paginate, masked cells);
`detail(R)` (record page: card + related tables); `board(R, groupBy)` (kanban over a
`states()` field); `dashboard` (grid of stat tiles + tables over typed `std/sql`
queries); `operatorPlane` (ADR-12 §6: condition inbox with restart buttons, approval
queue, masked impersonation, catalog/audit browse). Settings forms are a `form` variant
over `extendResource`.

**Exclusion rationale** (grafted from prior-art): tooltip (`title`/ARIA on existing
primitives), tabs/accordion (section + button + visibility), menu/dropdown (button +
list/dialog), breadcrumb (nav + text), toast (alert suffices), pagination (inside
`dataTable`), carousel (anti-pattern for the envelope), rich-text editor (`longtext`
via field), charts (a closed chart vocabulary is its own project; dashboards use stat
tiles + tables — deferred under the §5 reversibility-asymmetry bias, with the honesty
riders recorded below), date-range picker, file upload (std/files deferred), maps, calendar.
The set is closed because derivation must be total: each of the 13 field types maps to
exactly one input primitive and one render primitive, so `form()`/`table()` can never
meet a field they cannot render — a wider set breaks that proof for no envelope gain.

**Charts/aggregation — deferred with riders, not by neglect (R1-14: analytics-shaped product #2 + M6 stranger-review gate keep the deferral honest).**
Charts and cross-resource aggregates (§4) are the single gap the roster's own reviewers
named as load-bearing, so this deferral is deliberately *not* left to be validated by
accretion. Under the §5 reversibility asymmetry a chart family designed at N=1 — one CRM
dashboard — becomes wrong immortal kernel surface, so it stays out of the v1 roster; but
two binding riders (mechanized at ARCHITECTURE §5/§5.1) keep the deferral honest rather
than convenient: (i) the **second product built on regel must be analytics-shaped** — chosen
deliberately chart/aggregation-hungry so it tests roster closure at exactly this known gap,
and the roster may not be declared closed until tier-1 composition (stat tiles + tables over
typed `std/sql`) is *measured* insufficient against a real analytics product, or a chart
epoch-addition is specced from *two* products' requirements; (ii) a **stranger-review gate
on the reference dashboard at M6** — a reviewer outside the project judges "does this look
finished?" on the stat-tile/table dashboard and their verdict is recorded as an M6 gate
entry, so the absence of charts is falsified (or cleared) by a human before v1, not by a
customer ticket after. A chart family enters only by epoch addition, specced against two
products, never a v1 roster row.

**Escape-hatch policy, decided: there is no raw-HTML primitive and no `unsafeHtml`.**
A raw-string HTML sink defeats the six-leaf masking proof — one `unsafeHtml(dealNotes)`
and a vault token crosses unmasked. Custom polish is delivered streng's way: a bespoke
component is authored as a tier-1 composition and admitted through the gate, where V2
walks its AST — a hand-authored component that binds a `pii` value anywhere but the six
masking leaves fails admission exactly as a raw string would. Polish overlays derivation;
a hand-built component admits into the same slot a derived one filled.

### 8. Native-TCB adversarial harness (release gate)

The verifier suite (ADR-07 §4) is the security boundary *above* the native floor, but the
native-Go std bodies — vault routing, `std/sql` policy injection, `std/http` egress,
`std/crypto`, the derivation passes — hold the real authority and no verifier examines
them. That floor is the TCB, drawn one ring too small when "verifier coverage IS the
boundary" is read to exclude it. v1 attacks the floor directly rather than trusting it on
tests alone.

**The harness** (`gate/native-tcb/`, co-equal with ADR-07 §5's `gate/redpath/`) seeds
deliberately-malicious native std bodies and proves the surrounding machinery — the vault
CHECK (ADR-03 layer 1), capability routing (ADR-04 §5), the six verifiers (ADR-07 §4), and
the ADR-04 §6 regel-native differential oracle — catches each; or, where no surrounding
control can, documents exactly what the TCB is trusted for. Three seeded classes, each a
fixture family:

- **vault-leaking:** a native body (e.g. `std/http.get`, `std/log.write`, a vault-routing
  pass) that sinks an unmasked `pii`/vault value. The surrounding control that must catch
  it: the vault CHECK plus V2 pii-flow over the *caller's* AST and the six masking leaves
  (§7) — the harness proves a native body cannot become a laundering path around masking.
- **contract-violating:** a native body whose runtime behavior diverges from its declared
  dialect signature/contract (returns out-of-type, skips a declared postcondition). The
  control: the ADR-04 §6 differential oracle (production machine vs. independent reference
  reducer) and V4 boundary validators — a seeded contract-violating body must turn the
  oracle red (ADR-07 §5, R1-02).
- **effect-order-violating:** a native body whose declared effect class (`read`/`write`/
  `external`, §6) is a lie — a `read`-declared body that writes, or a `write` body that
  fires an unrecorded `external` effect. The control: effect-class ordering in the
  differential oracle plus the ADR-05 §7 step-transaction accounting — a mis-declared
  effect class must diverge (this is the std-conformance build gate of §6's red-path,
  strengthened to an adversarial seed rather than an accidental bug).

**Release gate (do not weaken).** The harness is **release-blocking** on the same standing
as ADR-07 §5's hostile corpus — "a green result on a hostile fixture fails the release":
a seeded malicious native body the surrounding machinery fails to catch, *or* a
caught-only-by-documentation case whose trust assumption is not written down, turns the
release **red** (R1-09: native-TCB harness is a release gate). Its coverage is carried as
`verifier_coverage`-style **monotone** rows keyed on the native-TCB threat classes
(vault-leak, contract-violation, effect-order), so a class can never be silently dropped
once added. Where a native body's authority genuinely cannot be externally checked — the
irreducible TCB, e.g. `std/crypto`'s vetted AEAD primitive trusted to be the vetted
implementation — the harness records that as an explicit **trusted-for** statement rather
than a passing catch: the TCB is *stated*, never silently assumed. The two Schneier gaps
close together — this harness attacks the native floor, §2's attestation pins the binary
that holds it.

## Alternatives Considered

- **prior-art-faithful: pure-TS std admitted through the live gate at genesis and
  evaluated from rows; a std-only ambient `native` declaration; 15-module roster with
  mime/csv/files/thin-i18n/mail-templates; 22 field types.** Rejected: interpreting std
  bodies contradicts ADR-03 §6 verbatim; boot-time canonicalization is the KT-A1 drift
  risk; the ambient `native` form reopens ADR-01's ban where the `NativeBody` node (no
  lowering production) achieves the floor inside the rules; the roster breadth spends
  edge #6 on batteries the reference product never exercises. Grafted: the
  Smalltalk/Unison/Python/Go hybrid survey as §1's canonical argument, the Ash-faithful
  actions surface, locale-formatting-inside-semantic-types, and the §7 exclusion table.
- **simplest-thing: facade rows carrying the derivation logic in TS with intrinsic
  dispatch only at the floor; `wf.step` wrapper API; 14 field types; the 25-roster.**
  Its roster is adopted verbatim (identical to red-path's) and its reference-app razor
  governs §3. Rejected: the facade reading implies interpreting non-intrinsic std from
  rows (ADR-03 §6); the `wf.step` wrapper is overruled per §6 — its boundedness rationale
  predates ADR-04/05 and an unwrapped awaited effect re-fires on recovery.
- **red-path-first (winner): genesis by build-time canonicalization, worst-failure-per-
  battery, six masking leaves, no-unsafeHtml.** Carries §§1–3, 5, 7. Corrections: its
  facade-hash dispatch is restated as `NativeBody` + dispatch-table bijection so the
  ADR-03 seam is a checkable equality; its `wf.*`-only checkpoint design is overruled by
  §6 for the same reason as simplest's (its own KT-A4 is discharged *stronger* by V5 at
  every await); its four-site capture claim is deleted — capture discipline is ADR-01
  R1–R5 + V5, everywhere.

## Consequences

- The catalog is complete from the first boot: app imports resolve to real std hashes,
  I1/I2 hold with no carve-outs, as-of closes over std versions, ADR-09 projects a
  compiling `std/` tree — while the interpreter executes zero std bodies in v1.
- Every std behavior change is a new hash in a new manifest (ADR-08), never a mutation;
  the dispatch bijection makes a binary/catalog mismatch a boot refusal, never drift.
- Native-implementing all shipped std is the accepted v1 tax, bounded by the tight §3
  roster; the reserved self-hosting seam prices its future reduction per-definition.
- Await-as-checkpoint makes workflow authoring zero-ceremony and every effect
  exactly-once, at one transaction per effectful await — a visible, uniform cost.
- Six masking leaves plus no-raw-HTML make the PII render obligation a finite,
  admission-verified set of code sites — a property, not a review. Deferred batteries
  and excluded components are named; each arrives later as a framework-authored binding
  or an epoch addition, never a registry install.
- Roster growth is governed by the reversibility asymmetry (R1-14: bias-to-defer, honesty riders): the closed rosters bias
  to defer because a deferral is deletable and an addition is immortal epoch surface. The
  one gap the reviewers named — charts/aggregation — is kept honest, not by faith, by an
  analytics-shaped product #2 that tests closure at that gap and an M6 stranger-review gate
  on the reference dashboard (ARCHITECTURE §5/§5.1); and `multiselect` is admitted only as
  `relation`-desugaring sugar, gated on the reference app growing a tag field — no new epoch
  surface and no new native TCB, its purity checked byte-identical by V6.
- The native TCB is no longer outside the perimeter (R1-09: TCB attacked + binary attested): the native floor is attacked
  by a release-gating adversarial harness (§8) and the binary that holds it is attested at
  boot against the epoch-pinned `H_dispatch` (§2). Schneier's two gaps — the TCB gets
  "tests, not proofs" and the binary is an unattested trust root — close as data (monotone
  harness rows) and as a boot equality, not as a promise.

## Red-Path Tests Implied

- **Genesis reproducibility** (§2 kill-test): two fresh databases, byte-identical std
  row sets and projections; mid-genesis kill at every statement boundary ⇒
  empty-or-complete, never partial.
- **Dispatch bijection:** strip one Go implementation from a test binary ⇒ boot refusal
  naming the orphan hash; an unregistered extra implementation ⇒ same.
- **NativeBody unwritability:** submit source containing the printed native stub through
  the live gate ⇒ rejected at lowering (no production), stable diagnostic.
- **PII derivation** (KT-A3): a `pii` field whose form/table derivation lacks a vault
  route or masking rule ⇒ V2/V6 reject; the row never exists.
- **Capture at any await** (KT-A4): a socket-typed value live across *any* workflow
  await — not only a wake call — ⇒ `CAPTURE_UNSERIALIZABLE` at admission.
- **Exactly-once per await:** crash between a `write`-class await's effect and its
  checkpoint at every boundary ⇒ resume replays from the last checkpoint, effect exists
  once; an `external`-class intent is delivered once by dedup key. A capability declared
  `read` that writes inside its intrinsic ⇒ caught by the std conformance build gate.
- **Roster totality:** for every (field type × form/table/detail) pair, derivation
  emits a render — enumerated product test; a semantic type without a mask rule on a
  `pii` wrap ⇒ `DERIVE_PARTIAL`.
- **Escape-hatch absence:** grep the admitted grammar and std surface for any
  string-to-markup sink ⇒ none exists; a custom component binding `pii` to a non-leaf ⇒
  V2 reject (ADR-07's corpus gains the fixture).
- **`multiselect` sugar is pure expansion** (§5 — R1-14: sugar desugars byte-identically to `relation`): if the reference app exercises a tag field, the `multiselect` sugar and the hand-written `relation` (hasMany) + `select`-multi form must derive **byte-identical** rows/artifacts; any divergence ⇒ V6 derivation-parity failure and the sugar is inadmissible. Absent a tag-field consumer the sugar is not admitted (it defers as the first epoch-addition candidate), so it can never enter v1 as unexercised roster surface.
- **Seeded evil native body** (§8 — R1-09: malicious std body caught or release stays red): a native `std/http.get` that
  egresses an unmasked `pii` value ⇒ the vault CHECK + V2 over the caller catch it and the
  release stays **red** until it is caught; a native body that lies about its effect class
  or violates its declared contract ⇒ the ADR-04 §6 differential oracle diverges and the
  release is red; an irreducibly-trusted body (`std/crypto` AEAD) ⇒ an explicit
  `trusted-for` row, never a silent pass. Native-TCB coverage is monotone.
- **Boot attestation mismatch** (§2 — R1-09: tampered dispatch table refuses boot): a test binary whose dispatch table is altered
  after the epoch pinned `H_dispatch` — a renamed intrinsic, a changed signature, a swapped
  Go body — ⇒ boot recomputes `H_dispatch`, finds the mismatch, and **refuses to serve**
  with the structured R1-05 boot-refuse diagnostic (`epoch.boot_refused`) naming pinned vs.
  computed; the gate never opens on an unattested table.

## Constraints Discharged or Budgeted

1. **Budgeted.** Await-as-checkpoint rides ADR-04/05's everywhere-serializable machine;
   capture discipline stays grammar + V5, with no site-bounding carve-out to maintain.
2. **Budgeted.** All shipped std is native Go (no interpreter tax on the world's floor);
   heavy lifting stays in `std/sql`; the self-hosting seam is reserved, mirror to AOT.
3. **Consumed.** `taak.signal` is the durable-condition write path; restarts render per
   ADR-05 §6 into the tier-2 operator plane (ADR-12).
4. **Discharged.** Ten derivation artifacts as explicit AST passes; the static/dynamic
   split (ADR-11) and the component roster are AST-level facts — the derivation layer
   works exactly as hard as constraint #4 predicted, and V6 checks it total.
5. **Budgeted.** Worst-failure→containment per battery states the verifier obligation
   battery-by-battery; the six-leaf masking proof is V2's render-path coverage claim.
6. **Discharged for this cluster.** Five deferrals, 13 field types, 25 components,
   plain-body mail, genesis-not-self-hosted: every cut is named and additive later.
