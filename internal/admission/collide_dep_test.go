package admission

import "testing"

// STAGE-E residue #11 (STAGE-D §13.11): a multi-import definition that references
// two symbols resolving to the SAME content hash must keep BOTH dependency edges.
// Every std TYPE shares the opaque genesis body (image.go), so their content
// hashes collide by design; a dependency map keyed by that hash collapses two
// distinct import edges into one, silently DROPPING the other. That corrupts the
// per-def type-hash classifier (flow.go newDefInfo), which populates its Vault /
// Conn sets FROM the dep edges — a dropped Vault edge blinds V2 to a pii escape,
// a dropped Conn edge blinds V5 to an unserializable capture.

// TestV2PiiEscapeSurvivesCollidingTypeImport: a served function returns a Vault
// value unmasked (a PII_ESCAPE) while ALSO importing a second std type (Org).
// The Org param annotation is lowered AFTER the Vault param, so a hash-keyed dep
// map drops the Vault edge — and V2 no longer sees `owner` as pii. The escape
// MUST still be caught. (RED on HEAD: admits; the pii value leaks past the gate.)
func TestV2PiiEscapeSurvivesCollidingTypeImport(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import type { Vault } from "std/pii";
import type { Org } from "std/identity";
export function showOwner(owner: Vault<string>, org: Org): string {
  return owner;
}
`
	v, err := admit(ctx, w.conn, src, "app/esc2", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected (PII_ESCAPE); a pii value leaked past V2 "+
			"because the Vault dep edge was dropped by a colliding std-type import; diags=%+v",
			v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PII_ESCAPE" {
		t.Fatalf("want PII_ESCAPE, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V2" {
		t.Fatalf("want V2 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
}
