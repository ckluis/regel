package cek

import "regel.dev/regel/internal/rast"

// Loop phases carried in Frame.Idx.
const (
	forInit   = 0
	forCond   = 1
	forBody   = 2
	forIncr   = 3
	whileCond = 0
	whileBody = 1
	doBody    = 0
	doCond    = 1
)

// --- C-style for(;;) --------------------------------------------------------

func (m *machine) evalFor(n *rast.Node) (Outcome, bool) {
	f := m.newFrame(FrFor)
	f.OuterEnv = m.env
	init := n.Kids[0]
	if init.IsNone() {
		f.Idx = forCond
		f.Env = m.env
		m.push(f)
		return m.forEvalCond(f)
	}
	f.Idx = forInit
	m.push(f)
	m.node = init
	m.path = f.Path.child(0)
	m.mode = ModeEval
	return Outcome{}, false
}

func (m *machine) forEvalCond(f *Frame) (Outcome, bool) {
	cond := f.Node.Kids[1]
	m.env = f.Env
	if cond.IsNone() { // for(;;) infinite: condition is always true
		f.Idx = forBody
		return m.forEvalBody(f)
	}
	f.Idx = forCond
	m.node = cond
	m.path = f.Path.child(1)
	m.mode = ModeEval
	return Outcome{}, false
}

func (m *machine) forEvalBody(f *Frame) (Outcome, bool) {
	m.env = f.Env
	f.Idx = forBody
	m.node = f.Node.Kids[3]
	m.path = f.Path.child(3)
	m.mode = ModeEval
	return Outcome{}, false
}

func (m *machine) forEvalIncr(f *Frame) (Outcome, bool) {
	incr := f.Node.Kids[2]
	m.env = f.Env
	if incr.IsNone() {
		return m.forEvalCond(f)
	}
	f.Idx = forIncr
	m.node = incr
	m.path = f.Path.child(2)
	m.mode = ModeEval
	return Outcome{}, false
}

// forResume dispatches an Apply-mode resumption of a FrFor frame.
func (m *machine) forResume(f *Frame) (Outcome, bool) {
	switch f.Idx {
	case forInit:
		f.Env = m.env // loop env after init binders pushed
		return m.forEvalCond(f)
	case forCond:
		if truthy(m.val) {
			return m.forEvalBody(f)
		}
		m.env = f.OuterEnv
		m.pop()
		m.apply(undef())
		return Outcome{}, false
	case forBody:
		return m.forEvalIncr(f)
	case forIncr:
		return m.forEvalCond(f)
	}
	return m.fault("bad for phase %d", f.Idx)
}

// --- while ------------------------------------------------------------------

func (m *machine) whileResume(f *Frame) (Outcome, bool) {
	switch f.Idx {
	case whileCond:
		if truthy(m.val) {
			m.env = f.OuterEnv
			f.Idx = whileBody
			m.node = f.Node.Kids[1]
			m.path = f.Path.child(1)
			m.mode = ModeEval
			return Outcome{}, false
		}
		m.env = f.OuterEnv
		m.pop()
		m.apply(undef())
		return Outcome{}, false
	case whileBody:
		return m.whileEvalCond(f)
	}
	return m.fault("bad while phase %d", f.Idx)
}

func (m *machine) whileEvalCond(f *Frame) (Outcome, bool) {
	m.env = f.OuterEnv
	f.Idx = whileCond
	m.node = f.Node.Kids[0]
	m.path = f.Path.child(0)
	m.mode = ModeEval
	return Outcome{}, false
}

// --- do-while ---------------------------------------------------------------

func (m *machine) doWhileResume(f *Frame) (Outcome, bool) {
	switch f.Idx {
	case doBody:
		return m.doWhileEvalCond(f)
	case doCond:
		if truthy(m.val) {
			m.env = f.OuterEnv
			f.Idx = doBody
			m.node = f.Node.Kids[0]
			m.path = f.Path.child(0)
			m.mode = ModeEval
			return Outcome{}, false
		}
		m.env = f.OuterEnv
		m.pop()
		m.apply(undef())
		return Outcome{}, false
	}
	return m.fault("bad do-while phase %d", f.Idx)
}

func (m *machine) doWhileEvalCond(f *Frame) (Outcome, bool) {
	m.env = f.OuterEnv
	f.Idx = doCond
	m.node = f.Node.Kids[1]
	m.path = f.Path.child(1)
	m.mode = ModeEval
	return Outcome{}, false
}

// --- for-of -----------------------------------------------------------------

// forOfStep binds the loop variable for the current element and evaluates the
// body, or finishes when the iterable is exhausted. A fresh binding is created
// per iteration (JS let-in-for-of semantics).
func (m *machine) forOfStep(f *Frame) (Outcome, bool) {
	if f.Obj.Tag != TagArray {
		m.env = f.OuterEnv
		m.pop()
		m.apply(undef())
		return Outcome{}, false
	}
	elems := f.Obj.arr().Elems
	if f.Idx >= len(elems) {
		m.env = f.OuterEnv
		m.pop()
		m.apply(undef())
		return Outcome{}, false
	}
	decl := f.Node.Kids[0]              // KVarDecl
	pat := decl.Kids[0].Kids[0].Kids[0] // KVarDecl→KList→KDeclr→pattern
	var slots []Value
	if err := bindPattern(pat, elems[f.Idx], &slots); err != nil {
		return m.fault("%v", err)
	}
	m.env = pushEnv(f.OuterEnv, slots)
	m.node = f.Node.Kids[2] // body
	m.path = f.Path.child(2)
	m.mode = ModeEval
	return Outcome{}, false
}

func (m *machine) forOfResume(f *Frame) (Outcome, bool) {
	if f.Idx < 0 { // just evaluated the iterable
		f.Obj = m.val
		f.Idx = 0
		return m.forOfStep(f)
	}
	f.Idx++
	return m.forOfStep(f)
}

// --- switch -----------------------------------------------------------------

// switchMatch selects the clause matching the discriminant (case labels are
// literals per ADR-01, so evalConst suffices) and begins executing its body.
func (m *machine) switchMatch(f *Frame) (Outcome, bool) {
	disc := f.Obj
	clauses := f.Node.Kids[1].Kids
	matched := -1
	defaultIdx := -1
	for i, cl := range clauses {
		if cl.U&1 != 0 { // default
			defaultIdx = i
			continue
		}
		lv, ok := evalConst(cl.Kids[0])
		if ok && strictEq(lv, disc) {
			matched = i
			break
		}
	}
	if matched < 0 {
		matched = defaultIdx
	}
	if matched < 0 {
		m.env = f.OuterEnv
		m.pop()
		m.apply(undef())
		return Outcome{}, false
	}
	f.Aux = matched
	f.Idx = 0
	return m.switchRun(f)
}

func (m *machine) switchRun(f *Frame) (Outcome, bool) {
	cl := f.Node.Kids[1].Kids[f.Aux]
	stmts := cl.Kids[1].Kids
	if f.Idx >= len(stmts) {
		m.env = f.OuterEnv
		m.pop()
		m.apply(undef())
		return Outcome{}, false
	}
	m.node = stmts[f.Idx]
	m.path = f.Path.child(1).child(f.Aux).child(1).child(f.Idx)
	m.mode = ModeEval
	return Outcome{}, false
}
