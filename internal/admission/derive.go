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

// fieldSpec is one declared resource field: a base semantic kind, the pii flag, and
// the base-specific parameters (select/states enum members; relation [kind, target]).
type fieldSpec struct {
	Name   string   `json:"-"`
	Base   string   `json:"base"`
	PII    bool     `json:"pii"`
	Params []string `json:"params,omitempty"`
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
	Additive    []string // additive DDL applied at step 6 (CREATE/ADD COLUMN/history/vault)
	Destructive []string // destructive statements — V6 rejects unless retire
	Partials    []string // attrs with no derivable mask — V6 DERIVE_PARTIAL
	Retired     []string // fields removed under intent=retire (staged lane)
	PolicyName  string
	// VaultRoutes is the pii field names routed to the vault (ADR-10 §4 item 5): a
	// pii value never lands in a base/history column — only as vault ciphertext.
	VaultRoutes []string
	// EmittedPasses is the ordered set of derivation passes this resource emits as
	// derived_artifact rows (ADR-10 §4). V6 checks it equals the required ten; a
	// suppressed pass ⇒ DERIVE_PARITY (declaration ≢ derived artifacts).
	EmittedPasses []string
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

// parseFieldSpec parses one field spec string (ADR-10 §5). Forms:
//
//	<base>                       plain scalar/composite base
//	select:a|b|c   states:a|b|c  closed enum (states = ordered history)
//	belongsTo:T    hasMany:T      relation (base "relation", Params=[kind,target])
//	pii:<spec>                    the pii modifier over any of the above
func parseFieldSpec(name, spec string) fieldSpec {
	f := fieldSpec{Name: name}
	if strings.HasPrefix(spec, "pii:") {
		f.PII = true
		spec = strings.TrimPrefix(spec, "pii:")
	}
	base := spec
	rest := ""
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		base, rest = spec[:i], spec[i+1:]
	}
	switch base {
	case "select", "states":
		f.Base = base
		if rest != "" {
			f.Params = strings.Split(rest, "|")
		}
	case "belongsTo", "hasMany":
		f.Base = "relation"
		f.Params = []string{base, rest}
	default:
		f.Base = base
	}
	return f
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
//
// The field-type bundle table (maskable / columnsFor / colScalarType) lives in
// fieldtypes.go — the closed ADR-10 §5 roster that makes derivation total.

// requiredPasses is the exact ten-artifact derivation roster (ADR-10 §4). V6
// derivation-parity checks every resource emits precisely these.
var requiredPasses = []string{
	"schema", "history", "validator", "policy", "vault",
	"horizon", "components", "openapi", "mcptools", "catalog",
}

// derivationTamper is a TEST-ONLY hook (default nil): the parity red-path sets it to
// mutate a resourcePlan (drop an EmittedPass, drop a VaultRoute) so the declaration
// no longer equals its derived artifacts, and V6 rejects with the parity diagnostic.
// Production leaves it nil; package tests run sequentially, so a plain var is safe.
var derivationTamper func(rp *resourcePlan)

// checkClause renders a column's inline CHECK, or "".
func checkClause(c column) string {
	if c.Check == "" {
		return ""
	}
	return " CHECK (" + c.Check + ")"
}

// historyDDL derives the per-resource history tier (ADR-10 §4 item 2): a shadow
// table mirroring the NON-pii columns (pii values live only in the vault, so they are
// structurally absent from history) plus a SECURITY DEFINER trigger — the
// regel_write_history pattern — that writes a row on every UPDATE/DELETE. Regenerated
// idempotently (CREATE IF NOT EXISTS + ADD COLUMN IF NOT EXISTS + CREATE OR REPLACE)
// so an additive field-add extends history without a destructive rebuild.
func historyDDL(table string, cols []column) []string {
	hist := table + "_history"
	fn := table + "_hist"
	trg := table + "_hist_trg"
	var out []string
	out = append(out, "CREATE TABLE IF NOT EXISTS "+hist+" (\n"+
		"  history_id bigserial PRIMARY KEY,\n"+
		"  id bigint NOT NULL,\n"+
		"  op text NOT NULL,\n"+
		"  valid_from timestamptz NOT NULL DEFAULT now()\n);")
	for _, c := range cols {
		out = append(out, "ALTER TABLE "+hist+" ADD COLUMN IF NOT EXISTS "+
			quoteIdent(c.Name)+" "+c.Type+";")
	}
	// Trigger body: copy id + every non-pii column into the shadow table.
	colNames := make([]string, 0, len(cols))
	oldRefs := make([]string, 0, len(cols))
	for _, c := range cols {
		colNames = append(colNames, quoteIdent(c.Name))
		oldRefs = append(oldRefs, "OLD."+quoteIdent(c.Name))
	}
	insCols := "id, op"
	insVals := "OLD.id, TG_OP"
	if len(colNames) > 0 {
		insCols = "id, " + strings.Join(colNames, ", ") + ", op"
		insVals = "OLD.id, " + strings.Join(oldRefs, ", ") + ", TG_OP"
	}
	out = append(out, "CREATE OR REPLACE FUNCTION "+fn+"() RETURNS trigger SECURITY DEFINER AS $$\n"+
		"BEGIN\n"+
		"  INSERT INTO "+hist+" ("+insCols+") VALUES ("+insVals+");\n"+
		"  RETURN OLD;\nEND; $$ LANGUAGE plpgsql;")
	out = append(out, "DROP TRIGGER IF EXISTS "+trg+" ON "+table+";")
	out = append(out, "CREATE TRIGGER "+trg+" AFTER UPDATE OR DELETE ON "+table+
		" FOR EACH ROW EXECUTE FUNCTION "+fn+"();")
	return out
}

// nonPiiColumns flattens the non-pii base columns of a resource's field set, in
// sorted field order (deterministic).
func nonPiiColumns(fields []fieldSpec) []column {
	var out []column
	for _, f := range fields { // caller pre-sorts by Name
		if f.PII {
			continue
		}
		out = append(out, columnsFor(f)...)
	}
	return out
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
		// Every resource derives the org/role predicate on every read path (ADR-10 §4
		// item 4): default orgScoped when the declaration wires none.
		policyName := rd.PolicyName
		if policyName == "" {
			policyName = "orgScoped"
		}
		rp := resourcePlan{
			Decl:       rd,
			TableName:  tableSlug(rd.CatalogName),
			NewShape:   map[string]fieldSpec{},
			PolicyName: policyName,
		}
		for _, f := range rd.Fields {
			rp.NewShape[f.Name] = f
		}
		if sh, ok := base[rd.CatalogName]; ok && sh.TableName != "" {
			rp.TableName = sh.TableName
		}

		sh, hasBase := base[rd.CatalogName]

		if !hasBase {
			// schema pass: a brand-new resource derives one CREATE TABLE with a typed
			// bigserial primary key. pii fields derive NO base column (vault-routed).
			var cols []string
			for _, f := range rd.Fields { // rd.Fields already sorted by Name
				if f.Name == "id" || !knownBase(f.Base) {
					rp.Partials = append(rp.Partials, f.Name) // id reserved; unknown base
					continue
				}
				if f.PII {
					if !piiWrappable(f.Base) {
						rp.Partials = append(rp.Partials, f.Name) // KT-A3 totality gap
						continue
					}
					rp.VaultRoutes = append(rp.VaultRoutes, f.Name)
					continue
				}
				for _, c := range columnsFor(f) {
					cols = append(cols, "  "+quoteIdent(c.Name)+" "+c.Type+checkClause(c))
				}
			}
			stmt := "CREATE TABLE IF NOT EXISTS " + rp.TableName +
				" (\n  id bigserial PRIMARY KEY"
			if len(cols) > 0 {
				stmt += ",\n" + strings.Join(cols, ",\n")
			}
			stmt += "\n);"
			rp.Additive = append(rp.Additive, stmt)
		} else {
			// schema pass: additive ADD COLUMN for new fields; a removed field derives a
			// destructive DROP (V6 rejects) unless intent=retire.
			for _, f := range rd.Fields {
				old, existed := sh.Fields[f.Name]
				if !existed {
					if f.Name == "id" || !knownBase(f.Base) {
						rp.Partials = append(rp.Partials, f.Name)
						continue
					}
					if f.PII {
						if !piiWrappable(f.Base) {
							rp.Partials = append(rp.Partials, f.Name)
							continue
						}
						rp.VaultRoutes = append(rp.VaultRoutes, f.Name)
						continue
					}
					for _, c := range columnsFor(f) {
						rp.Additive = append(rp.Additive,
							"ALTER TABLE "+rp.TableName+" ADD COLUMN IF NOT EXISTS "+
								quoteIdent(c.Name)+" "+c.Type+checkClause(c)+";")
					}
					continue
				}
				if f.PII && piiWrappable(f.Base) {
					rp.VaultRoutes = append(rp.VaultRoutes, f.Name)
				}
				if old.Base != f.Base || old.PII != f.PII {
					// A kind change is a rewrite — destructive, not additive. (Enum-member
					// evolution on select/states is a named D1 residue.)
					rp.Destructive = append(rp.Destructive,
						"ALTER TABLE "+rp.TableName+" ALTER COLUMN "+quoteIdent(f.Name)+
							" TYPE (rewrite of "+old.Base+"→"+f.Base+");")
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
				oldF := sh.Fields[name]
				oldCols := columnsFor(oldF)
				if len(oldCols) == 0 {
					// pii/hasMany removal drops vault/relation state, not a base column.
					rp.Destructive = append(rp.Destructive,
						"-- retire required: derived field "+quoteIdent(name)+" removal drops vault/relation state")
					continue
				}
				for _, c := range oldCols {
					rp.Destructive = append(rp.Destructive,
						"ALTER TABLE "+rp.TableName+" DROP COLUMN "+quoteIdent(c.Name)+";")
				}
			}
		}

		// history tier (ADR-10 §4 item 2): regenerated idempotently from the current
		// non-pii column set — appended AFTER the schema DDL, only when the schema pass
		// produced no totality gap (a partial resource never reaches migration).
		if len(rp.Partials) == 0 {
			rp.Additive = append(rp.Additive, historyDDL(rp.TableName, nonPiiColumns(rd.Fields))...)
		}

		// The emitted-pass roster (ADR-10 §4): every resource emits exactly the ten.
		rp.EmittedPasses = append([]string(nil), requiredPasses...)
		if derivationTamper != nil {
			derivationTamper(&rp)
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
// INSPECTABLE derived_artifact records — the ten ADR-10 §4 passes (rp.EmittedPasses),
// plus the retire pass under intent=retire. Rolled back whole on any later rejection.
func writeDerivedRows(ctx context.Context, q catalog.Querier, plan derivationPlan, scope Scope, admissionID int64) error {
	for _, rp := range plan.Resources {
		fieldsJSON, err := marshalShape(rp.NewShape)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
INSERT INTO derived_resource
  (resource_name, scope_kind, scope_id, def_hash, fields, policy_name, table_name, admission_id)
VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8)
ON CONFLICT (resource_name, scope_kind, scope_id) DO UPDATE
  SET def_hash=EXCLUDED.def_hash, fields=EXCLUDED.fields, policy_name=EXCLUDED.policy_name,
      table_name=EXCLUDED.table_name, admission_id=EXCLUDED.admission_id, updated_at=now()`,
			rp.Decl.CatalogName, scope.Kind, scope.ID, rp.Decl.DefHash, fieldsJSON,
			rp.PolicyName, rp.TableName, admissionID); err != nil {
			return err
		}

		// The ten ADR-10 §4 passes, each an INSPECTABLE derived_artifact row.
		for _, pass := range rp.EmittedPasses {
			detail, derr := passDetail(pass, rp, scope)
			if derr != nil {
				return derr
			}
			if err := insertArtifact(ctx, q, admissionID, rp.Decl.CatalogName, scope, pass, detail); err != nil {
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

// passDetail builds the JSON detail for one derivation pass over a resource plan.
// The shapes are the contract D2 (rendering/masking runtime) consumes: the
// components pass carries the form/table/detail AST; the vault + validator passes
// carry the per-pii mask-rule table.
func passDetail(pass string, rp resourcePlan, scope Scope) (string, error) {
	fields := sortedFields(rp.NewShape)
	var v any
	switch pass {
	case "schema":
		v = map[string]any{"table": rp.TableName, "additive": rp.Additive, "vault_routes": nz(rp.VaultRoutes)}
	case "history":
		v = map[string]any{"table": rp.TableName + "_history", "function": rp.TableName + "_hist",
			"trigger": rp.TableName + "_hist_trg", "excludes_pii": nz(rp.VaultRoutes)}
	case "validator":
		v = map[string]any{"resource": rp.Decl.CatalogName, "rules": fieldRules(fields)}
	case "policy":
		v = map[string]any{"policy": rp.PolicyName, "predicate": "org/role",
			"injected_into": "read", "target_horizon": relationTargets(fields)}
	case "vault":
		v = map[string]any{"vault_table": "vault", "key_table": "vault_key", "routes": vaultRouteRows(fields)}
	case "horizon":
		v = map[string]any{"read_scope": map[string]any{"kind": scope.Kind, "id": scope.ID},
			"invalidation_key": rp.Decl.CatalogName, "subscription_key": rp.Decl.CatalogName}
	case "components":
		v = map[string]any{"form": formComponent(rp, fields), "table": tableComponent(rp, fields),
			"detail": detailComponent(rp, fields)}
	case "openapi":
		v = openapiFragment(rp, fields)
	case "mcptools":
		short := shortName(rp.Decl.CatalogName)
		v = map[string]any{"tools": []map[string]any{
			{"name": short + ".query", "resource": rp.Decl.CatalogName, "op": "read", "masks_pii": true},
			{"name": short + ".mutate", "resource": rp.Decl.CatalogName, "op": "write", "policy": rp.PolicyName},
		}, "served_by": "resource.query/mutate"}
	case "catalog":
		v = map[string]any{"def_hash": rp.Decl.DefHash, "resource": rp.Decl.CatalogName,
			"field_count": len(fields), "passes": requiredPasses}
	default:
		v = map[string]any{}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// sortedFields returns the resource's fields sorted by name (deterministic detail).
func sortedFields(shape map[string]fieldSpec) []fieldSpec {
	out := make([]fieldSpec, 0, len(shape))
	for name, f := range shape {
		f.Name = name
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// fieldRules is the boundary-validator (R.parse) rule table — one rule per field,
// from the closed bundle. pii fields also carry their mask leaf.
func fieldRules(fields []fieldSpec) []map[string]any {
	out := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		b := fieldBundles[f.Base]
		r := map[string]any{"name": f.Name, "base": f.Base, "pii": f.PII, "validator": b.Validator}
		if len(f.Params) > 0 {
			r["params"] = f.Params
		}
		if f.PII {
			r["mask_leaf"] = b.MaskLeaf
		}
		if b.Ordered {
			r["ordered"] = true
		}
		out = append(out, r)
	}
	return out
}

// vaultRouteRows is the pii → vault mask-rule table (ADR-10 §4 item 5): the finite
// set of vault-routed fields, each with the §7 leaf its value is masked at.
func vaultRouteRows(fields []fieldSpec) []map[string]any {
	out := []map[string]any{}
	for _, f := range fields {
		if f.PII && piiWrappable(f.Base) {
			out = append(out, map[string]any{"field": f.Name, "base": f.Base,
				"mask_leaf": maskLeafFor(f.Base), "excluded_from_history": true})
		}
	}
	return out
}

// relationTargets records the target-horizon predicate note per relation field.
func relationTargets(fields []fieldSpec) []map[string]any {
	out := []map[string]any{}
	for _, f := range fields {
		if f.Base == "relation" {
			out = append(out, map[string]any{"field": f.Name, "kind": relKind(f),
				"target": relTarget(f), "predicate": "target.orgScoped"})
		}
	}
	return out
}

// formComponent / tableComponent / detailComponent are the tier-2 derived surfaces
// (ADR-10 §7), composed from tier-1 vocabulary records (cek.UITier1) chosen by the
// field's semantic bundle. Kept as DATA (an AST record); D2 renders it. pii cells are
// marked masked at their §7 leaf.
func formComponent(rp resourcePlan, fields []fieldSpec) map[string]any {
	kids := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		b := fieldBundles[f.Base]
		props := map[string]any{"name": f.Name, "control": b.Input, "type": f.Base}
		if f.PII {
			props["masked"] = true
			props["mask_leaf"] = b.MaskLeaf
		}
		if len(f.Params) > 0 {
			props["options"] = f.Params
		}
		kids = append(kids, map[string]any{"component": b.Input, "props": props})
	}
	return map[string]any{"component": "form", "resource": rp.Decl.CatalogName,
		"policy": rp.PolicyName, "children": kids}
}

func tableComponent(rp resourcePlan, fields []fieldSpec) map[string]any {
	cols := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		b := fieldBundles[f.Base]
		c := map[string]any{"name": f.Name, "render": b.Render}
		if f.PII {
			c["masked"] = true
			c["mask_leaf"] = b.MaskLeaf
		}
		if b.Ordered {
			c["board"] = true // states drives board/badge
		}
		cols = append(cols, c)
	}
	return map[string]any{"component": "table", "resource": rp.Decl.CatalogName, "columns": cols}
}

func detailComponent(rp resourcePlan, fields []fieldSpec) map[string]any {
	rows := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		b := fieldBundles[f.Base]
		r := map[string]any{"label": f.Name, "render": b.Render}
		if f.PII {
			r["masked"] = true
			r["mask_leaf"] = b.MaskLeaf
		}
		rows = append(rows, r)
	}
	return map[string]any{"component": "card", "resource": rp.Decl.CatalogName, "rows": rows}
}

// openapiFragment is the REST + OpenAPI artifact (ADR-10 §4 item 8): a minimal 3.1
// fragment served by the existing kernel/MCP read/mutate path (no new HTTP routes).
func openapiFragment(rp resourcePlan, fields []fieldSpec) map[string]any {
	props := map[string]any{"id": map[string]any{"type": "integer"}}
	for _, f := range fields {
		if f.PII {
			props[f.Name] = map[string]any{"type": "string", "x-masked": true}
			continue
		}
		props[f.Name] = map[string]any{"type": openapiType(f.Base)}
	}
	path := "/" + rp.Decl.CatalogName
	return map[string]any{
		"openapi": "3.1.0",
		"paths": map[string]any{
			path:            map[string]any{"get": map[string]any{"summary": "list"}, "post": map[string]any{"summary": "create"}},
			path + "/{id}":  map[string]any{"get": map[string]any{"summary": "read"}, "put": map[string]any{"summary": "update"}, "delete": map[string]any{"summary": "delete"}},
		},
		"components": map[string]any{"schemas": map[string]any{shortName(rp.Decl.CatalogName): map[string]any{"type": "object", "properties": props}}},
		"served_by": "kernel/MCP resource.query/mutate",
	}
}

func openapiType(base string) string {
	switch base {
	case "number", "money":
		return "number"
	case "boolean":
		return "boolean"
	default:
		return "string"
	}
}

// shortName is the last path segment of a catalog name (e.g. app/crm/Contact → Contact).
func shortName(catalog string) string {
	if i := strings.LastIndexByte(catalog, '/'); i >= 0 {
		return catalog[i+1:]
	}
	return catalog
}

// nz returns a non-nil slice so JSON renders [] not null (stable detail).
func nz(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
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
		out[k] = fieldSpec{Base: v.Base, PII: v.PII, Params: v.Params}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
