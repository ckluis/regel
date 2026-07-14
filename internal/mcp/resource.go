package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/pgwire"
)

// resource.go serves the ADR-12 §2 resource.query / resource.mutate tools over the
// C1 minimal derived-resource shape: masked reads (PII masked ALWAYS — the agent
// can never hold a reveal grant, §4) and policy-predicate + row-version-guarded
// writes (ADR-11 §7). The derived model lives in derived_resource (shape) + the
// physical res_* table (rows).

// maskToken is what a PII field materializes to on the agent plane — a token, never
// plaintext (ADR-12 §4). It contains none of the underlying value.
const maskToken = "‹masked›"

// maskLeakForRedPath, when set (confused-deputy load-bearing demo ONLY, §4a), makes
// resource.query render PII fields as PLAINTEXT — the masking control disabled. It
// exists solely so the corpus can PROVE masking load-bearing: flip it on and a
// confused-deputy exfil fixture escapes plaintext (the corpus reds); restore it and
// the fixture is masked again. Default false — the plane never leaks in normal
// operation. A var, mirroring mcp.leakOutOfScope / mcp.ResolutionFloor.
var maskLeakForRedPath bool

// derivedField is one column of a derived resource.
type derivedField struct {
	Base string `json:"base"`
	PII  bool   `json:"pii"`
}

// derivedInfo is a resolved derived resource: its physical table + field map + scope.
type derivedInfo struct {
	TableName string
	Fields    map[string]derivedField
	ScopeKind int
	ScopeID   string
}

// resolveDerived finds a derived resource by name (or qname) within the caller's
// visible scopes, most-specific-first. found=false ⇒ NOT_FOUND (floored by caller).
func resolveDerived(ctx context.Context, conn *pgwire.Conn, chain catalog.Chain, resource string) (*derivedInfo, bool, error) {
	name := resource
	if n, kind, id, ok := parseQName(resource); ok {
		if !scopeVisible(chain, kind, id) {
			return nil, false, nil
		}
		return loadDerived(ctx, conn, name0(n), kind, id)
	}
	// Plain name: pick the most-specific visible scope that has this resource.
	scopes := visibleScopes(chain)
	for i := len(scopes) - 1; i >= 0; i-- {
		di, ok, err := loadDerived(ctx, conn, name, scopes[i].Kind, scopes[i].ID)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return di, true, nil
		}
	}
	return nil, false, nil
}

func name0(n string) string { return n }

func loadDerived(ctx context.Context, conn *pgwire.Conn, name string, kind int, id string) (*derivedInfo, bool, error) {
	var fieldsJSON, table string
	found, err := conn.QueryRow(ctx,
		`SELECT fields::text, table_name FROM derived_resource
		 WHERE resource_name=$1 AND scope_kind=$2 AND scope_id=$3`,
		[]any{name, kind, id}, &fieldsJSON, &table)
	if err != nil || !found {
		return nil, false, err
	}
	raw := map[string]derivedField{}
	if err := json.Unmarshal([]byte(fieldsJSON), &raw); err != nil {
		return nil, false, err
	}
	return &derivedInfo{TableName: table, Fields: raw, ScopeKind: kind, ScopeID: id}, true, nil
}

// queryResource returns rows from a derived resource's physical table, PII masked
// ALWAYS (ADR-12 §4). filter is column→value equality over declared fields only.
func queryResource(ctx context.Context, conn *pgwire.Conn, chain catalog.Chain, resource string, filter map[string]any, limit int) (map[string]any, bool, error) {
	di, ok, err := resolveDerived(ctx, conn, chain, resource)
	if err != nil || !ok {
		return nil, false, err
	}
	cols := sortedFieldNames(di.Fields)
	selCols := []string{"id"}
	selCols = append(selCols, cols...)
	quoted := make([]string, len(selCols))
	for i, c := range selCols {
		quoted[i] = quoteIdent(c)
	}
	sqlText := "SELECT " + strings.Join(quoted, ", ") + " FROM " + quoteIdent(di.TableName)
	var args []any
	var wh []string
	idx := 1
	// filter over declared columns only (identifier allow-list defeats injection).
	for _, c := range cols {
		if v, present := filter[c]; present {
			wh = append(wh, fmt.Sprintf("%s = $%d", quoteIdent(c), idx))
			args = append(args, fmt.Sprintf("%v", v))
			idx++
		}
	}
	if len(wh) > 0 {
		sqlText += " WHERE " + strings.Join(wh, " AND ")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	sqlText += fmt.Sprintf(" ORDER BY id LIMIT %d", limit)

	rows, err := conn.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, false, err
	}
	out := []map[string]any{}
	masked := []string{}
	for name, f := range di.Fields {
		if f.PII {
			masked = append(masked, name)
		}
	}
	sort.Strings(masked)
	maskedSet := map[string]bool{}
	for _, m := range masked {
		maskedSet[m] = true
	}
	for rows.Next() {
		dest := make([]any, len(selCols))
		holders := make([]string, len(selCols))
		for i := range holders {
			dest[i] = &holders[i]
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, false, err
		}
		row := map[string]any{}
		for i, c := range selCols {
			if maskedSet[c] && !maskLeakForRedPath {
				row[c] = maskToken // PII masked ALWAYS — no plaintext leaves the plane
			} else {
				row[c] = holders[i]
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return map[string]any{"resource": makeQName(resource0(resource), di.ScopeKind, di.ScopeID),
		"rows": out, "masked_fields": masked}, true, nil
}

func resource0(r string) string {
	if n, _, _, ok := parseQName(r); ok {
		return n
	}
	return r
}

// mutateResource inserts or updates one row under the derived policy predicate +
// row-version guard (ADR-12 §2). An agent may mutate only within its own overlay
// scope (default-deny product data); the physical table carries a row_version
// column (added lazily) for optimistic concurrency.
func mutateResource(ctx context.Context, conn *pgwire.Conn, p admission.Principal, resource, op string, id int64, values map[string]any, baseVersion int) (map[string]any, error) {
	di, ok, err := resolveDerived(ctx, conn, p.Chain, resource)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]any{"ok": false, "error": "NOT_FOUND"}, nil
	}
	// Policy predicate (minimal): an agent writes only within its own overlay scope.
	if p.ActorKind == "agent" {
		overlay := admission.AgentOverlayScope(p)
		if di.ScopeKind != overlay.Kind || di.ScopeID != overlay.ID || overlay.ID == "" {
			return map[string]any{"ok": false, "error": "POLICY_DENIED",
				"detail": "agent may mutate only resources in its own overlay scope"}, nil
		}
	}
	if err := ensureRowVersion(ctx, conn, di.TableName); err != nil {
		return nil, err
	}
	cols := sortedFieldNames(di.Fields)
	allowed := map[string]bool{}
	for _, c := range cols {
		allowed[c] = true
	}
	// Only declared columns may be written (identifier allow-list).
	var setCols []string
	var setVals []any
	for _, c := range cols {
		if v, present := values[c]; present {
			setCols = append(setCols, c)
			setVals = append(setVals, fmt.Sprintf("%v", v))
		}
	}

	switch op {
	case "insert":
		colList := []string{}
		ph := []string{}
		args := []any{}
		for i, c := range setCols {
			colList = append(colList, quoteIdent(c))
			ph = append(ph, fmt.Sprintf("$%d", i+1))
			args = append(args, setVals[i])
		}
		var q string
		if len(colList) == 0 {
			q = "INSERT INTO " + quoteIdent(di.TableName) + " DEFAULT VALUES RETURNING id, row_version"
		} else {
			q = "INSERT INTO " + quoteIdent(di.TableName) + " (" + strings.Join(colList, ", ") +
				") VALUES (" + strings.Join(ph, ", ") + ") RETURNING id, row_version"
		}
		var newID int64
		var ver int
		if _, err := conn.QueryRow(ctx, q, args, &newID, &ver); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "id": newID, "rowVersion": ver}, nil

	case "update":
		if id == 0 {
			return map[string]any{"ok": false, "error": "MISSING_ID"}, nil
		}
		var assigns []string
		args := []any{}
		i := 1
		for j, c := range setCols {
			assigns = append(assigns, fmt.Sprintf("%s = $%d", quoteIdent(c), i))
			args = append(args, setVals[j])
			i++
		}
		assigns = append(assigns, "row_version = row_version + 1")
		q := "UPDATE " + quoteIdent(di.TableName) + " SET " + strings.Join(assigns, ", ") +
			fmt.Sprintf(" WHERE id = $%d AND row_version = $%d RETURNING row_version", i, i+1)
		args = append(args, id, baseVersion)
		var ver int
		found, err := conn.QueryRow(ctx, q, args, &ver)
		if err != nil {
			return nil, err
		}
		if !found {
			return map[string]any{"ok": false, "error": "VERSION_CONFLICT"}, nil
		}
		return map[string]any{"ok": true, "id": id, "rowVersion": ver}, nil

	default:
		return map[string]any{"ok": false, "error": "BAD_OP"}, nil
	}
}

// ensureRowVersion adds the OCC column if the derived table lacks it (Stage-C
// minimal row-version guard; the derive DDL is left untouched for determinism).
func ensureRowVersion(ctx context.Context, conn *pgwire.Conn, table string) error {
	_, err := conn.Exec(ctx,
		"ALTER TABLE "+quoteIdent(table)+" ADD COLUMN IF NOT EXISTS row_version int NOT NULL DEFAULT 1")
	return err
}

func sortedFieldNames(fields map[string]derivedField) []string {
	out := make([]string, 0, len(fields))
	for k := range fields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quoteIdent double-quotes a SQL identifier (used only on the declared-field
// allow-list and the deterministic table slug — never on arbitrary caller text).
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
