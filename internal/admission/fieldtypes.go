package admission

import "strings"

// fieldtypes.go is the closed semantic field-type roster (ADR-10 §5): the 13 base
// types + the pii modifier, encoded as a total Go table so erf derivation (§4) is
// total BY CONSTRUCTION — every (base type × render target) pair maps to exactly one
// physical shape, one validator rule, one input control, one render primitive, and
// one masking leaf. V6 derivation-parity's totality test enumerates this table; a
// base absent here is a compile-time gap, not a runtime surprise.
//
// The masking-leaf column is the ADR-10 §7 ◆ leaf a pii(<base>) value is masked at.
// It is "" for the two structurally non-leaf bases (address composite, relation edge)
// — pii over those is DERIVE_PARTIAL (KT-A3), the natural totality gap. Every
// non-empty maskLeaf is a member of cek.MaskingLeaves (asserted by a totality test).

// fieldBundle is one base type's closed bundle (ADR-10 §5): its validator rule id,
// its form input control (a tier-1 component), its render primitive (a tier-1
// component), and its masking leaf when pii-wrapped ("" ⇒ not pii-wrappable).
type fieldBundle struct {
	Validator string // R.parse rule id
	Input     string // tier-1 input control (form)
	Render    string // tier-1 render primitive (table/detail)
	MaskLeaf  string // ADR-10 §7 ◆ leaf for a pii wrap; "" ⇒ non-pii-wrappable
	Ordered   bool   // states: ordered-history semantics + board/badge derivability
}

// fieldBundles is the CLOSED roster (ADR-10 §5). The 13 semantic types: text,
// longtext, number, money, boolean, date, timestamp, email, phone, url, address,
// select/states (the enum family — two spellings, one family), relation. `multiselect`
// is deliberately absent (ADR-10 §5: it desugars to relation when a tag field earns
// it; not a 14th type). json/richtext/file are absent (derivation-totality escapes).
var fieldBundles = map[string]fieldBundle{
	"text":      {Validator: "string", Input: "field", Render: "text", MaskLeaf: "text"},
	"longtext":  {Validator: "string", Input: "field", Render: "text", MaskLeaf: "text"},
	"number":    {Validator: "number", Input: "field", Render: "text", MaskLeaf: "text"},
	"money":     {Validator: "money", Input: "field", Render: "money", MaskLeaf: "money"},
	"boolean":   {Validator: "boolean", Input: "checkbox", Render: "text", MaskLeaf: "text"},
	"date":      {Validator: "date", Input: "field", Render: "datetime", MaskLeaf: "text"},
	"timestamp": {Validator: "timestamp", Input: "field", Render: "datetime", MaskLeaf: "text"},
	"email":     {Validator: "email", Input: "field", Render: "text", MaskLeaf: "text"},
	"phone":     {Validator: "phone", Input: "field", Render: "text", MaskLeaf: "text"},
	"url":       {Validator: "url", Input: "field", Render: "text", MaskLeaf: "text"},
	// address: a composite of six text columns — structurally NOT a single value-leaf,
	// so it is not pii-wrappable (MaskLeaf ""); pii the constituent leaves instead.
	"address": {Validator: "address", Input: "field", Render: "text", MaskLeaf: ""},
	// select/states: closed enum via CHECK. states adds ordered-history semantics and
	// marks board/badge derivability. Masked at the badge leaf when pii-wrapped.
	"select": {Validator: "enum", Input: "select", Render: "badge", MaskLeaf: "badge"},
	"states": {Validator: "enum", Input: "select", Render: "badge", MaskLeaf: "badge", Ordered: true},
	// relation: belongsTo/hasMany. An FK EDGE has no single vaultable value — the
	// target's own pii fields are vaulted, never the edge — so not pii-wrappable.
	"relation": {Validator: "reference", Input: "select", Render: "text", MaskLeaf: ""},
}

// knownBase reports whether base is in the closed roster.
func knownBase(base string) bool { _, ok := fieldBundles[base]; return ok }

// piiWrappable reports whether a pii(<base>) is derivable (has a masking leaf). The
// two non-leaf bases (address, relation) are the totality gaps V6 flags DERIVE_PARTIAL.
func piiWrappable(base string) bool {
	b, ok := fieldBundles[base]
	return ok && b.MaskLeaf != ""
}

// maskable reports whether the derivation can produce a masking rule for a field:
// plain fields need none; a pii field needs a maskable base. Replaces the Stage-C
// text/email/phone allow-list with the closed bundle table.
func maskable(f fieldSpec) bool {
	if !f.PII {
		return knownBase(f.Base)
	}
	return piiWrappable(f.Base)
}

// column is one derived physical column of a resource.
type column struct {
	Name  string // physical column name
	Type  string // Postgres type
	Check string // optional CHECK body (without the CHECK keyword); "" ⇒ none
}

// columnsFor derives the physical column(s) for one field. A pii field derives NO
// base column (its value lives ONLY in the vault) — the caller routes it. money → two
// columns (minor-units bigint + currency text, never float); address → six text
// columns; select/states → one text column under a closed CHECK; relation belongsTo →
// one FK id column (hasMany is the inverse — no column on this table).
func columnsFor(f fieldSpec) []column {
	if f.PII {
		return nil // vault-routed; never a plaintext base column
	}
	q := quoteIdent(f.Name)
	switch f.Base {
	case "money":
		return []column{
			{Name: f.Name, Type: "bigint"},                    // minor units, exact
			{Name: f.Name + "_currency", Type: "text"},        // ISO currency
		}
	case "address":
		out := make([]column, 0, 6)
		for _, part := range addressParts {
			out = append(out, column{Name: f.Name + "_" + part, Type: "text"})
		}
		return out
	case "select", "states":
		chk := ""
		if len(f.Params) > 0 {
			lits := make([]string, len(f.Params))
			for i, v := range f.Params {
				lits[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
			}
			chk = q + " IN (" + strings.Join(lits, ", ") + ")"
		}
		return []column{{Name: f.Name, Type: "text", Check: chk}}
	case "relation":
		if relKind(f) == "hasMany" {
			return nil // inverse side — no column on this table
		}
		return []column{{Name: f.Name + "_id", Type: "bigint"}}
	default:
		return []column{{Name: f.Name, Type: colScalarType(f.Base)}}
	}
}

// addressParts is the fixed composite decomposition of an address (ADR-10 §5).
var addressParts = []string{"line1", "line2", "city", "region", "postal", "country"}

// colScalarType maps a scalar base to its Postgres type.
func colScalarType(base string) string {
	switch base {
	case "number":
		return "numeric"
	case "boolean":
		return "boolean"
	case "date":
		return "date"
	case "timestamp":
		return "timestamptz"
	default: // text, longtext, email, phone, url
		return "text"
	}
}

// relKind returns a relation field's subtype ("belongsTo"/"hasMany"), defaulting to
// belongsTo. relTarget returns the target resource name (or "").
func relKind(f fieldSpec) string {
	if len(f.Params) >= 1 && f.Params[0] != "" {
		return f.Params[0]
	}
	return "belongsTo"
}
func relTarget(f fieldSpec) string {
	if len(f.Params) >= 2 {
		return f.Params[1]
	}
	return ""
}

// maskLeafFor returns the ADR-10 §7 masking leaf for a pii field's base ("" if the
// base is non-pii-wrappable).
func maskLeafFor(base string) string { return fieldBundles[base].MaskLeaf }
