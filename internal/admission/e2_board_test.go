package admission

import (
	"encoding/json"
	"testing"

	"regel.dev/regel/internal/ui"
)

// e2_board_test.go is the BUILD-E increment D2 derivation battery: board(R) and
// dashboard are lowered into the SAME `template` derived_artifact bundle the
// derived form/table/detail ride (ADR-10 §7 tier-2, ADR-11 §1). board(R) is
// conditional on a states field (STAGE-D §13.2 board-derivability flag); dashboard
// is always derived (stat tiles over aggregates). Driven through REAL admission.

// dealBoardSrc declares a resource WITH a states field (board-derivable) plus a
// money field (dashboard sum) and a select field (dashboard count).
func dealBoardSrc() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Deal = resource({
  fields: {
    org: "text",
    title: "text",
    amount: "money",
    tier: "select:bronze|gold",
    stage: "states:lead|qualified|won"
  },
  policy: orgScoped,
});
`
}

// flatSrc declares a resource WITHOUT a states field (board NOT derivable).
func flatSrc() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Note = resource({
  fields: { org: "text", body: "text" },
  policy: orgScoped,
});
`
}

func templateBundleRaw(t *testing.T, w *world, resource string) map[string]json.RawMessage {
	t.Helper()
	var raw string
	ok, err := w.conn.QueryRow(ctxT(t),
		`SELECT detail::text FROM derived_artifact WHERE resource_name=$1 AND pass='template'`,
		[]any{resource}, &raw)
	if err != nil || !ok {
		t.Fatalf("template artifact for %s: ok=%v err=%v", resource, ok, err)
	}
	var bundle map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	return bundle
}

// TestD2BoardDerivedForStates: a states-bearing resource emits a board template
// with one keyed-list column per states member, grouped by the states field.
func TestD2BoardDerivedForStates(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	v, err := admit(ctx, w.conn, dealBoardSrc(), "app/crm", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit Deal: %q %+v", v.Outcome, v.Diagnostics)
	}
	bundle := templateBundleRaw(t, w, "app/crm/Deal")
	rawBoard, ok := bundle["board"]
	if !ok {
		t.Fatalf("states resource must emit a board template; bundle keys=%v", keysOf(bundle))
	}
	board, err := ui.DecodeTemplate(rawBoard)
	if err != nil {
		t.Fatalf("decode board: %v", err)
	}
	if board.Kind != "board" || board.GroupBy != "stage" {
		t.Fatalf("board kind=%q groupBy=%q, want board/stage", board.Kind, board.GroupBy)
	}
	// One spliceList column per states member (lead|qualified|won).
	cols := map[string]bool{}
	for _, s := range board.Slots {
		if s.Kind == "spliceList" {
			cols[s.Group] = true
		}
	}
	for _, m := range []string{"lead", "qualified", "won"} {
		if !cols[m] {
			t.Fatalf("board missing the %q column (slots=%+v)", m, board.Slots)
		}
	}
}

// TestD2DashboardAggregateTiles: every resource emits a dashboard template with a
// total tile, a count tile per enum member, and a sum tile per money field.
func TestD2DashboardAggregateTiles(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	if v, err := admit(ctx, w.conn, dealBoardSrc(), "app/crm", engineer("dev"), nil); err != nil || v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit Deal: %v %q %+v", err, v.Outcome, v.Diagnostics)
	}
	bundle := templateBundleRaw(t, w, "app/crm/Deal")
	dash, err := ui.DecodeTemplate(bundle["dashboard"])
	if err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	want := map[string]bool{
		"count:__total__":   false,
		"count:stage:lead":  false,
		"count:stage:won":   false,
		"count:tier:bronze": false,
		"sum:amount":        false,
	}
	for _, s := range dash.Slots {
		if _, ok := want[s.Field]; ok {
			want[s.Field] = true
		}
	}
	for field, seen := range want {
		if !seen {
			t.Fatalf("dashboard missing the %q stat tile", field)
		}
	}
}

// TestD2BoardRefusedWithoutStates (red-path a): a resource WITHOUT a states field
// emits NO board key — the derivation cleanly declines board rather than crashing.
func TestD2BoardRefusedWithoutStates(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	if v, err := admit(ctx, w.conn, flatSrc(), "app/crm", engineer("dev"), nil); err != nil || v.Outcome != OutcomeAdmitted {
		t.Fatalf("admit Note: %v %q %+v", err, v.Outcome, v.Diagnostics)
	}
	bundle := templateBundleRaw(t, w, "app/crm/Note")
	if _, ok := bundle["board"]; ok {
		t.Fatalf("a stateless resource must NOT derive a board template")
	}
	// dashboard is still derived (total tile), and the three base surfaces remain.
	for _, k := range []string{"detail", "form", "table", "dashboard"} {
		if _, ok := bundle[k]; !ok {
			t.Fatalf("stateless resource missing the %q template", k)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
