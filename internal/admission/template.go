package admission

import (
	"regel.dev/regel/internal/ui"
)

// template.go is the BUILD-D increment D2 render-template derivation (ADR-11 §1):
// the static/dynamic split lowering that runs as the step-5a 'template' pass. Every
// derived form/table/detail component (D1) is lowered to an immutable ui.Template —
// a constant static skeleton plus indexed dynamic binding slots — keyed by the
// resource definition hash and cached forever. The PATCH codec is the owned binary
// one (internal/ui/codec.go); the template itself is the inspectable JSON artifact.
//
// The lowering is a pure function of the resource's field set (the same closed
// bundle table D1's form/table/detail components read), so V6 derivation-parity's
// determinism guarantee extends to the template pass unchanged.

// lowerTemplates lowers a resource's three derived surfaces (detail, form, table)
// to render templates. The detail is the primary render/diff target (one text slot
// per field); the form binds input controls (value slots); the table binds a keyed
// row list (spliceList) over per-column text cells.
func lowerTemplates(rp resourcePlan, fields []fieldSpec) map[string]any {
	out := map[string]any{
		"version":   ui.TemplateVersion,
		"detail":    lowerDetail(rp, fields),
		"form":      lowerForm(rp, fields),
		"table":     lowerTable(rp, fields),
		"dashboard": lowerDashboard(rp, fields),
	}
	// BUILD-E (D2): board(R) is derivable ONLY when the resource carries a states
	// field (the ADR-10 §7 board(R, groupBy) surface; the board-derivability flag
	// rides the states() columns per STAGE-D §13.2). A stateless resource emits no
	// board key — the mount path surfaces that as a clean derivation refusal, never
	// a crash. These two surfaces ride the EXISTING `template` derivation pass (more
	// keys in the one bundle), so requiredPasses / V6 DERIVE_PARITY are unchanged;
	// board/dashboard are conditional (states/aggregate) and so could never be
	// unconditional required passes anyway.
	if sf, ok := statesField(fields); ok {
		out["board"] = lowerBoard(rp, fields, sf)
	}
	return out
}

// statesField returns the resource's states field (the ordered-history enum that
// makes board(R) derivable), if any. A resource has at most one board axis in v1.
func statesField(fields []fieldSpec) (fieldSpec, bool) {
	for _, f := range fields {
		if f.Base == "states" {
			return f, true
		}
	}
	return fieldSpec{}, false
}

// boardTitleField picks the card's title field: the first non-pii scalar text
// field that is neither the states axis nor the org policy column, so a card shows
// something a human reads. "" when the resource has no such field (card = badge only).
func boardTitleField(fields []fieldSpec, states fieldSpec) string {
	for _, f := range fields {
		if f.PII || f.Name == states.Name || f.Name == "org" {
			continue
		}
		if b := fieldBundles[f.Base]; b.Render == "text" {
			return f.Name
		}
	}
	return ""
}

// lowerBoard lowers a states-bearing resource to a KANBAN board template (ADR-10
// §7 board(R, groupBy), ADR-11 §1): a grid of one column per states member, each a
// keyed-list of cards grouped by the states field. A row moving between states is a
// spliceList remove from the old column + add to the new — the live kanban move
// patched through the SAME session machinery derived form/table/detail use.
func lowerBoard(rp resourcePlan, fields []fieldSpec, states fieldSpec) *ui.Template {
	t := &ui.Template{
		Version: ui.TemplateVersion, DefHash: rp.Decl.DefHash, Kind: "board",
		Resource: rp.Decl.CatalogName, Mount: "board", GroupBy: states.Name,
	}
	title := boardTitleField(fields, states)
	statesBundle := fieldBundles[states.Base]
	root := ui.Static("grid")
	for j, member := range states.Params {
		listIdx := len(t.Slots)
		t.Slots = append(t.Slots, ui.Slot{
			ID: slotIDFor("board.col", j), Kind: "spliceList", Group: member,
			ReadSet: []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "horizon"}},
		})
		cells := make([]*ui.Node, 0, 2)
		if title != "" {
			cellIdx := len(t.Slots)
			t.Slots = append(t.Slots, ui.Slot{
				ID: slotIDFor("board.title", j), Kind: "setText", Field: title, Leaf: "text",
				ReadSet: []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "horizon"}},
			})
			cells = append(cells, ui.Leaf("text", cellIdx))
		}
		badgeIdx := len(t.Slots)
		t.Slots = append(t.Slots, ui.Slot{
			ID: slotIDFor("board.badge", j), Kind: "setText", Field: states.Name, Leaf: statesBundle.Render,
			ReadSet: []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "horizon"}},
		})
		cells = append(cells, ui.Leaf(statesBundle.Render, badgeIdx))
		card := ui.Static("card", cells...)
		col := ui.Static("section", ui.Static("heading", ui.Lit(member)), ui.KeyedList("list", listIdx, card))
		root.Children = append(root.Children, col)
	}
	t.Root = root
	return t
}

// lowerDashboard lowers a resource to a DASHBOARD of stat tiles (ADR-10 §7
// dashboard = grid of stat tiles): a total-count tile, one count tile per enum
// (states/select) member, and one sum tile per money field. Each tile is a setText
// slot over a synthetic aggregate field (`count:__total__`, `count:<field>:<member>`,
// `sum:<field>`) the kernel fills from a horizon-scoped SELECT-only aggregate read;
// the horizon ReadSet subscribes the tile so a mutation re-aggregates it live.
func lowerDashboard(rp resourcePlan, fields []fieldSpec) *ui.Template {
	t := &ui.Template{
		Version: ui.TemplateVersion, DefHash: rp.Decl.DefHash, Kind: "dashboard",
		Resource: rp.Decl.CatalogName, Mount: "dashboard",
	}
	hz := []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "horizon"}}
	root := ui.Static("grid")
	tile := func(caption, field, leaf string) {
		idx := len(t.Slots)
		t.Slots = append(t.Slots, ui.Slot{
			ID: slotIDFor("dashboard", idx), Kind: "setText", Field: field, Leaf: leaf, ReadSet: hz,
		})
		root.Children = append(root.Children, ui.Static("card",
			ui.Static("label", ui.Lit(caption)),
			ui.Leaf(leaf, idx),
		))
	}
	tile("total", "count:__total__", "text")
	for _, f := range fields {
		if f.Base == "states" || f.Base == "select" {
			for _, m := range f.Params {
				tile(f.Name+": "+m, "count:"+f.Name+":"+m, "text")
			}
		}
	}
	for _, f := range fields {
		if f.Base == "money" && !f.PII {
			tile("Σ "+f.Name, "sum:"+f.Name, "money")
		}
	}
	t.Root = root
	return t
}

// lowerDetail: card > (stack: label + render-leaf) per field. Slot i binds field i.
func lowerDetail(rp resourcePlan, fields []fieldSpec) *ui.Template {
	t := &ui.Template{
		Version: ui.TemplateVersion, DefHash: rp.Decl.DefHash, Kind: "detail",
		Resource: rp.Decl.CatalogName, Mount: "detail",
	}
	rows := make([]*ui.Node, 0, len(fields))
	for i, f := range fields {
		b := fieldBundles[f.Base]
		id := slotIDFor("detail", i)
		t.Slots = append(t.Slots, ui.Slot{
			ID: id, Kind: "setText", Field: f.Name, Leaf: b.Render,
			Masked: f.PII, MaskLeaf: maskLeafIf(f, b),
			ReadSet: []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "rowId"}},
		})
		rows = append(rows, ui.Static("stack",
			ui.Static("label", ui.Lit(f.Name)),
			ui.Leaf(b.Render, i),
		))
	}
	t.Root = ui.Static("card", rows...)
	return t
}

// lowerForm: section > (stack: label + input-control value-slot) per field.
func lowerForm(rp resourcePlan, fields []fieldSpec) *ui.Template {
	t := &ui.Template{
		Version: ui.TemplateVersion, DefHash: rp.Decl.DefHash, Kind: "form",
		Resource: rp.Decl.CatalogName, Mount: "form",
	}
	rows := make([]*ui.Node, 0, len(fields)+1)
	for i, f := range fields {
		b := fieldBundles[f.Base]
		id := slotIDFor("form", i)
		t.Slots = append(t.Slots, ui.Slot{
			ID: id, Kind: "setValue", Field: f.Name, Leaf: b.Input,
			Masked: f.PII, MaskLeaf: maskLeafIf(f, b),
			ReadSet: []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "rowId"}},
		})
		rows = append(rows, ui.Static("stack",
			ui.Static("label", ui.Lit(f.Name)),
			ui.Leaf(b.Input, i),
		))
	}
	// A form-level alert slot (ADR-11 §7): server-authoritative validation failures
	// and the concurrent-edit reject-and-reconcile ("this record changed") patch
	// target it. Indexed last so the per-field value slots keep their field index.
	alertIdx := len(fields)
	t.Slots = append(t.Slots, ui.Slot{
		ID: slotIDFor("form", alertIdx), Kind: "setText", Field: "__alert__", Leaf: "alert",
		ReadSet: []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "rowId"}},
	})
	rows = append(rows, ui.Leaf("alert", alertIdx))
	t.Root = ui.Static("section", rows...)
	return t
}

// lowerTable: table > (thead static headers) + (tbody keyed-list of tr > text
// cells). The list slot (spliceList) is index 0; each column is a text slot.
func lowerTable(rp resourcePlan, fields []fieldSpec) *ui.Template {
	t := &ui.Template{
		Version: ui.TemplateVersion, DefHash: rp.Decl.DefHash, Kind: "table",
		Resource: rp.Decl.CatalogName, Mount: "table",
	}
	// Slot 0 is the list body (spliceList over the horizon).
	t.Slots = append(t.Slots, ui.Slot{
		ID: slotIDFor("table", 0), Kind: "spliceList",
		ReadSet: []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "horizon"}},
	})
	headers := make([]*ui.Node, 0, len(fields))
	cells := make([]*ui.Node, 0, len(fields))
	for i, f := range fields {
		b := fieldBundles[f.Base]
		colIdx := i + 1 // slot 0 is the list body
		id := slotIDFor("table", colIdx)
		t.Slots = append(t.Slots, ui.Slot{
			ID: id, Kind: "setText", Field: f.Name, Leaf: b.Render,
			Masked: f.PII, MaskLeaf: maskLeafIf(f, b),
			ReadSet: []ui.ReadKey{{Resource: rp.Decl.CatalogName, KeyClass: "horizon"}},
		})
		headers = append(headers, ui.Static("label", ui.Lit(f.Name)))
		cells = append(cells, ui.Leaf(b.Render, colIdx))
	}
	row := ui.Static("stack", cells...) // per-row cell group (rendered per data row)
	t.Root = ui.Static("table",
		ui.Static("nav", headers...),         // header band
		ui.KeyedList("list", 0, row),         // body: keyed rows, spliceList slot 0
	)
	return t
}

// maskLeafIf returns the §7 masking leaf for a pii field, else "".
func maskLeafIf(f fieldSpec, b fieldBundle) string {
	if f.PII {
		return b.MaskLeaf
	}
	return ""
}

// slotIDFor builds a stable slot id: "<mount>.<index>" (mount path + slot index,
// ADR-11 §1).
func slotIDFor(mount string, idx int) string { return mount + "." + itoaSmall(idx) }

func itoaSmall(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
