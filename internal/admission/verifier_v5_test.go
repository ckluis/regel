package admission

import "testing"

// Increment-C2 red-path-first fixtures for V5 capture (ADR-05 §3 seated in
// ADR-07 §4; STAGE-B residue #1 = ADR-05 red-path test 4a). For every await in a
// workflow-tier definition the live-variable set must lie inside the R2
// serializable lattice — the set the CFR value codec round-trips (cfr.EncodableTags).

func workflowTier(name string) func(*Patch) {
	return func(p *Patch) { p.Tier = map[string]string{name: "workflow"} }
}

// --- V5: CAPTURE_UNSERIALIZABLE (a connection live across an await) ------------
// ADR-05 red-path test 4a, executed at admission.
func TestV5CaptureUnserializableZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// A live host-resource handle (std/sql.Conn) is bound, an await intervenes,
	// and the handle is used afterwards — so it is live across the await.
	src := `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(): Promise<Conn> {
  const c: Conn = connect();
  await sleep(1);
  return c;
}
`
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/cap5", engineer("dev"), workflowTier("app/cap5/wf"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "CAPTURE_UNSERIALIZABLE" {
		t.Fatalf("want CAPTURE_UNSERIALIZABLE, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V5" {
		t.Fatalf("want V5 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
	assertZeroTrace(t, w, v, d, a, r, "app/cap5/wf")
}

// Green twin: the same shape carrying only an encodable value (a number) across
// the await admits.
func TestV5CaptureEncodableAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import { sleep } from "std/wf";
export async function wf(): Promise<number> {
  const c = 42;
  await sleep(1);
  return c;
}
`
	v, err := admit(ctx, w.conn, src, "app/enc5", engineer("dev"), workflowTier("app/enc5/wf"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
}

// A connection NOT live across the await (bound after it) admits — the verifier
// keys on liveness, not presence.
func TestV5ConnAfterAwaitAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(): Promise<Conn> {
  await sleep(1);
  const c: Conn = connect();
  return c;
}
`
	v, err := admit(ctx, w.conn, src, "app/after5", engineer("dev"), workflowTier("app/after5/wf"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
}

// --- Adversarial proof: with V5 disabled, the capture bomb ADMITS -------------
func TestV5LoadBearing(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import type { Conn } from "std/sql";
import { connect } from "std/sql";
import { sleep } from "std/wf";
export async function wf(): Promise<Conn> {
  const c: Conn = connect();
  await sleep(1);
  return c;
}
`
	disableV5 = true
	defer func() { disableV5 = false }()
	v, err := admit(ctx, w.conn, src, "app/proof5", engineer("dev"), workflowTier("app/proof5/wf"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("with V5 disabled the capture bomb must admit, got %q (%+v)", v.Outcome, v.Diagnostics)
	}
}
