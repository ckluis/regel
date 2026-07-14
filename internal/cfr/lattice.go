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
