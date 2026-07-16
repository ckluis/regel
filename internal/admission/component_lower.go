package admission

import (
	"regel.dev/regel/internal/rast"
	"regel.dev/regel/internal/ui"
)

// component_lower.go is the BUILD-E (D3) hand-authored component→template lowering
// (residue STAGE-D §13.3). Stage-D lowered only the DERIVED form/table/detail into
// render templates; a hand-authored component (an admitted TS def composing the
// ADR-10 §7 tier-1 vocabulary, e.g. a CRM `AccountCard` arranging text/badge/button
// around resource fields) passed V2 six-leaf enforcement but no admission pass
// lowered its body. This pass lowers such a def into the SAME ui.Template
// static/dynamic split (ADR-11 §1) the derived surfaces use, so it renders + patches
// through the identical session machinery.
//
// The lowering is minimal but REAL for the reference-corpus shape: a single-param
// component `(props) => <tier-1 composition>` whose leaves bind `props.<field>`
// value expressions. Each such leaf becomes a dynamic slot keyed on the resolved
// field (the DERIVED EvalSlot render path — so a hand-authored leaf is rendered and
// diffed byte-identically to a derived one); the leaf is masking-aware because the
// mount marks a slot bound to a pii resource field masked, exactly as derivation
// does. NAMED RESIDUE: deeper binding expressions (member chains, calls, index) are
// the EvalSlotExpr widening (already unit-tested, evalexpr.go) — the corpus needs
// only `props.<field>`; the six-leaf PII proof holds regardless because V2 rejects a
// pii value bound at any non-leaf component site at admission (unchanged).

// bindKeys are the tier-1 leaf props that carry a value binding (ADR-10 §7 leaves).
var bindKeys = map[string]bool{"value": true, "text": true, "title": true, "label": true}

// lowerComponent recognizes a hand-authored component definition and lowers its body
// to a render template. ok=false when the def is NOT a component (its returned
// expression is not a tier-1 std/ui composition) or when its composition uses an
// element outside the closed 25-roster (no raw-HTML escape hatch — ADR-10 §7 law).
func lowerComponent(ld loweredDef, im *Image) (*ui.Template, bool) {
	ret, ok := componentReturn(ld.Def.Body)
	if !ok {
		return nil, false
	}
	// The returned expression must itself be a tier-1 component call, else this def
	// is not a component (a resource/policy/plain helper) — skip, never a template.
	if ret.Kind != rast.KCall || uiComponentOf(calleeOf(ret), im) == "" {
		return nil, false
	}
	t := &ui.Template{
		Version: ui.TemplateVersion, DefHash: ld.Def.Hash, Kind: "component",
		Resource: "", Mount: "component",
	}
	root, ok := lowerCompNode(ret, im, t)
	if !ok {
		return nil, false
	}
	t.Root = root
	return t, true
}

// componentReturn extracts the single returned expression of a component function
// body (a block `{ return <expr> }` or an arrow expression body `=> <expr>`).
func componentReturn(body *rast.Node) (*rast.Node, bool) {
	body = unwrapValue(body)
	if body == nil || body.Kind != rast.KFunc || len(body.Kids) < 4 {
		return nil, false
	}
	b := body.Kids[3]
	if body.U&2 != 0 { // bit1 = arrow expression body: b IS the returned expr
		if b != nil && b.Kind != rast.KNone {
			return b, true
		}
		return nil, false
	}
	if b == nil || b.Kind != rast.KBlock || len(b.Kids) == 0 || b.Kids[0] == nil {
		return nil, false
	}
	for _, st := range b.Kids[0].Kids { // KList of statements
		if st != nil && st.Kind == rast.KReturn && len(st.Kids) >= 1 {
			if e := st.Kids[0]; e != nil && e.Kind != rast.KNone {
				return e, true
			}
		}
	}
	return nil, false
}

// lowerCompNode lowers one node of the composition. A tier-1 call with a dynamic
// value bind becomes a dynamic leaf slot; with a static literal value becomes a
// static leaf; otherwise it is a structural container whose children are lowered
// recursively. ok=false if any node is not a tier-1 element (the closed-roster law).
func lowerCompNode(node *rast.Node, im *Image, t *ui.Template) (*ui.Node, bool) {
	if node == nil || node.Kind != rast.KCall || len(node.Kids) < 2 {
		return nil, false
	}
	comp := uiComponentOf(calleeOf(node), im)
	if comp == "" {
		return nil, false // a non-tier-1 element in the composition — rejected
	}
	args := node.Kids[1]
	var propsObj, childrenArr *rast.Node
	if args != nil && len(args.Kids) >= 1 {
		propsObj = args.Kids[0]
	}
	if args != nil && len(args.Kids) >= 2 {
		childrenArr = args.Kids[1]
	}
	// Dynamic value-binding leaf: props.<field> at a value/text/title/label prop.
	if bindVal := valueBind(propsObj); bindVal != nil {
		slotIdx := len(t.Slots)
		kind := "setText"
		if comp == "field" || comp == "select" || comp == "checkbox" {
			kind = "setValue"
		}
		t.Slots = append(t.Slots, ui.Slot{
			ID:    slotIDFor("component", slotIdx),
			Kind:  kind,
			Field: memberField(bindVal), // "" for a deeper expr (EvalSlotExpr widening)
			Leaf:  comp,
			// Masked/MaskLeaf are set at MOUNT from the bound resource's pii fields
			// (the component does not know its backing resource until it is mounted).
		})
		return ui.Leaf(comp, slotIdx), true
	}
	// Static leaf: a literal caption/value.
	if lit, ok := literalValue(propsObj); ok {
		return ui.Static(comp, ui.Lit(lit)), true
	}
	// Structural container: lower each child element (a non-tier-1 child fails).
	n := ui.Static(comp)
	if childrenArr != nil && childrenArr.Kind == rast.KArray && len(childrenArr.Kids) >= 1 && childrenArr.Kids[0] != nil {
		for _, ch := range childrenArr.Kids[0].Kids {
			cn, ok := lowerCompNode(ch, im, t)
			if !ok {
				return nil, false
			}
			n.Children = append(n.Children, cn)
		}
	}
	return n, true
}

// calleeOf returns a call's callee node.
func calleeOf(call *rast.Node) *rast.Node {
	if call == nil || call.Kind != rast.KCall || len(call.Kids) < 1 {
		return nil
	}
	return call.Kids[0]
}

// valueBind returns the dynamic value expression of a props object's binding prop
// (value/text/title/label) that references the props parameter, or nil.
func valueBind(propsObj *rast.Node) *rast.Node {
	if propsObj == nil || propsObj.Kind != rast.KObject {
		return nil
	}
	for _, prop := range objProps(propsObj) {
		key, val := propKV(prop)
		if bindKeys[key] && referencesProps(val) {
			return val
		}
	}
	return nil
}

// literalValue returns a props object's static string literal binding value, if any.
func literalValue(propsObj *rast.Node) (string, bool) {
	if propsObj == nil || propsObj.Kind != rast.KObject {
		return "", false
	}
	for _, prop := range objProps(propsObj) {
		key, val := propKV(prop)
		if bindKeys[key] && val != nil && val.Kind == rast.KStr {
			return val.Str, true
		}
	}
	return "", false
}

// referencesProps reports whether n is a reference to the props parameter (KLocal
// index 0) or a member/index access rooted at it.
func referencesProps(n *rast.Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind {
	case rast.KLocal:
		return n.U == 0
	case rast.KMember, rast.KIndex:
		return len(n.Kids) >= 1 && referencesProps(n.Kids[0])
	}
	return false
}

// memberField resolves a depth-1 `props.<field>` access to <field>; "" otherwise.
func memberField(n *rast.Node) string {
	if n != nil && n.Kind == rast.KMember && len(n.Kids) >= 1 &&
		n.Kids[0] != nil && n.Kids[0].Kind == rast.KLocal && n.Kids[0].U == 0 {
		return n.Str
	}
	return ""
}
