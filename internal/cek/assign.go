package cek

import "regel.dev/regel/internal/rast"

// assignResume completes an assignment once its operands are evaluated (ADR-01
// compound assignment to let bindings and object properties).
func (m *machine) assignResume(f *Frame) (Outcome, bool) {
	node := f.Node
	target := node.Kids[0]
	op := rast.OpKind(node.U)
	base := baseOfAssign(op)

	switch f.Aux {
	case 0: // local target — RHS just evaluated
		var nv Value
		if base == rast.OpNone {
			nv = m.val
		} else {
			r, ok := binOp(base, f.Vals[0], m.val)
			if !ok {
				return m.fault("compound assign op %d", base)
			}
			nv = r
		}
		m.env.assign(target.U, nv)
		m.pop()
		m.apply(nv)
		return Outcome{}, false

	case 1: // member target: obj.prop
		if f.Idx == 0 {
			f.Obj = m.val
			f.Key = target.Str
			if base != rast.OpNone {
				f.Vals = []Value{getMember(f.Obj, f.Key)}
			}
			f.Idx = 1
			m.node = node.Kids[1] // RHS
			m.path = f.Path.child(1)
			m.mode = ModeEval
			return Outcome{}, false
		}
		nv := m.val
		if base != rast.OpNone {
			r, ok := binOp(base, f.Vals[0], m.val)
			if !ok {
				return m.fault("compound member assign op %d", base)
			}
			nv = r
		}
		setMember(f.Obj, f.Key, nv)
		m.pop()
		m.apply(nv)
		return Outcome{}, false

	case 2: // index target: obj[expr]
		switch f.Idx {
		case 0: // obj evaluated
			f.Obj = m.val
			f.Idx = 1
			m.node = target.Kids[1] // index expr
			m.path = f.Path.child(0).child(1)
			m.mode = ModeEval
			return Outcome{}, false
		case 1: // index evaluated
			f.IdxVal = m.val
			if base != rast.OpNone {
				f.Vals = []Value{getIndex(f.Obj, f.IdxVal)}
			}
			f.Idx = 2
			m.node = node.Kids[1] // RHS
			m.path = f.Path.child(1)
			m.mode = ModeEval
			return Outcome{}, false
		default: // RHS evaluated
			nv := m.val
			if base != rast.OpNone {
				r, ok := binOp(base, f.Vals[0], m.val)
				if !ok {
					return m.fault("compound index assign op %d", base)
				}
				nv = r
			}
			setIndex(f.Obj, f.IdxVal, nv)
			m.pop()
			m.apply(nv)
			return Outcome{}, false
		}
	}
	return m.fault("bad assign target class %d", f.Aux)
}

// updateResume completes a ++/-- on a member or index lvalue.
func (m *machine) updateResume(f *Frame) (Outcome, bool) {
	node := f.Node
	operand := node.Kids[0]
	switch f.Aux {
	case 1: // member
		obj := m.val
		key := operand.Str
		old := getMember(obj, key)
		nv, ret := updateValue(old, node.U)
		setMember(obj, key, nv)
		m.pop()
		if node.U&1 != 0 {
			m.apply(nv)
		} else {
			m.apply(ret)
		}
		return Outcome{}, false
	case 2: // index
		if f.Idx == 0 {
			f.Obj = m.val
			f.Idx = 1
			m.node = operand.Kids[1]
			m.path = f.Path.child(0).child(1)
			m.mode = ModeEval
			return Outcome{}, false
		}
		idx := m.val
		old := getIndex(f.Obj, idx)
		nv, ret := updateValue(old, node.U)
		setIndex(f.Obj, idx, nv)
		m.pop()
		if node.U&1 != 0 {
			m.apply(nv)
		} else {
			m.apply(ret)
		}
		return Outcome{}, false
	}
	return m.fault("bad update target class %d", f.Aux)
}
