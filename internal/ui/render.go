package ui

import (
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
)

// render.go is the server-side render: RenderFirstPaint walks a template's static
// skeleton and materializes every dynamic slot into full HTML (ADR-11 §1 "real
// HTML first paint, no hydration"). Every text value is HTML-escaped — there is NO
// raw-HTML path anywhere in this package (ADR-10 §7: no raw-HTML primitive ever),
// and no exported function accepts pre-escaped markup: the only inputs are the
// template (constant skeleton), typed data, and the mask context.

// Materialized is one slot's evaluated result. Snapshot is what enters the slot
// snapshot (digest input + last-sent map) — for a masking leaf it is ALWAYS the
// mask token (plus the grant id when revealed), so plaintext NEVER enters the
// snapshot (ADR-11 §8 invariant). Display is what is painted / framed — plaintext
// only when a live reveal grant holds, and only ever in the transient frame.
type Materialized struct {
	Snapshot string
	Display  string
}

// RenderData is the typed data one template renders over. For form/detail it is a
// single row (Subject + Fields); for table/list it is Rows. Fields carry only the
// NON-pii display values — pii plaintext never appears here; it is resolved (or
// masked) through MaskCtx at materialization, so the render data itself is safe to
// hold, log, or checkpoint.
type RenderData struct {
	Resource string
	Subject  string
	Fields   map[string]string
	Rows     []RowData
}

// RowData is one table/list row: a stable Key (the rowId, used for keyed-list
// diff), its Subject (mask key), and its non-pii field display values.
type RowData struct {
	Key     string
	Subject string
	Fields  map[string]string
}

// MaskCtx is the ADR-11 §8 runtime masking context. Reveal resolves a LIVE reveal
// grant for (resource, subject, field): ok=true returns the plaintext (recovered
// from the vault) and the grant id; ok=false means no live grant → the mask token
// is emitted. The DB-backed implementation (grant_row lookup + VaultReveal + a
// reveal_audit insert) is injected by the caller (admission/D3); this package
// stays pure. A revealed materialization is audit-rowed inside Reveal.
type MaskCtx struct {
	Principal string
	Reveal    func(resource, subject, field string) (plaintext, grantID string, ok bool)
}

// MaskGlyph is the masked-value glyph (ADR-11 §8). The full mask token is
// MaskGlyph + "·" + a 6-hex stable tag of (resource‖subject‖field) — it carries
// NONE of the underlying value, yet distinct masked fields get distinct tokens so
// the divergence digest tells them apart. Documented, stable, plaintext-free.
const MaskGlyph = "••••"

// MaskToken is the plaintext-free token a masking leaf materializes to when not
// revealed. Stable across renders for a given (resource, subject, field).
func MaskToken(resource, subject, field string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(resource))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(subject))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(field))
	return MaskGlyph + "·" + strconv.FormatUint(h.Sum64(), 16)[:6]
}

// materializeMask resolves a masking-leaf slot: token by default; on a live grant
// the snapshot stays masked (token|grantId) while Display carries plaintext.
func (mc *MaskCtx) materializeMask(resource, subject, field string) Materialized {
	token := MaskToken(resource, subject, field)
	if mc != nil && mc.Reveal != nil {
		if pt, gid, ok := mc.Reveal(resource, subject, field); ok {
			return Materialized{Snapshot: token + "|" + gid, Display: pt}
		}
	}
	return Materialized{Snapshot: token, Display: token}
}

// EvalSlot materializes one DERIVED-component slot by direct data lookup: a masking
// leaf routes through MaskCtx; a plain field is its display value. For hand-authored
// component defs use EvalSlotExpr (evalexpr.go), which re-evaluates the slot
// expression at its exprPath over the CEK value lattice.
func EvalSlot(s Slot, data RenderData, mc *MaskCtx) Materialized {
	if s.Masked {
		return mc.materializeMask(data.Resource, data.Subject, s.Field)
	}
	v := data.Fields[s.Field]
	return Materialized{Snapshot: v, Display: v}
}

// evalRowSlot materializes a slot against one table/list row.
func evalRowSlot(s Slot, resource string, row RowData, mc *MaskCtx) Materialized {
	if s.Masked {
		return mc.materializeMask(resource, row.Subject, s.Field)
	}
	v := row.Fields[s.Field]
	return Materialized{Snapshot: v, Display: v}
}

// RenderFirstPaint walks the skeleton and materializes every slot, returning the
// full HTML string and the slot snapshot state (the map the digest and the diff
// key on). ALL text is HTML-escaped. For a keyed-list node it expands one row
// subtree per data row, minting row-qualified slot ids (RowSlotID).
func RenderFirstPaint(t *Template, data RenderData, mc *MaskCtx) (string, map[string]Materialized) {
	state := map[string]Materialized{}
	var b strings.Builder
	renderNode(&b, t, t.Root, data, mc, state)
	return b.String(), state
}

// RowSlotID is the stable slot id of a per-row cell: the column slot id qualified
// by the row key. Deterministic and reversible enough for spliceList targeting.
func RowSlotID(colSlotID, rowKey string) string { return colSlotID + "#" + rowKey }

func renderNode(b *strings.Builder, t *Template, n *Node, data RenderData, mc *MaskCtx, state map[string]Materialized) {
	if n == nil {
		return
	}
	if n.Component == "" && n.List < 0 {
		b.WriteString(escapeText(n.Text)) // literal static text node
		return
	}
	el := elementFor(n.Component)
	// Keyed-list container: render header/children statically, then one Row per data row.
	if n.List >= 0 {
		listSlot := t.Slots[n.List]
		writeOpenTag(b, el, n, listSlot.ID, "spliceList")
		for _, row := range data.Rows {
			// BOARD grouping (BUILD-E D2): a column list slot renders only the rows
			// whose GroupBy states value equals this column's Group.
			if t.GroupBy != "" && listSlot.Group != "" && row.Fields[t.GroupBy] != listSlot.Group {
				continue
			}
			renderRow(b, t, n.Row, data.Resource, row, mc, state)
		}
		b.WriteString("</" + el.tag + ">")
		return
	}
	// Dynamic leaf: this element's text content is Slots[n.Slot].
	if n.Slot >= 0 {
		s := t.Slots[n.Slot]
		m := EvalSlot(s, data, mc)
		state[s.ID] = m
		writeOpenTag(b, el, n, s.ID, s.Kind)
		if s.Kind == "setValue" {
			// input-like: value rides an attribute, element is void/self-contained.
			b.WriteString("</" + el.tag + ">")
			return
		}
		b.WriteString(escapeText(m.Display))
		b.WriteString("</" + el.tag + ">")
		return
	}
	// Static container element with children.
	writeOpenTag(b, el, n, "", "")
	for _, c := range n.Children {
		renderNode(b, t, c, data, mc, state)
	}
	b.WriteString("</" + el.tag + ">")
}

// RenderRow renders ONE keyed table/list row to HTML + its row-qualified slot
// state, for a spliceList add (§2). resource is the mask key (the physical table).
// It locates the template's keyed-list row subtree; a template with no list yields
// empty output.
func RenderRow(t *Template, resource string, row RowData, mc *MaskCtx) (string, map[string]Materialized) {
	rowNode := findListRow(t, t.Root)
	if rowNode == nil {
		return "", map[string]Materialized{}
	}
	state := map[string]Materialized{}
	var b strings.Builder
	renderRow(&b, t, rowNode, resource, row, mc, state)
	return b.String(), state
}

// RenderRowForList renders ONE keyed row for a SPECIFIC list slot (BUILD-E D2): a
// board has several keyed-list columns, so a spliceList add must render the row
// subtree of the addressed column, not merely the first list found (RenderRow).
func RenderRowForList(t *Template, listSlotID, resource string, row RowData, mc *MaskCtx) (string, map[string]Materialized) {
	rowNode := findListRowByID(t, t.Root, listSlotID)
	if rowNode == nil {
		return "", map[string]Materialized{}
	}
	state := map[string]Materialized{}
	var b strings.Builder
	renderRow(&b, t, rowNode, resource, row, mc, state)
	return b.String(), state
}

// findListRowByID returns the per-row subtree of the keyed-list node whose slot id
// is listSlotID, or nil.
func findListRowByID(t *Template, n *Node, listSlotID string) *Node {
	if n == nil {
		return nil
	}
	if n.List >= 0 && t.Slots[n.List].ID == listSlotID {
		return n.Row
	}
	for _, c := range n.Children {
		if r := findListRowByID(t, c, listSlotID); r != nil {
			return r
		}
	}
	return nil
}

// findListRow returns the per-row subtree of the template's keyed-list node.
func findListRow(t *Template, n *Node) *Node {
	if n == nil {
		return nil
	}
	if n.List >= 0 {
		return n.Row
	}
	for _, c := range n.Children {
		if r := findListRow(t, c); r != nil {
			return r
		}
	}
	return nil
}

// renderRow expands one row subtree, minting row-qualified slot ids so each cell
// is independently diffable and digestible.
func renderRow(b *strings.Builder, t *Template, row *Node, resource string, rd RowData, mc *MaskCtx, state map[string]Materialized) {
	if row == nil {
		return
	}
	el := elementFor(row.Component)
	// Row element carries its stable key for spliceList add/remove/move targeting.
	b.WriteString("<" + el.tag)
	writeStaticAttrs(b, el, row)
	b.WriteString(` data-key="` + escapeAttr(rd.Key) + `">`)
	for _, c := range row.Children {
		if c.Slot >= 0 {
			s := t.Slots[c.Slot]
			m := evalRowSlot(s, resource, rd, mc)
			id := RowSlotID(s.ID, rd.Key)
			state[id] = m
			cel := elementFor(c.Component)
			b.WriteString("<" + cel.tag)
			writeStaticAttrs(b, cel, c)
			b.WriteString(` data-slot="` + escapeAttr(id) + `">`)
			b.WriteString(escapeText(m.Display))
			b.WriteString("</" + cel.tag + ">")
			continue
		}
		renderNode(b, t, c, RenderData{Resource: resource}, mc, state)
	}
	b.WriteString("</" + el.tag + ">")
}

// element is the HTML mapping of a tier-1 component: its tag plus fixed
// attributes (semantic role / ARIA / class) per ADR-10 §7.
type element struct {
	tag   string
	attrs [][2]string // fixed attribute pairs (role, aria-*, class)
}

func writeOpenTag(b *strings.Builder, el element, n *Node, slotID, slotKind string) {
	b.WriteString("<" + el.tag)
	writeStaticAttrs(b, el, n)
	if slotID != "" {
		b.WriteString(` data-slot="` + escapeAttr(slotID) + `"`)
		if slotKind == "spliceList" {
			b.WriteString(` data-list="1"`)
		}
	}
	b.WriteString(">")
}

func writeStaticAttrs(b *strings.Builder, el element, n *Node) {
	for _, a := range el.attrs {
		b.WriteString(" " + a[0] + `="` + escapeAttr(a[1]) + `"`)
	}
	// Author-static props render as data-* attributes in deterministic key order.
	if len(n.Props) > 0 {
		keys := make([]string, 0, len(n.Props))
		for k := range n.Props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(` data-` + escapeAttr(k) + `="` + escapeAttr(n.Props[k]) + `"`)
		}
	}
}

// elementFor maps every one of the 25 closed tier-1 components (ADR-10 §7) to a
// SEMANTIC, headless, CSS-var-themed element. ARIA is attached exactly where §7
// names it: section landmark, nav, alert live region, dialog focus-trap, status.
func elementFor(component string) element {
	switch component {
	case "page":
		return element{tag: "main", attrs: [][2]string{{"class", "rg-page"}}}
	case "section":
		return element{tag: "section", attrs: [][2]string{{"role", "region"}, {"class", "rg-section"}}}
	case "stack":
		return element{tag: "div", attrs: [][2]string{{"class", "rg-stack"}}}
	case "grid":
		return element{tag: "div", attrs: [][2]string{{"class", "rg-grid"}}}
	case "nav":
		return element{tag: "nav", attrs: [][2]string{{"class", "rg-nav"}}}
	case "heading":
		return element{tag: "h2", attrs: [][2]string{{"class", "rg-heading"}}}
	case "text":
		return element{tag: "span", attrs: [][2]string{{"class", "rg-text"}}}
	case "label":
		return element{tag: "label", attrs: [][2]string{{"class", "rg-label"}}}
	case "badge":
		return element{tag: "span", attrs: [][2]string{{"class", "rg-badge"}}}
	case "money":
		return element{tag: "span", attrs: [][2]string{{"class", "rg-money"}}}
	case "datetime":
		return element{tag: "time", attrs: [][2]string{{"class", "rg-datetime"}}}
	case "avatar":
		return element{tag: "span", attrs: [][2]string{{"class", "rg-avatar"}}}
	case "icon":
		return element{tag: "span", attrs: [][2]string{{"class", "rg-icon"}, {"aria-hidden", "true"}}}
	case "link":
		return element{tag: "a", attrs: [][2]string{{"class", "rg-link"}}}
	case "button":
		return element{tag: "button", attrs: [][2]string{{"type", "button"}, {"class", "rg-button"}}}
	case "field":
		return element{tag: "input", attrs: [][2]string{{"class", "rg-field"}}}
	case "select":
		return element{tag: "select", attrs: [][2]string{{"class", "rg-select"}}}
	case "checkbox":
		return element{tag: "input", attrs: [][2]string{{"type", "checkbox"}, {"class", "rg-checkbox"}}}
	case "dialog":
		return element{tag: "div", attrs: [][2]string{{"role", "dialog"}, {"aria-modal", "true"}, {"tabindex", "-1"}, {"class", "rg-dialog"}}}
	case "card":
		return element{tag: "article", attrs: [][2]string{{"class", "rg-card"}}}
	case "list":
		return element{tag: "ul", attrs: [][2]string{{"class", "rg-list"}}}
	case "table":
		return element{tag: "table", attrs: [][2]string{{"class", "rg-table"}}}
	case "alert":
		return element{tag: "div", attrs: [][2]string{{"role", "alert"}, {"aria-live", "assertive"}, {"class", "rg-alert"}}}
	case "spinner":
		return element{tag: "span", attrs: [][2]string{{"role", "status"}, {"aria-live", "polite"}, {"class", "rg-spinner"}}}
	case "empty":
		return element{tag: "div", attrs: [][2]string{{"class", "rg-empty"}}}
	default:
		// Unknown component names never reach here from a derived template (the
		// bundle table is closed); a hand-authored one is V-checked at admission.
		return element{tag: "div", attrs: [][2]string{{"class", "rg-unknown"}}}
	}
}

// escapeText HTML-escapes element text content (the ONLY way text enters HTML).
func escapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// escapeAttr HTML-escapes a double-quoted attribute value.
func escapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
