package cek

import (
	"math/big"

	"regel.dev/regel/internal/rast"
)

// newFrame builds a K frame anchored to the current C (node + path + env).
func (m *machine) newFrame(kind FrameKind) *Frame {
	return &Frame{Kind: kind, Node: m.node, Path: m.path.clone(), Env: m.env}
}

// evalStep performs one Eval-mode transition: reduce m.node in m.env, either
// producing a value (→ Apply) or pushing a frame and descending to a child.
func (m *machine) evalStep(meter Meter) (Outcome, bool) {
	n := m.node
	switch n.Kind {

	// --- literals & atoms ---
	case rast.KNum:
		m.apply(f64(f64bits(n.U)))
	case rast.KBigInt:
		z := new(big.Int).SetBytes(n.Mag)
		if n.U&1 != 0 {
			z.Neg(z)
		}
		m.apply(bigVal(z))
	case rast.KStr:
		m.apply(strVal(n.Str))
	case rast.KBool:
		m.apply(boolVal(n.U != 0))
	case rast.KNull:
		m.apply(null())
	case rast.KUndefined:
		m.apply(undef())
	case rast.KRegex:
		// Stage-A residue: regex evaluation returns an opaque handle; RE2
		// execution is std-battery work.
		m.apply(Value{Tag: TagOpaque, Ref: &OpaqueObj{Codec: "regex", Data: []byte(n.Str)}})
	case rast.KTemplate:
		f := m.newFrame(FrTemplate)
		f.Idx = 0
		m.push(f)
		return m.templateAdvance(f)

	case rast.KLocal:
		v, ok := m.env.lookup(n.U)
		if !ok {
			return m.fault("unbound local index %d", n.U)
		}
		m.apply(v)
	case rast.KRef:
		m.apply(closVal(&ClosureObj{DefHash: n.Str}))
	case rast.KSelfRef:
		m.apply(closVal(&ClosureObj{DefHash: m.defHash}))
	case rast.KName:
		return m.fault("unresolved free name %q", n.Str)

	// --- expressions ---
	case rast.KArray:
		f := m.newFrame(FrArray)
		f.Vals = []Value{}
		m.push(f)
		return m.arrayAdvance(f, meter)
	case rast.KObject:
		f := m.newFrame(FrObject)
		f.Idx = 0
		f.Obj = recVal(newRecord())
		m.push(f)
		return m.objectAdvance(f)
	case rast.KSpread:
		// Bare spread only appears inside array/object/call, handled there.
		m.evalChildOf(n, 0)
	case rast.KCall:
		f := m.newFrame(FrCall)
		f.Idx = 0
		m.push(f)
		m.evalChildOf(n, 0) // evaluate callee
	case rast.KMember:
		f := m.newFrame(FrMember)
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KIndex:
		f := m.newFrame(FrIndex)
		f.Idx = 0
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KBinary:
		op := rast.OpKind(n.U)
		switch {
		case isAssignOp(op):
			return m.evalAssign(n, op)
		case op == rast.OpAnd || op == rast.OpOr || op == rast.OpNullish:
			f := m.newFrame(FrLogic)
			f.Idx = 0
			m.push(f)
			m.evalChildOf(n, 0)
		default:
			f := m.newFrame(FrBin)
			f.Idx = 0
			m.push(f)
			m.evalChildOf(n, 0)
		}
	case rast.KUnary:
		f := m.newFrame(FrUnary)
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KUpdate:
		return m.evalUpdate(n)
	case rast.KCond:
		f := m.newFrame(FrCond)
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KTypeof:
		f := m.newFrame(FrTypeof)
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KAwait:
		f := m.newFrame(FrAwait)
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KAsConst:
		m.evalChildOf(n, 0)
	case rast.KSatisfy:
		m.evalChildOf(n, 0)
	case rast.KFunc:
		// Function/arrow expression → a closure capturing (def, path, env).
		m.apply(closVal(&ClosureObj{DefHash: m.defHash, Path: m.path.clone(), Env: m.env}))

	// --- statements ---
	case rast.KBlock:
		f := m.newFrame(FrBlock)
		f.OuterEnv = m.env
		f.Idx = 0
		m.push(f)
		return m.blockAdvance(f)
	case rast.KVarDecl:
		f := m.newFrame(FrVarDecl)
		f.Idx = 0
		f.Vals = nil
		m.push(f)
		return m.varDeclAdvance(f)
	case rast.KExprStmt:
		f := m.newFrame(FrExprStmt)
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KIf:
		f := m.newFrame(FrIf)
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KFor:
		return m.evalFor(n)
	case rast.KForOf:
		f := m.newFrame(FrForOf)
		f.OuterEnv = m.env
		f.Idx = -1 // iterable not yet evaluated
		m.push(f)
		m.evalChildOf(n, 1) // evaluate the iterable
	case rast.KWhile:
		f := m.newFrame(FrWhile)
		f.OuterEnv = m.env
		f.Idx = 0
		m.push(f)
		m.evalChildOf(n, 0) // evaluate the condition
	case rast.KDoWhile:
		f := m.newFrame(FrDoWhile)
		f.OuterEnv = m.env
		f.Idx = 0
		m.push(f)
		m.setChildControl(n, 0) // evaluate the body first
	case rast.KSwitch:
		f := m.newFrame(FrSwitch)
		f.OuterEnv = m.env
		f.Idx = -1 // -1 = discriminant not yet evaluated
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KBreak:
		m.raise(SigBreak, undef())
	case rast.KContinue:
		m.raise(SigContinue, undef())
	case rast.KReturn:
		if n.Kids[0].IsNone() {
			m.raise(SigReturn, undef())
		} else {
			f := m.newFrame(FrReturn)
			m.push(f)
			m.evalChildOf(n, 0)
		}
	case rast.KThrow:
		f := m.newFrame(FrThrow)
		m.push(f)
		m.evalChildOf(n, 0)
	case rast.KTry:
		return m.evalTry(n)

	default:
		return m.fault("cannot evaluate node kind %d", n.Kind)
	}
	return Outcome{}, false
}

// evalChildOf points C at child i of node n (path taken from m.path).
func (m *machine) evalChildOf(n *rast.Node, i int) {
	m.node = n.Kids[i]
	m.path = m.path.child(i)
	m.mode = ModeEval
}

// setChildControl points C at child i using the frame node's own path (for
// resumptions where m.path may have moved).
func (m *machine) setChildControl(n *rast.Node, i int) {
	m.node = n.Kids[i]
	m.mode = ModeEval
}

// isAssignOp reports whether an op is in the assignment family.
func isAssignOp(op rast.OpKind) bool { return op >= rast.OpAssign && op <= rast.OpNullAssign }

// baseOfAssign maps a compound assignment op to its underlying binary op
// (OpAssign → OpNone).
func baseOfAssign(op rast.OpKind) rast.OpKind {
	switch op {
	case rast.OpAddAssign:
		return rast.OpAdd
	case rast.OpSubAssign:
		return rast.OpSub
	case rast.OpMulAssign:
		return rast.OpMul
	case rast.OpDivAssign:
		return rast.OpDiv
	case rast.OpModAssign:
		return rast.OpMod
	case rast.OpExpAssign:
		return rast.OpExp
	case rast.OpShlAssign:
		return rast.OpShl
	case rast.OpShrAssign:
		return rast.OpShr
	case rast.OpUShrAssign:
		return rast.OpUShr
	case rast.OpBitAndAssign:
		return rast.OpBitAnd
	case rast.OpBitOrAssign:
		return rast.OpBitOr
	case rast.OpBitXorAssign:
		return rast.OpBitXor
	default:
		return rast.OpNone
	}
}
