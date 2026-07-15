package ui

import (
	"sort"
	"strings"
)

// diff.go computes the minimal per-slot patch (ADR-11 §1: the diff unit is the
// dynamic binding slot — never a DOM subtree, never a VDOM). Diff re-compares each
// slot's SNAPSHOT value (the masked value for a pii leaf) and frames only the
// deltas, carrying the Display value (plaintext only under a live grant) as the op
// payload. Structural list edits are DiffList (keyed add/remove/move).

// Diff produces the minimal op set for changed flat slots: it walks the union of
// slot ids present in BOTH snapshots and emits one op per slot whose snapshot
// changed. The op kind is the slot's declared kind (setText / setValue / setAttr);
// the payload is the new Display value. Row-qualified list-cell ids (col#key) are
// resolved to their column slot's kind. Structural row edits are DiffList.
func Diff(t *Template, last, next map[string]Materialized) []Op {
	ids := make([]string, 0, len(next))
	for id := range next {
		if _, ok := last[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // deterministic op order (aids tests + stable wire)
	var ops []Op
	for _, id := range ids {
		o, n := last[id], next[id]
		if o.Snapshot == n.Snapshot {
			continue // byte-identical snapshot ⇒ no op (empty-diff slots drop out)
		}
		ops = append(ops, Op{SlotID: id, Kind: t.kindForID(id), Attr: t.attrForID(id), Payload: n.Display})
	}
	return ops
}

// kindForID resolves the op kind of a slot id, stripping a row-qualifier (col#key).
func (t *Template) kindForID(id string) OpKind {
	base := id
	if i := strings.IndexByte(id, '#'); i >= 0 {
		base = id[:i]
	}
	if s := t.slotByID(base); s != nil {
		switch s.Kind {
		case "setValue":
			return OpSetValue
		case "setAttr":
			return OpSetAttr
		}
	}
	return OpSetText
}

func (t *Template) attrForID(id string) string {
	base := id
	if i := strings.IndexByte(id, '#'); i >= 0 {
		base = id[:i]
	}
	if s := t.slotByID(base); s != nil {
		return s.Attr
	}
	return ""
}

// ListRow is one keyed row's identity + rendered HTML, for the structural diff.
type ListRow struct {
	Key  string
	HTML string // rendered row HTML (used on add)
}

// DiffList computes the keyed add/remove/move splice for one list slot, comparing
// the previous ordered key sequence to the next. Removals and additions are keyed;
// a surviving row whose position changed is a move to its new index. Per-cell text
// changes on surviving rows are NOT here — those are ordinary setText ops from Diff
// over the row-qualified cell ids. Returns a single spliceList Op (empty splice
// list ⇒ no op, nil returned).
func DiffList(listSlotID string, last, next []ListRow) *Op {
	lastIdx := map[string]int{}
	for i, r := range last {
		lastIdx[r.Key] = i
	}
	nextIdx := map[string]int{}
	for i, r := range next {
		nextIdx[r.Key] = i
	}
	var splices []Splice
	// Removals: in last, gone in next.
	for _, r := range last {
		if _, ok := nextIdx[r.Key]; !ok {
			splices = append(splices, Splice{Kind: SpliceRemove, Key: r.Key})
		}
	}
	// Additions + moves: walk next in order.
	for i, r := range next {
		if _, existed := lastIdx[r.Key]; !existed {
			splices = append(splices, Splice{Kind: SpliceAdd, Key: r.Key, Index: i, HTML: r.HTML})
			continue
		}
		if lastIdx[r.Key] != i {
			splices = append(splices, Splice{Kind: SpliceMove, Key: r.Key, Index: i})
		}
	}
	if len(splices) == 0 {
		return nil
	}
	return &Op{SlotID: listSlotID, Kind: OpSpliceList, Splices: splices}
}
