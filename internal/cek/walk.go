package cek

// WalkValues visits every Value reachable from a State — the register value,
// the pending signal value, the environment heap (slots, parents), and every K
// frame's vals / member-target / pending-signal values, descending through
// arrays, records, and closure environments. Each distinct compound object is
// visited once (cycle- and alias-safe). It is the ADR-05 §4 capability-token
// walker: the store re-validates every TagCapToken held across a pause before
// re-entering the machine, so a token whose grant was revoked is refused at the
// claim, never smuggled back into a live handle.
func WalkValues(st *State, fn func(Value)) {
	if st == nil {
		return
	}
	seen := map[any]bool{}
	var walkV func(v Value)
	var walkEnv func(e *Env)
	walkV = func(v Value) {
		fn(v)
		switch v.Tag {
		case TagArray:
			a := v.Ref.(*ArrayObj)
			if seen[a] {
				return
			}
			seen[a] = true
			for _, el := range a.Elems {
				walkV(el)
			}
		case TagRecord:
			r := v.Ref.(*RecordObj)
			if seen[r] {
				return
			}
			seen[r] = true
			for _, k := range r.Keys {
				walkV(r.M[k])
			}
		case TagClosure:
			c := v.Ref.(*ClosureObj)
			walkEnv(c.Env)
		}
	}
	walkEnv = func(e *Env) {
		if e == nil || seen[e] {
			return
		}
		seen[e] = true
		for _, s := range e.Slots {
			walkV(s)
		}
		walkEnv(e.Parent)
	}

	walkV(st.Val)
	walkV(st.Sig.Val)
	walkEnv(st.Env)
	for _, f := range st.Kont {
		if f == nil {
			continue
		}
		walkEnv(f.Env)
		walkEnv(f.OuterEnv)
		walkEnv(f.RetEnv)
		for _, v := range f.Vals {
			walkV(v)
		}
		walkV(f.Obj)
		walkV(f.IdxVal)
		if f.Pend != nil {
			walkV(f.Pend.Val)
		}
	}
}

// CapToken reports whether v is a capability token and returns its grant handle
// (ADR-05 §2 serialized as the grant id / capability name). Exposed so the store
// can re-validate tokens without reaching into the Value internals.
func (v Value) CapToken() (string, bool) {
	if v.Tag == TagCapToken {
		return v.S, true
	}
	return "", false
}

// NewCapToken mints a capability-token Value bound to a grant handle. Exposed for
// the store's capability re-validation tests (ADR-05 §4 test 8b): the std surface
// does not mint tokens in Stage B, so a token held across a pause is crafted
// directly onto the parked State.
func NewCapToken(grant string) Value { return capVal(grant) }
