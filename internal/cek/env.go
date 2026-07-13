package cek

import "math"

// Env is one activation record in the environment chain (ADR-04 §2 E). Each
// record holds a slot array indexed by De Bruijn binder index and links to its
// parent. Records are immutable in LENGTH once created — introducing binders
// pushes a NEW child record — so a closure that captured an Env pointer can
// never observe a later declaration in an enclosing scope. Slot VALUES are
// mutable in place (reassignable `let`), which the dialect's capture ban (R1)
// makes safe across closures.
type Env struct {
	Parent *Env
	Slots  []Value
}

// pushEnv creates a child record carrying slots (source order; slot[len-1] is
// the most-recently-introduced binder, i.e. De Bruijn index 0 within it).
func pushEnv(parent *Env, slots []Value) *Env {
	return &Env{Parent: parent, Slots: slots}
}

// lookup resolves a De Bruijn index (0 = nearest binder) against the chain. The
// index counts individual slots from the innermost record's top, matching the
// lowerer/printer's flat binder stack (verified against internal/lower output).
func (e *Env) lookup(index uint64) (Value, bool) {
	i := int(index)
	f := e
	for f != nil {
		n := len(f.Slots)
		if i < n {
			return f.Slots[n-1-i], true
		}
		i -= n
		f = f.Parent
	}
	return undef(), false
}

// assign writes a value to the slot a De Bruijn index resolves to (reassignable
// `let`). Reports whether the slot existed.
func (e *Env) assign(index uint64, v Value) bool {
	i := int(index)
	f := e
	for f != nil {
		n := len(f.Slots)
		if i < n {
			f.Slots[n-1-i] = v
			return true
		}
		i -= n
		f = f.Parent
	}
	return false
}

func isNaN(f float64) bool { return math.IsNaN(f) }
