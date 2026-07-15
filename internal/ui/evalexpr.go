package ui

import (
	"math"
	"math/big"
	"strconv"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/rast"
)

// evalexpr.go is the hand-authored-component slot evaluator (ADR-11 §1). A derived
// component's slot is a direct data lookup (EvalSlot, render.go); a hand-authored
// component-kind definition's slot is a binding EXPRESSION at the slot's exprPath,
// re-evaluated over the CEK value lattice. This is minimal but REAL for the two
// forms the reference corpus needs — a prop reference and a field access (member /
// index chain over the props record) — reusing the exact cek.Value representation
// the interpreter and CFR codec share, so a slot value here is byte-for-byte what
// the machine would produce.
//
// RESIDUE (named): arbitrary expression forms (calls, arithmetic, conditionals)
// and per-row masking of a hand-authored leaf are a later widening — the derived
// path (the whole D1 surface) needs neither. V2 already forbids a pii value at any
// non-leaf hand-authored sink at admission, so an unmasked hand-authored leak is
// unnameable regardless.

// EvalSlotExpr re-evaluates the binding expression `expr` (the rast node at the
// slot's exprPath) over `props` (the component's props record value), returning the
// materialized slot value. For a non-masked slot Snapshot == Display; a masked
// slot is routed through MaskCtx exactly like the derived path when the caller
// supplies (resource, subject) — here the field key is the slot's Field/Leaf.
func EvalSlotExpr(s Slot, expr *rast.Node, props cek.Value, resource, subject string, mc *MaskCtx) Materialized {
	if s.Masked {
		field := s.Field
		if field == "" {
			field = s.Leaf
		}
		return mc.materializeMask(resource, subject, field)
	}
	v, ok := evalExpr(expr, props)
	if !ok {
		return Materialized{}
	}
	txt := valueText(v)
	return Materialized{Snapshot: txt, Display: txt}
}

// evalExpr evaluates a prop-ref / field-access expression against props. It handles
// the closed minimal set: the props local, member access, index access, and the
// scalar literals — returning (value, ok). Anything outside the set is (_, false).
func evalExpr(n *rast.Node, props cek.Value) (cek.Value, bool) {
	if n == nil {
		return cek.Value{}, false
	}
	switch n.Kind {
	case rast.KLocal:
		// The single value binder in a component body is the props parameter.
		if n.U == 0 {
			return props, true
		}
		return cek.Value{}, false
	case rast.KMember:
		if len(n.Kids) < 1 {
			return cek.Value{}, false
		}
		obj, ok := evalExpr(n.Kids[0], props)
		if !ok || obj.Tag != cek.TagRecord {
			return cek.Value{}, false
		}
		r, _ := obj.Ref.(*cek.RecordObj)
		if r == nil {
			return cek.Value{}, false
		}
		if v, ok := r.M[n.Str]; ok {
			return v, true
		}
		return cek.Value{}, false
	case rast.KIndex:
		if len(n.Kids) < 2 {
			return cek.Value{}, false
		}
		obj, ok := evalExpr(n.Kids[0], props)
		if !ok {
			return cek.Value{}, false
		}
		idx, ok := evalExpr(n.Kids[1], props)
		if !ok {
			return cek.Value{}, false
		}
		switch obj.Tag {
		case cek.TagRecord:
			if r, _ := obj.Ref.(*cek.RecordObj); r != nil && idx.Tag == cek.TagStr {
				if v, ok := r.M[idx.S]; ok {
					return v, true
				}
			}
		case cek.TagArray:
			if a, _ := obj.Ref.(*cek.ArrayObj); a != nil && idx.Tag == cek.TagF64 {
				i := int(idx.N)
				if i >= 0 && i < len(a.Elems) {
					return a.Elems[i], true
				}
			}
		}
		return cek.Value{}, false
	case rast.KStr:
		return cek.StrV(n.Str), true
	case rast.KNum:
		return cek.NumV(math.Float64frombits(n.U)), true
	case rast.KBool:
		return cek.BoolV(n.U != 0), true
	}
	return cek.Value{}, false
}

// valueText renders a cek.Value to slot display text (the same minimal, locale-free
// projection the derived path uses for scalars).
func valueText(v cek.Value) string {
	switch v.Tag {
	case cek.TagStr:
		return v.S
	case cek.TagF64:
		return strconv.FormatFloat(v.N, 'g', -1, 64)
	case cek.TagBool:
		if v.N != 0 {
			return "true"
		}
		return "false"
	case cek.TagBigInt:
		if z, ok := v.Ref.(*big.Int); ok {
			return z.String()
		}
	case cek.TagNull:
		return ""
	case cek.TagUndefined:
		return ""
	}
	return ""
}
