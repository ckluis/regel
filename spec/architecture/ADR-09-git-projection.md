# ADR-09: The git projection

## Status

Accepted — Phase 1

## Context

BRIEF §2 mandates a deterministic two-way git projection in v1 — the image is truth,
the repo is a view — or trust never arrives; §4 asks for the determinism mechanism,
the inbound path, and the projected scope. The concept doc's sentence is the spec:
"Git remains — as a deterministic projection, not as truth," and one gate means a PR
merge cannot be a side door around ADR-07.

Cross-ADR dependencies, stated explicitly:
- File bodies are ADR-02 `canonical_text` — already byte-canonical; docstrings and
  comments are `definition_meta` (ADR-02 §2) and are re-attached by the projector.
- The name→path function is the one ADR-07 §2's tsgo module host uses: one function,
  two consumers, so typechecking and projection can never disagree about layout.
- Commit metadata derives from the ADR-03 `admission` ledger; one commit per
  admission row. std/ files project from the ADR-03 §6 mirror rows.
- Inbound is exactly the ADR-07 pipeline: dry-run for PR checks (a rolled-back
  transaction), the real transaction on merge; verdicts are ADR-07 §6 objects with
  its leak discipline.
- The pii-flow verifier's `PII_LITERAL` rejection (ADR-07 V2) is what makes the
  projection structurally PII-free.

## Decision

### 1. Mapping: one file per definition, path = scoped name

```
app/<package>/<module>/<Name>.ts    -- product/package definitions
std/<module>/<Name>.ts              -- the world, projected READ-ONLY from mirror rows
.regel/catalog.lock                 -- sorted manifest: name → (hash, kind, epoch)
```

The name→path map is total and injective (ADR-07 §2's function). A file's body is the
definition's docstring (re-attached as a leading JSDoc block from `definition_meta`),
comments re-anchored best-effort per ADR-02, then the `canonical_text` of the name
pointer's current hash, with imports regenerated from `deps` (ADR-02 §2) — a complete,
compiling, human-readable module. A rename is a pointer move, so it projects as a git
rename with unchanged content — renames are metadata in the repo too. Derived rows
(schema DDL, forms, OpenAPI, validators) are **not** projected: they regenerate from
source, and projecting them would invite edits to artifacts that are not truth.
`catalog.lock` is the parity anchor: reviewers see hash movements, and any checkout
can verify file bytes against stored hashes offline.

### 2. Determinism: a fold over the admission ledger, byte-identical anywhere

**Granularity: one git commit per admission row.** `git log` is the admission history
is the code half of the history tier — one audit substance, three views.

Every commit field is a pure function of catalog + ledger data, nothing fresh:
- author = the admission principal as a stable synthetic identity
  (`<actor_kind>:<actor_id> <actor_id@regel>`);
- committer = the fixed identity `regel-projector <projector@regel>`;
- author and committer timestamps = the admission row's `created_at` — never
  projection-time wall clock;
- message = derived from the row (admission id, via, changed names, hashes, verdict
  summary);
- parent = the projection of the previous admission; tree entries are git's native
  sorted form over already-canonical bytes.

Therefore `project(ledger prefix) → commit SHA` is deterministic: two kernels folding
the same catalog emit **byte-identical objects and SHAs**, and re-projecting from
scratch reproduces the entire history. The SHA-reproducibility check (grafted from
the red-path proposal) is a release gate versioned with the epoch: fold the same
ledger range on two machines, assert identical SHAs.

### 3. Stored mirror, computed content — decided

The projection is **computed** (a pure fold; there is no writable repo state that can
drift from the image) but **served from a stored mirror**: the kernel constructs git
objects in-process (pure-Go object construction; no git binary, no cgo) and pushes to
a hosted remote after each admission. The truth branch (`main`) accepts updates from
the kernel's projector identity only — forge branch protection plus the projector's
sole write credential; humans and bots cannot advance it.

BUILD-C: at Stage C the "hosted remote" is a **kernel-owned local bare repository**
(filesystem path), written by the same pure-Go object construction — loose objects +
an atomic ref update by the projector identity only. Everything this section requires
(computed fold, stored mirror, SHA comparison, self-healing force-restore, audit row)
is real against that mirror; pointing it at a hosted forge remote (credentials, branch
protection, push transport) is operator infrastructure and rides as a named residue.
Git's native SHA-1 object format is used, so "byte-identical SHAs" means git object ids
any stock git client verifies.

Divergence is self-healing by construction: on every projection the kernel compares
the mirror's `main` SHA against the computed SHA for the ledger head; any mismatch
(force-push mangle, forge-side accident) is force-restored from the image and audited.
The mirror is a cache; the image is truth; "the repo lags or lies" has no durable
representation. An owned in-kernel git server (computed-on-fetch, the red-path
proposal's remote) is rejected for v1 as a second wire-protocol surface (§5 defers
it); determinism already delivers the same guarantee operationally.

### 4. Inbound: push and PR-merge are admission attempts

Un-admitted code lives only on feature branches — proposals, never truth.

- **PR opened or updated →** the kernel runs the full ADR-07 pipeline as a **dry-run**
  (the entire transaction, then `ROLLBACK`): the Verdict posts as a required status
  check, diagnostics as inline annotations on the changed lines, each with its `fix`.
  The check and the gate are the same code; there is no separate CI configuration to
  drift.
- **Merge →** the merge action (merge-queue or bot command; the forge's direct merge
  button is disabled on `main`) submits the PR's changed files as a Patch envelope —
  `via='git'`, author mapped from the verified git identity to a catalog principal,
  scope bound from that principal per ADR-07 step 2a — through the **real** admission
  transaction.
  **On ACCEPT:** rows insert, and the projector advances `main` to the canonical
  commit derived from the new admission row. The landed bytes are the printer's, not
  the pusher's — non-canonical formatting is silently normalized, gofmt-on-merge
  style, and the PR is marked merged against that commit.
  **On REJECT:** the transaction rolls back, `main` never moves, the PR stays open
  with the failing check and the structured Verdict (ADR-07 §6 leak discipline
  applies verbatim). There is no status a human can override into truth, because no
  human identity can write `main`.

BUILD-C: the inbound door ships at Stage C as kernel machinery — a git-submission
entry point that takes a branch's changed files, maps the verified git identity to a
catalog principal, and runs the ADR-07 pipeline as dry-run (PR check) or real
admission (merge), `via='git'`, returning the Verdict for the forge to render. The
forge-side wiring that invokes it (webhook listener, status-check posting, merge-queue
configuration) is the same operator-infrastructure residue as the hosted mirror above;
no timing hole is introduced because the local flow already proves the merge action
cannot advance `main` except through the gate.

One gate, three doors — CLI, Settings, git — same transaction, same verifiers, same
audit row. Rejection through git looks like rejection everywhere: a Verdict. R1-INT:
the PR-check renderer and the merge door switch on the typed `Verdict.outcome` enum
(ADR-07 §6, R1-08) — `stale-base` re-opens the PR, `admitted` advances `main`, and no
status string is ever sniffed; a new outcome door is a compile-flagged enum extension
here as on every other surface.

### 5. Scope, PII safety, and v1 boundary

**Projected:** product and package scopes, plus `std/` read-only. **Never projected:**
org/team/user overlays (tenant-private data; a shared repo would leak tenant existence
and customization and grow unboundedly — overlay history stays queryable in the
image), the vault, reveal grants, capability grants, history-tier data, runtime rows,
and KMS material — none of these are definition rows, so none are inputs to the fold.
The projection is structurally PII-free: code rows cannot contain vault-typed literals
(`PII_LITERAL`, ADR-07 V2), so projecting every definition byte is safe by
construction, not by redaction. `catalog.lock` carries names and hashes only.

**v1 ships:** the deterministic outbound fold + `catalog.lock`; the stored mirror with
projector-only `main` and self-healing restore; inbound PR dry-run checks and
merge-as-admission; the SHA-reproducibility release gate.
**Deferred, named:** per-tenant overlay repo export (opt-in, scoped to the tenant's
own principal); the owned in-kernel git remote (computed-on-fetch); signed projection
commits; incremental/sparse projection for very large catalogs; derived-artifact
read-only export.

## Alternatives Considered

- **simplest-thing:** its layout, per-admission commits, locked `main`, and overlay
  exclusion all appear here. Rejected pieces: its inbound flow reacts to a PR-merge
  webhook — by then the forge has already merged, so rejection would need to unwind a
  merge that happened; this ADR replaces it with merge-action-as-submission where the
  forge merge cannot occur without the gate (no timing hole). Its refusal to project
  std/ is overturned: the mirror rows exist (ADR-03 §6), and a clone that cannot
  resolve `std/` imports breaks the inherited editor plane — the projection should
  compile in an editor with zero extra setup.
- **prior-art-faithful (winner):** the fold-over-ledger determinism, shared name→path
  function, pinned-metadata rules, canonical re-projection on merge, std read-only
  projection, derived-rows exclusion, and dry-run-as-PR-check carry this ADR.
  Corrections: its per-tenant overlay repos are deferred rather than shipped
  (privacy-critical surface with no v1 consumer), and its go-git dependency framing
  is narrowed to in-process object construction under the kernel's owned-dependency
  discipline.
- **red-path-first:** its SHA-reproducibility kill-test, force-push analysis,
  PII-safety argument, and no-async-override merge rule are grafted. Rejected pieces:
  the owned authoritative `git-receive-pack`/computed-on-fetch remote in v1 — a
  second owned wire protocol for a one-team build, deliberately deferred behind the
  same guarantee delivered by determinism + self-healing restore; and v1 per-org
  overlay projection, deferred with per-tenant repos.

## Consequences

- Review, blame, bisect, editors, and existing code-reading habits keep working
  against a repo that is provably a view: any party can recompute the fold and check
  SHAs — trust is verifiable, not asserted.
- The forge is infrastructure, not authority: it holds a cache and hosts review
  conversation; every landing decision is the kernel's transaction. Forge outage
  degrades review, never truth.
- Normalization-on-merge means the diff a reviewer approved and the landed bytes can
  differ in formatting. The PR check shows the canonical rendering ahead of merge, so
  the surprise is bounded to trivia the printer owns; semantic identity is guaranteed
  by ADR-02 g2.
- Agents and humans share one loop: branch → PR → dry-run Verdict → fix from
  diagnostics → merge-as-admission. MCP-direct submission remains the fast path;
  git is the review-shaped door to the same gate.
- Not projecting overlays means a tenant's customizations have no git surface in v1;
  their audit story is catalog queries until per-tenant export ships. Stated,
  accepted.

## Red-Path Tests Implied

- **Byte-identical SHAs:** fold the same ledger range on two machines, and re-fold
  from an empty mirror; identical commit SHAs, including full history. Release gate
  per epoch.
- **Merge side door (impossible):** a PR whose dry-run check somehow shows green but
  whose merge-time admission rejects (base moved underneath) leaves `main` unmoved
  and the PR open with a `stale-base` Verdict; no sequence of forge operations lands
  unverified code on `main`.
- **Force-push mangle:** force-push garbage to the mirror's `main` with a stolen
  forge credential; next projection detects SHA mismatch, force-restores from the
  image, and writes an audit row; no admission consumed the mangled state.
- **Projection leak:** assert the projected tree for a full catalog contains zero
  vault tokens, grant rows, tenant identifiers, or overlay content; admit a patch
  attempting a PII literal — rejected at V2, so no projected byte can ever contain
  it.
- **Rename fidelity:** a pointer-only rename projects as a git rename with unchanged
  blob SHA and no hash change in `catalog.lock`.
- **Round-trip:** clone the repo, resubmit every file unchanged through the gate —
  every unit short-circuits as already-admitted (ADR-07 step 2d); the repo and image
  agree definition-for-definition, hash-for-hash.
- **Identity mapping:** a push from a git identity with no catalog principal is
  rejected at scope-bind with no admission row beyond the audit of the refusal.
- **Docstring edit:** editing only a JSDoc docstring in a PR admits as a metadata
  update — same hash in `catalog.lock`, a commit whose diff touches only the
  docstring block.

## Constraints Discharged or Budgeted

1. **Not implicated** beyond continuations being invisible to the projection —
   runtime rows are never folded.
2. **Budgeted.** Projection is off the evaluation path entirely: a post-commit fold
   plus a mirror push; serving traffic never waits on git.
3. **Not implicated** directly; rejected merges surface the same Verdict/diagnostic
   shapes agents already consume.
4. **Discharged (the visible half).** The projection is the canonical printer made
   public: one rendering, structurally diffable, hash-anchored via `catalog.lock` —
   homoiconicity a reviewer can `git diff`.
5. **Discharged for the git surface.** The PR check is the gate's dry-run and the
   merge is the gate itself; no CI side door exists because no second pipeline
   exists, and verdict leak discipline holds on the most public surface.
6. **Budgeted.** v1 is one fold, one mirror, one bot identity — no owned git server,
   no overlay export, no sync engine; each deferral is named and additive.
