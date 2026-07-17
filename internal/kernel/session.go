package kernel

// session.go is BUILD-D increment D3: UI sessions as ADR-05 continuation rows
// (ADR-11 §5). A session is one continuation row kind='session' whose CFR captures
// the mount expression, UI-local state (form draft), the last-sent slot snapshot +
// digest, and the principal chain — all as CFR-encodable values (records/strings),
// no new value tags. Its wake is {kind:'message', channel:<session_id>}: user
// events AND invalidations are messages on that channel (ADR-05 one-wake rule).
//
// The event loop composes cfr.ClaimAndStep (claim CAS + lease + step_seq fencing):
// a session-specific resume closure re-reads its subscribed resources through the
// erf read path (recording the subscription set), applies the delivered event or
// invalidation, diffs against the last-sent snapshot, and stashes the resulting
// patch frame. The checkpoint re-parks the row on its message channel and rewrites
// the subscription set — all in the one step transaction. Every checkpoint that
// advances step_seq emits exactly one frame (zero-op when nothing changed).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/ui"
)

// sessionCFR is the durable session payload (ADR-11 §5), encoded into the
// continuation's frames as a cek record of strings/records ONLY. Numeric-ish
// fields (Digest, RowVersion) ride as decimal strings so nothing exceeds the
// {record,string} value lattice — widening the CFR lattice is out of scope here.
type sessionCFR struct {
	View       string            `json:"view"`                // the /ui view path
	Kind       string            `json:"kind"`                // "detail" | "form" | "table"
	Resource   string            `json:"resource"`            // catalog resource name (subscription/invalidation key)
	Component  string            `json:"component,omitempty"` // BUILD-E D3: hand-authored component catalog name (Kind=="component")
	Table      string            `json:"table"`               // physical table (mask + read key)
	DefHash    string            `json:"def_hash"`            // template definition hash
	RowID      string            `json:"row_id"`              // detail/form subject id ("" for table/create)
	Principal  string            `json:"principal"`           // render principal (mask ctx)
	Horizon    string            `json:"horizon"`             // org/scope horizon (policy predicate value)
	RowVersion string            `json:"row_version"`
	Draft      map[string]string `json:"draft"`     // form draft (field -> value), preserved across reconcile
	UILocal    map[string]string `json:"ui_local"`  // UI-local state (open dialog, etc.)
	LastSnap   map[string]string `json:"last_snap"` // last-sent slot snapshot (slotId -> masked value)
	Digest     string            `json:"digest"`    // last-sent FNV-64 digest, decimal
	RowKeys    []string          `json:"row_keys"`  // table: last-sent ordered row-key sequence (spliceList diff)
	// BoardKeys is the per-column last-sent key sequence of a BOARD view (BUILD-E
	// D2): slotID -> ordered row keys in that states column. A state-move splices
	// the row out of its old column list and into the new — the live kanban move.
	BoardKeys map[string][]string `json:"board_keys,omitempty"`
}

func keysOfRows(rows []ui.RowData) []string {
	if len(rows) == 0 {
		return nil
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Key
	}
	return out
}

func (s *sessionCFR) digest() ui.Digest {
	n, _ := strconv.ParseUint(s.Digest, 10, 64)
	return ui.Digest(n)
}
func (s *sessionCFR) setDigest(d ui.Digest) { s.Digest = strconv.FormatUint(uint64(d), 10) }

// toState wraps the session payload in a minimal message-parked CEK state so the
// generalized cfr.Encode/Decode + ClaimAndStep machinery apply unchanged.
func (s *sessionCFR) toState() *cek.State {
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return &cek.State{ParkKind: cek.ParkWake, Tier: cek.TierTrusted, Val: jsonToValue(m)}
}

func sessionFromState(st *cek.State) (*sessionCFR, error) {
	m := valueToJSON(st.Val)
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var s sessionCFR
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Draft == nil {
		s.Draft = map[string]string{}
	}
	if s.UILocal == nil {
		s.UILocal = map[string]string{}
	}
	if s.LastSnap == nil {
		s.LastSnap = map[string]string{}
	}
	return &s, nil
}

func (s *sessionCFR) frames() ([]byte, error) { return cfr.Encode(s.toState()) }

// --- resource / field metadata ------------------------------------------------

type kfield struct {
	Name   string
	Base   string
	PII    bool
	Params []string
}

// viewMeta is the resolved backing of a mounted view.
type viewMeta struct {
	Resource   string
	Table      string
	PolicyName string
	Fields     []kfield // sorted by name
	Detail     *ui.Template
	Form       *ui.Template
	Table_     *ui.Template
	Board      *ui.Template // BUILD-E D2: nil when the resource has no states field
	Dashboard  *ui.Template // BUILD-E D2: stat tiles over aggregates
}

// loadViewMeta resolves a resource's derived shape + render templates. When asOf
// is non-nil (BUILD-E scenario d — as-of rollback observed through the UI), the
// TEMPLATE artifact is resolved AS-OF that instant: the derived_artifact table is
// append-only with a created_at, so the latest template row created at or before
// asOf is the schema/behavior the world had then. A field-add admitted after asOf
// is thus invisible to an as-of mount (its slots are simply absent), while a live
// (asOf==nil) mount resolves the current head template. The row-shape fields come
// from derived_resource (upserted, latest) — they gate data reads/validation, not
// what the as-of first paint renders; the template slots do that.
func loadViewMeta(ctx context.Context, conn *pgwire.Conn, resource string, asOf *time.Time) (*viewMeta, error) {
	var fieldsJSON, table, policy string
	ok, err := conn.QueryRow(ctx,
		`SELECT fields::text, table_name, coalesce(policy_name,'') FROM derived_resource
		 WHERE resource_name=$1 ORDER BY scope_kind, scope_id LIMIT 1`,
		[]any{resource}, &fieldsJSON, &table, &policy)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session: no derived resource %q", resource)
	}
	raw := map[string]struct {
		Base   string   `json:"base"`
		PII    bool     `json:"pii"`
		Params []string `json:"params"`
	}{}
	if err := json.Unmarshal([]byte(fieldsJSON), &raw); err != nil {
		return nil, err
	}
	vm := &viewMeta{Resource: resource, Table: table, PolicyName: policy}
	for name, f := range raw {
		vm.Fields = append(vm.Fields, kfield{Name: name, Base: f.Base, PII: f.PII, Params: f.Params})
	}
	sort.Slice(vm.Fields, func(i, j int) bool { return vm.Fields[i].Name < vm.Fields[j].Name })

	var raw2 string
	if asOf != nil {
		ok, err = conn.QueryRow(ctx,
			`SELECT detail::text FROM derived_artifact WHERE resource_name=$1 AND pass='template'
			 AND created_at <= $2 ORDER BY id DESC LIMIT 1`, []any{resource, *asOf}, &raw2)
	} else {
		ok, err = conn.QueryRow(ctx,
			`SELECT detail::text FROM derived_artifact WHERE resource_name=$1 AND pass='template'
			 ORDER BY id DESC LIMIT 1`, []any{resource}, &raw2)
	}
	if err != nil || !ok {
		return nil, fmt.Errorf("session: no template artifact for %q (ok=%v err=%v)", resource, ok, err)
	}
	var bundle struct {
		Detail    json.RawMessage `json:"detail"`
		Form      json.RawMessage `json:"form"`
		Table     json.RawMessage `json:"table"`
		Board     json.RawMessage `json:"board"`     // BUILD-E D2: absent when no states field
		Dashboard json.RawMessage `json:"dashboard"` // BUILD-E D2
	}
	if err := json.Unmarshal([]byte(raw2), &bundle); err != nil {
		return nil, err
	}
	vm.Detail, _ = ui.DecodeTemplate(bundle.Detail)
	vm.Form, _ = ui.DecodeTemplate(bundle.Form)
	vm.Table_, _ = ui.DecodeTemplate(bundle.Table)
	if len(bundle.Board) > 0 {
		vm.Board, _ = ui.DecodeTemplate(bundle.Board)
	}
	if len(bundle.Dashboard) > 0 {
		vm.Dashboard, _ = ui.DecodeTemplate(bundle.Dashboard)
	}
	return vm, nil
}

func (vm *viewMeta) template(kind string) *ui.Template {
	switch kind {
	case "form":
		return vm.Form
	case "table":
		return vm.Table_
	case "board":
		return vm.Board
	case "dashboard":
		return vm.Dashboard
	default:
		return vm.Detail
	}
}

// hasOrg reports whether the resource carries an `org` text field — the D3 policy
// predicate column. When present, reads filter `WHERE org = :horizon` and the
// session's horizon is its org; otherwise every row is in a single global horizon.
func (vm *viewMeta) hasOrg() bool {
	for _, f := range vm.Fields {
		if f.Name == "org" && f.Base == "text" && !f.PII {
			return true
		}
	}
	return false
}

// displayColumn returns the physical column that carries a field's display value
// (the first of columnsFor), or "" for a pii/hasMany field with no base column.
func displayColumn(f kfield) string {
	if f.PII {
		return ""
	}
	switch f.Base {
	case "money":
		return f.Name
	case "address":
		return f.Name + "_line1"
	case "relation":
		if len(f.Params) >= 1 && f.Params[0] == "hasMany" {
			return ""
		}
		return f.Name + "_id"
	default:
		return f.Name
	}
}

// --- the erf read path (ADR-11 §6): reads record subscriptions ----------------

// subKey is one (resource, key) subscription dependency.
type subKey struct {
	Resource string
	Key      string
}

// rowIDKey scopes a point-read subscription by BOTH the row id and the reader's
// horizon, so a mutation published under one horizon never wakes a session that
// read the same id under a different (excluding) horizon — policy-respecting
// invalidation for free (ADR-11 §6).
func rowIDKey(id, horizon string) string { return "rowId:" + id + "@" + horizon }
func horizonKey(hz string) string        { return "horizon:" + hz }

// erfRead is the point-read native (ADR-11 §6): it reads ONE row of a resource by
// id under the policy predicate (org-scoped), building RenderData for a detail/form
// render and recording (resource, key=rowId) into the subscription set. Returns
// ok=false when the row is absent or outside the session's horizon (policy denies).
func erfRead(ctx context.Context, conn *pgwire.Conn, vm *viewMeta, rowID, horizon string, subs *[]subKey) (ui.RenderData, string, bool, error) {
	*subs = append(*subs, subKey{Resource: vm.Resource, Key: rowIDKey(rowID, horizon)})
	sel := []string{"id::text", "coalesce(row_version,0)::text"}
	names := []string{}
	for _, f := range vm.Fields {
		col := displayColumn(f)
		if col == "" {
			continue
		}
		sel = append(sel, "coalesce("+quoteIdent(col)+"::text,'')")
		names = append(names, f.Name)
	}
	where := "id=$1"
	args := []any{rowID}
	if vm.hasOrg() {
		where += " AND org=$2"
		args = append(args, horizon)
	}
	sqlStr := "SELECT " + strings.Join(sel, ", ") + " FROM " + quoteIdent(vm.Table) + " WHERE " + where
	dests := make([]any, len(sel))
	vals := make([]string, len(sel))
	for i := range dests {
		dests[i] = &vals[i]
	}
	found, err := conn.QueryRow(ctx, sqlStr, args, dests...)
	if err != nil {
		return ui.RenderData{}, "", false, err
	}
	if !found {
		return ui.RenderData{}, "", false, nil // absent or out-of-horizon
	}
	data := ui.RenderData{Resource: vm.Table, Subject: vals[0], Fields: map[string]string{}}
	rowVersion := vals[1]
	for i, name := range names {
		data.Fields[name] = vals[i+2]
	}
	return data, rowVersion, true, nil
}

// erfList is the horizon-read native (ADR-11 §6): it lists a resource's rows under
// the policy predicate (org-scoped rows only) for a table render, recording
// (resource, key=horizon) into the subscription set — the horizon key the policy
// filter uses, so invalidation respects policy for free.
func erfList(ctx context.Context, conn *pgwire.Conn, vm *viewMeta, horizon string, subs *[]subKey) ([]ui.RowData, error) {
	*subs = append(*subs, subKey{Resource: vm.Resource, Key: horizonKey(horizon)})
	sel := []string{"id::text"}
	names := []string{}
	for _, f := range vm.Fields {
		col := displayColumn(f)
		if col == "" {
			continue
		}
		sel = append(sel, "coalesce("+quoteIdent(col)+"::text,'')")
		names = append(names, f.Name)
	}
	sqlStr := "SELECT " + strings.Join(sel, ", ") + " FROM " + quoteIdent(vm.Table)
	args := []any{}
	if vm.hasOrg() {
		sqlStr += " WHERE org=$1"
		args = append(args, horizon)
	}
	sqlStr += " ORDER BY id"
	rows, err := conn.Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ui.RowData
	for rows.Next() {
		vals := make([]string, len(sel))
		dests := make([]any, len(sel))
		for i := range dests {
			dests[i] = &vals[i]
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, err
		}
		rd := ui.RowData{Key: vals[0], Subject: vals[0], Fields: map[string]string{}}
		for i, name := range names {
			rd.Fields[name] = vals[i+1]
		}
		out = append(out, rd)
	}
	return out, rows.Err()
}

// aggregateDashboard computes the dashboard's stat-tile values (BUILD-E D2) with
// horizon-scoped SELECT-only aggregate reads over the derived table, recording a
// (resource, horizon) subscription so a mutation re-aggregates the tiles live. The
// synthetic field keys match the dashboard template slots exactly: `count:__total__`,
// `count:<field>:<member>` (pre-seeded 0 so an empty member still shows a tile),
// and `sum:<field>`. This is the ADR-10 §4 "dashboards ride typed std/sql queries"
// path — the same SELECT-only read discipline std/sql.query enforces via dbReader,
// issued kernel-side because the subscription-recording read lives in this loop
// (BUILD-E marker: ADR-10 §4/§7, ADR-11 §6).
func aggregateDashboard(ctx context.Context, conn *pgwire.Conn, vm *viewMeta, horizon string, subs *[]subKey) (map[string]string, error) {
	*subs = append(*subs, subKey{Resource: vm.Resource, Key: horizonKey(horizon)})
	out := map[string]string{}
	where := ""
	args := []any{}
	if vm.hasOrg() {
		where = " WHERE org=$1"
		args = append(args, horizon)
	}
	tbl := quoteIdent(vm.Table)
	var total int64
	if _, err := conn.QueryRow(ctx, "SELECT count(*) FROM "+tbl+where, args, &total); err != nil {
		return nil, err
	}
	out["count:__total__"] = strconv.FormatInt(total, 10)
	for _, f := range vm.Fields {
		if f.Base != "states" && f.Base != "select" {
			continue
		}
		for _, m := range f.Params {
			out["count:"+f.Name+":"+m] = "0"
		}
		col := quoteIdent(f.Name)
		rows, err := conn.Query(ctx, "SELECT "+col+"::text, count(*) FROM "+tbl+where+" GROUP BY "+col, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var member string
			var n int64
			if err := rows.Scan(&member, &n); err != nil {
				rows.Close()
				return nil, err
			}
			out["count:"+f.Name+":"+member] = strconv.FormatInt(n, 10)
		}
		cerr := rows.Err()
		rows.Close()
		if cerr != nil {
			return nil, cerr
		}
	}
	for _, f := range vm.Fields {
		if f.Base != "money" || f.PII {
			continue
		}
		var sum int64
		if _, err := conn.QueryRow(ctx,
			"SELECT coalesce(sum("+quoteIdent(f.Name)+"),0)::bigint FROM "+tbl+where, args, &sum); err != nil {
			return nil, err
		}
		out["sum:"+f.Name] = strconv.FormatInt(sum, 10)
	}
	return out, nil
}

// loadComponentTemplate reads a hand-authored component's lowered render template
// (BUILD-E D3) and applies masking flags from the BOUND resource's pii fields: a
// component leaf bound to a pii field is masked at its §7 leaf, exactly as
// derivation marks a derived leaf — the component does not know its backing
// resource until it is mounted over one, so the mask decision lands here. Returns a
// clean error when the component was never admitted (no component_template artifact).
func loadComponentTemplate(ctx context.Context, conn *pgwire.Conn, name string, vm *viewMeta) (*ui.Template, error) {
	var raw string
	ok, err := conn.QueryRow(ctx,
		`SELECT detail::text FROM derived_artifact WHERE resource_name=$1 AND pass='component_template'
		 ORDER BY id DESC LIMIT 1`, []any{name}, &raw)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session: no component %q (not admitted)", name)
	}
	ct, derr := ui.DecodeTemplate([]byte(raw))
	if derr != nil {
		return nil, derr
	}
	pii := map[string]string{} // field -> §7 mask leaf
	for _, f := range vm.Fields {
		if f.PII {
			pii[f.Name] = fieldMaskLeaf(f)
		}
	}
	for i := range ct.Slots {
		if leaf, ok := pii[ct.Slots[i].Field]; ok {
			ct.Slots[i].Masked = true
			ct.Slots[i].MaskLeaf = leaf
		}
		ct.Slots[i].ReadSet = []ui.ReadKey{{Resource: vm.Resource, KeyClass: "rowId"}}
	}
	return ct, nil
}

// fieldMaskLeaf returns a pii field's §7 masking leaf (the render leaf its value is
// masked at); "" for a non-pii-wrappable base (address/relation).
func fieldMaskLeaf(f kfield) string {
	switch f.Base {
	case "money":
		return "money"
	case "select", "states":
		return "badge"
	case "address", "relation":
		return ""
	default:
		return "text"
	}
}

// boardKeysFromRows partitions the mounted rows into the per-column key sequences a
// board's spliceList diff bases on (BUILD-E D2): slotID -> ordered keys in that
// states column.
func boardKeysFromRows(t *ui.Template, rows []ui.RowData) map[string][]string {
	out := map[string][]string{}
	if t == nil {
		return out
	}
	for _, sl := range t.Slots {
		if sl.Kind != "spliceList" {
			continue
		}
		var keys []string
		for _, rd := range rows {
			if rd.Fields[t.GroupBy] == sl.Group {
				keys = append(keys, rd.Key)
			}
		}
		out[sl.ID] = keys
	}
	return out
}

// renderView produces the first-paint state (slot snapshot + display map), the
// subscription set, and (for a table) the ordered row data — reading live derived
// tables through erf.read/list.
func renderView(ctx context.Context, conn *pgwire.Conn, vm *viewMeta, sess *sessionCFR, mc *ui.MaskCtx) (html string, state map[string]ui.Materialized, subs []subKey, rowVersion string, rows []ui.RowData, err error) {
	t := vm.template(sess.Kind)
	if sess.Kind == "table" || sess.Kind == "board" {
		// board is a table read grouped by state (BUILD-E D2): erf lists the same
		// horizon rows; RenderFirstPaint groups them into the states columns.
		rowsData, lerr := erfList(ctx, conn, vm, sess.Horizon, &subs)
		if lerr != nil {
			return "", nil, nil, "", nil, lerr
		}
		data := ui.RenderData{Resource: vm.Table, Rows: rowsData}
		html, state = ui.RenderFirstPaint(t, data, mc)
		return html, state, subs, "", rowsData, nil
	}
	if sess.Kind == "dashboard" {
		// BUILD-E D2: the dashboard aggregates over the resource (counts by
		// state/select member, sums over money) via horizon-scoped SELECT-only
		// reads, subscribing on the horizon so a mutation re-aggregates it live.
		aggFields, aerr := aggregateDashboard(ctx, conn, vm, sess.Horizon, &subs)
		if aerr != nil {
			return "", nil, nil, "", nil, aerr
		}
		data := ui.RenderData{Resource: vm.Table, Fields: aggFields}
		html, state = ui.RenderFirstPaint(t, data, mc)
		return html, state, subs, "", nil, nil
	}
	// A hand-authored component (BUILD-E D3) binds a resource ROW as its props and
	// renders through the SAME point-read path as detail: erf reads the row (records
	// the rowId subscription), and RenderFirstPaint materializes the component's
	// lowered slots exactly as a derived detail's — masking-aware, diffable, live.
	if sess.Kind == "component" {
		ct, cerr := loadComponentTemplate(ctx, conn, sess.Component, vm)
		if cerr != nil {
			return "", nil, nil, "", nil, cerr
		}
		t = ct
	}

	data, rv, ok, rerr := erfRead(ctx, conn, vm, sess.RowID, sess.Horizon, &subs)
	if rerr != nil {
		return "", nil, nil, "", nil, rerr
	}
	if !ok {
		// Out-of-horizon / absent: render the empty skeleton (no fields), still subscribed.
		data = ui.RenderData{Resource: vm.Table, Subject: sess.RowID, Fields: map[string]string{}}
	}
	// A form re-render overlays any preserved draft so a reconcile keeps unsaved edits,
	// and surfaces the server-authoritative alert (validation / concurrent-edit) into
	// the §7 alert slot.
	if sess.Kind == "form" {
		for k, v := range sess.Draft {
			data.Fields[k] = v
		}
		data.Fields["__alert__"] = sess.UILocal["__alert__"]
	}
	html, state = ui.RenderFirstPaint(t, data, mc)
	return html, state, subs, rv, nil, nil
}

func snapOf(state map[string]ui.Materialized) map[string]string {
	out := make(map[string]string, len(state))
	for id, m := range state {
		out[id] = m.Snapshot
	}
	return out
}

// displayMapOf projects the display values keyed by slot id.
func displayMapOf(state map[string]ui.Materialized) map[string]string {
	out := make(map[string]string, len(state))
	for id, m := range state {
		out[id] = m.Display
	}
	return out
}

// --- subscription table maintenance -------------------------------------------

// writeSubscriptions replaces a session's subscription set atomically on conn
// (used inside the render/checkpoint transaction, ADR-11 §5).
func writeSubscriptions(ctx context.Context, conn cfr.DB, sessionID string, subs []subKey) error {
	if _, err := conn.Exec(ctx, `DELETE FROM subscription WHERE session_id=$1`, sessionID); err != nil {
		return err
	}
	seen := map[subKey]bool{}
	for _, s := range subs {
		if seen[s] {
			continue
		}
		seen[s] = true
		if _, err := conn.Exec(ctx,
			`INSERT INTO subscription (session_id, resource, key) VALUES ($1,$2,$3)
			 ON CONFLICT DO NOTHING`, sessionID, s.Resource, s.Key); err != nil {
			return err
		}
	}
	return nil
}

// --- mount (GET /ui) ----------------------------------------------------------

// mountResult is what a first-paint mount produced for the HTTP layer.
type mountResult struct {
	SessionID string
	EventSeq  int64
	HTML      string
}

// mountSession resolves a view, renders first paint, and creates the session
// continuation row + its subscriptions in ONE transaction (ADR-11 §5). The row is
// message-parked on its own id channel; eventSeq starts at step_seq 0.
// serveReadSnapshot runs fn inside a REPEATABLE READ, READ ONLY transaction on conn
// — the SERVE-side read isolation (L7 R1 P2.7). The render/mount read phase issues
// several reads (loadViewMeta's resource + template artifact, the component
// template, the mask context, renderView's data rows); at READ COMMITTED a
// concurrent admission committing a name_pointer / derived-artifact flip BETWEEN two
// of them splits dispatch (template from one epoch, rows from another). One snapshot
// pins them all. Mirrors dbreader.Query's asOf snapshot. fn must not write (the
// SERIALIZABLE write phase runs AFTER this returns); the read-only txn is committed
// (nothing to persist) or rolled back on error.
func serveReadSnapshot(ctx context.Context, conn *pgwire.Conn, fn func() error) error {
	if err := conn.BeginReadSnapshot(ctx); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_ = conn.Rollback(ctx)
		return err
	}
	return conn.Commit(ctx)
}

func (s *Server) mountSession(ctx context.Context, view, principal, horizon, component string, asOf *time.Time) (mountResult, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return mountResult{}, err
	}
	defer s.pool.Release(conn)

	// The whole read/render phase runs under ONE REPEATABLE READ snapshot (L7): the
	// template artifact, the component template, and the data rows are all resolved
	// against the same instant, so a concurrent re-derivation cannot split dispatch.
	var (
		resource, kind, rowID string
		vm                    *viewMeta
		defHash               string
		sess                  *sessionCFR
		html                  string
		state                 map[string]ui.Materialized
		subs                  []subKey
		rowVersion            string
		rows                  []ui.RowData
		earlyOP               *mountResult // operatorPlane early return (read-only, no continuation)
	)
	if rerr := serveReadSnapshot(ctx, conn, func() error {
		// BUILD-E D2: the operatorPlane is a GLOBAL operator surface, not backed by a
		// derived resource — a dedicated server-rendered read of the live substrate
		// tables (durable_condition inbox + gate_refusal ledger). Read-only in v1 (no
		// SSE/continuation row; named cut in operatorplane.go).
		if strings.Trim(view, "/") == "operatorPlane" {
			opHTML, herr := s.renderOperatorPlane(ctx, conn)
			if herr != nil {
				return herr
			}
			earlyOP = &mountResult{SessionID: admission.NewUUID(), EventSeq: 0, HTML: opHTML}
			return nil
		}

		var perr error
		resource, kind, rowID, perr = parseView(view)
		if perr != nil {
			return perr
		}
		var verr error
		vm, verr = loadViewMeta(ctx, conn, resource, asOf)
		if verr != nil {
			return verr
		}
		// BUILD-E D3: a `?component=<name>` mount overlays a hand-authored component into
		// the detail slot — same resource row (props), same rowId subscription, same
		// render/diff path as the derived detail it replaces (ADR-10 §7 "polish overlays
		// derivation; a hand-built component admits into the same slot a derived one
		// filled"). defHash comes from the component's own template.
		if component != "" {
			kind = "component"
			ct, cerr := loadComponentTemplate(ctx, conn, component, vm)
			if cerr != nil {
				return cerr
			}
			defHash = ct.DefHash
		} else {
			// BUILD-E D2 (red-path a): board is derivable ONLY for a states-bearing
			// resource; a board mount on a stateless one is a clean derivation refusal,
			// not a crash — the template is simply absent.
			if vm.template(kind) == nil {
				return fmt.Errorf("session: %s has no %s surface (board requires a states field)", resource, kind)
			}
			defHash = vm.template(kind).DefHash
		}
		mc, merr := admission.BuildMaskCtx(ctx, conn, principal)
		if merr != nil {
			return merr
		}
		sess = &sessionCFR{
			View: view, Kind: kind, Resource: resource, Component: component, Table: vm.Table, DefHash: defHash,
			RowID: rowID, Principal: principal, Horizon: horizon,
			Draft: map[string]string{}, UILocal: map[string]string{},
		}
		html, state, subs, rowVersion, rows, err = renderView(ctx, conn, vm, sess, mc)
		return err
	}); rerr != nil {
		return mountResult{}, rerr
	}
	if earlyOP != nil {
		return *earlyOP, nil
	}
	sess.LastSnap = snapOf(state)
	sess.setDigest(ui.FullDigest(sess.LastSnap))
	sess.RowVersion = rowVersion
	sess.RowKeys = keysOfRows(rows)
	if sess.Kind == "board" {
		sess.BoardKeys = boardKeysFromRows(vm.Board, rows)
	}

	frames, err := sess.frames()
	if err != nil {
		return mountResult{}, err
	}
	sessionID := admission.NewUUID()
	principalJSON := fmt.Sprintf(`{"subject":%q}`, principal)

	if err := cfr.RetrySerializable(ctx, "mount", func(int) error {
		if e := conn.BeginSerializable(ctx); e != nil {
			return e
		}
		committed := false
		defer func() {
			if !committed {
				_ = conn.Rollback(ctx)
			}
		}()
		if _, e := conn.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
VALUES ($1::uuid,'session',$2,$3,$4,('\x'||$5)::bytea,
  jsonb_build_object('kind','message','channel',$7::text),'sleeping',$6::jsonb,0)`,
			sessionID, sess.DefHash, epochOrOne(s.epoch), cfr.FormatVersion, hexOf(frames), principalJSON, sessionID); e != nil {
			return e
		}
		if e := writeSubscriptions(ctx, conn, sessionID, subs); e != nil {
			return e
		}
		if e := conn.Commit(ctx); e != nil {
			return e
		}
		committed = true
		return nil
	}); err != nil {
		return mountResult{}, err
	}

	// Seed the SSE ring so a reconnect at cursor 0 replays nothing but is gapless.
	s.sse().ensure(sessionID)
	return mountResult{SessionID: sessionID, EventSeq: 0, HTML: html}, nil
}

// parseView splits a /ui view path into (resource, kind, rowId). Forms:
//
//	<resource>/table                — horizon list
//	<resource>/detail/<id>          — point read
//	<resource>/form/<id>            — edit form
//	<resource>/form                 — create form (no row)
func parseView(view string) (resource, kind, rowID string, err error) {
	view = strings.Trim(view, "/")
	parts := strings.Split(view, "/")
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("session: view %q needs <resource>/<kind>[/<id>]", view)
	}
	// The kind is the last non-id segment; an id (all digits) may follow.
	last := parts[len(parts)-1]
	if isAllDigits(last) && len(parts) >= 3 {
		rowID = last
		kind = parts[len(parts)-2]
		resource = strings.Join(parts[:len(parts)-2], "/")
	} else {
		kind = last
		resource = strings.Join(parts[:len(parts)-1], "/")
	}
	switch kind {
	case "detail", "form", "table", "board", "dashboard":
	default:
		return "", "", "", fmt.Errorf("session: unknown view kind %q", kind)
	}
	return resource, kind, rowID, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func epochOrOne(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

// quoteIdent double-quotes a SQL identifier (matching admission.quoteIdent).
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func hexOf(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexd[c>>4]
		out[i*2+1] = hexd[c&0xf]
	}
	return string(out)
}

// idleTTL is the ADR-11 §5 session idle expiry (30 min from updated_at).
const idleTTL = 30 * time.Minute
