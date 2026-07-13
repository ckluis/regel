// Package cek is the owned defunctionalized CEK machine (ADR-04) that executes
// the canonical regel-AST (internal/rast) directly. It is a small-step abstract
// machine with three fully-reified, serializable registers — Control, Env,
// Kont — so every transition boundary is a valid pause point: pausing is "stop
// stepping and write C/E/K down" (ADR-05 serializes exactly this state).
//
// # Representation choices (ADR-04 §2)
//
//   - Control (C): (defHash, node path, mode). The node path is the child-index
//     path into the definition's canonical AST; a live *rast.Node is cached for
//     speed and re-derived from the path on decode. `mode` (+ the top K frame's
//     kind and progress) realizes the ADR's per-node "phase".
//   - Env (E): a chain of immutable-length activation records, each a slot array
//     indexed by De Bruijn index (0 = nearest binder). Introducing binders pushes
//     a NEW record (records never grow), so a captured closure environment can
//     never be corrupted by later declarations — capture is pointer sharing.
//   - Kont (K): an explicit heap stack of frames {kind, path, vals[], …}; one
//     kind per admitted composite node. Recursion is reified into K, so the Go
//     stack stays bounded regardless of program depth. TryK/CatchK/FinallyK are
//     ordinary frames, so throw-across-await unwinding and finally re-execution
//     survive a pause.
//
// Values are one closed tagged union — exactly ADR-01 R2's serializable lattice.
package cek

import (
	"math/big"

	"regel.dev/regel/internal/rast"
)

// Tag is the closed Value discriminator. Values are STABLE for CFR format v1
// (append-only; never renumber).
type Tag uint8

const (
	TagUndefined Tag = 0
	TagNull      Tag = 1
	TagBool      Tag = 2
	TagF64       Tag = 3
	TagBigInt    Tag = 4
	TagStr       Tag = 5
	TagArray     Tag = 6
	TagRecord    Tag = 7
	TagClosure   Tag = 8
	TagCapToken  Tag = 9
	TagOpaque    Tag = 10
)

// Value is the closed tagged union (ADR-01 R2). Scalar tags (Undefined, Null,
// Bool, F64) never touch Ref, so they never allocate on the heap: arithmetic
// reads and writes N only. Compound tags carry a pointer in Ref.
type Value struct {
	Tag Tag
	N   float64 // F64 payload; Bool uses N!=0
	S   string  // Str payload; CapToken grant id
	Ref any     // *big.Int | *ArrayObj | *RecordObj | *ClosureObj | *OpaqueObj
}

// ArrayObj is a mutable, pointer-shared array (JS reference semantics).
type ArrayObj struct{ Elems []Value }

// RecordObj is an insertion-ordered record. Iteration order is Keys (never Go
// map order) so evaluation is deterministic (ADR-04 §6.5).
type RecordObj struct {
	Keys []string
	M    map[string]Value
}

// ClosureObj anchors a closure to immortal facts: the definition hash and the
// node path of its KFunc within that definition, plus the captured environment.
// It serializes as (defHash, path, envPtr) per ADR-05 §2.
type ClosureObj struct {
	DefHash string
	Path    Path
	Env     *Env
}

// OpaqueObj is a std opaque handle that declares its own codec (ADR-01 R2). In
// Stage A the only producers are the regex residue and native intent values.
type OpaqueObj struct {
	Codec string
	Data  []byte
}

// --- constructors -----------------------------------------------------------

func undef() Value { return Value{Tag: TagUndefined} }
func null() Value  { return Value{Tag: TagNull} }
func boolVal(b bool) Value {
	if b {
		return Value{Tag: TagBool, N: 1}
	}
	return Value{Tag: TagBool, N: 0}
}
func f64(n float64) Value         { return Value{Tag: TagF64, N: n} }
func strVal(s string) Value       { return Value{Tag: TagStr, S: s} }
func bigVal(z *big.Int) Value     { return Value{Tag: TagBigInt, Ref: z} }
func arrVal(a *ArrayObj) Value    { return Value{Tag: TagArray, Ref: a} }
func recVal(r *RecordObj) Value   { return Value{Tag: TagRecord, Ref: r} }
func closVal(c *ClosureObj) Value { return Value{Tag: TagClosure, Ref: c} }
func capVal(grantID string) Value { return Value{Tag: TagCapToken, S: grantID} }

// --- accessors --------------------------------------------------------------

func (v Value) asBool() bool     { return v.N != 0 }
func (v Value) big() *big.Int    { return v.Ref.(*big.Int) }
func (v Value) arr() *ArrayObj   { return v.Ref.(*ArrayObj) }
func (v Value) rec() *RecordObj  { return v.Ref.(*RecordObj) }
func (v Value) clo() *ClosureObj { return v.Ref.(*ClosureObj) }

func newRecord() *RecordObj { return &RecordObj{M: map[string]Value{}} }

func (r *RecordObj) set(k string, v Value) {
	if _, ok := r.M[k]; !ok {
		r.Keys = append(r.Keys, k)
	}
	r.M[k] = v
}

func (r *RecordObj) get(k string) (Value, bool) {
	v, ok := r.M[k]
	return v, ok
}

// truthy implements JS ToBoolean over the admitted lattice.
func truthy(v Value) bool {
	switch v.Tag {
	case TagUndefined, TagNull:
		return false
	case TagBool:
		return v.N != 0
	case TagF64:
		return v.N != 0 && !isNaN(v.N)
	case TagBigInt:
		return v.big().Sign() != 0
	case TagStr:
		return v.S != ""
	default:
		return true // arrays, records, closures, tokens, handles are truthy
	}
}

// typeofStr implements the `typeof` operator over the lattice.
func typeofStr(v Value) string {
	switch v.Tag {
	case TagUndefined:
		return "undefined"
	case TagNull:
		return "object"
	case TagBool:
		return "boolean"
	case TagF64:
		return "number"
	case TagBigInt:
		return "bigint"
	case TagStr:
		return "string"
	case TagClosure:
		return "function"
	default:
		return "object" // arrays, records, tokens, handles
	}
}

// nodePath navigation ---------------------------------------------------------

// Path is a node path: a sequence of child indices into a definition's AST. The
// empty path is the definition root. It is the stable, code-version-independent
// coordinate ADR-04 §2 anchors suspension to.
type Path []uint16

func (p Path) clone() Path {
	if len(p) == 0 {
		return nil
	}
	out := make(Path, len(p))
	copy(out, p)
	return out
}

func (p Path) child(i int) Path {
	out := make(Path, len(p)+1)
	copy(out, p)
	out[len(p)] = uint16(i)
	return out
}

// navigate walks a path from a root node, returning the addressed node.
func navigate(root *rast.Node, p Path) (*rast.Node, bool) {
	n := root
	for _, i := range p {
		if n == nil || int(i) >= len(n.Kids) {
			return nil, false
		}
		n = n.Kids[int(i)]
	}
	return n, n != nil
}
