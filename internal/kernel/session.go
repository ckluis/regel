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
	View       string            `json:"view"`      // the /ui view path
	Kind       string            `json:"kind"`      // "detail" | "form" | "table"
	Resource   string            `json:"resource"`  // catalog resource name (subscription/invalidation key)
	Table      string            `json:"table"`     // physical table (mask + read key)
	DefHash    string            `json:"def_hash"`  // template definition hash
	RowID      string            `json:"row_id"`    // detail/form subject id ("" for table/create)
	Principal  string            `json:"principal"` // render principal (mask ctx)
	Horizon    string            `json:"horizon"`   // org/scope horizon (policy predicate value)
	RowVersion string            `json:"row_version"`
	Draft      map[string]string `json:"draft"`     // form draft (field -> value), preserved across reconcile
	UILocal    map[string]string `json:"ui_local"`  // UI-local state (open dialog, etc.)
	LastSnap   map[string]string `json:"last_snap"` // last-sent slot snapshot (slotId -> masked value)
	Digest     string            `json:"digest"`    // last-sent FNV-64 digest, decimal
	RowKeys    []string          `json:"row_keys"`  // table: last-sent ordered row-key sequence (spliceList diff)
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
}

// loadViewMeta resolves a resource's derived shape + render templates.
func loadViewMeta(ctx context.Context, conn *pgwire.Conn, resource string) (*viewMeta, error) {
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
	ok, err = conn.QueryRow(ctx,
		`SELECT detail::text FROM derived_artifact WHERE resource_name=$1 AND pass='template'
		 ORDER BY id DESC LIMIT 1`, []any{resource}, &raw2)
	if err != nil || !ok {
		return nil, fmt.Errorf("session: no template artifact for %q (ok=%v err=%v)", resource, ok, err)
	}
	var bundle struct {
		Detail json.RawMessage `json:"detail"`
		Form   json.RawMessage `json:"form"`
		Table  json.RawMessage `json:"table"`
	}
	if err := json.Unmarshal([]byte(raw2), &bundle); err != nil {
		return nil, err
	}
	vm.Detail, _ = ui.DecodeTemplate(bundle.Detail)
	vm.Form, _ = ui.DecodeTemplate(bundle.Form)
	vm.Table_, _ = ui.DecodeTemplate(bundle.Table)
	return vm, nil
}

func (vm *viewMeta) template(kind string) *ui.Template {
	switch kind {
	case "form":
		return vm.Form
	case "table":
		return vm.Table_
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

// renderView produces the first-paint state (slot snapshot + display map), the
// subscription set, and (for a table) the ordered row data — reading live derived
// tables through erf.read/list.
func renderView(ctx context.Context, conn *pgwire.Conn, vm *viewMeta, sess *sessionCFR, mc *ui.MaskCtx) (html string, state map[string]ui.Materialized, subs []subKey, rowVersion string, rows []ui.RowData, err error) {
	t := vm.template(sess.Kind)
	if sess.Kind == "table" {
		rowsData, lerr := erfList(ctx, conn, vm, sess.Horizon, &subs)
		if lerr != nil {
			return "", nil, nil, "", nil, lerr
		}
		data := ui.RenderData{Resource: vm.Table, Rows: rowsData}
		html, state = ui.RenderFirstPaint(t, data, mc)
		return html, state, subs, "", rowsData, nil
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
func (s *Server) mountSession(ctx context.Context, view, principal, horizon string) (mountResult, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return mountResult{}, err
	}
	defer s.pool.Release(conn)

	resource, kind, rowID, perr := parseView(view)
	if perr != nil {
		return mountResult{}, perr
	}
	vm, err := loadViewMeta(ctx, conn, resource)
	if err != nil {
		return mountResult{}, err
	}
	mc, err := admission.BuildMaskCtx(ctx, conn, principal)
	if err != nil {
		return mountResult{}, err
	}
	sess := &sessionCFR{
		View: view, Kind: kind, Resource: resource, Table: vm.Table, DefHash: vm.template(kind).DefHash,
		RowID: rowID, Principal: principal, Horizon: horizon,
		Draft: map[string]string{}, UILocal: map[string]string{},
	}
	html, state, subs, rowVersion, rows, err := renderView(ctx, conn, vm, sess, mc)
	if err != nil {
		return mountResult{}, err
	}
	sess.LastSnap = snapOf(state)
	sess.setDigest(ui.FullDigest(sess.LastSnap))
	sess.RowVersion = rowVersion
	sess.RowKeys = keysOfRows(rows)

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
//   <resource>/table                — horizon list
//   <resource>/detail/<id>          — point read
//   <resource>/form/<id>            — edit form
//   <resource>/form                 — create form (no row)
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
	case "detail", "form", "table":
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
