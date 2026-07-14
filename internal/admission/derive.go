package admission

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/rast"
)

// The Stage-C derivation seam (ADR-07 §1 step 5a, BUILD-C marker). After tsgo and
// before the verifier suite, explicit ordered PURE passes run over base ⊕ patch,
// producing PROPOSED derived rows + migration_sql — nothing physical is applied
// at 5a. The Stage-C pass set (ADR-10 §4 at MINIMAL scope):
//
//	(a) schema pass  — a declared resource derives additive CREATE TABLE / ALTER
//	    TABLE ... ADD COLUMN against the shape recorded in derived_resource (never
//	    information_schema). A REMOVED field derives the destructive statement (so
//	    V6 rejects it) unless the envelope carries intent=retire, which routes it
//	    to the staged maintenance lane (no inline DROP; a named retire artifact).
//	(b) policy-wiring pass — the derived read/query path consults the declared
//	    policy; the wired policy hash feeds V3 catalog-parity.
//	(c) derived-artifact record — the proposed rows are stored INSPECTABLE in
//	    derived_artifact so V3/V6 query them and C4's resource.query serves them.
//
// buildPlan is a pure function of (resources, policies, recorded base shapes,
// intent): the same inputs yield byte-identical derived rows + migration_sql
// (the ADR-07 determinism guarantee, asserted by TestDerivationDeterministic).
// std/pii mask/reveal and std/contract pre/post passes are increment C2 — room
// left per ADR-10 naming, not built here.

// fieldSpec is one declared resource field: a base semantic kind and the pii flag.
type fieldSpec struct {
	Name string `json:"-"`
	Base string `json:"base"`
	PII  bool   `json:"pii"`
}

// resourceDecl is a parsed erf resource(...) declaration.
type resourceDecl struct {
	CatalogName string
	DefHash     string
	Fields      []fieldSpec // sorted by Name
	PolicyHash  string      // "" when the resource declares no policy
	PolicyName  string      // display name of the wired policy ("" when none)
}

// policyArtifact is a parsed app-declared policy declaration (a policy(name)
// call) — the governance artifact V3 requires be wired by some resource.
type policyArtifact struct {
	CatalogName string
	DefHash     string
}

// derivedShape is the last-admitted derived shape recorded in derived_resource.
type derivedShape struct {
	Fields     map[string]fieldSpec
	TableName  string
	PolicyName string
}

// resourcePlan is the proposed derivation for one resource.
type resourcePlan struct {
	Decl        resourceDecl
	TableName   string
	NewShape    map[string]fieldSpec
	Additive    []string // additive DDL applied at step 6 (CREATE/ADD COLUMN)
	Destructive []string // destructive statements — V6 rejects unless retire
	Partials    []string // attrs with no derivable mask — V6 DERIVE_PARTIAL
	Retired     []string // fields removed under intent=retire (staged lane)
	PolicyName  string
}

// derivationPlan is the whole step-5a output, consumed by V3/V6 and step 6.
type derivationPlan struct {
	Resources    []resourcePlan
	Policies     []policyArtifact
	WiredPolicy  map[string]bool // policy def hashes referenced by ≥1 resource
	MigrationSQL string          // additive statements, deterministic order
}

// --- parsing (pure) ----------------------------------------------------------

// unwrapValue strips a KSatisfy type-annotation wrapper from a DefValue body.
func unwrapValue(n *rast.Node) *rast.Node {
	if n != nil && n.Kind == rast.KSatisfy && len(n.Kids) > 0 {
		return n.Kids[0]
	}
	return n
}

// stdCallIntrinsic returns the std intrinsic a call expression targets, or "".
func stdCallIntrinsic(body *rast.Node, im *Image) (string, *rast.Node, bool) {
	body = unwrapValue(body)
	if body == nil || body.Kind != rast.KCall || len(body.Kids) < 2 {
		return "", nil, false
	}
	callee := body.Kids[0]
	if callee == nil || callee.Kind != rast.KRef {
		return "", nil, false
	}
	e := im.ByHash[callee.Str]
	if e == nil {
		return "", nil, false
	}
	return e.Intrinsic, body.Kids[1], true // (intrinsic, argList, ok)
}

// parseResource recognizes a resource(...) declaration and extracts its field map
// and wired policy. hashToName resolves an in-patch policy hash to its name.
func parseResource(ld loweredDef, im *Image, hashToName map[string]string) (resourceDecl, bool) {
	if ld.Def.Kind != rast.DefValue {
		return resourceDecl{}, false
	}
	intrinsic, argList, ok := stdCallIntrinsic(ld.Def.Body, im)
	if !ok || intrinsic != "std/resource.resource" {
		return resourceDecl{}, false
	}
	if argList.Kind != rast.KList || len(argList.Kids) == 0 {
		return resourceDecl{}, false
	}
	obj := argList.Kids[0]
	if obj == nil || obj.Kind != rast.KObject {
		return resourceDecl{}, false
	}
	rd := resourceDecl{CatalogName: ld.CatalogName, DefHash: ld.Def.Hash}
	for _, prop := range objProps(obj) {
		key, val := propKV(prop)
		switch key {
		case "fields":
			if val != nil && val.Kind == rast.KObject {
				for _, fp := range objProps(val) {
					fname, fval := propKV(fp)
					if fname == "" || fval == nil || fval.Kind != rast.KStr {
						continue
					}
					rd.Fields = append(rd.Fields, parseFieldSpec(fname, fval.Str))
				}
			}
		case "policy":
			if val != nil && val.Kind == rast.KRef {
				rd.PolicyHash = val.Str
			}
		}
	}
	sort.Slice(rd.Fields, func(i, j int) bool { return rd.Fields[i].Name < rd.Fields[j].Name })
	if rd.PolicyHash != "" {
		if e := im.ByHash[rd.PolicyHash]; e != nil {
			rd.PolicyName = e.Export
		} else if n, ok := hashToName[rd.PolicyHash]; ok {
			rd.PolicyName = n
		} else {
			rd.PolicyName = rd.PolicyHash
		}
	}
	return rd, true
}

// parsePolicy recognizes an app-declared policy(name) declaration.
func parsePolicy(ld loweredDef, im *Image) (policyArtifact, bool) {
	if ld.Def.Kind != rast.DefValue {
		return policyArtifact{}, false
	}
	intrinsic, _, ok := stdCallIntrinsic(ld.Def.Body, im)
	if !ok || intrinsic != "std/policy.policy" {
		return policyArtifact{}, false
	}
	return policyArtifact{CatalogName: ld.CatalogName, DefHash: ld.Def.Hash}, true
}

func parseFieldSpec(name, spec string) fieldSpec {
	if strings.HasPrefix(spec, "pii:") {
		return fieldSpec{Name: name, Base: strings.TrimPrefix(spec, "pii:"), PII: true}
	}
	return fieldSpec{Name: name, Base: spec, PII: false}
}

// objProps returns the KProp children of a KObject (skipping spreads).
func objProps(obj *rast.Node) []*rast.Node {
	if obj == nil || len(obj.Kids) == 0 || obj.Kids[0] == nil {
		return nil
	}
	var out []*rast.Node
	for _, k := range obj.Kids[0].Kids {
		if k != nil && k.Kind == rast.KProp {
			out = append(out, k)
		}
	}
	return out
}

// propKV returns a KProp's string key and value node.
func propKV(prop *rast.Node) (string, *rast.Node) {
	if prop == nil || len(prop.Kids) < 2 {
		return "", nil
	}
	key := prop.Kids[0]
	if key == nil || (key.Kind != rast.KStrPart && key.Kind != rast.KStr) {
		return "", prop.Kids[1]
	}
	return key.Str, prop.Kids[1]
}

// --- the schema/policy passes (pure) -----------------------------------------

// maskable reports whether the Stage-C derivation can derive a masking rule for a
// field. Plain fields need none; a pii(<base>) field needs a rule from the mask
// table, which at Stage-C scope covers text/email/phone (address masking is C2).
func maskable(f fieldSpec) bool {
	if !f.PII {
		return true
	}
	switch f.Base {
	case "text", "email", "phone":
		return true
	default:
		return false
	}
}

// colType maps a base semantic kind to its physical Postgres column type.
func colType(base string) string {
	switch base {
	case "number":
		return "numeric"
	case "boolean":
		return "boolean"
	case "date":
		return "date"
	case "timestamp":
		return "timestamptz"
	default: // text, longtext, email, phone, url, address
		return "text"
	}
}

// tableSlug derives the deterministic physical table name for a resource.
func tableSlug(catalogName string) string {
	var b strings.Builder
	b.WriteString("res_")
	for _, r := range strings.ToLower(catalogName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// quoteIdent double-quotes a SQL identifier, escaping embedded quotes.
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// buildPlan runs the schema + policy-wiring passes over base ⊕ patch. Pure: same
// inputs ⇒ byte-identical plan (ADR-07 determinism).
func buildPlan(resources []resourceDecl, policies []policyArtifact, base map[string]derivedShape, intent string) derivationPlan {
	sort.Slice(resources, func(i, j int) bool { return resources[i].CatalogName < resources[j].CatalogName })

	plan := derivationPlan{Policies: policies, WiredPolicy: map[string]bool{}}
	var applied []string

	for _, rd := range resources {
		// Sort fields here so the plan is deterministic for any caller, not only
		// through parseResource (self-contained purity).
		rd.Fields = append([]fieldSpec(nil), rd.Fields...)
		sort.Slice(rd.Fields, func(i, j int) bool { return rd.Fields[i].Name < rd.Fields[j].Name })
		if rd.PolicyHash != "" {
			plan.WiredPolicy[rd.PolicyHash] = true // policy-wiring pass (b)
		}
		rp := resourcePlan{
			Decl:       rd,
			TableName:  tableSlug(rd.CatalogName),
			NewShape:   map[string]fieldSpec{},
			PolicyName: rd.PolicyName,
		}
		for _, f := range rd.Fields {
			rp.NewShape[f.Name] = f
		}
		if sh, ok := base[rd.CatalogName]; ok && sh.TableName != "" {
			rp.TableName = sh.TableName
		}

		sh, hasBase := base[rd.CatalogName]

		if !hasBase {
			// schema pass (a): a brand-new resource derives one CREATE TABLE.
			var cols []string
			for _, f := range rd.Fields { // rd.Fields already sorted by Name
				if !maskable(f) {
					rp.Partials = append(rp.Partials, f.Name)
					continue
				}
				cols = append(cols, "  "+quoteIdent(f.Name)+" "+colType(f.Base))
			}
			stmt := "CREATE TABLE IF NOT EXISTS " + rp.TableName +
				" (\n  id bigserial PRIMARY KEY"
			if len(cols) > 0 {
				stmt += ",\n" + strings.Join(cols, ",\n")
			}
			stmt += "\n);"
			rp.Additive = append(rp.Additive, stmt)
		} else {
			// schema pass (a): additive ADD COLUMN for new fields; a removed field
			// derives a destructive DROP (V6 rejects) unless intent=retire.
			for _, f := range rd.Fields {
				old, existed := sh.Fields[f.Name]
				if !existed {
					if !maskable(f) {
						rp.Partials = append(rp.Partials, f.Name)
						continue
					}
					rp.Additive = append(rp.Additive,
						"ALTER TABLE "+rp.TableName+" ADD COLUMN IF NOT EXISTS "+
							quoteIdent(f.Name)+" "+colType(f.Base)+";")
					continue
				}
				if old.Base != f.Base || old.PII != f.PII {
					// A kind change is a rewrite — destructive, not additive.
					rp.Destructive = append(rp.Destructive,
						"ALTER TABLE "+rp.TableName+" ALTER COLUMN "+quoteIdent(f.Name)+
							" TYPE "+colType(f.Base)+";")
				}
			}
			removed := make([]string, 0)
			for name := range sh.Fields {
				if _, still := rp.NewShape[name]; !still {
					removed = append(removed, name)
				}
			}
			sort.Strings(removed)
			for _, name := range removed {
				if intent == "retire" {
					rp.Retired = append(rp.Retired, name) // staged maintenance lane
					continue
				}
				rp.Destructive = append(rp.Destructive,
					"ALTER TABLE "+rp.TableName+" DROP COLUMN "+quoteIdent(name)+";")
			}
		}

		applied = append(applied, rp.Additive...)
		plan.Resources = append(plan.Resources, rp)
	}
	plan.MigrationSQL = strings.Join(applied, "\n")
	return plan
}

// --- IO: load base shapes, write proposed derived rows (step 5a) --------------

// deriveResources runs the step-5a seam over the frozen snapshot + patch: it
// parses the resource/policy declarations, loads the recorded base shapes, builds
// the pure plan, and writes the proposed derived rows (INSPECTABLE; rolled back
// whole on any later rejection — zero trace). It applies NO physical DDL.
func deriveResources(ctx context.Context, q catalog.Querier, lowered []loweredDef, patch Patch, scope Scope, im *Image, admissionID int64) (derivationPlan, error) {
	hashToName := map[string]string{}
	for _, ld := range lowered {
		hashToName[ld.Def.Hash] = ld.CatalogName
	}
	var resources []resourceDecl
	var policies []policyArtifact
	for _, ld := range lowered {
		if rd, ok := parseResource(ld, im, hashToName); ok {
			resources = append(resources, rd)
		}
		if pa, ok := parsePolicy(ld, im); ok {
			policies = append(policies, pa)
		}
	}
	base := map[string]derivedShape{}
	for _, rd := range resources {
		sh, ok, err := loadDerivedShape(ctx, q, rd.CatalogName, scope)
		if err != nil {
			return derivationPlan{}, err
		}
		if ok {
			base[rd.CatalogName] = sh
		}
	}
	plan := buildPlan(resources, policies, base, patch.Intent)
	if err := writeDerivedRows(ctx, q, plan, scope, admissionID); err != nil {
		return derivationPlan{}, err
	}
	// Contract pass (BUILD-C, ADR-07 §4 V4): each contract-bearing definition
	// derives a boundary-validator artifact — a declared governance artifact V3
	// parity then covers. The clauses' purity is enforced by V4 at step 5b.
	for _, ld := range lowered {
		clauses := findContractClauses(ld.Def, im)
		if len(clauses) == 0 {
			continue
		}
		detail, _ := json.Marshal(map[string]any{"clauses": clauses})
		if err := insertArtifact(ctx, q, admissionID, ld.CatalogName, scope, "validator", string(detail)); err != nil {
			return derivationPlan{}, err
		}
	}
	return plan, nil
}

// loadDerivedShape reads the recorded derived shape for a resource, if any.
func loadDerivedShape(ctx context.Context, q catalog.Querier, name string, scope Scope) (derivedShape, bool, error) {
	var fieldsJSON, tableName, policyName string
	ok, err := q.QueryRow(ctx,
		`SELECT fields::text, table_name, coalesce(policy_name,'') FROM derived_resource
		 WHERE resource_name=$1 AND scope_kind=$2 AND scope_id=$3`,
		[]any{name, scope.Kind, scope.ID}, &fieldsJSON, &tableName, &policyName)
	if err != nil || !ok {
		return derivedShape{}, ok, err
	}
	raw := map[string]fieldSpec{}
	if err := json.Unmarshal([]byte(fieldsJSON), &raw); err != nil {
		return derivedShape{}, true, err
	}
	sh := derivedShape{Fields: map[string]fieldSpec{}, TableName: tableName, PolicyName: policyName}
	for k, v := range raw {
		v.Name = k
		sh.Fields[k] = v
	}
	return sh, true, nil
}

// writeDerivedRows upserts the proposed derived_resource shape and inserts the
// INSPECTABLE derived_artifact records (schema, policy, retire passes).
func writeDerivedRows(ctx context.Context, q catalog.Querier, plan derivationPlan, scope Scope, admissionID int64) error {
	for _, rp := range plan.Resources {
		fieldsJSON, err := marshalShape(rp.NewShape)
		if err != nil {
			return err
		}
		var policyArg any
		if rp.PolicyName != "" {
			policyArg = rp.PolicyName
		}
		if _, err := q.Exec(ctx, `
INSERT INTO derived_resource
  (resource_name, scope_kind, scope_id, def_hash, fields, policy_name, table_name, admission_id)
VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8)
ON CONFLICT (resource_name, scope_kind, scope_id) DO UPDATE
  SET def_hash=EXCLUDED.def_hash, fields=EXCLUDED.fields, policy_name=EXCLUDED.policy_name,
      table_name=EXCLUDED.table_name, admission_id=EXCLUDED.admission_id, updated_at=now()`,
			rp.Decl.CatalogName, scope.Kind, scope.ID, rp.Decl.DefHash, fieldsJSON,
			policyArg, rp.TableName, admissionID); err != nil {
			return err
		}

		schemaDetail, _ := json.Marshal(map[string]any{
			"table": rp.TableName, "additive": rp.Additive})
		if err := insertArtifact(ctx, q, admissionID, rp.Decl.CatalogName, scope, "schema", string(schemaDetail)); err != nil {
			return err
		}
		if rp.PolicyName != "" {
			polDetail, _ := json.Marshal(map[string]any{"policy": rp.PolicyName})
			if err := insertArtifact(ctx, q, admissionID, rp.Decl.CatalogName, scope, "policy", string(polDetail)); err != nil {
				return err
			}
		}
		if len(rp.Retired) > 0 {
			retDetail, _ := json.Marshal(map[string]any{"attrs": rp.Retired})
			if err := insertArtifact(ctx, q, admissionID, rp.Decl.CatalogName, scope, "retire", string(retDetail)); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertArtifact(ctx context.Context, q catalog.Querier, admissionID int64, name string, scope Scope, pass, detail string) error {
	_, err := q.Exec(ctx, `
INSERT INTO derived_artifact (admission_id, resource_name, scope_kind, scope_id, pass, detail)
VALUES ($1,$2,$3,$4,$5,$6::jsonb)`,
		admissionID, name, scope.Kind, scope.ID, pass, detail)
	return err
}

// marshalShape renders a field map to deterministic JSON (map keys sorted by the
// encoder), so a re-derivation of the same shape is byte-identical.
func marshalShape(shape map[string]fieldSpec) (string, error) {
	out := map[string]fieldSpec{}
	for k, v := range shape {
		out[k] = fieldSpec{Base: v.Base, PII: v.PII}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
