package kernel

// operatorplane.go is the BUILD-E (D2) minimal operatorPlane: an operator-facing
// tier-2 derived surface (ADR-12 §7, referenced as §6 in ADR-10 §7 — a stale
// cross-ref fixed by a BUILD-E marker) rendered by ADR-11's template machinery.
// v1 ships two REAL panels read live from the substrate tables: the durable-
// condition inbox (every open ADR-05 durable_condition + its restart names as a
// restart button) and the refusal ledger (gate_refusal rows). This is the "operator
// desk" scenario reviews look at.
//
// NAMED CUT (v1): the panels are server-rendered read-only (no live SSE loop, no
// working restart-button POST, no approval-queue delta / masked-impersonation /
// catalog-browse panels — ADR-12 §7 panels 2-4). The buttons render the exact
// restart targets an operator would press; wiring the press is the D-desk follow-on.

import (
	"context"
	"strconv"
	"strings"

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

var opConditionCols = []opCol{{"class", "text"}, {"status", "badge"}, {"restart", "button"}}
var opRefusalCols = []opCol{{"principal", "text"}, {"outcome", "badge"}, {"scope", "text"}}

// renderOperatorPlane reads the open durable conditions and the refusal ledger live
// and server-renders both panels through the ADR-11 template machinery.
func (s *Server) renderOperatorPlane(ctx context.Context, conn *pgwire.Conn) (string, error) {
	condRows, err := operatorConditions(ctx, conn)
	if err != nil {
		return "", err
	}
	refRows, err := operatorRefusals(ctx, conn)
	if err != nil {
		return "", err
	}
	condT := opListTemplate("opcond", "condition inbox", opConditionCols)
	refT := opListTemplate("opref", "refusal ledger", opRefusalCols)
	condHTML, _ := ui.RenderFirstPaint(condT, ui.RenderData{Resource: "durable_condition", Rows: condRows}, nil)
	refHTML, _ := ui.RenderFirstPaint(refT, ui.RenderData{Resource: "gate_refusal", Rows: refRows}, nil)
	var b strings.Builder
	b.WriteString(`<main class="rg-page" data-view="operatorPlane">`)
	b.WriteString(condHTML)
	b.WriteString(refHTML)
	b.WriteString(`</main>`)
	return b.String(), nil
}

// operatorConditions lists every OPEN durable_condition with its restart names — the
// ADR-12 §7 restart-button targets (bound to {condition_id, restart_name}).
func operatorConditions(ctx context.Context, conn *pgwire.Conn) ([]ui.RowData, error) {
	rows, err := conn.Query(ctx, `
SELECT dc.id::text, dc.class, dc.status, coalesce(string_agg(r.name, ','), '')
  FROM durable_condition dc
  LEFT JOIN restart r ON r.condition_id = dc.id
 WHERE dc.status = 'open'
 GROUP BY dc.id, dc.class, dc.status, dc.signaled_at
 ORDER BY dc.signaled_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ui.RowData
	for rows.Next() {
		var id, class, status, restarts string
		if err := rows.Scan(&id, &class, &status, &restarts); err != nil {
			return nil, err
		}
		if restarts == "" {
			restarts = "(no restart)"
		}
		out = append(out, ui.RowData{Key: id, Subject: id, Fields: map[string]string{
			"class": class, "status": status, "restart": restarts,
		}})
	}
	return out, rows.Err()
}

// operatorRefusals lists the durable gate_refusal ledger (ADR-12 §7 panel 2 seed):
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
