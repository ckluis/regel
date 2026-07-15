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
	return map[string]any{
		"version": ui.TemplateVersion,
		"detail":  lowerDetail(rp, fields),
		"form":    lowerForm(rp, fields),
		"table":   lowerTable(rp, fields),
	}
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
