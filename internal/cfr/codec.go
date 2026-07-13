// Package cfr is the owned continuation-frame representation (ADR-05 §2): a
// self-describing, versioned binary TLV codec for the CEK machine's C/E/K state,
// plus the minimal continuation store (Park / PickRestart / ClaimAndResume).
//
// The codec shares rast's primitive encodings — f64 as the 8-byte bit pattern,
// bigint as sign + magnitude, length-prefixed UTF-8. The environment heap and
// mutable heap objects (arrays, records) are content-shared: each distinct
// pointer is serialized once and referenced by index, so aliasing and cycles
// survive a park/resume. Decode is versioned and fails closed: an unknown
// version, tag, frame kind, or a truncation yields a typed error, never a panic
// and never a partial resume (ADR-05 §6 test 4b).
package cfr

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"regel.dev/regel/internal/cek"
)

// FormatVersion is the CFR wire version (STAGE-A-PLAN pin #8).
const FormatVersion = 1

// object-table type tags (mutable / shared heap objects, content-shared).
const (
	objEnv    = 1
	objArray  = 2
	objRecord = 3
)

// ErrCFR is the base class for all CFR decode failures (fail closed → the caller
// records a step.failed condition, ADR-05 §6 test 4b).
var ErrCFR = errors.New("cfr: decode failed")

func decodeErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCFR, fmt.Sprintf(format, args...))
}

// --- Encode -----------------------------------------------------------------

type encoder struct {
	buf    []byte
	objIdx map[any]int
	objs   []any
}

// Encode serializes a machine State to a CFR blob (ADR-05 §2).
func Encode(st *cek.State) ([]byte, error) {
	e := &encoder{objIdx: map[any]int{}}

	// Pass 1: intern every distinct heap object reachable from the state.
	e.internValue(st.Val)
	e.internValue(st.Sig.Val)
	e.internEnv(st.Env)
	for _, f := range st.Kont {
		e.internEnv(f.Env)
		e.internEnv(f.OuterEnv)
		e.internEnv(f.RetEnv)
		for _, v := range f.Vals {
			e.internValue(v)
		}
		e.internValue(f.Obj)
		e.internValue(f.IdxVal)
		if f.Pend != nil {
			e.internValue(f.Pend.Val)
		}
	}

	// Pass 2: emit.
	e.byte(FormatVersion)
	e.emitHeap()

	e.str(st.DefHash)
	e.path(st.Path)
	e.byte(byte(st.Mode))
	e.byte(byte(st.ParkKind))
	e.byte(byte(st.Tier))
	e.svarint(st.FuelSteps)
	e.svarint(st.FuelAlloc)
	e.value(st.Val)
	e.byte(byte(st.Sig.Kind))
	e.value(st.Sig.Val)
	e.envRef(st.Env)

	e.uvarint(uint64(len(st.Kont)))
	for _, f := range st.Kont {
		e.frame(f)
	}
	return e.buf, nil
}

// EncodeValue serializes a single Value with the CFR value codec (shared heap
// interning, same wire rules as Encode) — used for continuation.result and
// channel_message.payload. RED STUB: real body lands GREEN.
func EncodeValue(v cek.Value) ([]byte, error) {
	return nil, fmt.Errorf("%w: EncodeValue not implemented", ErrCFR)
}

func (e *encoder) emitHeap() {
	e.uvarint(uint64(len(e.objs)))
	for _, o := range e.objs {
		switch o.(type) {
		case *cek.Env:
			e.byte(objEnv)
		case *cek.ArrayObj:
			e.byte(objArray)
		case *cek.RecordObj:
			e.byte(objRecord)
		default:
			e.byte(0)
		}
	}
	for _, o := range e.objs {
		switch v := o.(type) {
		case *cek.Env:
			e.envRef(v.Parent)
			e.uvarint(uint64(len(v.Slots)))
			for _, s := range v.Slots {
				e.value(s)
			}
		case *cek.ArrayObj:
			e.uvarint(uint64(len(v.Elems)))
			for _, el := range v.Elems {
				e.value(el)
			}
		case *cek.RecordObj:
			e.uvarint(uint64(len(v.Keys)))
			for _, k := range v.Keys {
				e.str(k)
				e.value(v.M[k])
			}
		}
	}
}

func (e *encoder) frame(f *cek.Frame) {
	e.byte(byte(f.Kind))
	e.path(f.Path)
	e.svarint(int64(f.Idx))
	e.svarint(int64(f.Aux))
	e.uvarint(uint64(len(f.Vals)))
	for _, v := range f.Vals {
		e.value(v)
	}
	e.envRef(f.Env)
	e.envRef(f.OuterEnv)
	e.str(f.RetDef)
	e.path(f.RetPath)
	e.envRef(f.RetEnv)
	e.value(f.Obj)
	e.str(f.Key)
	e.value(f.IdxVal)
	if f.Pend == nil {
		e.byte(0)
	} else {
		e.byte(1)
		e.byte(byte(f.Pend.Kind))
		e.value(f.Pend.Val)
	}
}

// intern -----------------------------------------------------------------------

func (e *encoder) internValue(v cek.Value) {
	switch v.Tag {
	case cek.TagArray:
		a := v.Ref.(*cek.ArrayObj)
		if e.intern(a) {
			for _, el := range a.Elems {
				e.internValue(el)
			}
		}
	case cek.TagRecord:
		r := v.Ref.(*cek.RecordObj)
		if e.intern(r) {
			for _, k := range r.Keys {
				e.internValue(r.M[k])
			}
		}
	case cek.TagClosure:
		c := v.Ref.(*cek.ClosureObj)
		e.internEnv(c.Env)
	}
}

func (e *encoder) internEnv(env *cek.Env) {
	if env == nil {
		return
	}
	if e.intern(env) {
		e.internEnv(env.Parent)
		for _, s := range env.Slots {
			e.internValue(s)
		}
	}
}

// intern assigns o an index if new; returns true when newly added (recurse).
func (e *encoder) intern(o any) bool {
	if _, ok := e.objIdx[o]; ok {
		return false
	}
	e.objIdx[o] = len(e.objs)
	e.objs = append(e.objs, o)
	return true
}

// value encoding ---------------------------------------------------------------

func (e *encoder) value(v cek.Value) {
	e.byte(byte(v.Tag))
	switch v.Tag {
	case cek.TagUndefined, cek.TagNull:
	case cek.TagBool:
		if v.N != 0 {
			e.byte(1)
		} else {
			e.byte(0)
		}
	case cek.TagF64:
		e.f64(v.N)
	case cek.TagStr, cek.TagCapToken:
		e.str(v.S)
	case cek.TagBigInt:
		z := v.Ref.(*big.Int)
		if z.Sign() < 0 {
			e.byte(1)
		} else {
			e.byte(0)
		}
		e.bytes(z.Bytes())
	case cek.TagArray, cek.TagRecord:
		e.objRef(v.Ref)
	case cek.TagClosure:
		c := v.Ref.(*cek.ClosureObj)
		e.str(c.DefHash)
		e.path(c.Path)
		e.envRef(c.Env)
	case cek.TagOpaque:
		o := v.Ref.(*cek.OpaqueObj)
		e.str(o.Codec)
		e.bytes(o.Data)
	}
}

func (e *encoder) objRef(o any) {
	idx, ok := e.objIdx[o]
	if !ok {
		e.uvarint(0)
		return
	}
	e.uvarint(uint64(idx) + 1)
}

func (e *encoder) envRef(env *cek.Env) {
	if env == nil {
		e.uvarint(0)
		return
	}
	e.objRef(env)
}

// primitive writers ------------------------------------------------------------

func (e *encoder) byte(b byte) { e.buf = append(e.buf, b) }

func (e *encoder) uvarint(v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	e.buf = append(e.buf, tmp[:n]...)
}

func (e *encoder) svarint(v int64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutVarint(tmp[:], v)
	e.buf = append(e.buf, tmp[:n]...)
}

func (e *encoder) f64(f float64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], mathFloat64bits(f))
	e.buf = append(e.buf, b[:]...)
}

func (e *encoder) bytes(b []byte) {
	e.uvarint(uint64(len(b)))
	e.buf = append(e.buf, b...)
}

func (e *encoder) str(s string) {
	e.uvarint(uint64(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *encoder) path(p cek.Path) {
	e.uvarint(uint64(len(p)))
	for _, x := range p {
		e.uvarint(uint64(x))
	}
}
