package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"regel.dev/regel/internal/cek"
)

// d1_derive_test.go is the BUILD-D (increment D1) red-path + acceptance battery for
// the full erf resource(...) derivation: the closed 13-type roster, the ten-artifact
// passes, the vault + crypto-shred substrate, and the V6 derivation-parity extensions.

// contactSrc is the end-to-end demo resource exercising ALL 13 field types + 2 pii
// wraps (ADR-10 §5): text, longtext, number, money, boolean, date, timestamp, email
// (via pii), phone (via pii), url, address, select/states (enum family), relation.
func contactSrc(module string) string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Contact = resource({
  fields: {
    name: "text",
    notes: "longtext",
    score: "number",
    dealValue: "money",
    active: "boolean",
    closedOn: "date",
    lastSeen: "timestamp",
    email: "pii:email",
    phone: "pii:phone",
    site: "url",
    hq: "address",
    tier: "select:bronze|silver|gold",
    stage: "states:new|active|won|lost",
    company: "belongsTo:Company"
  },
  policy: orgScoped,
});
`
}

// --- the closed bundle table is total (pure, no DB) --------------------------

func TestFieldTypeBundleTotality(t *testing.T) {
	// The 13 ADR-10 §5 semantic types (select/states = the enum family, two spellings).
	for _, base := range []string{
		"text", "longtext", "number", "money", "boolean", "date", "timestamp",
		"email", "phone", "url", "address", "select", "states", "relation",
	} {
		b, ok := fieldBundles[base]
		if !ok {
			t.Fatalf("base %q missing from the closed bundle table (derivation not total)", base)
		}
		// Input control + render primitive must be real tier-1 components (ADR-10 §7).
		if !inUITier1(b.Input) {
			t.Errorf("base %q input control %q not a tier-1 component", base, b.Input)
		}
		if !inUITier1(b.Render) {
			t.Errorf("base %q render primitive %q not a tier-1 component", base, b.Render)
		}
		// A non-empty mask leaf must be one of the six §7 masking leaves.
		if b.MaskLeaf != "" && !cek.MaskingLeaves[b.MaskLeaf] {
			t.Errorf("base %q mask leaf %q is not one of the six masking leaves", base, b.MaskLeaf)
		}
	}
	// The two structurally non-leaf bases are the only non-pii-wrappable ones (KT-A3).
	for _, base := range []string{"address", "relation"} {
		if piiWrappable(base) {
			t.Errorf("base %q must be non-pii-wrappable (no single vaultable value)", base)
		}
	}
	// multiselect is NOT a 14th type (ADR-10 §5 — it desugars to relation).
	if _, ok := fieldBundles["multiselect"]; ok {
		t.Fatal("multiselect must not be a field-type roster row (it desugars to relation)")
	}
	for _, banned := range []string{"json", "richtext", "file", "markdown"} {
		if _, ok := fieldBundles[banned]; ok {
			t.Fatalf("%q must not be a field type (derivation-totality escape)", banned)
		}
	}
}

func inUITier1(name string) bool {
	for _, n := range cek.UITier1 {
		if n == name {
			return true
		}
	}
	return false
}

// --- roster totality: every (field type × form/table/detail) pair binds -------

func TestRosterTotalityEndToEnd(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	v, err := admit(ctx, w.conn, contactSrc("app/crm"), "app/crm", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome=%q want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
	// Every V-stage is green (V1..V6).
	for _, stg := range []string{"V1", "V2", "V3", "V4", "V5", "V6"} {
		if !stagePassed(v, stg) {
			t.Fatalf("stage %s not green in %+v", stg, v.Stages)
		}
	}

	// Exactly the ten ADR-10 §4 passes are present (one artifact row each).
	for _, pass := range requiredPasses {
		if got := w.count("SELECT count(*) FROM derived_artifact WHERE resource_name='app/crm/Contact' AND pass=$1", pass); got != 1 {
			t.Fatalf("pass %q artifact rows = %d, want 1", pass, got)
		}
	}
	if got := w.count("SELECT count(*) FROM derived_artifact WHERE resource_name='app/crm/Contact'"); got != len(requiredPasses) {
		t.Fatalf("derived_artifact rows = %d, want exactly %d (the ten passes)", got, len(requiredPasses))
	}

	// The components pass emits a render binding for EVERY field in EACH of the three
	// render targets (form/table/detail) — the enumerated (type × target) product.
	comp := passDetailJSON(t, w, "app/crm/Contact", "components")
	form := comp["form"].(map[string]any)
	tbl := comp["table"].(map[string]any)
	det := comp["detail"].(map[string]any)
	const nFields = 14
	if n := len(form["children"].([]any)); n != nFields {
		t.Fatalf("form bindings = %d, want %d (every field renders an input)", n, nFields)
	}
	if n := len(tbl["columns"].([]any)); n != nFields {
		t.Fatalf("table bindings = %d, want %d (every field renders a column)", n, nFields)
	}
	if n := len(det["rows"].([]any)); n != nFields {
		t.Fatalf("detail bindings = %d, want %d (every field renders a row)", n, nFields)
	}

	// The physical table exists with the derived (non-pii) columns; money → 2 cols,
	// address → 6 cols; pii fields (email, phone) have NO base column.
	tbName := tableSlug("app/crm/Contact")
	if !w.tableExists(tbName) {
		t.Fatalf("base table %s not created", tbName)
	}
	for _, col := range []string{"name", "dealValue", "dealValue_currency", "hq_line1", "hq_country", "tier", "stage", "company_id", "score"} {
		if !w.columnExists(tbName, col) {
			t.Errorf("expected column %q on %s", col, tbName)
		}
	}
	for _, col := range []string{"email", "phone"} {
		if w.columnExists(tbName, col) {
			t.Errorf("pii field %q must NOT be a base column (vault-routed)", col)
		}
	}
	// The history tier is live: shadow table + trigger, pii excluded.
	if !w.tableExists(tbName + "_history") {
		t.Fatalf("history table %s_history not created", tbName)
	}
	if got := w.count("SELECT count(*) FROM pg_trigger WHERE tgname=$1", tbName+"_hist_trg"); got != 1 {
		t.Fatalf("history trigger missing (%d)", got)
	}
	if w.columnExists(tbName+"_history", "email") {
		t.Error("pii field email must NOT appear in the history table")
	}

	// The vault route artifact names exactly the two pii fields with their mask leaves.
	vault := passDetailJSON(t, w, "app/crm/Contact", "vault")
	routes := vault["routes"].([]any)
	if len(routes) != 2 {
		t.Fatalf("vault routes = %d, want 2 (email, phone)", len(routes))
	}
}

// --- pii-wrap totality + the DERIVE_PARTIAL gap (KT-A3) -----------------------

func TestPiiWrapTotality(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// Every scalar value-leaf pii wrap derives (a vault route + mask rule).
	for i, base := range []string{"text", "longtext", "number", "money", "boolean", "date", "timestamp", "email", "phone", "url"} {
		src := dealSrc(fmt.Sprintf(`title: "text", secret: "pii:%s"`, base))
		v, err := admit(ctx, w.conn, src, fmt.Sprintf("app/pw%d", i), engineer("dev"), nil)
		if err != nil {
			t.Fatalf("pii:%s admit: %v", base, err)
		}
		if v.Outcome != OutcomeAdmitted {
			t.Fatalf("pii:%s must admit (scalar leaf), got %q %+v", base, v.Outcome, v.Diagnostics)
		}
	}
	// The composite address is the representable totality gap: pii:address ⇒ DERIVE_PARTIAL.
	v, err := admit(ctx, w.conn, dealSrc(`title: "text", home: "pii:address"`), "app/pwaddr", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("pii:address admit: %v", err)
	}
	if v.Outcome != OutcomeRejected || !hasDiag(v, "V6", "DERIVE_PARTIAL") {
		t.Fatalf("pii:address must be DERIVE_PARTIAL, got %q %+v", v.Outcome, v.Diagnostics)
	}
}

// --- V6 derivation-parity: declaration ≢ derived artifacts (tamper) -----------

func TestDeriveParityTamperRejects(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// Clean twin: no tamper ⇒ admits with all ten passes.
	vc, err := admit(ctx, w.conn, dealSrc(`title: "text"`), "app/par0", engineer("dev"), nil)
	if err != nil || vc.Outcome != OutcomeAdmitted {
		t.Fatalf("clean twin must admit: %v / %q %+v", err, vc.Outcome, vc.Diagnostics)
	}

	// Mutant: suppress one derivation pass ⇒ the declaration no longer equals its
	// derived artifacts ⇒ V6 DERIVE_PARITY, zero trace.
	defsBefore := w.count("SELECT count(*) FROM definition")
	derivationTamper = func(rp *resourcePlan) {
		// drop the "components" pass
		var kept []string
		for _, p := range rp.EmittedPasses {
			if p != "components" {
				kept = append(kept, p)
			}
		}
		rp.EmittedPasses = kept
	}
	defer func() { derivationTamper = nil }()

	v, err := admit(ctx, w.conn, dealSrc(`title: "text"`), "app/par1", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected || !hasDiag(v, "V6", "DERIVE_PARITY") {
		t.Fatalf("tampered derivation must be DERIVE_PARITY, got %q %+v", v.Outcome, v.Diagnostics)
	}
	if got := w.count("SELECT count(*) FROM definition"); got != defsBefore {
		t.Fatalf("zero-trace violated: definition rows %d→%d", defsBefore, got)
	}
	if dr, da := w.derivedCounts("app/par1/Deal"); dr != 0 || da != 0 {
		t.Fatalf("derived rows for parity-rejected resource exist (dr=%d da=%d)", dr, da)
	}
}

// KT-A3 internal arm: a pii field whose vault route is suppressed ⇒ DERIVE_PARTIAL.
func TestKTA3VaultRouteSuppressedRejects(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	derivationTamper = func(rp *resourcePlan) { rp.VaultRoutes = nil } // drop the vault route
	defer func() { derivationTamper = nil }()

	v, err := admit(ctx, w.conn, dealSrc(`title: "text", secret: "pii:email"`), "app/kta3", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected || !hasDiag(v, "V6", "DERIVE_PARTIAL") {
		t.Fatalf("pii field without a vault route must be DERIVE_PARTIAL, got %q %+v", v.Outcome, v.Diagnostics)
	}
	if dr, _ := w.derivedCounts("app/kta3/Deal"); dr != 0 {
		t.Fatal("derived rows exist for a KT-A3-rejected resource")
	}
}

// --- history tier: an UPDATE writes history; pii never appears there ----------

func TestHistoryExcludesPii(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	v, err := admit(ctx, w.conn, dealSrc(`title: "text", email: "pii:email"`), "app/hist", engineer("dev"), nil)
	if err != nil || v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit: %v / %q %+v", err, v.Outcome, v.Diagnostics)
	}
	tb := tableSlug("app/hist/Deal")

	// Insert a base row + route the pii value to the vault.
	var id int64
	if _, err := w.conn.QueryRow(ctx,
		`INSERT INTO `+quoteIdent(tb)+` (title) VALUES ('acme') RETURNING id`, nil, &id); err != nil {
		t.Fatal(err)
	}
	const secret = "ceo@acme.example"
	if err := VaultPut(ctx, w.conn, tb, fmt.Sprintf("%d", id), "email", secret); err != nil {
		t.Fatal(err)
	}

	// An UPDATE fires the history trigger.
	if _, err := w.conn.Exec(ctx, `UPDATE `+quoteIdent(tb)+` SET title='acme corp' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got := w.count("SELECT count(*) FROM " + quoteIdent(tb+"_history")); got != 1 {
		t.Fatalf("history rows after UPDATE = %d, want 1", got)
	}
	// The pii plaintext appears in NEITHER the base table nor its history (grep).
	assertNoPlaintext(t, w, tb, secret)
	assertNoPlaintext(t, w, tb+"_history", secret)
	// The email column does not even exist in history.
	if w.columnExists(tb+"_history", "email") {
		t.Fatal("history table must not carry a pii column")
	}
}

// --- vault + crypto-shred lifecycle ------------------------------------------

func TestVaultSealAndCryptoShred(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	v, err := admit(ctx, w.conn, dealSrc(`title: "text", email: "pii:email"`), "app/vault", engineer("dev"), nil)
	if err != nil || v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit: %v / %q %+v", err, v.Outcome, v.Diagnostics)
	}
	tb := tableSlug("app/vault/Deal")
	var id int64
	if _, err := w.conn.QueryRow(ctx,
		`INSERT INTO `+quoteIdent(tb)+` (title) VALUES ('acme') RETURNING id`, nil, &id); err != nil {
		t.Fatal(err)
	}
	subj := fmt.Sprintf("%d", id)
	const secret = "founder@acme.example"
	if err := VaultPut(ctx, w.conn, tb, subj, "email", secret); err != nil {
		t.Fatal(err)
	}

	// Stored ONLY as ciphertext: the vault carries it, the base table never does, and
	// the ciphertext is not the plaintext.
	var ct string
	if _, err := w.conn.QueryRow(ctx,
		`SELECT ciphertext FROM vault WHERE resource=$1 AND subject_id=$2 AND field='email'`,
		[]any{tb, subj}, &ct); err != nil {
		t.Fatal(err)
	}
	if ct == "" || strings.Contains(ct, secret) {
		t.Fatalf("vault must store ciphertext, not plaintext (%q)", ct)
	}
	assertNoPlaintext(t, w, tb, secret)

	// A reveal (grant-gated upstream) recovers the plaintext BEFORE shred.
	pt, ok, err := VaultReveal(ctx, w.conn, tb, subj, "email")
	if err != nil || !ok || pt != secret {
		t.Fatalf("reveal before shred = %q ok=%v err=%v, want %q", pt, ok, err, secret)
	}

	// Crypto-shred: deletes the subject key + writes an attestation, one txn.
	attID, n, err := CryptoShred(ctx, w.conn, tb, subj, "operator:dpo")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || attID == 0 {
		t.Fatalf("shred deleted %d keys (want 1), attestation id=%d", n, attID)
	}
	if got := w.count("SELECT count(*) FROM vault_key WHERE resource=$1 AND subject_id=$2", tb, subj); got != 0 {
		t.Fatalf("vault_key survived shred (%d)", got)
	}
	if got := w.count("SELECT count(*) FROM shred_attestation WHERE id=$1 AND resource=$2", attID, tb); got != 1 {
		t.Fatal("shred attestation row missing")
	}
	// Post-shred: the ciphertext is undecryptable — the read returns the mask token.
	pt, ok, err = VaultReveal(ctx, w.conn, tb, subj, "email")
	if err != nil {
		t.Fatal(err)
	}
	if ok || pt != VaultMaskToken {
		t.Fatalf("post-shred read = %q ok=%v, want mask token undecryptable", pt, ok)
	}
	// The ciphertext row remains, but no key can ever open it again.
	if got := w.count("SELECT count(*) FROM vault WHERE resource=$1 AND subject_id=$2", tb, subj); got != 1 {
		t.Fatal("ciphertext should remain (crypto-shred destroys the key, not the blob)")
	}
}

// --- helpers -----------------------------------------------------------------

func stagePassed(v Verdict, stage string) bool {
	for _, s := range v.Stages {
		if s.Stage == stage {
			return s.Status == "pass"
		}
	}
	return false
}

func hasDiag(v Verdict, stage, code string) bool {
	for _, d := range v.Diagnostics {
		if d.StageOrVerifier == stage && d.Code == code {
			return true
		}
	}
	return false
}

func passDetailJSON(t *testing.T, w *world, resource, pass string) map[string]any {
	t.Helper()
	var detail string
	ok, err := w.conn.QueryRow(context.Background(),
		`SELECT detail::text FROM derived_artifact WHERE resource_name=$1 AND pass=$2`,
		[]any{resource, pass}, &detail)
	if err != nil || !ok {
		t.Fatalf("load %s/%s artifact: ok=%v err=%v", resource, pass, ok, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(detail), &m); err != nil {
		t.Fatalf("unmarshal %s detail: %v", pass, err)
	}
	return m
}

func (w *world) tableExists(name string) bool {
	return w.count("SELECT count(*) FROM information_schema.tables WHERE table_name=$1", name) == 1
}

func (w *world) columnExists(table, col string) bool {
	return w.count("SELECT count(*) FROM information_schema.columns WHERE table_name=$1 AND column_name=$2", table, col) == 1
}

func assertNoPlaintext(t *testing.T, w *world, table, secret string) {
	t.Helper()
	// Cast the whole row to text and search — a total grep over every column.
	got := w.count("SELECT count(*) FROM "+quoteIdent(table)+" t WHERE t::text LIKE $1", "%"+secret+"%")
	if got != 0 {
		t.Fatalf("plaintext %q found in %s (%d rows)", secret, table, got)
	}
}
