package admission

import "testing"

// Increment-C2 red-path-first fixtures for V2 pii-flow (ADR-07 §4). Each red
// fixture asserts its SPECIFIC reject code AND zero trace (no definition /
// admission / pointer row; the gate_refusal row persists), mirroring
// TestV1CapUngrantedZeroTrace. Green twins are the sibling legal forms: a
// masked flow admits.

// assertZeroTrace checks a rejected admission left no immortal trace but did
// mint a durable refusal row.
func assertZeroTrace(t *testing.T, w *world, v Verdict, defsBefore, admsBefore, refBefore int, name string) {
	t.Helper()
	if v.RefusalID == "" {
		t.Fatal("rejected verdict must carry a refusal_id")
	}
	if got := w.count("SELECT count(*) FROM definition"); got != defsBefore {
		t.Fatalf("definition rows changed (%d → %d)", defsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM admission"); got != admsBefore {
		t.Fatalf("admission rows changed (%d → %d)", admsBefore, got)
	}
	if name != "" {
		if got := w.count("SELECT count(*) FROM name_pointer WHERE name=$1", name); got != 0 {
			t.Fatalf("pointer for rejected def exists (%d)", got)
		}
	}
	if got := w.count("SELECT count(*) FROM gate_refusal WHERE refusal_id=$1", v.RefusalID); got != 1 {
		t.Fatalf("gate_refusal row for %s missing", v.RefusalID)
	}
	if got := w.count("SELECT count(*) FROM gate_refusal"); got != refBefore+1 {
		t.Fatalf("gate_refusal count %d, want %d", got, refBefore+1)
	}
}

func (w *world) snapshot() (int, int, int) {
	return w.count("SELECT count(*) FROM definition"),
		w.count("SELECT count(*) FROM admission"),
		w.count("SELECT count(*) FROM gate_refusal")
}

// --- V2: PII_ESCAPE (a vault field returned unmasked at a boundary) -----------

func TestV2PiiEscapeZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// A served (exported) function returns a vault-typed value unmasked.
	src := `import type { Vault } from "std/pii";
export function showOwner(owner: Vault<string>): string {
  return owner;
}
`
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/esc", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PII_ESCAPE" {
		t.Fatalf("want PII_ESCAPE, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V2" {
		t.Fatalf("want V2 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
	assertZeroTrace(t, w, v, d, a, r, "app/esc/showOwner")
}

// Multi-hop: taint through a helper function is STILL caught (a value that
// crosses a private forwarder still reaches the served boundary as pii).
func TestV2PiiEscapeMultiHopCaught(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import type { Vault } from "std/pii";
function forward(v: Vault<string>): Vault<string> {
  return v;
}
export function leak(owner: Vault<string>): string {
  return forward(owner);
}
`
	v, err := admit(ctx, w.conn, src, "app/hop", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PII_ESCAPE" {
		t.Fatalf("want PII_ESCAPE (through helper), got %+v", v.Diagnostics)
	}
}

// Green twin: masking the same flow admits.
func TestV2PiiMaskedAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import type { Vault } from "std/pii";
import { mask } from "std/pii";
export function showOwner(owner: Vault<string>): string {
  return mask(owner);
}
`
	v, err := admit(ctx, w.conn, src, "app/msk", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/msk/showOwner'"); got != 1 {
		t.Fatalf("masked def pointer missing (%d)", got)
	}
}

// --- V2: PII_ESCAPE into the non-capability log sink (RESIDUE_LOG_SINK) --------
// BUILD-E (D4, ADR-10 §3/§8): std/log.write declares effect class `external` but
// bears NO capability, so V2's old capability-keyed sink set omitted it — a Vault
// value routed into log.write ADMITTED (the RESIDUE_LOG_SINK gap). The external-
// effect sink arm closes it: the log sink is now in V2's sink set.
func TestV2LogSinkPiiEscape(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// A Vault value routed into the non-capability external sink std/log.write.
	src := `import { write } from "std/log";
import type { Vault } from "std/pii";
export function audit(owner: Vault<string>): void {
  write(owner);
}
`
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/logsink", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected — a Vault value into log.write must be caught; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PII_ESCAPE" {
		t.Fatalf("want PII_ESCAPE, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V2" {
		t.Fatalf("want V2 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
	assertZeroTrace(t, w, v, d, a, r, "app/logsink/audit")
}

// Green twin: a NON-pii value into log.write still admits (the positive path is
// intact — only tainted values are caught).
func TestV2LogSinkNonPiiAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import { write } from "std/log";
export function audit(msg: string): void {
  write(msg);
}
`
	v, err := admit(ctx, w.conn, src, "app/logok", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/logok/audit'"); got != 1 {
		t.Fatalf("non-pii log.write def pointer missing (%d)", got)
	}
}

// --- V2: PII_LITERAL (a vault-typed literal in code) --------------------------

func TestV2PiiLiteralZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// A literal given vault type — must never be immortalized (ADR-03 interaction).
	src := `import type { Vault } from "std/pii";
import { mask } from "std/pii";
export function leak(): string {
  const secret: Vault<string> = "hunter2";
  return mask(secret);
}
`
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/lit", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PII_LITERAL" {
		t.Fatalf("want PII_LITERAL, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V2" {
		t.Fatalf("want V2 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
	assertZeroTrace(t, w, v, d, a, r, "app/lit/leak")
	// The literal value never enters the immortal store.
	if got := w.count("SELECT count(*) FROM definition WHERE canonical_text LIKE '%hunter2%'"); got != 0 {
		t.Fatalf("pii literal was immortalized (%d rows)", got)
	}
}

// --- Adversarial proof: with V2 disabled, the escape fixture ADMITS -----------
// (demonstrates V2 — not another stage — is what catches it; then restores).
func TestV2LoadBearing(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := `import type { Vault } from "std/pii";
export function showOwner(owner: Vault<string>): string {
  return owner;
}
`
	disableV2 = true
	defer func() { disableV2 = false }()
	v, err := admit(ctx, w.conn, src, "app/proof2", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("with V2 disabled the escape must admit, got %q (%+v)", v.Outcome, v.Diagnostics)
	}
}
