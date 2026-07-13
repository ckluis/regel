package cek

import "errors"

// unwindStep propagates a signal (throw / break / continue / return) through K,
// popping frames until a frame handles it (ADR-01 exceptions realized over
// frames; ADR-04 §2 TryK/FinallyK). Finally frames run on the way out.
func (m *machine) unwindStep() (Outcome, bool) {
	if len(m.kont) == 0 {
		return m.uncaught()
	}
	f := m.top()
	switch f.Kind {

	case FrRet:
		m.pop()
		if m.sig.Kind == SigReturn {
			v := m.sig.Val
			if len(m.kont) == 0 {
				return Outcome{Kind: OutDone, Value: v}, true
			}
			m.restoreCaller(f)
			m.apply(v)
			return Outcome{}, false
		}
		// throw / break / continue crossing a function boundary
		if len(m.kont) == 0 {
			return m.uncaught()
		}
		m.restoreCaller(f)
		m.mode = ModeUnwind
		return Outcome{}, false

	case FrCatch:
		if m.sig.Kind == SigThrow {
			exc := m.sig.Val
			m.pop()
			catchNode := f.Node.Kids[1] // KCatch
			if catchNode.U&1 != 0 {     // has param
				var slots []Value
				if err := bindPattern(catchNode.Kids[0], exc, &slots); err != nil {
					return m.fault("%v", err)
				}
				m.env = pushEnv(f.OuterEnv, slots)
			} else {
				m.env = f.OuterEnv
			}
			m.node = catchNode.Kids[1] // catch block
			m.path = f.Path.child(1).child(1)
			m.mode = ModeEval
			return Outcome{}, false
		}
		m.pop() // non-throw signal: pass through
		return Outcome{}, false

	case FrFinally:
		s := m.sig
		m.triggerFinally(f, &s)
		return Outcome{}, false

	case FrFinallyRun:
		// A signal raised while finally runs supersedes any pending signal.
		m.pop()
		return Outcome{}, false

	case FrFor:
		switch m.sig.Kind {
		case SigBreak:
			m.env = f.OuterEnv
			m.pop()
			m.apply(undef())
		case SigContinue:
			return m.forEvalIncr(f)
		default:
			m.env = f.OuterEnv
			m.pop()
		}
		return Outcome{}, false

	case FrWhile:
		switch m.sig.Kind {
		case SigBreak:
			m.env = f.OuterEnv
			m.pop()
			m.apply(undef())
		case SigContinue:
			return m.whileEvalCond(f)
		default:
			m.env = f.OuterEnv
			m.pop()
		}
		return Outcome{}, false

	case FrDoWhile:
		switch m.sig.Kind {
		case SigBreak:
			m.env = f.OuterEnv
			m.pop()
			m.apply(undef())
		case SigContinue:
			return m.doWhileEvalCond(f)
		default:
			m.env = f.OuterEnv
			m.pop()
		}
		return Outcome{}, false

	case FrForOf:
		switch m.sig.Kind {
		case SigBreak:
			m.env = f.OuterEnv
			m.pop()
			m.apply(undef())
		case SigContinue:
			f.Idx++
			return m.forOfStep(f)
		default:
			m.env = f.OuterEnv
			m.pop()
		}
		return Outcome{}, false

	case FrSwitch:
		if m.sig.Kind == SigBreak {
			m.env = f.OuterEnv
			m.pop()
			m.apply(undef())
			return Outcome{}, false
		}
		m.env = f.OuterEnv
		m.pop()
		return Outcome{}, false

	default:
		// Ordinary frames abandon their partial work; the handler that catches
		// the signal resets C/E, so no intermediate restore is required.
		m.pop()
		return Outcome{}, false
	}
}

// uncaught terminates a run whose signal escaped all frames.
func (m *machine) uncaught() (Outcome, bool) {
	switch m.sig.Kind {
	case SigThrow:
		return Outcome{Kind: OutFaulted, Fault: m.sig.Val, Transitions: m.transitions}, true
	case SigReturn:
		return Outcome{Kind: OutDone, Value: m.sig.Val, Transitions: m.transitions}, true
	default:
		return Outcome{Kind: OutError, Err: errors.New("cek: unhandled break/continue at top level"), Transitions: m.transitions}, true
	}
}
