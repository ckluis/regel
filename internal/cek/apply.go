package cek

import "regel.dev/regel/internal/rast"

// applyStep performs one Apply-mode transition: deliver m.val to the top K frame,
// which resumes its reduction (ADR-04 §2).
func (m *machine) applyStep(meter Meter) (Outcome, bool) {
	if len(m.kont) == 0 {
		return Outcome{Kind: OutDone, Value: m.val}, true
	}
	f := m.top()
	switch f.Kind {

	case FrRet:
		m.pop()
		if len(m.kont) == 0 {
			return Outcome{Kind: OutDone, Value: m.val}, true
		}
		m.restoreCaller(f)
		m.mode = ModeApply
		return Outcome{}, false

	case FrBlock:
		f.Idx++
		return m.blockAdvance(f)

	case FrVarDecl:
		d := f.Node.Kids[0].Kids[f.Idx]
		if err := bindPattern(d.Kids[0], m.val, &f.Vals); err != nil {
			return m.fault("%v", err)
		}
		f.Idx++
		return m.varDeclAdvance(f)

	case FrExprStmt:
		m.pop()
		m.apply(undef())

	case FrIf:
		n := f.Node
		m.pop()
		if truthy(m.val) {
			m.node = n.Kids[1]
			m.path = f.Path.child(1)
			m.mode = ModeEval
		} else if !n.Kids[2].IsNone() {
			m.node = n.Kids[2]
			m.path = f.Path.child(2)
			m.mode = ModeEval
		} else {
			m.apply(undef())
		}

	case FrBin:
		if f.Idx == 0 {
			f.Vals = []Value{m.val}
			f.Idx = 1
			m.node = f.Node.Kids[1]
			m.path = f.Path.child(1)
			m.mode = ModeEval
			return Outcome{}, false
		}
		op := rast.OpKind(f.Node.U)
		m.pop()
		if op == rast.OpIn {
			m.apply(boolVal(hasOwn(propKeyString(f.Vals[0]), m.val)))
			return Outcome{}, false
		}
		r, ok := binOp(op, f.Vals[0], m.val)
		if !ok {
			return m.fault("binary op %d on tags %d,%d", op, f.Vals[0].Tag, m.val.Tag)
		}
		m.apply(r)

	case FrLogic:
		op := rast.OpKind(f.Node.U)
		if f.Idx == 0 {
			left := m.val
			short := false
			switch op {
			case rast.OpAnd:
				short = !truthy(left)
			case rast.OpOr:
				short = truthy(left)
			case rast.OpNullish:
				short = !(left.Tag == TagNull || left.Tag == TagUndefined)
			}
			if short {
				m.pop()
				m.apply(left)
				return Outcome{}, false
			}
			f.Idx = 1
			m.node = f.Node.Kids[1]
			m.path = f.Path.child(1)
			m.mode = ModeEval
			return Outcome{}, false
		}
		m.pop()
		m.apply(m.val)

	case FrUnary:
		m.pop()
		r, ok := unaryOp(rast.OpKind(f.Node.U), m.val)
		if !ok {
			return m.fault("unary op %d on tag %d", rast.OpKind(f.Node.U), m.val.Tag)
		}
		m.apply(r)

	case FrTypeof:
		m.pop()
		m.apply(strVal(typeofStr(m.val)))

	case FrAwait:
		m.pop()
		m.apply(m.val)

	case FrCond:
		n := f.Node
		m.pop()
		if truthy(m.val) {
			m.node = n.Kids[1]
			m.path = f.Path.child(1)
		} else {
			m.node = n.Kids[2]
			m.path = f.Path.child(2)
		}
		m.mode = ModeEval

	case FrMember:
		n := f.Node
		obj := m.val
		m.pop()
		if n.U&1 != 0 && (obj.Tag == TagNull || obj.Tag == TagUndefined) {
			m.apply(undef())
			return Outcome{}, false
		}
		m.apply(getMember(obj, n.Str))

	case FrIndex:
		if f.Idx == 0 {
			n := f.Node
			f.Obj = m.val
			if n.U&1 != 0 && (f.Obj.Tag == TagNull || f.Obj.Tag == TagUndefined) {
				m.pop()
				m.apply(undef())
				return Outcome{}, false
			}
			f.Idx = 1
			m.node = n.Kids[1]
			m.path = f.Path.child(1)
			m.mode = ModeEval
			return Outcome{}, false
		}
		idx := m.val
		obj := f.Obj
		m.pop()
		m.apply(getIndex(obj, idx))

	case FrArray:
		e := f.Node.Kids[0].Kids[f.Idx]
		if e.Kind == rast.KSpread {
			if m.val.Tag == TagArray {
				f.Vals = append(f.Vals, m.val.arr().Elems...)
			}
		} else {
			f.Vals = append(f.Vals, m.val)
		}
		f.Idx++
		return m.arrayAdvance(f, meter)

	case FrObject:
		mem := f.Node.Kids[0].Kids[f.Idx]
		if mem.Kind == rast.KSpread {
			mergeInto(f.Obj.rec(), m.val)
		} else {
			key := objPropKey(mem)
			f.Obj.rec().set(key, m.val)
		}
		f.Idx++
		return m.objectAdvance(f)

	case FrCall:
		return m.callResume(f, meter)

	case FrReturn:
		m.pop()
		m.raise(SigReturn, m.val)

	case FrThrow:
		m.pop()
		m.raise(SigThrow, m.val)

	case FrAssign:
		return m.assignResume(f)

	case FrUpdate:
		return m.updateResume(f)

	case FrTemplate:
		f.Vals = append(f.Vals, m.val)
		f.Idx++
		return m.templateAdvance(f)

	case FrFor:
		return m.forResume(f)
	case FrWhile:
		return m.whileResume(f)
	case FrDoWhile:
		return m.doWhileResume(f)
	case FrForOf:
		return m.forOfResume(f)
	case FrSwitch:
		if f.Idx == -1 {
			f.Obj = m.val
			return m.switchMatch(f)
		}
		f.Idx++
		return m.switchRun(f)

	case FrCatch:
		// Normal completion of the try block: pass the value through; finally
		// (if any) runs next below us.
		m.pop()
		m.apply(m.val)

	case FrFinally:
		// Normal completion reaches finally: run it with no pending signal.
		m.triggerFinally(f, nil)

	case FrFinallyRun:
		// finally block completed normally: resume the pending signal, if any.
		m.pop()
		if f.Pend != nil {
			m.sig = *f.Pend
			m.mode = ModeUnwind
		} else {
			m.apply(undef())
		}

	default:
		return m.fault("apply: unhandled frame kind %d", f.Kind)
	}
	return Outcome{}, false
}

// restoreCaller resets C/E to the caller's context recorded on a FrRet frame.
func (m *machine) restoreCaller(f *Frame) {
	m.defHash = f.RetDef
	if f.RetDef != "" {
		if root, err := m.in.loadAST(f.RetDef); err == nil {
			m.root = root
		}
	}
	m.env = f.RetEnv
	m.path = f.RetPath
}

// mergeInto merges a spread source's own keys / indices into a record.
func mergeInto(dst *RecordObj, src Value) {
	switch src.Tag {
	case TagRecord:
		s := src.rec()
		for _, k := range s.Keys {
			dst.set(k, s.M[k])
		}
	case TagArray:
		for i, e := range src.arr().Elems {
			dst.set(itoa(i), e)
		}
	}
}

// callResume advances a FrCall: it collects the callee, then arguments, then
// performs the call (ADR-04 §2 closures / §5 native dispatch).
func (m *machine) callResume(f *Frame, meter Meter) (Outcome, bool) {
	if f.Idx == 0 {
		callee := m.val
		f.Vals = []Value{callee}
		if f.Node.U&1 != 0 && (callee.Tag == TagNull || callee.Tag == TagUndefined) {
			m.pop()
			m.apply(undef())
			return Outcome{}, false
		}
		f.Idx = 1
		f.Aux = 0
		return m.argAdvance(f)
	}
	// an argument just evaluated
	arg := f.Node.Kids[1].Kids[f.Aux]
	if arg.Kind == rast.KSpread {
		if m.val.Tag == TagArray {
			f.Vals = append(f.Vals, m.val.arr().Elems...)
		}
	} else {
		f.Vals = append(f.Vals, m.val)
	}
	f.Aux++
	return m.argAdvance(f)
}

func (m *machine) argAdvance(f *Frame) (Outcome, bool) {
	args := f.Node.Kids[1].Kids
	if f.Aux >= len(args) {
		return m.performCall(f)
	}
	arg := args[f.Aux]
	if arg.Kind == rast.KSpread {
		m.node = arg.Kids[0]
		m.path = f.Path.child(1).child(f.Aux).child(0)
	} else {
		m.node = arg
		m.path = f.Path.child(1).child(f.Aux)
	}
	m.mode = ModeEval
	return Outcome{}, false
}

// performCall dispatches a fully-evaluated call to a closure (a KFunc body, a new
// activation) or to a native (§5). A native that returns a Condition parks.
func (m *machine) performCall(f *Frame) (Outcome, bool) {
	callee := f.Vals[0]
	args := f.Vals[1:]
	if callee.Tag != TagClosure {
		return m.fault("call of non-function (tag %d)", callee.Tag)
	}
	clo := callee.clo()
	fnRoot, err := m.in.loadAST(clo.DefHash)
	if err != nil {
		return m.fault("load callee def %s: %v", clo.DefHash, err)
	}
	fnNode, ok := navigate(fnRoot, clo.Path)
	if !ok {
		return m.fault("navigate callee path in %s", clo.DefHash)
	}
	switch fnNode.Kind {
	case rast.KNativeBody:
		return m.performNative(f, clo.DefHash, args)
	case rast.KFunc:
		act, err := bindParams(fnNode, args, clo.Env)
		if err != nil {
			return m.fault("bind params: %v", err)
		}
		callPath := f.Path.clone()
		m.pop() // pop FrCall
		m.push(&Frame{Kind: FrRet, RetDef: m.defHash, RetPath: callPath, RetEnv: m.env})
		m.defHash = clo.DefHash
		m.root = fnRoot
		m.env = act
		m.node = fnNode.Kids[3]
		m.path = clo.Path.child(3)
		m.mode = ModeEval
		return Outcome{}, false
	default:
		return m.fault("callee is neither function nor native (kind %d)", fnNode.Kind)
	}
}

// performNative dispatches a native. A non-nil Condition parks on a std signal:
// resume delivers the restart value at this call point (ParkSignal).
func (m *machine) performNative(f *Frame, hash string, args []Value) (Outcome, bool) {
	fn, ok := m.in.reg.lookup(hash)
	if !ok {
		return m.fault("no native registered for %s", hash)
	}
	v, cond := fn(m.host, args)
	if cond != nil {
		callPath := f.Path.clone()
		m.pop() // pop FrCall; resume re-enters in apply mode at this point
		m.path = callPath
		m.mode = ModeApply
		m.val = undef()
		st := m.snapshot(ParkSignal)
		return Outcome{Kind: OutParked, State: st, Condition: cond, Transitions: m.transitions}, true
	}
	m.pop()
	m.apply(v)
	return Outcome{}, false
}
