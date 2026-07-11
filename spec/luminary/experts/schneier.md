# BRUCE SCHNEIER — Security & Threat Modeling

## VERDICT: CONCERNS

The enumerated boundary is, unusually, actually attacked — hostile corpus, dual mutation
testing, monotone coverage rows. The red-flag trigger ("no adversarial analysis") is not
met, and vault reads are held by a structural DB CHECK (§4 layer 1), not just V2. So no
P0. But the security perimeter is drawn one ring too small, and two side channels the
"byte-identical" and "deterministic" claims do not cover survive the probe.

## FINDINGS

1. **[P1] The security boundary excludes its own TCB.** "Verifier coverage IS the security boundary" omits the native-Go std bodies holding the real authority — vault routing, `std/http` egress, `std/sql` policy injection, derivation passes — which no verifier examines and which get "tests," not the hostile corpus and dual mutation the six enjoy. A contract-violating native body is arbitrary kernel authority, invisible to the gate. CITE: "that rests on tests, not proofs." (RISKS.md, R7 residual)

2. **[P2] Parse-depth DoS precedes every budget.** The primary control guards the type graph at step 4; parse runs first at step 2b, so a deeply nested submission can fatally exhaust the Go stack — unrecoverable, killing the process — before the wall-clock backstop or fuel charge lands. CITE: "on the instantiated type graph." (ADR-07 §3)

3. **[P2] Cross-tenant existence is a timing oracle.** Resolving a real out-of-scope overlay does more pipeline work than fast-failing a hallucinated name; identical bytes are not identical latency, so the disclaimed existence oracle returns through the clock. CITE: "is byte-identical to the response for a name" (ADR-12 §3)

4. **[P2] Product integrity rests on one human reading green.** A verifier-clean malicious patch is by construction admissible; no technical control (capability-delta surfacing, second reviewer, risk diff) backs the approver — the token stops byte-drift, not intent. CITE: "persuasive wrong patch with a green dry-run Verdict is approvable" (RISKS.md, R11 residual)

5. **[P2] The binary is an unattested trust root.** The one bypass embeds the hash→Go dispatch table; whoever builds the binary owns all std authority, and signed artifacts are deferred — v1 has no attestation between build sandbox and shipped kernel. CITE: "Genesis is the only gate bypass in the system" (ADR-10 §2)

## RECOMMENDATIONS

- Stand up a native-TCB adversarial harness co-equal with `gate/redpath/`: seeded contract-violating and vault-leaking native std bodies (esp. `std/http`, `std/sql`, `std/crypto`, vault routing) that the reference-app red paths must catch; make its coverage a `verifier_coverage`-style queryable row and a release gate. Verify: a seeded egress-of-unmasked-`pii` in a native body fails the release.
- Add a deterministic parse-depth ceiling in the lowering pass (reject over N nesting), tested by a 10⁵-deep-nesting fixture that must return `PARSE_BUDGET`, not crash a kernel; assert process liveness during the attack.
- Make the unnameable-reads kill-test timing-aware: assert p99 latency for out-of-scope-overlay vs hallucinated-name resolution is statistically indistinguishable, or force both down the same fast-fail path before any dep/typecheck work.
- Require the approval queue to render a machine-computed capability/PII/DDL delta beside the green Verdict, and record it in the admission row; verify a patch that widens egress surface cannot be approved without that delta shown.
- Ship binary attestation now (reproducible-build digest pinned in the `epoch` row, checked at boot) rather than deferring with signed projection commits; verify a tampered dispatch table refuses boot.

## RED FLAG

NONE — the enumerated boundary carries real adversarial analysis and vault plaintext is held by a structural CHECK, not by the enumerated V2 alone; the gaps above are P1/P2, not an unmodeled path to secrets.
