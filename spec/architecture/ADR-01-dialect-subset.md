# ADR-01: The regel dialect subset

## Status

Accepted — Phase 1

## Context

BRIEF §4 requires the exact strict-subset grammar: which TS7 constructs are admitted,
which are banned, how the bans are enforced, and the capture discipline that makes
constraint #1 (continuations serialized stably for years) a grammar property rather than
a runtime hope.

Cross-ADR dependencies, stated explicitly:
- This subset is chosen so the canonical printer (ADR-02) is **total**: every admitted
  construct has exactly one canonical rendering and one binary encoding. Nothing is
  admitted here that ADR-02 cannot print or encode deterministically.
- Identity is defined on the **owned regel-AST** (ADR-02), never on tsgo's AST or on
  source text. tsgo is a parser/typechecker we feed; a tsgo version bump cannot move a
  hash.
- The capture discipline below is what makes ADR-03's immortal definition rows a
  sufficient anchor for serialized environments: every capturable value is either a
  content-hash pointer into the catalog or a member of a closed serializable lattice.

## Decision

### 1. Shape of the definition

The dialect is a **whitelist of regel-AST node kinds** (default-deny). A submitted
module is decomposed at admission into its top-level declarations; each declaration is
one definition row (ADR-03). Import statements are consumed by the gate (resolved to
content-hash references, ADR-02) and are not part of any definition's body.

### 2. Banned constructs (each with its one-line reason)

| Construct | Verdict | Reason |
|---|---|---|
| `class` / `extends` / `implements` / `#private` / class accessors | BANNED | Prototype identity and method-`this` are unserializable and unhashable; data is shapes, behavior is functions |
| `this` (every form), `this`-typing, `.call` / `.apply` / `.bind` | BANNED | No receiver exists to capture, version, or rebind — the single ban that makes closures serialize as (hash, env) |
| decorators | BANNED | Runtime definition mutation; derivation is explicit AST passes, never reflected metadata |
| getters / setters (object literals included) | BANNED | Computation hidden behind property access defeats canonical printing, capability visibility, and PII-flow analysis |
| `var` | BANNED | Function-scope hoisting makes capture sites unstable; `let`/`const` only |
| `enum` (incl. `const enum`) | BANNED | Runtime reverse-map emission and nominal quirks; use string-literal unions / `states(...)` |
| `namespace`, module augmentation, `declare`, ambient anything | BANNED | Non-content-addressable name scopes; modules are files, files become rows |
| `new` (all uses) | BANNED | No constructors exist in the dialect; std exposes factory functions; structurally kills `new Function` and `new Promise` |
| `instanceof` | BANNED | Prototype identity test over a dialect with no prototypes |
| `delete` | BANNED | Shape mutation breaks canonical shapes and PII-flow tracking |
| `function*` / `yield` / user generators, async generators | BANNED | A second suspension surface doubles the continuation serializer (constraint #1); laziness routes through std `Iter<T>` / `AsyncIter<T>` with kernel-owned serializable state |
| `Symbol`, computed symbol keys, well-known-symbol protocols | BANNED | Non-serializable runtime identity and hidden protocol hooks |
| labeled statements, labeled `break`/`continue` | BANNED | Structured control only; keeps the CPS control-flow graph small |
| `for-in` | BANNED | Prototype-enumeration semantics; use `for-of` over `keys(...)` |
| `with`, `eval`, `Proxy`, `Reflect`, sloppy mode | BANNED | Inherited streng bans; dynamic scope and interception are ungovernable |
| tagged templates | BANNED | Arbitrary tag code over literal structure; std builders are ordinary functions |
| comma operator, `void` | BANNED | Hidden sequencing / expression with no honest value |
| floating promises (a `Promise`-typed expression statement) | BANNED | An un-awaited effect is an un-checkpointed effect; a Promise may only be awaited, returned, or passed to a std structured-concurrency combinator (`all`, `race`) |
| `debugger` | BANNED | Host-debugger hook; no meaning in the kernel |
| `any`, `as` (except `as const`), `<T>x` assertions, non-null `!` | BANNED | The visible lies; `unknown` + narrowing is the honest path |
| `Function` type, `object` type | BANNED | Untyped callable / untyped shape — `any` smuggling routes |
| regex backreferences, lookahead/lookbehind | BANNED | Backtracking (ReDoS) is unrepresentable; the engine is RE2 |
| non-finite numeric literals (e.g. `1e400`), lone surrogates in strings | BANNED | No canonical encoding exists for them (ADR-02) |
| non-ASCII identifiers | BANNED | Deletes the unicode-confusability and normalization pond; identifiers are `[A-Za-z_$][A-Za-z0-9_$]*`; human language lives in string literals and i18n |

### 3. Admitted surface, stated positively

**Statements.** `const`/`let` declarations; expression statements; `if`/`else`;
`switch` over literal-union discriminants (case labels are literals; a non-empty case
must end in `break`, `return`, `continue`, or `throw` — no fallthrough); `for(;;)`;
`for-of` over arrays and std `Iter<T>`; `for await-of` over std `AsyncIter<T>`; `while`;
`do-while`; unlabeled `break`/`continue`; `return`; `throw`; `try`/`catch`/`finally`;
block; `function` declarations (sync and `async`); `import`/`export` resolving against
`std/` and `app/` only.

**Expressions.** Literals: number (finite f64), `bigint`, string, template literal,
boolean, `null`, `undefined`, array, object, regex (RE2-safe). Arrow functions (sync and
`async`), `function` expressions; calls; member access (dot, and computed with
string/number keys); spread/rest; array and object destructuring; `?.`; `??`; ternary;
arithmetic, comparison, logical, and bitwise operators; compound assignment to `let`
bindings and object properties; `typeof`; `in` (own-key test — dialect values have no
prototype chain, so own-key IS the semantics); `await`; `as const`; `satisfies`.
Object-literal method shorthand is admitted as sugar and canonicalized by the printer to
an arrow-function property (semantically identical in a `this`-free dialect).

**Types (tsgo-checked; IN the hash per ADR-02).** Primitives; literal types; `interface`
and object types; `type` aliases; unions/intersections; arrays/tuples; generics with
explicit constraints; `readonly`; index signatures (value type must not be a banned
type); `keyof`, indexed access, `typeof`-type; conditional, mapped, and template-literal
types (the derivation layer's working surface); function types and overload signatures;
`unknown`.

**Exceptions semantics.** `throw`/`try`/`catch`/`finally` are **within-evaluation**
control flow: the CPS transform carries a handler-frame stack, so unwinding may cross
`await` and `finally` re-runs correctly on resume. The `catch` variable is `unknown`.
Durable failure is never `throw`: a workflow step signals via the std
`signal(condition, restarts)` API, which writes a durable-condition row. An uncaught
`throw` aborts the evaluation turn, rolls back its transaction, and the kernel records a
fault row.

**No ambient globals.** The dialect has no `Math`, `Date`, `JSON`, `console`, or any
other ambient binding beyond literals and the admitted operators. All of these are
`std/` imports; nondeterminism (time, randomness) is reachable only through capability
handles bound in the environment. The exact std module roster belongs to the world
cluster; the grammar-level fact decided here is: **zero ambient value bindings**.

**Dependency acyclicity.** A definition may reference itself (self-recursion; the
self-name is a reserved node, ADR-02). Mutual recursion **across** definitions is
rejected at admission (dependency cycle error) — the Merkle DAG stays a DAG. The
fix-in-the-error: merge the cycle into one definition or route through a std dispatch
table.

### 4. Enforcement mechanism

One pipeline, all stages inside the admission transaction (ADR-03), in this order:

1. **Parse** with vendored tsgo. Parse failure rejects.
2. **Lower** the tsgo AST to the owned regel-AST. The lowering is default-deny: a tsgo
   node kind with no regel-AST production rejects with a stable diagnostic code. New
   syntax in a future tsgo fails closed.
3. **Grammar gate** over the regel-AST: every ban in §2, switch discipline, floating-
   promise check, acyclicity, and the capture discipline (§5). Every rejection carries
   the std replacement in the diagnostic ("fix in the error").
4. **Canonical print + hash** (ADR-02).
5. **tsgo typecheck** of the canonical text against the catalog graph, under the locked
   config: `strict`, `noImplicitAny`, `exactOptionalPropertyTypes`,
   `noUncheckedIndexedAccess`, `useUnknownInCatchVariables`, `noUnusedLocals`,
   `isolatedModules`, `verbatimModuleSyntax`, module resolution pinned to a closed
   resolver over `std/` + `app/` only. What is stored is exactly what was typechecked.
6. **Verifier suite**, then insert (ADR-03).

**Governor.** "Unmetered" trusted code means un-billed, not un-bounded: every evaluation
turn (trusted included) runs under a generous kernel step/wall ceiling; breach signals a
`runaway` durable condition and rolls the turn back. Fuel pricing applies to the sandbox
tier only; the governor applies to all. A type-correct `while(true)` cannot hang the
kernel.

### 5. Capture discipline (constraint #1, grammar-enforced)

The grammar gate computes every closure's free-variable set. All five rules are
admission-time errors, never runtime checks:

- **R1 — const-only capture.** A closure may capture module-level bindings, parameters,
  and `const` locals. Capturing a `let` that is reassigned anywhere in an enclosing
  function is rejected (fix: bind a `const` copy). Captured environments are immutable
  snapshots; there is no by-reference aliasing to serialize.
- **R2 — serializable lattice.** Every captured value's type must lie in the closed
  lattice: primitives, `string`, `number`, `bigint`, `boolean`, `null`, `undefined`,
  arrays/tuples/records of lattice members, literal-union tags, std opaque handles that
  declare their own serialization (ids, capability tokens, `Iter` state), and functions
  per R3. Live resources (connections, sockets) are never values — they are reached only
  through capability calls — so nothing unserializable exists to capture.
- **R3 — functions as values.** A captured function is either a module-level definition
  (serialized as its immortal content-hash pointer, ADR-03) or a closure whose own
  captures satisfy R1–R5 recursively.
- **R4 — no receiver.** Follows from the `this` ban; frames serialize as
  (definition-hash, program counter, locals) with no hidden slot.
- **R5 — no ambient nondeterminism.** Follows from zero ambient globals: time and
  randomness are capability handles, so they arrive in the environment, never as free
  variables. The environment is the policy.

## Alternatives Considered

- **simplest-thing:** admitted user generators and banned regex literals. Rejected on
  both: generators are a second suspension surface that widens the deepest bet
  (constraint #1), and the regex ban fights corpus fluency where RE2 semantics carry the
  construct safely. Its const-only capture rule (R1) and whitelist-visitor cheapness are
  adopted.
- **prior-art-faithful:** admitted generators (rejected, as above) and proposed the
  three-authority gate with a locked tsgo config, the `new` ban, and the ambient-
  nondeterminism capture rule — all three grafted here. Its lower-from-tsgo enforcement
  replaces the winner's owned parser.
- **red-path-first (winner):** banned `throw`/`try`/`catch` and proposed an owned parser
  day one. Both overturned: exceptions are the corpus's most common idiom and are
  CPS-transformable with handler frames (durable conditions remain a separate data API);
  the owned parser is a second-engine cost that lowering from vendored tsgo avoids while
  keeping identity on the owned AST. Everything else — one suspension surface, floating-
  promise ban, serializable lattice, governor, default-deny — is its design and stands.

## Consequences

- The interpreter implements exactly one suspension mechanism (async/await CPS) and one
  value model (plain data, no prototypes, no receivers); every ban above is engine it
  never builds.
- Corpus friction is concentrated and predictable: no classes, no generators, no `new`.
  Models write the admitted surface fluently; the gate teaches the rest via
  fix-in-the-error diagnostics.
- `for (let i = 0; …)` loop variables are reassigned and therefore uncapturable (R1);
  the idiomatic replacement is `for-of`. This is deliberate stiffness, not an oversight.
- A future tsgo cannot change what regel admits (default-deny lowering) or what it
  stores (identity on the owned AST).

## Red-Path Tests Implied

- One rejection fixture per banned row in §2, each asserting a stable diagnostic code
  and a std replacement in the message — written before the interpreter exists.
- Capture kill-tests: reassigned-`let` capture across `await` rejected; closure
  capturing a value, checkpointed across `await`, resumed after kernel restart on a
  different process, observes the exact snapshot.
- Floating-promise fixture: a bare `sendMail(x)` expression statement rejected;
  `await`ed and `all(...)`-wrapped forms admitted.
- Governor: type-correct `while(true)` in trusted tier signals `runaway`, rolls back,
  kernel stays live.
- Fuzz: random token streams and random subset-valid ASTs — every input yields clean
  rejection or stable admission; never a crash or partial admit.
- Acyclicity: two mutually recursive definitions rejected with the cycle named.

## Constraints Discharged or Budgeted

1. **Discharged at the grammar.** R1–R5 + one suspension surface + acyclic deps make
   capture serialization a property of what can be admitted at all.
2. **Budgeted.** Every ban shrinks the owned interpreter; async/await + std `Iter` is
   the entire suspension surface it owes.
3. **Budgeted (interface reserved).** `signal(condition, restarts)` is the durable-
   failure path; `throw` is explicitly not it. Schema lands in the taak cluster.
4. **Discharged jointly with ADR-02.** The whitelist IS the AST schema's surface; every
   admitted node has one canonical form.
5. **Budgeted, honestly.** The visible lies are banned; structural-variance holes
   survive and are the verifier suite's stated burden, not the type system's.
6. **Budgeted.** No owned parser; one gate pipeline; rejection fixtures precede
   features (red-path-first staging).
