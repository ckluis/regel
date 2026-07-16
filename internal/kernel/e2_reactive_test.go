package kernel

import (
	"context"
	"net/http"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/ui"
)

// e2_reactive_test.go is the BUILD-E increment D2 kernel battery: the tier-2
// board(R) + dashboard surfaces render server-side and live-update through the
// SAME session machinery derived form/table/detail ride (ADR-10 §7, ADR-11 §5/§6).

// dealSrc declares a states-bearing resource (board-derivable) with a money field
// (dashboard sum) under org policy.
func dealSrc() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Deal = resource({
  fields: { org: "text", name: "text", stage: "states:lead|won", amount: "money" },
  policy: orgScoped,
});
`
}

func (se *sessionEnv) admitDeal(t *testing.T) {
	t.Helper()
	v := se.admit(t, dealSrc(), "app/rx", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Deal: %q (%+v)", v.Outcome, v.Diagnostics)
	}
}

func (se *sessionEnv) dealTable() string { return "res_" + tblSlug("app/rx/Deal") }

func (se *sessionEnv) seedDeal(t *testing.T, org, name, stage string, amount int64) int64 {
	t.Helper()
	tbl := se.dealTable()
	var id int64
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(context.Background(),
			`INSERT INTO `+quoteIdent(tbl)+` (org, name, stage, amount) VALUES ($1,$2,$3,$4) RETURNING id`,
			[]any{org, name, stage, amount}, &id); err != nil {
			t.Fatalf("seed deal: %v", err)
		}
	})
	return id
}

func boardColSlot(t *testing.T, srv *Server, resource, member string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := srv.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.pool.Release(conn)
	vm, err := loadViewMeta(ctx, conn, resource, nil)
	if err != nil || vm.Board == nil {
		t.Fatalf("no board template for %s (err=%v)", resource, err)
	}
	for _, sl := range vm.Board.Slots {
		if sl.Kind == "spliceList" && sl.Group == member {
			return sl.ID
		}
	}
	t.Fatalf("no board column for state %q", member)
	return ""
}

func dashSlot(t *testing.T, srv *Server, resource, field string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := srv.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.pool.Release(conn)
	vm, err := loadViewMeta(ctx, conn, resource, nil)
	if err != nil || vm.Dashboard == nil {
		t.Fatalf("no dashboard template for %s (err=%v)", resource, err)
	}
	for _, sl := range vm.Dashboard.Slots {
		if sl.Field == field {
			return sl.ID
		}
	}
	t.Fatalf("no dashboard tile for field %q", field)
	return ""
}

func hasSplice(op ui.Op, kind ui.SpliceKind, key string) bool {
	for _, s := range op.Splices {
		if s.Kind == kind && s.Key == key {
			return true
		}
	}
	return false
}

// TestBoardLiveStateMove (red-path b): the board renders rows grouped by state, and
// moving a deal lead->won (a form submit) splices it out of the lead column and
// into the won column on a live board session.
func TestBoardLiveStateMove(t *testing.T) {
	se := newSessionEnv(t)
	se.admitDeal(t)
	id := se.seedDeal(t, "acme", "AcmeCo", "lead", 100)

	board := se.mount(t, "app/rx/Deal/board", "human:a", "acme")
	// First paint groups the lead card under the lead column (the row-qualified
	// title cell exists in the lead column, not the won column).
	leadCol := boardColSlot(t, se.srv, "app/rx/Deal", "lead")
	wonCol := boardColSlot(t, se.srv, "app/rx/Deal", "won")
	leadTitle := boardCellSlot(t, se.srv, "app/rx/Deal", "lead")
	if _, ok := board.slots[ui.RowSlotID(leadTitle, fmtID(id))]; !ok {
		t.Fatalf("lead card not grouped under the lead column at first paint; slots=%v", board.slots)
	}

	cb := board.openSSE(0)
	defer cb.close()
	time.Sleep(150 * time.Millisecond) // let the SSE subscription register

	// Move the deal to won via a real form submit.
	stageForm := slotForField(t, se.srv, "app/rx/Deal", "form", "stage")
	ed := se.mount(t, "app/rx/Deal/form/"+fmtID(id), "human:e", "acme")
	ed.postEvent("input", stageForm, "won")
	r := ed.postEvent("submit", "", "")
	if applied, _ := r["applied"].(bool); !applied {
		t.Fatalf("stage-move submit not applied: %+v", r)
	}

	f := cb.nextFrame(t, 4*time.Second)
	rmOp, ok := opFor(f, leadCol)
	if !ok || rmOp.Kind != ui.OpSpliceList || !hasSplice(rmOp, ui.SpliceRemove, fmtID(id)) {
		t.Fatalf("board did not splice the row OUT of the lead column: %+v", f)
	}
	addOp, ok := opFor(f, wonCol)
	if !ok || !hasSplice(addOp, ui.SpliceAdd, fmtID(id)) {
		t.Fatalf("board did not splice the row INTO the won column: %+v", f)
	}
}

// boardCellSlot returns the title cell slot id of a board column.
func boardCellSlot(t *testing.T, srv *Server, resource, member string) string {
	t.Helper()
	ctx := context.Background()
	conn, _ := srv.pool.Acquire(ctx)
	defer srv.pool.Release(conn)
	vm, _ := loadViewMeta(ctx, conn, resource, nil)
	// The title cell shares the column's index; find the setText slot whose id
	// carries the same trailing index as the column's spliceList slot.
	var colIdx string
	for _, sl := range vm.Board.Slots {
		if sl.Kind == "spliceList" && sl.Group == member {
			colIdx = sl.ID[len(sl.ID)-1:]
		}
	}
	for _, sl := range vm.Board.Slots {
		if sl.Kind == "setText" && sl.Field == "name" && sl.ID[len(sl.ID)-1:] == colIdx {
			return sl.ID
		}
	}
	t.Fatalf("no title cell for column %q", member)
	return ""
}

// TestDashboardLiveAggregate (red-path c): the dashboard aggregates counts by state
// and a money sum, and re-aggregates live when a deal changes state.
func TestDashboardLiveAggregate(t *testing.T) {
	se := newSessionEnv(t)
	se.admitDeal(t)
	id1 := se.seedDeal(t, "acme", "A", "lead", 100)
	_ = se.seedDeal(t, "acme", "B", "lead", 200)

	dash := se.mount(t, "app/rx/Deal/dashboard", "human:a", "acme")
	totalSlot := dashSlot(t, se.srv, "app/rx/Deal", "count:__total__")
	leadSlot := dashSlot(t, se.srv, "app/rx/Deal", "count:stage:lead")
	wonSlot := dashSlot(t, se.srv, "app/rx/Deal", "count:stage:won")
	sumSlot := dashSlot(t, se.srv, "app/rx/Deal", "sum:amount")

	if got := dash.slots[totalSlot]; got != "2" {
		t.Fatalf("total tile = %q, want 2", got)
	}
	if got := dash.slots[leadSlot]; got != "2" {
		t.Fatalf("lead count tile = %q, want 2", got)
	}
	if got := dash.slots[wonSlot]; got != "0" {
		t.Fatalf("won count tile = %q, want 0", got)
	}
	if got := dash.slots[sumSlot]; got != "300" {
		t.Fatalf("amount sum tile = %q, want 300", got)
	}

	cd := dash.openSSE(0)
	defer cd.close()
	time.Sleep(150 * time.Millisecond)

	// Move deal A to won: lead 2->1, won 0->1, total unchanged.
	stageForm := slotForField(t, se.srv, "app/rx/Deal", "form", "stage")
	ed := se.mount(t, "app/rx/Deal/form/"+fmtID(id1), "human:e", "acme")
	ed.postEvent("input", stageForm, "won")
	if r := ed.postEvent("submit", "", ""); r["applied"] != true {
		t.Fatalf("submit not applied: %+v", r)
	}

	f := cd.nextFrame(t, 4*time.Second)
	leadOp, ok := opFor(f, leadSlot)
	if !ok || leadOp.Payload != "1" {
		t.Fatalf("lead count did not drop to 1: %+v", f)
	}
	wonOp, ok := opFor(f, wonSlot)
	if !ok || wonOp.Payload != "1" {
		t.Fatalf("won count did not rise to 1: %+v", f)
	}
	if _, moved := opFor(f, totalSlot); moved {
		t.Fatalf("total count must be unchanged (no op), got one: %+v", f)
	}
}

// TestBoardMountRefusedWithoutStates (red-path a): mounting a board on a resource
// with NO states field is a clean 400 refusal, never a crash.
func TestBoardMountRefusedWithoutStates(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t) // Widget has no states field

	req, _ := http.NewRequest("GET", se.ts.URL+"/ui/app/rx/Widget/board", nil)
	req.Header.Set("X-Regel-Actor", "human:a")
	req.Header.Set("X-Regel-Horizon", "acme")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("board mount request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("stateless board mount = %d, want 400 (clean refusal)", resp.StatusCode)
	}
}
