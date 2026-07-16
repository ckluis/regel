package cfr

import "regel.dev/regel/internal/cek"

// The R2 serializable lattice, shared with the admission capture verifier
// (ADR-05 §3, ADR-07 §4 V5): "encodable ≡ admitted". This is the SINGLE source
// of truth for which cek Value kinds round-trip through the CFR value codec, so
// the capture verifier does not maintain a second, drift-prone list — it
// consumes EncodableTags() directly.
//
// Widening the codec (a new Value tag the codec can encode/decode and that
// TagValid admits) automatically widens the lattice V5 admits; narrowing the
// codec (dropping a tag from TagValid, or a branch from the value codec) narrows
// V5 in lockstep. The lattice_test drift test proves the set agrees with what the
// codec actually round-trips, so the two can never silently diverge.
//
// Note the type-vs-tag distinction ADR-05 §3 draws: a *live host resource*
// (a connection, socket, in-flight promise) is "never a dialect value at all —
// the codec has no tag for it". Such a value's static type maps to NO tag, so V5
// rejects it; that is orthogonal to this set, which enumerates the tags that DO
// exist and DO round-trip. Every existing tag is encodable; non-encodability is a
// property of app types that have no Value representation, not of any tag here.

// EncodableTags returns the set of cek Value tags the CFR value codec round-trips
// — exactly the tags cek.TagValid admits (0 … TagOpaque). Deriving the set from
// TagValid (not a hand-kept list) is what makes a new codec tag widen V5
// automatically: bump TagValid's ceiling and add the codec branch, and this set —
// and therefore the lattice V5 admits — grows with it.
func EncodableTags() map[cek.Tag]bool {
	out := map[cek.Tag]bool{}
	for t := cek.Tag(0); cek.TagValid(t); t++ {
		out[t] = true
	}
	return out
}

// Encodable reports whether a single tag lies in the serializable lattice.
func Encodable(t cek.Tag) bool { return cek.TagValid(t) }

// StateTags returns the set of cek Value tags REACHABLE from a decoded machine
// State — every value held in the control register, the pending signal, the
// environment chain, and every K frame (ADR-08 §4 O4: a lattice-narrowing epoch
// must enumerate the sleeping continuations holding a newly-banned tag). It
// mirrors the encoder's intern traversal (codec.go), so it visits exactly the
// values a park serialized. `migrate N` consumes this to classify a parked
// continuation `needs-hold` when it holds a to-be-banned tag.
func StateTags(st *cek.State) map[cek.Tag]bool {
	w := &tagWalk{tags: map[cek.Tag]bool{}, seen: map[any]bool{}}
	w.value(st.Val)
	w.value(st.Sig.Val)
	w.env(st.Env)
	for _, f := range st.Kont {
		w.env(f.Env)
		w.env(f.OuterEnv)
		w.env(f.RetEnv)
		for _, v := range f.Vals {
			w.value(v)
		}
		w.value(f.Obj)
		w.value(f.IdxVal)
		if f.Pend != nil {
			w.value(f.Pend.Val)
		}
	}
	return w.tags
}

type tagWalk struct {
	tags map[cek.Tag]bool
	seen map[any]bool
}

func (w *tagWalk) value(v cek.Value) {
	w.tags[v.Tag] = true
	switch v.Tag {
	case cek.TagArray:
		a := v.Ref.(*cek.ArrayObj)
		if !w.seen[a] {
			w.seen[a] = true
			for _, el := range a.Elems {
				w.value(el)
			}
		}
	case cek.TagRecord:
		r := v.Ref.(*cek.RecordObj)
		if !w.seen[r] {
			w.seen[r] = true
			for _, k := range r.Keys {
				w.value(r.M[k])
			}
		}
	case cek.TagClosure:
		c := v.Ref.(*cek.ClosureObj)
		w.env(c.Env)
	}
}

func (w *tagWalk) env(env *cek.Env) {
	if env == nil || w.seen[env] {
		return
	}
	w.seen[env] = true
	w.env(env.Parent)
	for _, s := range env.Slots {
		w.value(s)
	}
}
