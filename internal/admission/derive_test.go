package admission

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Stage-C red-path-first fixtures (ADR-07 §4 V3/V6). Each red fixture asserts its
// SPECIFIC reject code AND zero trace (no definition / pointer / admission /
// derived row; the gate_refusal row persists), mirroring
// TestV1CapUngrantedZeroTrace. The green twins are the sibling legal forms:
// a wired policy passes V3, an additive field-add and the retire-intent path pass
// V6.

// resourceSrc builds an app module that declares a resource named `Deal` over the
// given field lines (e.g. `title: "text"`) and wires the std orgScoped policy.
func dealSrc(fields string) string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Deal = resource({
  fields: { ` + fields + ` },
  policy: orgScoped,
});
`
}

// derivedCounts snapshots the derivation-tier row counts for a resource name.
func (w *world) derivedCounts(name string) (int, int) {
	dr := w.count("SELECT count(*) FROM derived_resource WHERE resource_name=$1", name)
	da := w.count("SELECT count(*) FROM derived_artifact WHERE resource_name=$1", name)
	return dr, da
}

// --- V3: PARITY_UNWIRED (a declared-but-unconsulted policy) -------------------

func TestV3PolicyUnwiredZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// An exported policy declaration that NO resource / read path wires.
	src := `import { policy } from "std/policy";
export const teamScoped = policy("team");
`
	defsBefore := w.count("SELECT count(*) FROM definition")
	admsBefore := w.count("SELECT count(*) FROM admission")
	refBefore := w.count("SELECT count(*) FROM gate_refusal")

	v, err := admit(ctx, w.conn, src, "app/pol", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "PARITY_UNWIRED" {
		t.Fatalf("want PARITY_UNWIRED, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V3" {
		t.Fatalf("want V3 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
	if v.RefusalID == "" {
		t.Fatal("rejected verdict must carry a refusal_id")
	}
	// ZERO TRACE.
	if got := w.count("SELECT count(*) FROM definition"); got != defsBefore {
		t.Fatalf("definition rows changed (%d → %d)", defsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM admission"); got != admsBefore {
		t.Fatalf("admission rows changed (%d → %d)", admsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/pol/teamScoped'"); got != 0 {
		t.Fatalf("pointer for rejected policy exists (%d)", got)
	}
	if dr, da := w.derivedCounts("app/pol/teamScoped"); dr != 0 || da != 0 {
		t.Fatalf("derived rows for rejected policy exist (dr=%d da=%d)", dr, da)
	}
	if got := w.count("SELECT count(*) FROM gate_refusal"); got != refBefore+1 {
		t.Fatalf("gate_refusal count %d, want %d", got, refBefore+1)
	}
}

// Green twin: the same policy WIRED by a resource admits.
func TestV3PolicyWiredAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	src := `import { resource } from "std/resource";
import { policy } from "std/policy";
export const teamScoped = policy("team");
export const Deal = resource({
  fields: { title: "text" },
  policy: teamScoped,
});
`
	v, err := admit(ctx, w.conn, src, "app/wired", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/wired/teamScoped'"); got != 1 {
		t.Fatalf("policy pointer missing (%d)", got)
	}
}

// --- V6: DERIVE_PARTIAL (a PII field with no derivable masking rule) ----------

func TestV6DerivePartialZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// `pii:address` is a well-typed FieldSpec but has no Stage-C mask rule, so the
	// masking derivation is partial over that attribute.
	src := dealSrc(`title: "text", home: "pii:address"`)
	defsBefore := w.count("SELECT count(*) FROM definition")
	admsBefore := w.count("SELECT count(*) FROM admission")

	v, err := admit(ctx, w.conn, src, "app/part", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "DERIVE_PARTIAL" {
		t.Fatalf("want DERIVE_PARTIAL, got %+v", v.Diagnostics)
	}
	if v.Diagnostics[0].StageOrVerifier != "V6" {
		t.Fatalf("want V6 stage, got %q", v.Diagnostics[0].StageOrVerifier)
	}
	// ZERO TRACE.
	if got := w.count("SELECT count(*) FROM definition"); got != defsBefore {
		t.Fatalf("definition rows changed (%d → %d)", defsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM admission"); got != admsBefore {
		t.Fatalf("admission rows changed (%d → %d)", admsBefore, got)
	}
	if dr, da := w.derivedCounts("app/part/Deal"); dr != 0 || da != 0 {
		t.Fatalf("derived rows for partial resource exist (dr=%d da=%d)", dr, da)
	}
	if got := w.count("SELECT count(*) FROM gate_refusal WHERE refusal_id=$1", v.RefusalID); got != 1 {
		t.Fatal("missing refusal row")
	}
}

// --- V6: DDL_DESTRUCTIVE (a removed field without retire intent) --------------

func TestV6DdlDestructiveZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// Admit Deal{title, owner}, then re-admit Deal{title} (owner removed) with no
	// retire intent: the schema pass derives a DROP COLUMN, which V6 rejects.
	v1, err := admit(ctx, w.conn, dealSrc(`title: "text", owner: "pii:text"`), "app/dd", engineer("dev"), nil)
	if err != nil || v1.Outcome != OutcomeAdmitted {
		t.Fatalf("first admit: %v / %q (%+v)", err, v1.Outcome, v1.Diagnostics)
	}
	base := w.count("SELECT count(*) FROM derived_resource WHERE resource_name='app/dd/Deal'")
	head := dealHead(t, w, "app/dd/Deal")

	v2, err := admit(ctx, w.conn, dealSrc(`title: "text"`), "app/dd", engineer("dev"), func(p *Patch) {
		p.BaseHashes = map[string]string{"app/dd/Deal": head}
	})
	if err != nil {
		t.Fatalf("second admit: %v", err)
	}
	if v2.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v2.Outcome, v2.Diagnostics)
	}
	if len(v2.Diagnostics) == 0 || v2.Diagnostics[0].Code != "DDL_DESTRUCTIVE" {
		t.Fatalf("want DDL_DESTRUCTIVE, got %+v", v2.Diagnostics)
	}
	if !strings.Contains(strings.ToLower(v2.Diagnostics[0].Subject+v2.Diagnostics[0].Message), "owner") {
		t.Fatalf("destructive diagnostic should name the dropped column: %+v", v2.Diagnostics[0])
	}
	// ZERO TRACE: the first admission's derived shape is untouched, no new drop.
	if got := w.count("SELECT count(*) FROM derived_resource WHERE resource_name='app/dd/Deal'"); got != base {
		t.Fatalf("derived_resource changed on rejected drop (%d → %d)", base, got)
	}
	if got := dealHead(t, w, "app/dd/Deal"); got != head {
		t.Fatalf("pointer moved on rejected drop (%s → %s)", head, got)
	}
}

// Green twin (additive): admit Deal{title}, then add owner → ALTER ADD COLUMN.
func TestV6AdditiveFieldAddAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	v1, err := admit(ctx, w.conn, dealSrc(`title: "text"`), "app/add", engineer("dev"), nil)
	if err != nil || v1.Outcome != OutcomeAdmitted {
		t.Fatalf("first admit: %v / %q (%+v)", err, v1.Outcome, v1.Diagnostics)
	}
	head := dealHead(t, w, "app/add/Deal")

	v2, err := admit(ctx, w.conn, dealSrc(`title: "text", owner: "pii:text"`), "app/add", engineer("dev"), func(p *Patch) {
		p.BaseHashes = map[string]string{"app/add/Deal": head}
	})
	if err != nil {
		t.Fatalf("second admit: %v", err)
	}
	if v2.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v2.Outcome, v2.Diagnostics)
	}
	var mig string
	if _, err := w.conn.QueryRow(ctx, `SELECT coalesce(migration_sql,'') FROM admission WHERE id=$1`,
		[]any{v2.AdmissionID}, &mig); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(mig), "ADD COLUMN") {
		t.Fatalf("additive migration should ALTER ... ADD COLUMN, got %q", mig)
	}
}

// Green twin (retire): the same field removal WITH intent=retire admits, and the
// applied migration carries no inline destructive DDL.
func TestV6RetireIntentAdmits(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	v1, err := admit(ctx, w.conn, dealSrc(`title: "text", owner: "pii:text"`), "app/ret", engineer("dev"), nil)
	if err != nil || v1.Outcome != OutcomeAdmitted {
		t.Fatalf("first admit: %v / %q (%+v)", err, v1.Outcome, v1.Diagnostics)
	}
	head := dealHead(t, w, "app/ret/Deal")

	v2, err := admit(ctx, w.conn, dealSrc(`title: "text"`), "app/ret", engineer("dev"), func(p *Patch) {
		p.Intent = "retire"
		p.BaseHashes = map[string]string{"app/ret/Deal": head}
	})
	if err != nil {
		t.Fatalf("second admit: %v", err)
	}
	if v2.Outcome != OutcomeAdmitted {
		t.Fatalf("retire outcome = %q, want admitted; diags=%+v", v2.Outcome, v2.Diagnostics)
	}
	var mig string
	if _, err := w.conn.QueryRow(ctx, `SELECT coalesce(migration_sql,'') FROM admission WHERE id=$1`,
		[]any{v2.AdmissionID}, &mig); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(mig), "DROP COLUMN") {
		t.Fatalf("retire lane must not emit inline DROP COLUMN, got %q", mig)
	}
	// The retirement is recorded as a named, inspectable artifact.
	if got := w.count("SELECT count(*) FROM derived_artifact WHERE resource_name='app/ret/Deal' AND pass='retire'"); got != 1 {
		t.Fatalf("retire artifact rows = %d, want 1", got)
	}
}

// --- Derivation determinism (ADR-07 §1 5a): same patch + snapshot ⇒ byte-
//     identical derived rows + migration_sql --------------------------------

// TestDerivationDeterministicPure asserts buildPlan is a pure function: identical
// (resources, policies, base shape, intent) yield byte-identical migration_sql
// and a deep-equal plan across repeated calls, regardless of input field order.
func TestDerivationDeterministicPure(t *testing.T) {
	base := map[string]derivedShape{
		"app/x/Deal": {
			Fields:    map[string]fieldSpec{"title": {Name: "title", Base: "text"}},
			TableName: "res_app_x_deal",
		},
	}
	mk := func() []resourceDecl {
		return []resourceDecl{{
			CatalogName: "app/x/Deal",
			DefHash:     "r1_deal",
			// deliberately unsorted on input — buildPlan must sort.
			Fields: []fieldSpec{
				{Name: "owner", Base: "text", PII: true},
				{Name: "title", Base: "text"},
				{Name: "amount", Base: "number"},
			},
		}}
	}
	p1 := buildPlan(mk(), nil, base, "")
	p2 := buildPlan(mk(), nil, base, "")
	if p1.MigrationSQL != p2.MigrationSQL {
		t.Fatalf("migration_sql not deterministic:\n%q\n%q", p1.MigrationSQL, p2.MigrationSQL)
	}
	if !reflect.DeepEqual(p1, p2) {
		t.Fatalf("plan not deterministic:\n%+v\n%+v", p1, p2)
	}
	// The additive ADD COLUMNs come out in sorted field order, every time.
	if !strings.Contains(p1.MigrationSQL, "amount") || !strings.Contains(p1.MigrationSQL, "owner") {
		t.Fatalf("expected additive columns, got %q", p1.MigrationSQL)
	}
	if strings.Index(p1.MigrationSQL, "amount") > strings.Index(p1.MigrationSQL, "owner") {
		t.Fatalf("additive DDL not in sorted order: %q", p1.MigrationSQL)
	}
}

// TestDerivationDeterministicAcrossWorlds admits the identical resource patch into
// two fresh catalogs and asserts the applied migration_sql and the derived_artifact
// schema record are byte-identical — the ADR-07 hermeticity guarantee at the
// derivation tier (two fresh databases cannot disagree).
func TestDerivationDeterministicAcrossWorlds(t *testing.T) {
	src := dealSrc(`title: "text", owner: "pii:text", amount: "number"`)
	one := func() (string, string) {
		w := setupWorld(t)
		ctx := ctxT(t)
		v, err := admit(ctx, w.conn, src, "app/det", engineer("dev"), nil)
		if err != nil || v.Outcome != OutcomeAdmitted {
			t.Fatalf("admit: %v / %q (%+v)", err, v.Outcome, v.Diagnostics)
		}
		var mig, detail string
		if _, err := w.conn.QueryRow(ctx, `SELECT coalesce(migration_sql,'') FROM admission WHERE id=$1`,
			[]any{v.AdmissionID}, &mig); err != nil {
			t.Fatal(err)
		}
		if _, err := w.conn.QueryRow(ctx,
			`SELECT detail::text FROM derived_artifact WHERE resource_name='app/det/Deal' AND pass='schema'`,
			nil, &detail); err != nil {
			t.Fatal(err)
		}
		return mig, detail
	}
	migA, detailA := one()
	migB, detailB := one()
	if migA != migB {
		t.Fatalf("migration_sql differs across worlds:\n%q\n%q", migA, migB)
	}
	if detailA != detailB {
		t.Fatalf("derived_artifact detail differs across worlds:\n%q\n%q", detailA, detailB)
	}
	if !strings.Contains(strings.ToUpper(migA), "CREATE TABLE") {
		t.Fatalf("expected CREATE TABLE in migration, got %q", migA)
	}
}

// dealHead returns the current head hash of a catalogued name.
func dealHead(t *testing.T, w *world, name string) string {
	t.Helper()
	var h string
	ok, err := w.conn.QueryRow(context.Background(),
		`SELECT hash FROM name_pointer WHERE name=$1 AND scope_kind=0 AND scope_id=''`, []any{name}, &h)
	if err != nil || !ok {
		t.Fatalf("head %s: ok=%v err=%v", name, ok, err)
	}
	return h
}
