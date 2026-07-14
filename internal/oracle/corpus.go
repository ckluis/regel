package oracle

// Case is one regel-native differential-oracle corpus case (ADR-04 §6 harness
// 3): a canonical program plus its input vectors, run identically through the
// production CEK machine and this package's reference reducer. The corpus grows
// from the existing fixture shapes — contract-bearing defs, validator-bearing
// boundaries, multi-effect workflows — and covers exactly the three layers the
// oracle exists for. Data only: no evaluator code.
type Case struct {
	Name   string
	Module string // catalog module name, e.g. "app/oc1"
	Entry  string // exported function to run (catalog name = Module + "/" + Entry)
	Source string
	// Inputs are the input vectors; each element is float64 | string | bool.
	Inputs [][]any
}

// Corpus is the versioned oracle corpus. ANY divergence between the production
// machine and the reference reducer on any case/vector — verdict, validator
// outcome/subject, effect-class order, or produced value — is red (R1-02).
var Corpus = []Case{

	// --- layer (a): contract enforcement semantics -----------------------------
	{
		Name:   "pre-holds-and-violates",
		Module: "app/oc1",
		Entry:  "double",
		Source: `import { pre } from "std/contract";
export function double(x: number): number {
  pre(x > 0);
  return x * 2;
}
`,
		Inputs: [][]any{{3.0}, {-1.0}, {0.0}},
	},
	{
		Name:   "pre-guards-effect",
		Module: "app/oc2",
		Entry:  "notify",
		Source: `import { pre } from "std/contract";
import { send } from "std/mail";
export function notify(qty: number): string {
  pre(qty > 0);
  const r = send("ops@example.com", "shipped");
  return r.intent;
}
`,
		// The violating vector must fire NO effect (boundary refused ⇒ nothing).
		Inputs: [][]any{{5.0}, {-2.0}},
	},
	{
		Name:   "pre-clause-over-strings",
		Module: "app/oc3",
		Entry:  "greet",
		Source: `import { pre } from "std/contract";
export function greet(name: string): string {
  pre(name !== "");
  return "hello, " + name;
}
`,
		Inputs: [][]any{{"ada"}, {""}},
	},

	// --- layer (b): derived boundary validator outcomes ------------------------
	{
		Name:   "post-boundary-validator",
		Module: "app/oc4",
		Entry:  "debit",
		Source: `import { post } from "std/contract";
export function debit(balance: number, amount: number): number {
  const next = balance - amount;
  post(next >= 0);
  return next;
}
`,
		Inputs: [][]any{{100.0, 30.0}, {10.0, 30.0}, {30.0, 30.0}},
	},
	{
		Name:   "post-numeric-zero-edge",
		Module: "app/oc5",
		Entry:  "scale",
		Source: `import { post } from "std/contract";
export function scale(x: number, k: number): number {
  const y = x * k;
  post(y);
  return y;
}
`,
		// k = 0 makes the validator predicate the NUMBER 0 — falsy, so the
		// boundary must reject; the weakened-accept-set mutant admits exactly this.
		Inputs: [][]any{{7.0, 2.0}, {7.0, 0.0}},
	},
	{
		Name:   "pre-and-post-same-def",
		Module: "app/oc6",
		Entry:  "clamp",
		Source: `import { pre, post } from "std/contract";
export function clamp(x: number): number {
  pre(x >= 0);
  const y = x > 100 ? 100 : x;
  post(y <= 100);
  return y;
}
`,
		Inputs: [][]any{{42.0}, {250.0}, {-1.0}},
	},

	// --- layer (c): effect-class ordering --------------------------------------
	{
		Name:   "effect-order-mail-channel-mail",
		Module: "app/oc7",
		Entry:  "flow",
		Source: `import { send } from "std/mail";
import { send as wfsend } from "std/wf";
export function flow(n: number): number {
  send("first@example.com", "one");
  wfsend("audit", n);
  send("second@example.com", "two");
  return n + 1;
}
`,
		Inputs: [][]any{{1.0}},
	},
	{
		Name:   "effect-order-two-mails",
		Module: "app/oc8",
		Entry:  "twice",
		Source: `import { send } from "std/mail";
export function twice(who: string): string {
  send(who, "a");
  const r = send(who, "b");
  return r.subject;
}
`,
		Inputs: [][]any{{"x@example.com"}},
	},
	{
		Name:   "effects-between-passing-contracts",
		Module: "app/oc9",
		Entry:  "ship",
		Source: `import { pre, post } from "std/contract";
import { send } from "std/mail";
export function ship(qty: number): number {
  pre(qty > 0);
  send("wh@example.com", "pick");
  send("wh@example.com", "pack");
  post(qty < 1000);
  return qty;
}
`,
		Inputs: [][]any{{3.0}, {-3.0}},
	},

	// --- produced values (observable iv) over plain shapes ---------------------
	{
		Name:   "plain-record-and-template",
		Module: "app/oc10",
		Entry:  "describe",
		Source: `export function describe(name: string, n: number): { label: string; total: number } {
  const total = n * 3 + 1;
  return { label: ` + "`item ${name}`" + `, total: total };
}
`,
		Inputs: [][]any{{"bolt", 4.0}, {"nut", 0.0}},
	},
	{
		Name:   "loop-accumulator-with-post",
		Module: "app/oc11",
		Entry:  "sum",
		Source: `import { post } from "std/contract";
export function sum(n: number): number {
  let acc = 0;
  let i = 1;
  while (i <= n) {
    acc = acc + i;
    i = i + 1;
  }
  post(acc >= 0);
  return acc;
}
`,
		Inputs: [][]any{{5.0}, {0.0}},
	},
	{
		Name:   "helper-closure-boundary",
		Module: "app/oc12",
		Entry:  "apply",
		Source: `import { post } from "std/contract";
export function apply(x: number): number {
  const inc = (v: number): number => v + 1;
  const y = inc(inc(x));
  post(y !== x);
  return y;
}
`,
		Inputs: [][]any{{10.0}, {-2.0}},
	},
}
