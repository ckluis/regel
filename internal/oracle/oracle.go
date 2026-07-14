// Package oracle is the regel-native differential oracle's INDEPENDENT
// reference reducer (ADR-04 §6 harness 3, R1-02; seated in the ADR-07 §5
// release gate). It is a deliberately minimal, independently authored, big-step
// tree-walking evaluator over the canonical regel-AST that shares NO code path
// with the production CEK machine (internal/cek): not the step function, not
// the frame dispatch, not the contract/validator/effect implementations, not
// the meter, not the Value union. It shares only the rast node types — the AST
// is the program under test, not evaluator code.
//
// It exists only to be a second, disagreeing witness over the three layers the
// base-dialect differential fuzz is structurally blind to:
//
//	(a) contract enforcement semantics — whether a std/contract.pre/post clause
//	    holds or is violated for given inputs, and the resulting verdict;
//	(b) derived boundary validator outcomes — accept/reject of an input with
//	    the identical rejection subject (at Stage-C scope the derived validator
//	    artifact IS the contract clause set, discharged at the eval boundary);
//	(c) effect-class ordering — the exact sequence of effect classes
//	    capability-bearing natives emit.
//
// It may be slow, non-serializable, and continuation-free (it never pauses); it
// is a dev-machine artifact only and never ships in or near the kernel.
package oracle

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"regel.dev/regel/internal/rast"
)

// --- the reference Value lattice (independent of cek.Value) -------------------

// VKind discriminates the reference reducer's own value union.
type VKind uint8

const (
	VUndef VKind = iota
	VNull
	VBool
	VNum
	VStr
	VArr
	VRec
	VClo
	VNative
)

// Value is the reference reducer's value. Deliberately its own shape — sharing
// cek.Value would share the very representation under test.
type Value struct {
	Kind   VKind
	Num    float64
	Bool   bool
	Str    string
	Arr    *Arr
	Rec    *Rec
	Clo    *Closure
	Native string // std intrinsic symbol, e.g. "std/mail.send"
}

// Arr is a pointer-shared array (JS reference semantics).
type Arr struct{ Elems []Value }

// Rec is an insertion-ordered record.
type Rec struct {
	Keys []string
	M    map[string]Value
}

// Closure captures a function node and its defining environment.
type Closure struct {
	Fn  *rast.Node // a KFunc
	Env *env
}

func undef() Value          { return Value{Kind: VUndef} }
func vnull() Value          { return Value{Kind: VNull} }
func vbool(b bool) Value    { return Value{Kind: VBool, Bool: b} }
func vnum(n float64) Value  { return Value{Kind: VNum, Num: n} }
func vstr(s string) Value   { return Value{Kind: VStr, Str: s} }
func vrec(r *Rec) Value     { return Value{Kind: VRec, Rec: r} }
func varr(a *Arr) Value     { return Value{Kind: VArr, Arr: a} }
func newRec() *Rec          { return &Rec{M: map[string]Value{}} }
func (r *Rec) set(k string, v Value) {
	if _, ok := r.M[k]; !ok {
		r.Keys = append(r.Keys, k)
	}
	r.M[k] = v
}

// --- environment (the lowered AST's De Bruijn binding contract) ---------------

// env mirrors the lowering contract: a chain of fixed-length records; a KLocal
// index counts slots from the innermost record's top outward. The CONTRACT is
// the lowerer's (shared via the AST); the implementation here is this package's
// own.
type env struct {
	parent *env
	slots  []Value
}

func (e *env) lookup(idx uint64) (Value, bool) {
	i := int(idx)
	for f := e; f != nil; f = f.parent {
		n := len(f.slots)
		if i < n {
			return f.slots[n-1-i], true
		}
		i -= n
	}
	return undef(), false
}

func (e *env) assign(idx uint64, v Value) bool {
	i := int(idx)
	for f := e; f != nil; f = f.parent {
		n := len(f.slots)
		if i < n {
			f.slots[n-1-i] = v
			return true
		}
		i -= n
	}
	return false
}

// --- results -------------------------------------------------------------------

// Result is the reference reducer's observable outcome — the four ADR-04 §6
// comparison observables (verdict, validator outcome/subject, effect order,
// produced value) in one shape.
type Result struct {
	Kind    string // "value" | "violation" | "throw" | "error"
	Value   Value  // Kind == "value"
	Clause  string // Kind == "violation": the rejecting clause subject ("pre"|"post")
	Thrown  Value  // Kind == "throw"
	Err     string // Kind == "error"
	Effects []string
}

// Machine is one reference evaluation context.
type Machine struct {
	// Defs maps a definition hash to its canonical body (app defs under test).
	Defs map[string]*rast.Node
	// Intrinsics maps a std definition hash to its intrinsic symbol (genesis
	// image DATA, not code): the reducer dispatches its OWN native
	// implementations by symbol.
	Intrinsics map[string]string

	effects   []string
	violation string
}

// control is the big-step non-local flow discriminant.
type control uint8

const (
	flowNone control = iota
	flowReturn
	flowBreak
	flowContinue
	flowThrow
	flowHalt // a contract/validator violation: unwind everything, fire nothing
)

// Run evaluates the definition at hash with args and returns the observable
// Result. A violated boundary clause halts evaluation with the rejecting clause
// subject and an EMPTY effect trace (a refused turn fires nothing — the same
// boundary semantics the production park realizes).
func (m *Machine) Run(hash string, args []Value) Result {
	root, ok := m.Defs[hash]
	if !ok {
		return Result{Kind: "error", Err: "unknown def " + hash}
	}
	m.effects = nil
	m.violation = ""

	var v Value
	var fl control
	var err error
	if root.Kind == rast.KFunc {
		v, fl, err = m.callFunction(&Closure{Fn: root, Env: nil}, args)
	} else {
		v, fl, err = m.evalExpr(root, nil)
	}
	if err != nil {
		return Result{Kind: "error", Err: err.Error()}
	}
	switch fl {
	case flowHalt:
		return Result{Kind: "violation", Clause: m.violation, Effects: nil}
	case flowThrow:
		return Result{Kind: "throw", Thrown: v, Effects: m.effects}
	default:
		return Result{Kind: "value", Value: v, Effects: m.effects}
	}
}

// callFunction binds args to the function's parameters and executes the body.
func (m *Machine) callFunction(clo *Closure, args []Value) (Value, control, error) {
	fn := clo.Fn
	params := fn.Kids[0].Kids
	var slots []Value
	ai := 0
	for _, prm := range params {
		pat := prm.Kids[0]
		if pat.Kind != rast.KBindId {
			return undef(), flowNone, fmt.Errorf("oracle: only simple parameter binders are covered")
		}
		if prm.U&1 != 0 { // rest
			rest := &Arr{}
			for ; ai < len(args); ai++ {
				rest.Elems = append(rest.Elems, args[ai])
			}
			slots = append(slots, varr(rest))
			continue
		}
		v := undef()
		if ai < len(args) {
			v = args[ai]
		}
		ai++
		slots = append(slots, v)
	}
	act := &env{parent: clo.Env, slots: slots}
	body := fn.Kids[3]
	if body.Kind == rast.KBlock {
		v, fl, err := m.execBlock(body, act)
		if err != nil || fl == flowThrow || fl == flowHalt {
			return v, fl, err
		}
		if fl == flowReturn {
			return v, flowNone, nil
		}
		return undef(), flowNone, nil // fell off the end
	}
	// arrow expression body
	return m.evalExpr(body, act)
}

// execBlock executes a statement list under a fresh scope discipline: each
// KVarDecl statement extends the chain with one new record.
func (m *Machine) execBlock(blk *rast.Node, e *env) (Value, control, error) {
	cur := e
	for _, st := range blk.Kids[0].Kids {
		v, fl, ne, err := m.execStmt(st, cur)
		if err != nil || fl != flowNone {
			return v, fl, err
		}
		cur = ne
	}
	return undef(), flowNone, nil
}

// execStmt executes one statement; a KVarDecl returns the extended env.
func (m *Machine) execStmt(n *rast.Node, e *env) (Value, control, *env, error) {
	switch n.Kind {
	case rast.KVarDecl:
		var slots []Value
		for _, d := range n.Kids[0].Kids { // KDeclr [pattern, type, init]
			if d.Kids[0].Kind != rast.KBindId {
				return undef(), flowNone, e, fmt.Errorf("oracle: only simple declarators are covered")
			}
			init := d.Kids[2]
			v := undef()
			if !init.IsNone() {
				var fl control
				var err error
				v, fl, err = m.evalExpr(init, e)
				if err != nil || fl != flowNone {
					return v, fl, e, err
				}
			}
			slots = append(slots, v)
		}
		return undef(), flowNone, &env{parent: e, slots: slots}, nil
	case rast.KExprStmt:
		v, fl, err := m.evalExpr(n.Kids[0], e)
		return v, fl, e, err
	case rast.KIf:
		c, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return c, fl, e, err
		}
		if truthy(c) {
			v, fl, err := m.execNested(n.Kids[1], e)
			return v, fl, e, err
		}
		if !n.Kids[2].IsNone() {
			v, fl, err := m.execNested(n.Kids[2], e)
			return v, fl, e, err
		}
		return undef(), flowNone, e, nil
	case rast.KWhile:
		for {
			c, fl, err := m.evalExpr(n.Kids[0], e)
			if err != nil || fl != flowNone {
				return c, fl, e, err
			}
			if !truthy(c) {
				return undef(), flowNone, e, nil
			}
			v, fl, err := m.execNested(n.Kids[1], e)
			if err != nil {
				return v, fl, e, err
			}
			switch fl {
			case flowBreak:
				return undef(), flowNone, e, nil
			case flowContinue, flowNone:
			default:
				return v, fl, e, err
			}
		}
	case rast.KReturn:
		if n.Kids[0].IsNone() {
			return undef(), flowReturn, e, nil
		}
		v, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return v, fl, e, err
		}
		return v, flowReturn, e, nil
	case rast.KThrow:
		v, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return v, fl, e, err
		}
		return v, flowThrow, e, nil
	case rast.KBreak:
		return undef(), flowBreak, e, nil
	case rast.KContinue:
		return undef(), flowContinue, e, nil
	case rast.KBlock:
		v, fl, err := m.execBlock(n, e)
		return v, fl, e, err
	default:
		// An expression in statement position.
		v, fl, err := m.evalExpr(n, e)
		return v, fl, e, err
	}
}

// execNested runs a statement or block as a nested scope.
func (m *Machine) execNested(n *rast.Node, e *env) (Value, control, error) {
	if n.Kind == rast.KBlock {
		return m.execBlock(n, e)
	}
	v, fl, _, err := m.execStmt(n, e)
	return v, fl, err
}

// evalExpr evaluates an expression node.
func (m *Machine) evalExpr(n *rast.Node, e *env) (Value, control, error) {
	switch n.Kind {
	case rast.KNum:
		return vnum(math.Float64frombits(n.U)), flowNone, nil
	case rast.KStr:
		return vstr(n.Str), flowNone, nil
	case rast.KBool:
		return vbool(n.U != 0), flowNone, nil
	case rast.KNull:
		return vnull(), flowNone, nil
	case rast.KUndefined:
		return undef(), flowNone, nil
	case rast.KTemplate:
		var sb strings.Builder
		for _, p := range n.Kids[0].Kids {
			if p.Kind == rast.KStrPart {
				sb.WriteString(p.Str)
				continue
			}
			v, fl, err := m.evalExpr(p, e)
			if err != nil || fl != flowNone {
				return v, fl, err
			}
			sb.WriteString(toStr(v))
		}
		return vstr(sb.String()), flowNone, nil
	case rast.KLocal:
		v, ok := e.lookup(n.U)
		if !ok {
			return undef(), flowNone, fmt.Errorf("oracle: unbound local %d", n.U)
		}
		return v, flowNone, nil
	case rast.KRef:
		if intr, ok := m.Intrinsics[n.Str]; ok {
			return Value{Kind: VNative, Native: intr}, flowNone, nil
		}
		if def, ok := m.Defs[n.Str]; ok && def.Kind == rast.KFunc {
			return Value{Kind: VClo, Clo: &Closure{Fn: def}}, flowNone, nil
		}
		return undef(), flowNone, fmt.Errorf("oracle: unresolved ref %s", n.Str)
	case rast.KArray:
		out := &Arr{}
		for _, el := range n.Kids[0].Kids {
			if el.IsNone() {
				out.Elems = append(out.Elems, undef())
				continue
			}
			if el.Kind == rast.KSpread {
				v, fl, err := m.evalExpr(el.Kids[0], e)
				if err != nil || fl != flowNone {
					return v, fl, err
				}
				if v.Kind == VArr {
					out.Elems = append(out.Elems, v.Arr.Elems...)
				}
				continue
			}
			v, fl, err := m.evalExpr(el, e)
			if err != nil || fl != flowNone {
				return v, fl, err
			}
			out.Elems = append(out.Elems, v)
		}
		return varr(out), flowNone, nil
	case rast.KObject:
		r := newRec()
		for _, mem := range n.Kids[0].Kids {
			if mem.Kind != rast.KProp {
				return undef(), flowNone, fmt.Errorf("oracle: object member kind %d not covered", mem.Kind)
			}
			key := mem.Kids[0]
			if mem.U&1 != 0 || (key.Kind != rast.KStrPart && key.Kind != rast.KStr && key.Kind != rast.KName) {
				return undef(), flowNone, fmt.Errorf("oracle: computed keys not covered")
			}
			v, fl, err := m.evalExpr(mem.Kids[1], e)
			if err != nil || fl != flowNone {
				return v, fl, err
			}
			r.set(key.Str, v)
		}
		return vrec(r), flowNone, nil
	case rast.KCall:
		callee, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return callee, fl, err
		}
		var args []Value
		for _, a := range n.Kids[1].Kids {
			if a.Kind == rast.KSpread {
				v, fl, err := m.evalExpr(a.Kids[0], e)
				if err != nil || fl != flowNone {
					return v, fl, err
				}
				if v.Kind == VArr {
					args = append(args, v.Arr.Elems...)
				}
				continue
			}
			v, fl, err := m.evalExpr(a, e)
			if err != nil || fl != flowNone {
				return v, fl, err
			}
			args = append(args, v)
		}
		switch callee.Kind {
		case VNative:
			return m.callNative(callee.Native, args)
		case VClo:
			return m.callFunction(callee.Clo, args)
		default:
			return undef(), flowNone, fmt.Errorf("oracle: call of non-function")
		}
	case rast.KMember:
		obj, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return obj, fl, err
		}
		if n.U&1 != 0 && (obj.Kind == VNull || obj.Kind == VUndef) {
			return undef(), flowNone, nil
		}
		return member(obj, n.Str), flowNone, nil
	case rast.KIndex:
		obj, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return obj, fl, err
		}
		idx, fl, err := m.evalExpr(n.Kids[1], e)
		if err != nil || fl != flowNone {
			return idx, fl, err
		}
		switch obj.Kind {
		case VArr:
			if idx.Kind == VNum {
				i := int(idx.Num)
				if i >= 0 && i < len(obj.Arr.Elems) {
					return obj.Arr.Elems[i], flowNone, nil
				}
			}
			return undef(), flowNone, nil
		case VRec:
			if v, ok := obj.Rec.M[toStr(idx)]; ok {
				return v, flowNone, nil
			}
			return undef(), flowNone, nil
		default:
			return undef(), flowNone, nil
		}
	case rast.KBinary:
		op := rast.OpKind(n.U)
		if op >= rast.OpAssign && op <= rast.OpNullAssign {
			return m.evalAssign(n, e)
		}
		if op == rast.OpAnd || op == rast.OpOr || op == rast.OpNullish {
			l, fl, err := m.evalExpr(n.Kids[0], e)
			if err != nil || fl != flowNone {
				return l, fl, err
			}
			switch op {
			case rast.OpAnd:
				if !truthy(l) {
					return l, flowNone, nil
				}
			case rast.OpOr:
				if truthy(l) {
					return l, flowNone, nil
				}
			case rast.OpNullish:
				if l.Kind != VNull && l.Kind != VUndef {
					return l, flowNone, nil
				}
			}
			return m.evalExpr(n.Kids[1], e)
		}
		l, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return l, fl, err
		}
		r, fl, err := m.evalExpr(n.Kids[1], e)
		if err != nil || fl != flowNone {
			return r, fl, err
		}
		return binary(op, l, r)
	case rast.KUnary:
		v, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return v, fl, err
		}
		switch rast.OpKind(n.U) {
		case rast.OpNeg:
			nn, ok := toNum(v)
			if !ok {
				return undef(), flowNone, fmt.Errorf("oracle: -non-number")
			}
			return vnum(-nn), flowNone, nil
		case rast.OpPos:
			nn, ok := toNum(v)
			if !ok {
				return undef(), flowNone, fmt.Errorf("oracle: +non-number")
			}
			return vnum(nn), flowNone, nil
		case rast.OpNot:
			return vbool(!truthy(v)), flowNone, nil
		default:
			return undef(), flowNone, fmt.Errorf("oracle: unary op %d not covered", n.U)
		}
	case rast.KCond:
		c, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return c, fl, err
		}
		if truthy(c) {
			return m.evalExpr(n.Kids[1], e)
		}
		return m.evalExpr(n.Kids[2], e)
	case rast.KTypeof:
		v, fl, err := m.evalExpr(n.Kids[0], e)
		if err != nil || fl != flowNone {
			return v, fl, err
		}
		return vstr(typeofStr(v)), flowNone, nil
	case rast.KAwait, rast.KAsConst:
		return m.evalExpr(n.Kids[0], e)
	case rast.KSatisfy:
		return m.evalExpr(n.Kids[0], e)
	case rast.KFunc:
		return Value{Kind: VClo, Clo: &Closure{Fn: n, Env: e}}, flowNone, nil
	default:
		return undef(), flowNone, fmt.Errorf("oracle: expression kind %d not covered", n.Kind)
	}
}

// evalAssign handles KBinary assignment ops on simple KLocal targets.
func (m *Machine) evalAssign(n *rast.Node, e *env) (Value, control, error) {
	target := n.Kids[0]
	if target.Kind != rast.KLocal {
		return undef(), flowNone, fmt.Errorf("oracle: only local assignment is covered")
	}
	rhs, fl, err := m.evalExpr(n.Kids[1], e)
	if err != nil || fl != flowNone {
		return rhs, fl, err
	}
	op := rast.OpKind(n.U)
	if op != rast.OpAssign {
		cur, ok := e.lookup(target.U)
		if !ok {
			return undef(), flowNone, fmt.Errorf("oracle: unbound assign target")
		}
		base := map[rast.OpKind]rast.OpKind{
			rast.OpAddAssign: rast.OpAdd, rast.OpSubAssign: rast.OpSub,
			rast.OpMulAssign: rast.OpMul, rast.OpDivAssign: rast.OpDiv,
			rast.OpModAssign: rast.OpMod,
		}[op]
		if base == rast.OpNone {
			return undef(), flowNone, fmt.Errorf("oracle: compound assign op %d not covered", op)
		}
		v, fl, err := binary(base, cur, rhs)
		if err != nil || fl != flowNone {
			return v, fl, err
		}
		rhs = v
	}
	if !e.assign(target.U, rhs) {
		return undef(), flowNone, fmt.Errorf("oracle: unbound assign target")
	}
	return rhs, flowNone, nil
}

// --- the reference natives: independent implementations of the three layers ---

// callNative dispatches the reducer's OWN std implementations by intrinsic
// symbol. Contract enforcement (layer a), boundary-validator outcomes (layer b),
// and effect recording (layer c) are all implemented here, independently.
func (m *Machine) callNative(intrinsic string, args []Value) (Value, control, error) {
	arg0 := undef()
	if len(args) > 0 {
		arg0 = args[0]
	}
	switch intrinsic {
	case "std/contract.pre":
		// (a)+(b): a falsy precondition predicate refuses the boundary. The turn
		// halts; no effect fires.
		if !truthy(arg0) {
			m.violation = "pre"
			return undef(), flowHalt, nil
		}
		return undef(), flowNone, nil
	case "std/contract.post":
		// (b): the postcondition boundary validator over the exit value.
		if !truthy(arg0) {
			m.violation = "post"
			return undef(), flowHalt, nil
		}
		return undef(), flowNone, nil
	case "std/contract.requires", "std/contract.ensures":
		return vbool(truthy(arg0)), flowNone, nil
	case "std/mail.send":
		// (c): effect class recorded in call order.
		m.effects = append(m.effects, "mail.send")
		to, subject := "", ""
		if len(args) > 0 {
			to = toStr(args[0])
		}
		if len(args) > 1 {
			subject = toStr(args[1])
		}
		r := newRec()
		r.set("intent", vstr("mail.send"))
		r.set("to", vstr(to))
		r.set("subject", vstr(subject))
		return vrec(r), flowNone, nil
	case "std/wf.send":
		// (c): channel.send effect class.
		m.effects = append(m.effects, "channel.send")
		return undef(), flowNone, nil
	case "std/iter.keys":
		out := &Arr{}
		if arg0.Kind == VRec {
			for _, k := range arg0.Rec.Keys {
				out.Elems = append(out.Elems, vstr(k))
			}
		}
		if arg0.Kind == VArr {
			for i := range arg0.Arr.Elems {
				out.Elems = append(out.Elems, vstr(strconv.Itoa(i)))
			}
		}
		return varr(out), flowNone, nil
	default:
		return undef(), flowNone, fmt.Errorf("oracle: native %s not covered", intrinsic)
	}
}

// --- semantics helpers (independently authored) --------------------------------

func truthy(v Value) bool {
	switch v.Kind {
	case VUndef, VNull:
		return false
	case VBool:
		return v.Bool
	case VNum:
		return v.Num != 0 && !math.IsNaN(v.Num)
	case VStr:
		return v.Str != ""
	default:
		return true
	}
}

func typeofStr(v Value) string {
	switch v.Kind {
	case VUndef:
		return "undefined"
	case VBool:
		return "boolean"
	case VNum:
		return "number"
	case VStr:
		return "string"
	case VClo, VNative:
		return "function"
	default:
		return "object"
	}
}

func toNum(v Value) (float64, bool) {
	switch v.Kind {
	case VNum:
		return v.Num, true
	case VBool:
		if v.Bool {
			return 1, true
		}
		return 0, true
	case VNull:
		return 0, true
	case VUndef:
		return math.NaN(), true
	default:
		return 0, false
	}
}

// toStr renders a value as JS ToString over the covered lattice.
func toStr(v Value) string {
	switch v.Kind {
	case VUndef:
		return "undefined"
	case VNull:
		return "null"
	case VBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case VNum:
		return NumToStr(v.Num)
	case VStr:
		return v.Str
	case VArr:
		parts := make([]string, 0, len(v.Arr.Elems))
		for _, el := range v.Arr.Elems {
			parts = append(parts, toStr(el))
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// NumToStr is JS Number→string for the corpus's value range (integers and
// ordinary decimals; exotic exponent formatting is out of corpus scope).
func NumToStr(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func member(v Value, name string) Value {
	switch v.Kind {
	case VRec:
		if got, ok := v.Rec.M[name]; ok {
			return got
		}
		return undef()
	case VArr:
		if name == "length" {
			return vnum(float64(len(v.Arr.Elems)))
		}
		return undef()
	case VStr:
		if name == "length" {
			return vnum(float64(len([]rune(v.Str)))) // corpus scope: BMP-only strings
		}
		return undef()
	default:
		return undef()
	}
}

func binary(op rast.OpKind, a, b Value) (Value, control, error) {
	switch op {
	case rast.OpAdd:
		if a.Kind == VStr || b.Kind == VStr {
			return vstr(toStr(a) + toStr(b)), flowNone, nil
		}
		x, ok1 := toNum(a)
		y, ok2 := toNum(b)
		if !ok1 || !ok2 {
			return undef(), flowNone, fmt.Errorf("oracle: + on uncovered kinds")
		}
		return vnum(x + y), flowNone, nil
	case rast.OpSub, rast.OpMul, rast.OpDiv, rast.OpMod, rast.OpExp:
		x, ok1 := toNum(a)
		y, ok2 := toNum(b)
		if !ok1 || !ok2 {
			return undef(), flowNone, fmt.Errorf("oracle: arith on uncovered kinds")
		}
		switch op {
		case rast.OpSub:
			return vnum(x - y), flowNone, nil
		case rast.OpMul:
			return vnum(x * y), flowNone, nil
		case rast.OpDiv:
			return vnum(x / y), flowNone, nil
		case rast.OpMod:
			return vnum(math.Mod(x, y)), flowNone, nil
		default:
			return vnum(math.Pow(x, y)), flowNone, nil
		}
	case rast.OpLt, rast.OpGt, rast.OpLe, rast.OpGe:
		if a.Kind == VStr && b.Kind == VStr {
			c := strings.Compare(a.Str, b.Str)
			switch op {
			case rast.OpLt:
				return vbool(c < 0), flowNone, nil
			case rast.OpGt:
				return vbool(c > 0), flowNone, nil
			case rast.OpLe:
				return vbool(c <= 0), flowNone, nil
			default:
				return vbool(c >= 0), flowNone, nil
			}
		}
		x, ok1 := toNum(a)
		y, ok2 := toNum(b)
		if !ok1 || !ok2 {
			return undef(), flowNone, fmt.Errorf("oracle: compare on uncovered kinds")
		}
		switch op {
		case rast.OpLt:
			return vbool(x < y), flowNone, nil
		case rast.OpGt:
			return vbool(x > y), flowNone, nil
		case rast.OpLe:
			return vbool(x <= y), flowNone, nil
		default:
			return vbool(x >= y), flowNone, nil
		}
	case rast.OpEqEqEq, rast.OpNeEqEq:
		eq := strictEq(a, b)
		if op == rast.OpNeEqEq {
			eq = !eq
		}
		return vbool(eq), flowNone, nil
	case rast.OpEqEq, rast.OpNeEq:
		// Corpus scope: loose equality only over same-kind or null/undefined.
		eq := strictEq(a, b) ||
			((a.Kind == VNull || a.Kind == VUndef) && (b.Kind == VNull || b.Kind == VUndef))
		if op == rast.OpNeEq {
			eq = !eq
		}
		return vbool(eq), flowNone, nil
	default:
		return undef(), flowNone, fmt.Errorf("oracle: binary op %d not covered", op)
	}
}

func strictEq(a, b Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case VUndef, VNull:
		return true
	case VBool:
		return a.Bool == b.Bool
	case VNum:
		return a.Num == b.Num // NaN !== NaN falls out
	case VStr:
		return a.Str == b.Str
	case VArr:
		return a.Arr == b.Arr
	case VRec:
		return a.Rec == b.Rec
	case VClo:
		return a.Clo == b.Clo
	case VNative:
		return a.Native == b.Native
	default:
		return false
	}
}

// Render projects a Value to the canonical comparison string the differential
// harness uses for the produced-value observable (ADR-04 §6 observable iv).
func Render(v Value) string {
	var sb strings.Builder
	render(&sb, v)
	return sb.String()
}

func render(sb *strings.Builder, v Value) {
	switch v.Kind {
	case VUndef:
		sb.WriteString("undefined")
	case VNull:
		sb.WriteString("null")
	case VBool:
		sb.WriteString(strconv.FormatBool(v.Bool))
	case VNum:
		sb.WriteString("num:" + NumToStr(v.Num))
	case VStr:
		sb.WriteString(strconv.Quote(v.Str))
	case VArr:
		sb.WriteString("[")
		for i, el := range v.Arr.Elems {
			if i > 0 {
				sb.WriteString(",")
			}
			render(sb, el)
		}
		sb.WriteString("]")
	case VRec:
		sb.WriteString("{")
		keys := append([]string(nil), v.Rec.Keys...)
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(strconv.Quote(k) + ":")
			render(sb, v.Rec.M[k])
		}
		sb.WriteString("}")
	case VClo, VNative:
		sb.WriteString("<function>")
	}
}
