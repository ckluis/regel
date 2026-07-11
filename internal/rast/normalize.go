package rast

import (
	"bytes"
	"sort"
)

// Normalize returns a normalized deep copy of n (ADR-02 §2 ordering rules). It:
//
//   - preserves value-level order (statements, array elements, object-literal
//     properties, params, arguments, template parts) — untouched; and
//   - sorts type-level set-like members: union/intersection members by their
//     canonical encoding, and interface/object-type members likewise (a superset
//     of "by key" that is total over index/call signatures too).
//
// Method-shorthand → arrow and name → Ref/SelfRef/De Bruijn substitution are
// performed upstream in lowering (they need the Resolver and binder context);
// Normalize assumes those are already applied. Normalize is idempotent:
// Normalize(Normalize(x)) deep-equals Normalize(x), and children are normalized
// before a parent sorts, so the sort key is stable.
func Normalize(n *Node) *Node {
	if n == nil {
		return none()
	}
	cp := &Node{Kind: n.Kind, Str: n.Str, U: n.U}
	if len(n.Mag) > 0 {
		cp.Mag = append([]byte(nil), n.Mag...)
	}
	if len(n.Kids) > 0 {
		cp.Kids = make([]*Node, len(n.Kids))
		for i, c := range n.Kids {
			cp.Kids[i] = Normalize(c)
		}
	}
	switch cp.Kind {
	case TUnion, TInter:
		sortListChild(cp, 0)
	case TObject:
		sortListChild(cp, 0)
	case KInterface:
		sortListChild(cp, 1)
	}
	return cp
}

// sortListChild sorts the elements of the KList child at index i by their
// canonical encoding. The child must exist and be a KList.
func sortListChild(n *Node, i int) {
	if i >= len(n.Kids) {
		return
	}
	lst := n.Kids[i]
	if lst == nil || lst.Kind != KList || len(lst.Kids) < 2 {
		return
	}
	encs := make([][]byte, len(lst.Kids))
	for j, m := range lst.Kids {
		encs[j] = canonEncode(m)
	}
	idx := make([]int, len(lst.Kids))
	for j := range idx {
		idx[j] = j
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return bytes.Compare(encs[idx[a]], encs[idx[b]]) < 0
	})
	sorted := make([]*Node, len(lst.Kids))
	for j, k := range idx {
		sorted[j] = lst.Kids[k]
	}
	lst.Kids = sorted
}

// Equal reports structural equality (used by tests and dedupe checks).
func Equal(a, b *Node) bool {
	if a == nil || b == nil {
		return a.IsNone() && b.IsNone()
	}
	if a.Kind != b.Kind || a.Str != b.Str || a.U != b.U || !bytes.Equal(a.Mag, b.Mag) {
		return false
	}
	if len(a.Kids) != len(b.Kids) {
		return false
	}
	for i := range a.Kids {
		if !Equal(a.Kids[i], b.Kids[i]) {
			return false
		}
	}
	return true
}
