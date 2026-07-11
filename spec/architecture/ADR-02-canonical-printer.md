# ADR-02: Canonical printer, AST encoding, and content addressing

## Status

Accepted — Phase 1

## Context

BRIEF §4 requires the canonical printer spec and hashing scheme: what byte-form is
hashed, the full normalization rules, what is inside vs outside the hash, the algorithm
and encoding, the round-trip guarantee, and how the AST schema versions without moving
existing hashes. This fixes identity for the whole system — constraint #4's
approximated homoiconicity is delivered exactly here (AST schema + one rendering +
hash), and constraint #1's serialized environments reference these hashes for years.

Cross-ADR dependencies, stated explicitly:
- ADR-01's whitelist is this ADR's totality guarantee: every admitted node kind has
  exactly one normalization, one encoding, and one printed rendering. The printer and
  encoder are total over the subset by construction.
- The address defined here (`r<n>_…`) is ADR-03's primary key on `definition` and the
  only identity continuations, deps, and name pointers ever reference.

## Decision

### 1. What is hashed: canonical AST bytes, not text

`hash = SHA-256( domain ‖ canonEncode( normalize( regelAST ) ) )` where
`domain = "regel-ast/" + n + "\n"` and `n` is the AST-schema version.

Canonical **text** is a derived projection of the stored AST — for humans, the git
projection, and tsgo input — defended by a round-trip property (§5), never a hash
input. This severs identity from formatting: the printer can improve without re-hashing
the world, and a vendored-tsgo bump cannot move a hash because the encoding is over the
owned regel-AST only.

### 2. Normalization rules (applied before encoding)

- **Trivia.** Whitespace, semicolons, quote style, parentheses that only restate
  precedence: absent from the AST, irrelevant to the hash.
- **Comments.** Stripped from the AST into a node-path-keyed sidecar
  (`definition_meta.comments`, ADR-03). Best-effort metadata: a comment whose anchor
  node disappears in an edit is dropped, stated plainly.
- **Docstrings.** A leading `/** … */` JSDoc block on a declaration is extracted to
  `definition_meta.docstring`. Editing a docstring or comment is a metadata update on
  the same hash.
- **Ordering.** Value-level order is observable and **preserved**: statement sequences,
  array elements, object-literal properties, function parameters, arguments. Type-level
  set-like order is unobservable and **sorted**: interface/object-type members (by key),
  union/intersection members (by member encoding), overload signatures excepted
  (declaration order is meaningful — preserved). Import statements do not survive to the
  AST at all: references are substituted (below), and the printed projection regenerates
  imports from deps, sorted by (module, name).
- **Numbers.** Encoded as the exact IEEE-754 f64 bit pattern (8 bytes, big-endian):
  `1.0`, `1`, `0x1`, `1_000`… unify; `-0` is distinct from `0`; non-finite literals are
  already rejected (ADR-01). `bigint`: sign byte + minimal big-endian magnitude.
- **Strings and templates.** All escapes decoded to code points; encoded as well-formed
  UTF-8; **exact code points preserved — never NFC/NFD-mutated** (a literal is program
  data); lone surrogates already rejected (ADR-01). Template literals encode as
  alternating string parts and expression nodes.
- **Regex literals.** Encoded as (pattern code points, flags sorted lexicographically);
  RE2-validity was already enforced at the gate.
- **Method shorthand.** Object-literal method shorthand is normalized to an
  arrow-function property (ADR-01 §3) before encoding — one form, one hash.
- **Name → pointer substitution.** Every reference to another catalogued definition
  (app/ or std/, per ADR-03 §6) is replaced by a `Ref(hash)` node carrying the
  referent's full address. The definition's own name is replaced by a `SelfRef` node
  (self-recursion hashes without knowing its name). The store is therefore a Merkle
  DAG: editing a leaf re-addresses its dependents through their `Ref` nodes.
- **Local names → De Bruijn indices.** Local bindings and parameters are
  alpha-normalized to binder indices; display names are not encoded. Renaming a local
  never changes a hash; alpha-equivalent definitions deduplicate to one row. Display
  names survive in `canonical_text` (ADR-03); on hash-dedupe the existing row's text
  and metadata win.

### 3. In vs out of the hashed form

| Item | Verdict | Reason |
|---|---|---|
| Structure, literals, operators | IN | The program |
| Type annotations | IN | Types do not erase in regel — they derive boundary validators and admitted surface; a type change is a behavior change |
| Contracts | IN | Contracts are ordinary subset code (std/contract combinators in the definition body), so they are hashed by construction; one hash means one behavior, including contract enforcement. They are additionally mirrored to a queryable column (ADR-03) for the verifier |
| Comments | OUT | Non-semantic; sidecar metadata |
| Docstrings | OUT | First-class metadata field with no behavior |
| Local binder names, self-name | OUT | Alpha-normalized; renames are metadata |
| Definition names, import statements | OUT | Names are catalog pointers (ADR-03); imports regenerate from deps |
| Formatting of every kind | OUT | Not in the AST |

### 4. Hash algorithm, encoding, address format

- **Algorithm: SHA-256** (Go `crypto/sha256`). Boring, stdlib, zero dependencies,
  universally verifiable. Speed is irrelevant at admission granularity.
- **`canonEncode`: an owned TLV binary format.** One tag byte per regel-AST node kind;
  fields in fixed schema-declared order; varint length prefixes; f64 as 8 bytes;
  strings as length-prefixed UTF-8; child lists length-prefixed. Exactly one byte
  sequence per normalized AST. The format spec is versioned with the AST schema and is
  a kernel-owned artifact — no CBOR, no multihash, no third-party encoder.
- **Address: `r<n>_` + lowercase Crockford base32 of the full 32-byte digest** (52
  characters, untruncated; truncation is display-only). The `r<n>` prefix names the
  AST-schema version and travels inside every pointer, dep edge, and serialized
  environment.

### 5. Round-trip guarantee and kill-tests

Guarantees, for every admitted definition `a`:
1. `parse` (tsgo + lowering) is total on `print(a)` — the printer's output always
   re-admits.
2. `hash(lower(parse(print(a)))) == hash(a)` — printing loses no identity.
3. `print(parse(print(a))) == print(a)` byte-for-byte — the text projection is a fixed
   point.
4. At admission, the kernel verifies `hash(normalize(lower(parse(canonical_text))))`
   equals the stored hash before insert — a printer bug is caught at the gate, never in
   the store.

Kill-tests (all are release gates for printer/encoder changes, run before any feature
work — red-path-first):
- **Mutation matrix** (grafted from the prior-art proposal): perturbing whitespace,
  comments, docstrings, local names, import order, quote style, number spelling
  (`1.0`→`1`), or type-member order asserts the **same** hash; perturbing a literal
  value, a type annotation, a contract, or a referenced definition's body asserts a
  **different** hash. The matrix is the executable form of §2–§3.
- **World-rehash canary (R1-10: replays parse→lower from `canonical_text`, not the stored
  AST alone)**: nightly replay of every historical definition, in **two legs**, the second
  of which is the load-bearing one:
  - *(encoder leg)* replay each stored AST through normalize→encode→hash, asserting no
    stored address moves — this catches an encoder or printer change that would shift an
    existing hash.
  - *(pipeline leg)* re-run the **full parse→lower pipeline from each row's
    `canonical_text`** and assert `hash(normalize(lower(parse(canonical_text))))` (guarantee
    4's equation, evaluated over the whole historical corpus, not just at admission) equals
    the row's stored address.

  Replaying the stored AST alone is near-tautological over unchanged bytes: it re-exercises
  only the encoder and never re-runs parse or lower, so a vendored-tsgo bump or a lowering
  change that re-maps an existing `canonical_text` to a *different* AST — hence a different
  hash — is invisible to it. That text↔AST seam is the corpus's **#1 declared drift risk**:
  §1 deliberately severs identity from text precisely so the printer and tsgo may move
  within an epoch, which is exactly what makes a silent parse/lower drift possible, so the
  canary must watch that seam directly rather than the bytes it already trusts. **Red** is
  any historical row whose `canonical_text` no longer lowers to its stored address (pipeline
  leg) or whose stored AST no longer encodes to it (encoder leg); either fires *before* the
  changed parser, lowering, or encoder ships, and the offending `(address, leg)` pairs are
  the alarm payload.
- **Property fuzz**: an owned generator of random subset-valid ASTs asserts guarantees
  1–3; token-level fuzz of canonical text asserts clean-reject-or-stable.
- **Adversarial corpus**: NFC vs NFD string pairs (must hash differently — data is
  preserved), `-0` vs `0`, bigint edge magnitudes, deep template nesting, maximal
  De Bruijn depth, alpha-equivalent definition pairs (must hash identically).

### 5.5 Self-certifying byte-restore (immortal-store recovery)

<!-- R1-03: content addressing makes byte-restore self-certifying; no role regains UPDATE -->
Content addressing gives the immortal `definition` store a recovery property that
detection-only stores lack: **a restored byte sequence is correct if and only if it
rehashes to the content address it claims.** The address is `hash(domain ‖ ast)` (§4);
the `ast` column is exactly that hashed preimage. So when the ADR-03 scrubber detects an
`ast` (or address) mismatch, the correct bytes are not lost — they are *pinned* by the
row's own primary key and independently rederivable three ways: from this row's
`canonical_text` via `hash(normalize(lower(parse(canonical_text))))` (guarantee 4), from
the git projection, and from physical backups/replicas. Repair is therefore possible
**without any standing mutation privilege**, because the restore is verified, not trusted:

- **Fails closed on digest mismatch.** A candidate restore is accepted only if
  `SHA-256(domain ‖ candidate_ast) == hash` (the address the row already carries) *and*
  `hash(normalize(lower(parse(canonical_text)))) == hash`. Wrong bytes cannot verify, so
  a byte-restore can never forge a code identity; if no source rehashes to the address the
  restore refuses and the row stays quarantined (the incident escalates rather than
  guessing).
- **No role ever regains UPDATE.** Self-certification is what lets recovery keep the
  ADR-03 §1 posture intact: UPDATE/DELETE stay revoked from every database role, including
  the kernel's, *permanently*. The restore is an out-of-band physical repair executed
  under audited break-glass superuser access — which exists at the Postgres layer
  regardless of any grant and is the least-privilege-preserving path precisely because it
  adds no standing credential, no repair role, and no guard trigger to the attack surface.
  The auditable claim "no application role can rewrite code identity" is never weakened to
  "…except the repair role."
- **Byte-restore, not supersede-around.** For byte/address corruption the only correct
  motion is restore-to-hash: re-admitting the same program produces the *same* address and
  ADR-03 step 3's `ON CONFLICT (hash) DO NOTHING` would silently keep the corrupt row,
  while a genuinely new address would cascade through the Merkle closure (§2) and strand
  every continuation that stored the exact old hash. `supersedes` (§6) is reserved for
  *semantic* re-admission, never for repairing bytes.

The store-level DDL/role mechanism, the operational runbook, and the release-gated drill
that exercises this path live in ADR-03 (§4a, Recovery Runbook, and CI Verification Gates).

### 6. AST-schema versioning and hash immortality

- The AST schema, `normalize`, and `canonEncode` are versioned together as `r<n>`,
  bound to the epoch. The version is in the domain-separation prefix and in every
  address.
- **Existing hashes are never recomputed.** Every `r<n>` decoder is kept in the kernel
  forever (append-only decoders). A new epoch introducing `r2` re-hashes nothing: new
  admissions encode under `r2`; `r1` rows denote exactly the bytes they always did, and
  a decade-old continuation resumes against them.
- The only way a definition acquires a new address is **explicit re-admission** (e.g.
  `regel migrate N` re-admitting a definition that needs new-epoch semantics), which
  inserts a new row whose `supersedes` column (ADR-03, grafted from the prior-art
  proposal) links the old address. A silent global rehash is structurally impossible.

## Alternatives Considered

- **simplest-thing: hash the canonical text.** Rejected: it couples identity to
  formatting forever — every printer improvement mints new hashes for the entire world
  (its own schema-version preimage makes this routine rather than impossible), and
  without alpha-normalization a local rename re-hashes a definition and every dependent.
  Its insistence on a boring stdlib hash and its idempotence-fuzzer-first staging are
  adopted.
- **prior-art-faithful: SHA-256 over deterministic CBOR, multihash/base32 addresses,
  contracts in a separate `contracts_hash`.** The AST-bytes choice, De Bruijn
  alpha-normalization, types-IN verdict, and mutation matrix are adopted. Rejected
  pieces: CBOR + multihash add external spec surface with no Go-stdlib support (an
  owned encoder gains nothing from wearing a CBOR badge); the separate contracts hash
  lets two admissions share a code hash while behaving differently under contract
  enforcement — one identity must capture everything behavior-affecting.
- **red-path-first (winner): BLAKE3 over an owned TLV encoding.** Its design carries
  this ADR — AST-bytes identity, owned TLV, version-in-address, append-only decoders,
  world-rehash canary, string-data preservation. One graft against it: SHA-256 replaces
  BLAKE3, which is a third-party pure-Go dependency bought for speed no admission path
  needs; tree-hash sub-node addressing is a capability nothing in v1 consumes.

## Consequences

- Two artifacts must stay bit-stable per epoch: the regel-AST schema and `canonEncode`.
  Both are small, owned, versioned, and guarded by the canary.
- The printer is free to improve typography within an epoch; only guarantee 1–4
  compliance is frozen, not aesthetics. Text is trustworthy because it round-trips, not
  because it is the identity.
- Alpha-normalization means the catalog deduplicates alpha-equivalent code across
  authors; the second author sees the first author's local names in the stored text.
  Accepted and stated.
- Comment anchoring is best-effort metadata; programs never depend on it.
- Every dependent's address changes when a dependency's body changes (Merkle). This is
  the point: a patch names exact hashes, and drift is impossible.

## Red-Path Tests Implied

The four kill-test families in §5 (mutation matrix, world-rehash canary, property/token
fuzz, adversarial corpus), plus:
- Store-integrity check: guarantee 4 rejects any submitted row whose text and hash
  disagree; a bit-flipped `ast` column is caught by the ADR-03 scrubber.
  <!-- R1-03: self-certifying restore red path (detection now has a verified exit) -->
  Detection is no longer a dead end: the caught row is repaired by the §5.5
  self-certifying byte-restore, whose end-to-end recovery drill is a RELEASE GATE in
  ADR-03 (corrupt one immortal `ast` byte → run the runbook → assert scrubber-clean and
  fail-closed on non-matching restore bytes). A candidate restore that does not rehash to
  the address MUST be rejected by guarantee 4 / §4 address equality — the fail-closed leg
  of that drill exercises this ADR's verification, not just ADR-03's operations.
- Cross-epoch: admit under `r1`, upgrade kernel to `r2`, assert the `r1` row's address
  and bytes are untouched, `r1` decoder still evaluates it, and re-admission produces
  an `r2` row with a `supersedes` link.
- Dedupe: two alpha-equivalent submissions yield one row; the response returns the
  existing address.

## Constraints Discharged or Budgeted

1. **Discharged (the anchor half).** Immortal, version-prefixed addresses + De Bruijn
   normalization are exactly what serialized environments can reference stably for
   years; ADR-01 guarantees only such values are captured.
2. **Not implicated.** Encoding/hashing costs are admission-time, off the evaluation
   path.
3. **Not implicated** beyond durable-condition rows referencing addresses (taak
   cluster).
4. **Discharged.** This ADR is the homoiconicity approximation: explicit AST schema,
   one rendering, one byte-form, one hash — structurally diffable, SELECT-able.
5. **Budgeted.** Identity is sound; type soundness is not claimed — the verifier suite
   remains the security boundary.
6. **Budgeted.** The encoder/printer is the deepest-bet artifact built and kill-tested
   first; the canary keeps it honest forever after.
