package kernel

// operatorplane.go is the operatorPlane. v1 (BUILD-E / D2) shipped it read-only:
// an operator-facing tier-2 derived surface (ADR-12 §7) rendered by ADR-11's
// template machinery, two panels server-read from the substrate tables — the
// durable-condition inbox and the gate_refusal ledger.
//
// STAGE-F R4 promotes it to v1.1 WITHOUT adding CRM/operator business rules to Go
// (grep-proven): it is still fixed kernel CHROME over the substrate tables, and the
// three v1.1 additions all ride machinery that already exists:
//
//   (1) SSE live updates — the operatorPlane mount now creates a REAL reactive
//       session (continuation + subscriptions to the durable_condition/gate_refusal
//       "resources") instead of the read-only early return, so the SAME ADR-11 §6
//       invalidation LISTEN loop that drives every resource view re-renders the
//       plane and pushes a splice frame onto its SSE stream when a condition
//       resolves. Zero new transport; the reactive layer's re-render→diff→frame path
//       is reused verbatim (runOperatorStep below).
//   (2) Approval-delta panel — a third panel projecting the pending→resolved
//       transitions (durable_condition.resolved_restart = approve/abort/refuse, +
//       resolved_by) — the delta the operator watches, sourced from the same
//       verdict/condition rows.
//   (3) Write actions through existing doors — the condition inbox carries each
//       row's continuation_id, so the rendered restart button targets the EXISTING
//       restart door (POST /continuation/{id}/restart, or the MCP condition.restart
//       tool). No new authority: the door's own fence (RESTART_DISABLED /
//       CONDITION_MOVED / CAP_REFUSED) refuses; the plane only surfaces it.

import (
	"context"
	"strconv"
	"strings"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/ui"
)

// opCol is one column of an operator-plane list: the row field it binds and the
// tier-1 leaf it renders at (button for the restart action, badge for status).
type opCol struct {
	field string
	leaf  string
}

// opListTemplate builds a headless tier-1 list panel: a titled section holding a
// keyed list of cards, one text/badge/button cell per column. It is a hand-built
// ui.Template (no app admission) — the operator plane is fixed kernel chrome, so its
// template is constructed here rather than derived from a resource declaration.
func opListTemplate(mount, title string, cols []opCol) *ui.Template {
	t := &ui.Template{Version: ui.TemplateVersion, DefHash: "operatorPlane/" + mount, Kind: "table", Mount: mount}
	t.Slots = append(t.Slots, ui.Slot{ID: mount + ".0", Kind: "spliceList"}) // slot 0 = list body
	cells := make([]*ui.Node, 0, len(cols))
	for i, c := range cols {
		idx := i + 1
		t.Slots = append(t.Slots, ui.Slot{ID: mount + "." + strconv.Itoa(idx), Kind: "setText", Field: c.field, Leaf: c.leaf})
		cells = append(cells, ui.Leaf(c.leaf, idx))
	}
	row := ui.Static("card", cells...)
	t.Root = ui.Static("section",
		ui.Static("heading", ui.Lit(title)),
		ui.KeyedList("list", 0, row),
	)
	return t
}

var opConditionCols = []opCol{{"class", "text"}, {"status", "badge"}, {"restart", "button"}, {"continuation", "text"}}
var opRefusalCols = []opCol{{"principal", "text"}, {"outcome", "badge"}, {"scope", "text"}}
var opDeltaCols = []opCol{{"class", "text"}, {"outcome", "badge"}, {"by", "text"}}

// opPanel is one rendered operator-plane panel: its template, the mask/render
// resource string, the list slot id, and the current rows. First paint and every
// live re-render build the SAME panels — the delta is DiffList over the rows.
type opPanel struct {
	tmpl     *ui.Template
	resource string
	listSlot string
	rows     []ui.RowData
}

// operatorPanels reads all three operator panels' live rows from the substrate
// tables (durable_condition inbox, gate_refusal ledger, approval delta). This is
// the single source both the first-paint mount and the live re-render step call, so
// the reactive diff is always against a consistent read.
func (s *Server) operatorPanels(ctx context.Context, conn *pgwire.Conn) ([]opPanel, error) {
	condRows, err := operatorConditions(ctx, conn)
	if err != nil {
		return nil, err
	}
	refRows, err := operatorRefusals(ctx, conn)
	if err != nil {
		return nil, err
	}
	deltaRows, err := operatorDeltas(ctx, conn)
	if err != nil {
		return nil, err
	}
	condT := opListTemplate("opcond", "condition inbox", opConditionCols)
	refT := opListTemplate("opref", "refusal ledger", opRefusalCols)
	deltaT := opListTemplate("opdelta", "approval delta", opDeltaCols)
	return []opPanel{
		{condT, "durable_condition", condT.Slots[0].ID, condRows},
		{refT, "gate_refusal", refT.Slots[0].ID, refRows},
		{deltaT, "durable_condition", deltaT.Slots[0].ID, deltaRows},
	}, nil
}

// renderOperatorPanels server-renders every panel's first paint and returns the
// combined HTML plus the per-list key sequence (the live-diff baseline).
func renderOperatorPanels(panels []opPanel) (string, map[string][]string) {
	var b strings.Builder
	b.WriteString(`<main class="rg-page" data-view="operatorPlane">`)
	keys := map[string][]string{}
	for _, p := range panels {
		html, _ := ui.RenderFirstPaint(p.tmpl, ui.RenderData{Resource: p.resource, Rows: p.rows}, nil)
		b.WriteString(html)
		keys[p.listSlot] = keysOfRows(p.rows)
	}
	b.WriteString(`</main>`)
	return b.String(), keys
}

// operatorSplices diffs each panel's rows against the last-sent key sequence and
// returns the spliceList ops (a resolved condition leaves the inbox; a new
// resolution/refusal is added) plus the advanced key sequences. This is the exact
// keyed-list diff the resource table path uses (session_step.go) — no new diff.
func operatorSplices(panels []opPanel, lastKeys map[string][]string) ([]ui.Op, map[string][]string) {
	var ops []ui.Op
	next := map[string][]string{}
	for _, p := range panels {
		lastRows := listRowsFromKeys(lastKeys[p.listSlot])
		nextRows := make([]ui.ListRow, len(p.rows))
		for i, rd := range p.rows {
			h, _ := ui.RenderRow(p.tmpl, p.resource, rd, nil)
			nextRows[i] = ui.ListRow{Key: rd.Key, HTML: h}
		}
		if sp := ui.DiffList(p.listSlot, lastRows, nextRows); sp != nil {
			ops = append(ops, *sp)
		}
		next[p.listSlot] = keysOfRows(p.rows)
	}
	return ops, next
}

// runOperatorStep is the R4 reactive re-render of the operator plane inside the
// ADR-11 §5 session step (called from runSessionStep when Kind=="operator"). It
// re-reads all three panels, splice-diffs each list against the last-sent key
// sequence, builds ONE frame at atSeq+1, advances the baseline, and re-parks the
// row on its own message channel — the identical outcome shape a resource session
// returns. INVARIANT (§2): exactly one frame per checkpoint (empty splice ⇒ zero-op
// frame). No new authority, no CRM logic — a keyed-list diff over substrate reads.
func (s *Server) runOperatorStep(ctx context.Context, conn *pgwire.Conn, sessionID string, atSeq int64, sess *sessionCFR) (cek.Outcome, *ui.Frame, error) {
	panels, err := s.operatorPanels(ctx, conn)
	if err != nil {
		return cek.Outcome{}, nil, err
	}
	ops, next := operatorSplices(panels, sess.BoardKeys)
	// Divergence digest over the per-list key sequences (the state the client holds).
	snap := map[string]string{}
	for slot, ks := range next {
		snap[slot] = strings.Join(ks, ",")
	}
	dig := ui.FullDigest(snap)
	frame := &ui.Frame{EventSeq: uint64(atSeq + 1), SnapshotHash: uint64(dig), Ops: ops}
	sess.BoardKeys = next
	sess.setDigest(dig)
	out := cek.Outcome{
		Kind:  cek.OutParked,
		State: sess.toState(),
		Wake:  &cek.Wake{Kind: cek.WakeMessage, Channel: sessionID},
	}
	return out, frame, nil
}

// renderOperatorPlane server-renders the whole plane (first paint). Retained for the
// read-only render path; the reactive mount uses operatorPanels + renderOperatorPanels.
func (s *Server) renderOperatorPlane(ctx context.Context, conn *pgwire.Conn) (string, error) {
	panels, err := s.operatorPanels(ctx, conn)
	if err != nil {
		return "", err
	}
	html, _ := renderOperatorPanels(panels)
	return html, nil
}

// operatorConditions lists every OPEN durable_condition with its restart names AND
// its continuation_id — the ADR-12 §7 restart-button targets. The continuation_id is
// what the rendered restart button POSTs to the existing restart door (R4).
func operatorConditions(ctx context.Context, conn *pgwire.Conn) ([]ui.RowData, error) {
	rows, err := conn.Query(ctx, `
SELECT dc.id::text, dc.class, dc.status, coalesce(string_agg(r.name, ','), ''), dc.continuation_id::text
  FROM durable_condition dc
  JOIN continuation c ON c.id = dc.continuation_id
  LEFT JOIN restart r ON r.condition_id = dc.id
 WHERE dc.status = 'open'
 GROUP BY dc.id, dc.class, dc.status, dc.signaled_at, dc.continuation_id
 ORDER BY dc.signaled_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ui.RowData
	for rows.Next() {
		var id, class, status, restarts, contID string
		if err := rows.Scan(&id, &class, &status, &restarts, &contID); err != nil {
			return nil, err
		}
		if restarts == "" {
			restarts = "(no restart)"
		}
		out = append(out, ui.RowData{Key: id, Subject: id, Fields: map[string]string{
			"class": class, "status": status, "restart": restarts, "continuation": contID,
		}})
	}
	return out, rows.Err()
}

// operatorRefusals lists the durable gate_refusal ledger (ADR-12 §7 panel 2):
// principal, outcome, and attempted scope of every audited refusal.
func operatorRefusals(ctx context.Context, conn *pgwire.Conn) ([]ui.RowData, error) {
	rows, err := conn.Query(ctx, `
SELECT refusal_id::text, principal, outcome, coalesce(scope_attempted, '')
  FROM gate_refusal
 ORDER BY created_at DESC
 LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ui.RowData
	for rows.Next() {
		var id, principal, outcome, scope string
		if err := rows.Scan(&id, &principal, &outcome, &scope); err != nil {
			return nil, err
		}
		out = append(out, ui.RowData{Key: id, Subject: id, Fields: map[string]string{
			"principal": principal, "outcome": outcome, "scope": scope,
		}})
	}
	return out, rows.Err()
}

// operatorDeltas is the R4 approval-delta panel: every RESOLVED durable_condition,
// with the restart that resolved it (approve/abort/refuse) and who resolved it —
// the pending→approved/refused transition the operator watches. Sourced from the
// durable_condition resolution rows (the verdict/approval decisions), newest first.
func operatorDeltas(ctx context.Context, conn *pgwire.Conn) ([]ui.RowData, error) {
	rows, err := conn.Query(ctx, `
SELECT dc.id::text, dc.class, r.name, coalesce(dc.resolved_by, '')
  FROM durable_condition dc
  JOIN restart r ON r.id = dc.resolved_restart
 WHERE dc.status = 'resolved'
 ORDER BY dc.resolved_at DESC
 LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ui.RowData
	for rows.Next() {
		var id, class, outcome, by string
		if err := rows.Scan(&id, &class, &outcome, &by); err != nil {
			return nil, err
		}
		out = append(out, ui.RowData{Key: id, Subject: id, Fields: map[string]string{
			"class": class, "outcome": outcome, "by": by,
		}})
	}
	return out, rows.Err()
}
