package cek

import (
	"strings"

	"regel.dev/regel/internal/rast"
)

// --- statement-sequence advance helpers -------------------------------------

// blockAdvance evaluates the next statement of a block, or restores scope and
// finishes when the statement list is exhausted.
func (m *machine) blockAdvance(f *Frame) (Outcome, bool) {
	stmts := f.Node.Kids[0].Kids
	if f.Idx >= len(stmts) {
		m.env = f.OuterEnv
		m.pop()
		m.apply(undef())
		return Outcome{}, false
	}
	m.node = stmts[f.Idx]
	m.path = f.Path.child(0).child(f.Idx)
	m.mode = ModeEval
	return Outcome{}, false
}

// varDeclAdvance evaluates the next declarator initializer (in the pre-binding
// env), binding no-init declarators inline, then extends the env at the end.
func (m *machine) varDeclAdvance(f *Frame) (Outcome, bool) {
	decls := f.Node.Kids[0].Kids
	for f.Idx < len(decls) {
		d := decls[f.Idx] // KDeclr Kids=[pattern, type, init]
		init := d.Kids[2]
		if init.IsNone() {
			if err := bindPattern(d.Kids[0], undef(), &f.Vals); err != nil {
				return m.fault("%v", err)
			}
			f.Idx++
			continue
		}
		m.node = init
		m.path = f.Path.child(0).child(f.Idx).child(2)
		m.mode = ModeEval
		return Outcome{}, false
	}
	m.env = pushEnv(m.env, f.Vals)
	m.pop()
	m.apply(undef())
	return Outcome{}, false
}

// templateAdvance evaluates the next interpolation expression of a template, or
// assembles the final string.
func (m *machine) templateAdvance(f *Frame) (Outcome, bool) {
	parts := f.Node.Kids[0].Kids
	for f.Idx < len(parts) {
		p := parts[f.Idx]
		if p.Kind == rast.KStrPart {
			f.Idx++
			continue
		}
		m.node = p
		m.path = f.Path.child(0).child(f.Idx)
		m.mode = ModeEval
		return Outcome{}, false
	}
	var sb strings.Builder
	vi := 0
	for _, p := range parts {
		if p.Kind == rast.KStrPart {
			sb.WriteString(p.Str)
		} else {
			if vi < len(f.Vals) {
				sb.WriteString(toStr(f.Vals[vi]))
			}
			vi++
		}
	}
	m.pop()
	m.apply(strVal(sb.String()))
	return Outcome{}, false
}

// arrayAdvance evaluates the next array element (or spread), or builds the array.
func (m *machine) arrayAdvance(f *Frame, meter Meter) (Outcome, bool) {
	elems := f.Node.Kids[0].Kids
	for f.Idx < len(elems) {
		e := elems[f.Idx]
		if e.IsNone() { // elision hole
			f.Vals = append(f.Vals, undef())
			f.Idx++
			continue
		}
		if e.Kind == rast.KSpread {
			m.node = e.Kids[0]
			m.path = f.Path.child(0).child(f.Idx).child(0)
		} else {
			m.node = e
			m.path = f.Path.child(0).child(f.Idx)
		}
		m.mode = ModeEval
		return Outcome{}, false
	}
	meter.chargeAlloc(int64(len(f.Vals))*16 + 32)
	m.pop()
	m.apply(arrVal(&ArrayObj{Elems: f.Vals}))
	return Outcome{}, false
}

// objectAdvance evaluates the next property value (or spread source), or finishes
// the record. Non-computed keys only; computed keys are read via evalConst.
func (m *machine) objectAdvance(f *Frame) (Outcome, bool) {
	members := f.Node.Kids[0].Kids
	for f.Idx < len(members) {
		mem := members[f.Idx]
		if mem.Kind == rast.KSpread {
			m.node = mem.Kids[0]
			m.path = f.Path.child(0).child(f.Idx).child(0)
		} else {
			m.node = mem.Kids[1] // KProp value
			m.path = f.Path.child(0).child(f.Idx).child(1)
		}
		m.mode = ModeEval
		return Outcome{}, false
	}
	m.pop()
	m.apply(f.Obj)
	return Outcome{}, false
}

// objPropKey extracts a KProp key (non-computed) or evaluates a constant computed
// key.
func objPropKey(mem *rast.Node) string {
	key := mem.Kids[0]
	if mem.U&1 != 0 { // computed
		if kv, ok := evalConst(key); ok {
			return propKeyString(kv)
		}
		return ""
	}
	switch key.Kind {
	case rast.KStr, rast.KStrPart, rast.KName:
		return key.Str
	case rast.KNum:
		return numToStr(f64bits(key.U))
	default:
		return ""
	}
}

// --- assignment -------------------------------------------------------------

func (m *machine) evalAssign(n *rast.Node, op rast.OpKind) (Outcome, bool) {
	target := n.Kids[0]
	// Logical assignment (&&=, ||=, ??=) — supported for local targets.
	if op == rast.OpAndAssign || op == rast.OpOrAssign || op == rast.OpNullAssign {
		if target.Kind != rast.KLocal {
			return m.fault("logical assignment to non-local target (Stage A)")
		}
		cur, _ := m.env.lookup(target.U)
		do := false
		switch op {
		case rast.OpAndAssign:
			do = truthy(cur)
		case rast.OpOrAssign:
			do = !truthy(cur)
		case rast.OpNullAssign:
			do = cur.Tag == TagNull || cur.Tag == TagUndefined
		}
		if !do {
			m.apply(cur)
			return Outcome{}, false
		}
		f := m.newFrame(FrAssign)
		f.Aux = 0 // local
		m.push(f)
		m.evalChildOf(n, 1)
		return Outcome{}, false
	}

	f := m.newFrame(FrAssign)
	switch target.Kind {
	case rast.KLocal:
		f.Aux = 0
		if baseOfAssign(op) != rast.OpNone {
			old, _ := m.env.lookup(target.U)
			f.Vals = []Value{old}
		}
		m.push(f)
		m.evalChildOf(n, 1) // eval RHS
	case rast.KMember:
		f.Aux = 1
		f.Idx = 0
		m.push(f)
		// evaluate the member object: n/0(member)/0(obj)
		m.node = target.Kids[0]
		m.path = m.path.child(0).child(0)
		m.mode = ModeEval
	case rast.KIndex:
		f.Aux = 2
		f.Idx = 0
		m.push(f)
		m.node = target.Kids[0]
		m.path = m.path.child(0).child(0)
		m.mode = ModeEval
	default:
		return m.fault("assignment to unsupported target kind %d", target.Kind)
	}
	return Outcome{}, false
}

// --- update (++/--) ---------------------------------------------------------

func (m *machine) evalUpdate(n *rast.Node) (Outcome, bool) {
	operand := n.Kids[0]
	if operand.Kind == rast.KLocal {
		cur, _ := m.env.lookup(operand.U)
		nv, ret := updateValue(cur, n.U)
		m.env.assign(operand.U, nv)
		if n.U&1 != 0 { // prefix
			m.apply(nv)
		} else {
			m.apply(ret)
		}
		return Outcome{}, false
	}
	// Member/index update: evaluate the target object first.
	f := m.newFrame(FrUpdate)
	f.Idx = 0
	if operand.Kind == rast.KMember {
		f.Aux = 1
	} else if operand.Kind == rast.KIndex {
		f.Aux = 2
	} else {
		return m.fault("update of unsupported target kind %d", operand.Kind)
	}
	m.push(f)
	m.node = operand.Kids[0]
	m.path = m.path.child(0).child(0)
	m.mode = ModeEval
	return Outcome{}, false
}

// updateValue computes (newValue, postfixResult) for a ++/-- on a value. flags
// is the KUpdate.U (bit0 prefix, bits1.. OpKind).
func updateValue(cur Value, flags uint64) (Value, Value) {
	dec := rast.OpKind(flags>>1) == rast.OpDec
	if cur.Tag == TagBigInt {
		one := bigOne()
		nz := cur.big()
		var res = newBig()
		if dec {
			res.Sub(nz, one)
		} else {
			res.Add(nz, one)
		}
		return bigVal(res), cur
	}
	x, _ := toNum(cur)
	nx := x + 1
	if dec {
		nx = x - 1
	}
	return f64(nx), f64(x)
}

// --- try / catch / finally --------------------------------------------------

func (m *machine) evalTry(n *rast.Node) (Outcome, bool) {
	catchNode := n.Kids[1]
	finallyNode := n.Kids[2]
	if !finallyNode.IsNone() {
		ff := m.newFrame(FrFinally)
		ff.OuterEnv = m.env
		m.push(ff)
	}
	if !catchNode.IsNone() {
		cf := m.newFrame(FrCatch)
		cf.OuterEnv = m.env
		m.push(cf)
	}
	m.node = n.Kids[0] // try block
	m.path = m.path.child(0)
	m.mode = ModeEval
	return Outcome{}, false
}

// triggerFinally pops FrFinally, pushes a FrFinallyRun carrying the pending
// signal (nil for normal completion), and evaluates the finally block.
func (m *machine) triggerFinally(f *Frame, pend *Signal) {
	m.pop() // pop FrFinally
	run := &Frame{Kind: FrFinallyRun, Node: f.Node, Path: f.Path.clone(), OuterEnv: f.OuterEnv, Pend: pend}
	m.push(run)
	m.env = f.OuterEnv
	m.node = f.Node.Kids[2] // finally block
	m.path = f.Path.child(2)
	m.mode = ModeEval
}
