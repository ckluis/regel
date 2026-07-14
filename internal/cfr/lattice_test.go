package cfr

import (
	"math/big"
	"testing"

	"regel.dev/regel/internal/cek"
)

// representatives builds one Value per candidate tag so the drift test can prove
// each round-trips (or does not) through the value codec.
func representatives() map[cek.Tag]cek.Value {
	return map[cek.Tag]cek.Value{
		cek.TagUndefined: {Tag: cek.TagUndefined},
		cek.TagNull:      {Tag: cek.TagNull},
		cek.TagBool:      {Tag: cek.TagBool, N: 1},
		cek.TagF64:       {Tag: cek.TagF64, N: 3.5},
		cek.TagBigInt:    {Tag: cek.TagBigInt, Ref: big.NewInt(7)},
		cek.TagStr:       {Tag: cek.TagStr, S: "x"},
		cek.TagArray:     {Tag: cek.TagArray, Ref: &cek.ArrayObj{Elems: []cek.Value{{Tag: cek.TagF64, N: 1}}}},
		cek.TagRecord:    {Tag: cek.TagRecord, Ref: &cek.RecordObj{Keys: []string{"a"}, M: map[string]cek.Value{"a": {Tag: cek.TagNull}}}},
		cek.TagClosure:   {Tag: cek.TagClosure, Ref: &cek.ClosureObj{DefHash: "r1_x", Path: cek.Path{0}}},
		cek.TagCapToken:  {Tag: cek.TagCapToken, S: "grant1"},
		cek.TagOpaque:    {Tag: cek.TagOpaque, Ref: &cek.OpaqueObj{Codec: "regex", Data: []byte{1, 2}}},
	}
}

// TestLatticeCodecDriftAgree is the ADR-05 §3 / ADR-07 §4 V5 drift test: the
// exported serializable lattice (EncodableTags — the SINGLE source V5 consumes)
// must equal exactly the set of tags the CFR value codec round-trips. A tag in
// the set that fails to round-trip (a codec branch removed under it) or a tag the
// codec round-trips that is absent from the set (a widened codec the set forgot)
// fails here — so V5 and the codec can never silently disagree.
func TestLatticeCodecDriftAgree(t *testing.T) {
	set := EncodableTags()
	reps := representatives()

	// Every tag the codec actually round-trips must be in the lattice.
	for tag, v := range reps {
		blob, err := EncodeValue(v)
		roundTrips := err == nil
		if roundTrips {
			got, derr := DecodeValue(blob)
			roundTrips = derr == nil && got.Tag == tag
		}
		if roundTrips && !set[tag] {
			t.Fatalf("tag %d round-trips through the codec but is absent from EncodableTags — the lattice narrowed behind the codec", tag)
		}
		if !roundTrips && set[tag] {
			t.Fatalf("tag %d is in EncodableTags but does not round-trip through the codec — the lattice widened past the codec", tag)
		}
	}

	// Every tag claimed encodable must have a representative that round-trips —
	// no tag may be in the set without a codec branch proving it.
	for tag := range set {
		v, ok := reps[tag]
		if !ok {
			t.Fatalf("EncodableTags claims tag %d encodable but the drift test has no representative — add one so the codec branch is exercised", tag)
		}
		blob, err := EncodeValue(v)
		if err != nil {
			t.Fatalf("encodable tag %d failed to encode: %v", tag, err)
		}
		got, err := DecodeValue(blob)
		if err != nil || got.Tag != tag {
			t.Fatalf("encodable tag %d failed to round-trip (got tag %d, err %v)", tag, got.Tag, err)
		}
	}
}

// TestLatticeExcludesUndefinedTag proves the lattice is bounded by the codec's
// own validity predicate: a tag one past the defined range is neither valid nor
// encodable, so a V5 type-classifier that maps a host resource to "no tag" (a
// value past the ceiling) is correctly refused admission.
func TestLatticeExcludesUndefinedTag(t *testing.T) {
	beyond := cek.Tag(cek.TagOpaque + 1)
	if EncodableTags()[beyond] {
		t.Fatalf("tag %d beyond the codec ceiling must not be encodable", beyond)
	}
	if Encodable(beyond) {
		t.Fatalf("Encodable(%d) must be false beyond the codec ceiling", beyond)
	}
}
