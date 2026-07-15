// Package ui is BUILD-D increment D2: the PURE server-side render machinery for
// the ADR-11 reactive layer. It owns the render template model (static/dynamic
// split), first-paint HTML rendering over the closed ADR-10 §7 tier-1 vocabulary,
// the per-slot diff, the incremental order-independent snapshot digest (§4), the
// owned binary patch-frame codec (§2), and the runtime PII masking token (§8).
//
// This package builds NO HTTP routes, NO SSE, NO session rows — D3 owns transport,
// sessions, subscriptions, and the event loop. Everything here is a pure function
// of its inputs (the DB-backed reveal/audit resolver is injected via MaskCtx, so
// the render/diff/digest/codec core stays testable with no database).
package ui

import "hash/fnv"

// Digest is the ADR-11 §4 divergence digest: a 64-bit ORDER-INDEPENDENT sum of
// per-slot terms h(slotId ‖ value), taken mod 2^64 (Go uint64 arithmetic wraps,
// which IS mod 2^64). Because addition is a commutative group operation and each
// slot contributes exactly one term, the digest is a pure function of the current
// (slotId → value) map regardless of the order or history of edits — so it is
// maintained incrementally in O(changed slots), never recomputed over the view.
type Digest uint64

// digestSep separates slotId from value in the FNV pre-image so that
// h("ab"‖"c") ≠ h("a"‖"bc"). Slot ids are template-generated (no NUL bytes).
const digestSep = 0x00

// slotTerm is h(slotId ‖ value) = FNV-1a-64 over slotId, a separator, and value.
func slotTerm(slotID, value string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(slotID))
	_, _ = h.Write([]byte{digestSep})
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

// FullDigest recomputes the digest over an entire slot snapshot — the reference
// the incremental path is proven equal to, and the value shipped on a resync
// (§4). O(view): used at first paint and resync only.
func FullDigest(snapshot map[string]string) Digest {
	var d uint64
	for id, v := range snapshot {
		d += slotTerm(id, v)
	}
	return Digest(d)
}

// Set updates the digest for a slot whose value changed v_old → v_new: subtract
// the old term, add the new term (mod 2^64). O(1). This is the exact incremental
// step §4 mandates — a MID-SEQUENCE value change (the case a position-ordered
// running hash could not update in place) is handled correctly because every term
// is keyed by its own slotId, not by position.
func (d Digest) Set(slotID, oldV, newV string) Digest {
	return Digest(uint64(d) - slotTerm(slotID, oldV) + slotTerm(slotID, newV))
}

// Add folds a newly-added slot into the digest (a spliceList add). O(1).
func (d Digest) Add(slotID, value string) Digest {
	return Digest(uint64(d) + slotTerm(slotID, value))
}

// Remove folds a removed slot out of the digest (a spliceList remove). O(1).
func (d Digest) Remove(slotID, value string) Digest {
	return Digest(uint64(d) - slotTerm(slotID, value))
}
