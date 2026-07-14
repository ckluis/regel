// Package redpath is the ADR-07 §5 hostile corpus: one data-only fixture per
// admission-facing red-path in ADR-01..07 that exists today, plus the ADR-07 §5
// named fuzz-grown variants (import squat, cast obfuscation, PII-through-N-hops,
// capability-via-alias, capture-through-iterator).
//
// The corpus is PURE DATA (stdlib only — no admission import, no cycle). ONE
// runner in internal/admission translates each Fixture into a real Patch +
// Principal and drives it through the REAL admission pipeline against a fresh
// scratch DB, asserting Outcome=rejected with the fixture's SPECIFIC reject code.
// A green (admitted) result on a hostile fixture fails the run. The same runner
// backs the direction-(ii) mutant harness: with a production weakening armed, a
// hostile fixture that flips to green KILLS that mutant (a survivor blocks the
// release).
package redpath

// Fixture is one hostile red-path case.
type Fixture struct {
	Name        string // stable fixture id
	Component   string // owning enforcement site: V1..V6 | grammar-gate | resolver | seeders
	ThreatClass string // stable threat-class id (cap.ungranted, pii.escape, ...)
	Module      string // catalog module path, e.g. "app/cap"
	Source      string // the hostile module source
	ExpectCode  string // the required reject diagnostic code

	// Declared is the per-def declared capability set, keyed by SHORT def name
	// (the runner composes the full catalog name with the run-prefixed module).
	Declared map[string][]string
	// Tier is the per-def execution tier, keyed by short def name.
	Tier map[string]string
	// Intent is the maintenance-lane discriminant (V6 retire path); "" is ordinary.
	Intent string
	// Agent submits as an MCP agent principal (else a human engineer). OrgID is the
	// agent's scope-chain org — used by the seeder out-of-chain fixture.
	Agent bool
	OrgID string
	// ReadLog is the content-seeder read-log (seeders fixtures).
	ReadLog []Seed

	// Prelude, when non-empty, is admitted (must succeed) before Source to set up a
	// base shape the main submission then mutates. It shares the same Module.
	// BaseName is the short def name whose prior head hash becomes the main
	// submission's declared base (so the re-admit is a genuine pointer move).
	Prelude  string
	BaseName string
}

// Seed is one content-seeder read-log entry a fixture submits.
type Seed struct {
	SourceKind string
	SourceRef  string
	ScopeKind  int
	ScopeID    string
	SeededBy   string
}

// Corpus is the full hostile fixture set (ADR-07 §5). Every fixture asserts a
// SPECIFIC reject code; the runner rejects a green result.
var Corpus = []Fixture{
	// --- V1 capability-audit ------------------------------------------------
	{
		Name: "v1-cap-ungranted", Component: "V1", ThreatClass: "cap.ungranted",
		Module: "app/cap", ExpectCode: "CAP_UNGRANTED",
		Declared: map[string][]string{"notify": {"crm.read"}},
		Source: `import { send } from "std/mail";
export function notify(): void {
  send("a@b.com", "hi");
}
`,
	},
	{
		// FUZZ VARIANT: capability-via-alias — renaming the capability import does
		// not dodge V1 (it keys on the resolved dep hash, not the local name).
		Name: "v1-cap-via-alias", Component: "V1", ThreatClass: "cap.ungranted",
		Module: "app/alias", ExpectCode: "CAP_UNGRANTED",
		Declared: map[string][]string{"notify": {"crm.read"}},
		Source: `import { send as s } from "std/mail";
export function notify(): void {
  s("a@b.com", "hi");
}
`,
	},

	// --- V2 pii-flow --------------------------------------------------------
	{
		Name: "v2-pii-escape-return", Component: "V2", ThreatClass: "pii.escape",
		Module: "app/esc", ExpectCode: "PII_ESCAPE",
		Source: `import type { Vault } from "std/pii";
export function showOwner(owner: Vault<string>): string {
  return owner;
}
`,
	},
	{
		// Kills V2_DROP_LOG_SINK: a vault value flows into the capability (outbound/
		// log) sink mail.send; only the sink-set check catches this one.
		Name: "v2-pii-escape-sink", Component: "V2", ThreatClass: "pii.escape",
		Module: "app/mailsink", ExpectCode: "PII_ESCAPE",
		Declared: map[string][]string{"leak": {"mail.send"}},
		Source: `import { send } from "std/mail";
import type { Vault } from "std/pii";
export function leak(owner: Vault<string>): void {
  send("a@b.com", owner);
}
`,
	},
	{
		Name: "v2-pii-literal", Component: "V2", ThreatClass: "pii.literal",
		Module: "app/lit", ExpectCode: "PII_LITERAL",
		Source: `import type { Vault } from "std/pii";
import { mask } from "std/pii";
export function leak(): string {
  const secret: Vault<string> = "hunter2";
  return mask(secret);
}
`,
	},
	{
		// FUZZ VARIANT: PII-through-N-hops — taint through a private forwarder still
		// reaches the served boundary as pii.
		Name: "v2-pii-multihop", Component: "V2", ThreatClass: "pii.escape",
		Module: "app/hop", ExpectCode: "PII_ESCAPE",
		Source: `import type { Vault } from "std/pii";
function forward(v: Vault<string>): Vault<string> {
  return v;
}
export function leak(owner: Vault<string>): string {
  return forward(owner);
}
`,
	},

	// --- V3 catalog-parity --------------------------------------------------
	{
		Name: "v3-policy-unwired", Component: "V3", ThreatClass: "parity.unwired",
		Module: "app/pol", ExpectCode: "PARITY_UNWIRED",
		Source: `import { policy } from "std/policy";
export const teamScoped = policy("team");
`,
	},

	// --- V4 contracts -------------------------------------------------------
	{
		Name: "v4-contract-effectful", Component: "V4", ThreatClass: "contract.effectful",
		Module: "app/eff", ExpectCode: "CONTRACT_EFFECTFUL",
		Declared: map[string][]string{"bad": {"mail.send"}},
		Source: `import { post } from "std/contract";
import { send } from "std/mail";
export function bad(x: number): number {
  post(send("a@b.com", "x") !== null);
  return x;
}
`,
	},
	{
		Name: "v4-contract-malformed", Component: "V4", ThreatClass: "contract.malformed",
		Module: "app/mal", ExpectCode: "CONTRACT_MALFORMED",
		Source: `import { pre } from "std/contract";
import { orgScoped } from "std/policy";
export function bad(x: number): number {
  pre(orgScoped !== null);
  return x;
}
`,
	},

	// --- V5 capture ---------------------------------------------------------
	{
		// ADR-05 red-path test 4a. FUZZ VARIANT capture-through-iterator RESIDUE:
		// the loop-scope binder form is a V5 walk residue (flow.go STAGE-C RESIDUE:
		// for-of/while binder scopes are conservatively skipped); the nearest
		// representable variant — a host resource live across a straight-line await
		// — is covered here.
		Name: "v5-capture-unserializable", Component: "V5", ThreatClass: "capture.unserializable",
		Module: "app/cap5", ExpectCode: "CAPTURE_UNSERIALIZABLE",
		Tier:   map[string]string{"wf": "workflow"},
		Source: `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(): Promise<Conn> {
  const c: Conn = connect();
  await sleep(1);
  return c;
}
`,
	},

	// --- V6 derivation-parity ----------------------------------------------
	{
		Name: "v6-derive-partial", Component: "V6", ThreatClass: "derive.partial",
		Module: "app/part", ExpectCode: "DERIVE_PARTIAL",
		Source: `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Deal = resource({
  fields: { title: "text", home: "pii:address" },
  policy: orgScoped,
});
`,
	},
	{
		// A removed field with no retire intent derives an inline DROP COLUMN. The
		// prelude admits Deal{title,owner}; the main submission drops owner.
		Name: "v6-ddl-destructive", Component: "V6", ThreatClass: "ddl.destructive",
		Module: "app/dd", ExpectCode: "DDL_DESTRUCTIVE", BaseName: "Deal",
		Prelude: `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Deal = resource({
  fields: { title: "text", owner: "pii:text" },
  policy: orgScoped,
});
`,
		Source: `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Deal = resource({
  fields: { title: "text" },
  policy: orgScoped,
});
`,
	},

	// --- grammar gate (relocated ADR-01 §2 bans + R1) -----------------------
	{
		Name: "gate-ban-class", Component: "grammar-gate", ThreatClass: "ban.class",
		Module: "app/ban", ExpectCode: "BAN_CLASS",
		Source: `export class Foo {}
`,
	},
	{
		// FUZZ VARIANT: cast obfuscation — a non-`as const` cast is a banned form.
		Name: "gate-ban-as-cast", Component: "grammar-gate", ThreatClass: "ban.as_cast",
		Module: "app/cast", ExpectCode: "BAN_AS_CAST",
		Source: `export const x = 1 as unknown;
`,
	},
	{
		Name: "gate-floating-promise", Component: "grammar-gate", ThreatClass: "ban.floating_promise",
		Module: "app/float", ExpectCode: "FLOATING_PROMISE",
		Source: `async function bg(): Promise<void> {
  return;
}
export function run(): void {
  bg();
}
`,
	},
	{
		Name: "gate-capture-r1", Component: "grammar-gate", ThreatClass: "ban.capture_r1",
		Module: "app/caplet", ExpectCode: "CAPTURE_LET",
		Source: `export function f(): number {
  let x = 1;
  const g = (): number => x;
  x = 2;
  return g();
}
`,
	},

	// --- resolver (relocated ADR-01/ADR-02 import closure) ------------------
	{
		// FUZZ VARIANT: import squat / out-of-world import. A truly hallucinated
		// module is tsgo-redundant (both the resolver and tsgo reject it), so it
		// cannot uniquely witness a resolver weakening; the REPRESENTABLE resolver-
		// unique out-of-world is the L0-stub/catalog gap: std/resource's stub
		// exports the type `Resource`, but no such definition exists in the catalog
		// world, so tsgo accepts the import while the catalog resolver refuses it.
		// This is what kills RESOLVER_ADMIT_OUT_OF_WORLD: the mutant binds the
		// out-of-world import to an in-world sentinel, so the fixture flips green.
		Name: "resolver-out-of-world", Component: "resolver", ThreatClass: "import.out_of_world",
		Module: "app/squat", ExpectCode: "IMPORT_UNRESOLVED",
		Source: `import type { Resource } from "std/resource";
export function use(r: Resource): number {
  return 1;
}
`,
	},

	// --- seeders (ADR-07 §1 / §6 content-seeder attribution) ----------------
	{
		Name: "seeder-out-of-chain", Component: "seeders", ThreatClass: "seeder.out_of_chain",
		Module: "app/seedbad", ExpectCode: "SEEDER_OUT_OF_CHAIN",
		Agent:  true, OrgID: "org1",
		ReadLog: []Seed{{
			SourceKind: "resource", SourceRef: "app/other/Deal",
			ScopeKind: 2, ScopeID: "org2", SeededBy: "agent:a1",
		}},
		Source: `export function f(): number {
  return 1;
}
`,
	},
}
