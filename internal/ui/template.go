package ui

import "encoding/json"

// TemplateVersion is the render-template encoding version. The template artifact
// is a JSON derived_artifact (immutable, cached forever by definition hash); the
// PATCH frame is the owned BINARY codec (codec.go), a distinct wire format.
const TemplateVersion = 1

// Template is the ADR-11 §1 static/dynamic split of one component-kind definition:
// a constant static skeleton (Root) plus indexed dynamic binding Slots. It is a
// derived artifact keyed by the definition hash — immutable, cache-forever. Slot
// ids are stable component-instance paths (mount path + slot index), so a given
// template always addresses the same slot the same way.
type Template struct {
	Version  int    `json:"version"`
	DefHash  string `json:"def_hash"`  // the keying definition hash (immutable key)
	Kind     string `json:"kind"`      // "form" | "table" | "detail" | "board" | "dashboard" | "component"
	Resource string `json:"resource"`  // backing resource catalog name ("" for hand-authored)
	Mount    string `json:"mount"`     // mount-path prefix for slot ids (e.g. "detail")
	Root     *Node  `json:"root"`      // the static skeleton (with embedded slot refs)
	Slots    []Slot `json:"slots"`     // indexed dynamic binding slots
	// GroupBy is the states field name a BOARD template groups its rows by (BUILD-E
	// D2, ADR-10 §7 board(R, groupBy)): each keyed-list column slot carries a Group
	// value and renders only the rows whose GroupBy field equals it. "" for every
	// non-board template (form/table/detail/dashboard/component).
	GroupBy string `json:"group_by,omitempty"`
}

// Node is one node of the static skeleton. A node is exactly one of:
//   - a literal text node (Component=="" , Text set) — constant markup text;
//   - a component element (Component set) — an ADR-10 §7 tier-1 element with static
//     Props and Children; if Slot>=0 its text content is the dynamic Slots[Slot];
//   - a keyed-list container (List>=0) whose rows are Row rendered per data row and
//     diffed by spliceList (List indexes the list slot in Slots).
type Node struct {
	Component string            `json:"c,omitempty"`
	Text      string            `json:"t,omitempty"`
	Props     map[string]string `json:"p,omitempty"`
	Children  []*Node           `json:"k,omitempty"`
	Slot      int               `json:"s"`            // dynamic text/value slot index, or -1
	List      int               `json:"l"`            // keyed-list slot index, or -1
	Row       *Node             `json:"r,omitempty"`  // per-row subtree (when List>=0)
}

// Slot is one indexed dynamic binding (ADR-11 §1): {slotId, exprPath, readSet}.
// exprPath is an ADR-02 node path into a hand-authored definition, OR a field path
// into a derived-component record (Field). readSet is the (resource, key-class)
// set the slot depends on — for derived components the backing resource + a
// horizon/rowId key class; for hand-authored, the reachable erf.read calls.
type Slot struct {
	ID       string    `json:"id"`                 // stable component-instance path
	Kind     string    `json:"kind"`               // "setText" | "setAttr" | "setValue" | "spliceList"
	ExprPath []int     `json:"expr_path,omitempty"`// ADR-02 node path (hand-authored)
	Field    string    `json:"field,omitempty"`    // backing field (derived component)
	Leaf     string    `json:"leaf,omitempty"`     // rendering tier-1 component (text/badge/money/…)
	Attr     string    `json:"attr,omitempty"`     // attribute name (Kind==setAttr)
	Masked   bool      `json:"masked,omitempty"`   // bound to a pii/vault value
	MaskLeaf string    `json:"mask_leaf,omitempty"`// the §7 masking leaf when Masked
	ReadSet  []ReadKey `json:"read_set,omitempty"`
	// Group is a BOARD column list slot's states value (BUILD-E D2): the spliceList
	// renders only rows whose Template.GroupBy field equals Group. "" otherwise.
	Group string `json:"group,omitempty"`
}

// ReadKey is one (resource, key-class) dependency of a slot (ADR-11 §6): key-class
// is "rowId" for a point read and "horizon" for a list read — the same horizon the
// policy filter uses, so invalidation respects policy for free.
type ReadKey struct {
	Resource string `json:"resource"`
	KeyClass string `json:"key_class"` // "rowId" | "horizon"
}

// Encode marshals the template to its JSON artifact form (deterministic map key
// order via struct fields; slot order is the indexed order).
func (t *Template) Encode() ([]byte, error) { return json.Marshal(t) }

// DecodeTemplate parses a template artifact.
func DecodeTemplate(b []byte) (*Template, error) {
	var t Template
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// --- node constructors (avoid the Slot/List zero-value trap) ------------------
// A bare &Node{} has Slot==0 and List==0, which would be read as "dynamic slot 0"
// / "list slot 0". These constructors set the -1 sentinels explicitly, so callers
// (the derivation lowering and tests) never mis-declare a static node.

// Static builds a static container element with children.
func Static(component string, children ...*Node) *Node {
	return &Node{Component: component, Slot: -1, List: -1, Children: children}
}

// Leaf builds a dynamic leaf: the element's text/value content is Slots[slot].
func Leaf(component string, slot int) *Node {
	return &Node{Component: component, Slot: slot, List: -1}
}

// Lit builds a literal static-text node.
func Lit(text string) *Node { return &Node{Text: text, Slot: -1, List: -1} }

// KeyedList builds a keyed-list container bound to Slots[listSlot], whose rows are
// `row` rendered once per data row and diffed by spliceList.
func KeyedList(component string, listSlot int, row *Node) *Node {
	return &Node{Component: component, Slot: -1, List: listSlot, Row: row}
}

// slotByID returns the slot with the given id, or nil.
func (t *Template) slotByID(id string) *Slot {
	for i := range t.Slots {
		if t.Slots[i].ID == id {
			return &t.Slots[i]
		}
	}
	return nil
}
