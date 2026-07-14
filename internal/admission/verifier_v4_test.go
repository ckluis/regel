package admission

import (
	"context"
	"testing"
)

// Increment-C2 red-path-first fixtures for V4 contracts (ADR-07 §4). pre/post
// combinators must be well-formed against the definition's types and PURE.

// grantMailSend grants + returns the declare mutator so a contract clause that
// names mail.send passes V1 (declared ⊆ granted) and reaches V4.
func grantMailSend(t *testing.T, w *world, ctx context.Context, defName string) func(*Patch) {
	t.Helper()
	if _, err := w.conn.Exec(ctx,
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ($1,'mail.send','','test')`,
		engineer("dev").Subject()); err != nil {
		t.Fatal(err)
	}
	return func(p *Patch) {
		p.DeclaredCapabilities = map[string][]string{defName: {"mail.send"}}
	}
}

// --- V4: CONTRACT_EFFECTFUL (a clause names a capability) ----------------------

func TestV4ContractEffectfulZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// A postcondition that calls a capability-bearing binding is effectful.
	src := `import { post } from "std/contract";
import { send } from "std/mail";
export function bad(x: number): number {
  post(send("a@b.com", "x") !== null);
  return x;
}
`
	mut := grantMailSend(t, w, ctx, "app/eff/bad")
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/eff", engineer("dev"), mut)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "CONTRACT_EFFECTFUL" {
		t.Fatalf("want CONTRACT_EFFECTFUL, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V4" {
		t.Fatalf("want V4 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
	assertZeroTrace(t, w, v, d, a, r, "app/eff/bad")
}

// --- V4: CONTRACT_MALFORMED (a clause names a governance / out-of-scope symbol) ---

func TestV4ContractMalformedZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import { pre } from "std/contract";
import { orgScoped } from "std/policy";
export function bad(x: number): number {
  pre(orgScoped !== null);
  return x;
}
`
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/mal", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "CONTRACT_MALFORMED" {
		t.Fatalf("want CONTRACT_MALFORMED, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V4" {
		t.Fatalf("want V4 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
	assertZeroTrace(t, w, v, d, a, r, "app/mal/bad")
}

// --- Green twin: a pure contract admits and derives its validator artifact ----

func TestV4PureContractAdmitsAndDerivesValidator(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import { pre, post } from "std/contract";
export function transfer(amount: number): number {
  pre(amount > 0);
  post(amount > 0);
  return amount;
}
`
	v, err := admit(ctx, w.conn, src, "app/pure", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
	// The derivation seam derives a boundary-validator artifact for the contract.
	if got := w.count("SELECT count(*) FROM derived_artifact WHERE resource_name='app/pure/transfer' AND pass='validator'"); got != 1 {
		t.Fatalf("validator artifact rows = %d, want 1", got)
	}
	// Contracts are mirrored to the queryable definition.contracts column.
	if got := w.count("SELECT count(*) FROM definition d JOIN name_pointer np ON np.hash=d.hash WHERE np.name='app/pure/transfer' AND d.contracts <> '[]'::jsonb"); got != 1 {
		t.Fatalf("definition.contracts not mirrored (%d)", got)
	}
}

// --- Adversarial proof: with V4 disabled, the effectful contract ADMITS -------
func TestV4LoadBearing(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import { pre } from "std/contract";
import { orgScoped } from "std/policy";
export function bad(x: number): number {
  pre(orgScoped !== null);
  return x;
}
`
	disableV4 = true
	defer func() { disableV4 = false }()
	v, err := admit(ctx, w.conn, src, "app/proof4", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("with V4 disabled the malformed contract must admit, got %q (%+v)", v.Outcome, v.Diagnostics)
	}
}
