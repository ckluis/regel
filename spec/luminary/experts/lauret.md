# ARNAULD LAURET — API Design & Governance

## VERDICT: CONCERNS

One P1, three P2, no P0, no red flag. The twelve ADRs are internally coherent on
*mechanism*; the gaps are at the **caller-facing** seams — the shapes and names a client
who never read the ADR must consume. Gate parity is the design's crown jewel and its
liability: one Verdict schema on four doors means one under-specified field is a
four-surface breaking change later.

## FINDINGS

1. **[P1] The Verdict has no outcome discriminant.** The object sold as making an agent's
   retry loop "convergent" exposes `admitted: bool` plus `diagnostics[]`, yet the prose
   defines four outcomes a caller must branch on oppositely — already-admitted (stop),
   `stale-base` (re-read head), `retry-exhausted` (give up), `ADMISSION_BUDGET` (a
   "Verdict-shaped refusal" that even adds an out-of-schema `retry-after`). None is typed;
   a caller who skipped ADR-07 §6 cannot dispatch them from the shape, and adding a status
   enum after ship breaks MCP, PR checks, git, and the operator plane at once.
   CITE: "then returns a `retry-exhausted` verdict; a patch whose declared base hashes no" (ADR-07, §6).

2. **[P2] A scoped name is addressed three incompatible ways in one surface.** Tools take
   `name@scope` (delimited string), resources use path order scope-first
   (`catalog://name/{scope}/{name}`), and `catalog.search` takes `scope?` as a separate
   field. One concept, three encodings the same agent juggles per call.
   CITE: "name@scope, asOf?" (ADR-12, §2 Tools).

3. **[P2] `verdict.get {patch_id}` cannot address the refusals it is said to serve.**
   Refusal-ledger rows are keyed without a patch_id, and a budget refusal never opens a
   transaction to mint one — so the refused caller has no id to pass to learn why.
   CITE: "records `(principal, scope_attempted, submitted_hashes, verdict," (ADR-12, §5).

4. **[P2] The browser is handed a kernel-internal counter as its reconnect cursor.** The
   contract holds only if every `step_seq` increment emits exactly one SSE frame; an
   empty-diff or coalesced re-render that checkpoints without pushing a frame leaves gaps
   `Last-Event-ID` replay cannot fill — unspecified either way.
   CITE: "`eventSeq` is the session's `step_seq`; it is the SSE event id" (ADR-11, §2).

## RECOMMENDATIONS

- Add a typed `outcome` enum to the Verdict (`admitted | stale-base | retry-exhausted |
  already-admitted | budget-exhausted`) and fold `retry-after` into the schema. Verify:
  a red-path fixture asserts every non-admit path returns a *known* enum value, and the
  MCP/PR/operator renderers switch on it — no renderer reads a string not in the enum.
- Pick one scoped-name encoding and make tools, resources, and `search` share it. Verify:
  grep the tool/resource roster — every scoped-name argument matches one grammar; a
  round-trip test parses a `catalog.search` result and feeds it to `catalog.get` and
  `catalog://` unmodified.
- Make refusals retrievable: assign a `patch_id` (or `refusal_id`) before any refusal,
  including pre-`BEGIN` budget rejections, and return it in the refusal object. Verify:
  the spam-flood red-path asserts `verdict.get` on the returned id yields the refusal.
- Specify the frame/cursor invariant explicitly: state whether an empty-diff checkpoint
  emits a zero-op frame. Verify: the Reconnect red-path drops SSE across a no-change
  invalidation, then asserts cursor replay yields no gap and no full repaint.

## RED FLAG

NONE — the four gaps are caller-facing contract under-specification, each fixable
pre-ship and none producing irreversible or unsafe output; they file as P1/P2, not a P0.
