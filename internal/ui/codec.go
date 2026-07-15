package ui

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// codec.go is the OWNED binary patch-frame codec (ADR-11 §2): the wire format D3
// ships over SSE. A frame is [eventSeq, snapshotHash, ops[]]; an op is [slotId,
// kind, payload]; kind ∈ {setText, setAttr, setValue, spliceList}. It is versioned
// with a leading version byte so the epoch can evolve the wire without ambiguity.
//
// This is a DISTINCT format from the template artifact (JSON, template.go): the
// template is cached-forever inspectable derivation output; the frame is the hot
// per-event binary delta.

// CodecVersion is the leading byte of every encoded frame.
const CodecVersion = 1

// OpKind is the patch op discriminant (ADR-11 §2).
type OpKind uint8

const (
	OpSetText    OpKind = 1 // set a slot's text content
	OpSetAttr    OpKind = 2 // set a slot element attribute (Attr=name, Payload=value)
	OpSetValue   OpKind = 3 // set an interactive primitive's value
	OpSpliceList OpKind = 4 // keyed add/remove/move over a list/table's children
)

// SpliceKind is one keyed list edit within a spliceList op.
type SpliceKind uint8

const (
	SpliceAdd    SpliceKind = 1 // insert a keyed row (HTML) at Index
	SpliceRemove SpliceKind = 2 // remove the keyed row
	SpliceMove   SpliceKind = 3 // move the keyed row to Index
)

// Splice is one keyed list edit.
type Splice struct {
	Kind  SpliceKind
	Key   string
	Index int
	HTML  string // rendered row HTML (SpliceAdd only)
}

// Op is one patch op (ADR-11 §2 [slotId, kind, payload]).
type Op struct {
	SlotID  string
	Kind    OpKind
	Attr    string   // setAttr attribute name
	Payload string   // setText/setValue value, or setAttr value
	Splices []Splice // spliceList edits
}

// Frame is one patch frame (ADR-11 §2 [eventSeq, snapshotHash, ops[]]). EventSeq
// is the session's step_seq (the SSE event id + fencing counter, one number).
type Frame struct {
	EventSeq     uint64
	SnapshotHash uint64
	Ops          []Op
}

// EncodeFrame serializes a frame to its owned binary wire form (version-tagged).
func EncodeFrame(f Frame) []byte {
	var b []byte
	b = append(b, CodecVersion)
	b = appendU64(b, f.EventSeq)
	b = appendU64(b, f.SnapshotHash)
	b = appendUvarint(b, uint64(len(f.Ops)))
	for _, op := range f.Ops {
		b = append(b, byte(op.Kind))
		b = appendStr(b, op.SlotID)
		switch op.Kind {
		case OpSetText, OpSetValue:
			b = appendStr(b, op.Payload)
		case OpSetAttr:
			b = appendStr(b, op.Attr)
			b = appendStr(b, op.Payload)
		case OpSpliceList:
			b = appendUvarint(b, uint64(len(op.Splices)))
			for _, s := range op.Splices {
				b = append(b, byte(s.Kind))
				b = appendStr(b, s.Key)
				b = appendUvarint(b, uint64(s.Index))
				b = appendStr(b, s.HTML)
			}
		}
	}
	return b
}

// DecodeFrame parses a binary frame. It errors on a version mismatch, an unknown
// op/splice kind, or truncation — never panics on hostile input.
func DecodeFrame(b []byte) (Frame, error) {
	d := &dec{b: b}
	ver, ok := d.byte()
	if !ok {
		return Frame{}, errors.New("ui: empty frame")
	}
	if ver != CodecVersion {
		return Frame{}, fmt.Errorf("ui: unsupported codec version %d (want %d)", ver, CodecVersion)
	}
	var f Frame
	var err error
	if f.EventSeq, err = d.u64(); err != nil {
		return Frame{}, err
	}
	if f.SnapshotHash, err = d.u64(); err != nil {
		return Frame{}, err
	}
	n, err := d.uvarint()
	if err != nil {
		return Frame{}, err
	}
	for i := uint64(0); i < n; i++ {
		kb, ok := d.byte()
		if !ok {
			return Frame{}, errTrunc
		}
		op := Op{Kind: OpKind(kb)}
		if op.SlotID, err = d.str(); err != nil {
			return Frame{}, err
		}
		switch op.Kind {
		case OpSetText, OpSetValue:
			if op.Payload, err = d.str(); err != nil {
				return Frame{}, err
			}
		case OpSetAttr:
			if op.Attr, err = d.str(); err != nil {
				return Frame{}, err
			}
			if op.Payload, err = d.str(); err != nil {
				return Frame{}, err
			}
		case OpSpliceList:
			sn, err := d.uvarint()
			if err != nil {
				return Frame{}, err
			}
			for j := uint64(0); j < sn; j++ {
				skb, ok := d.byte()
				if !ok {
					return Frame{}, errTrunc
				}
				s := Splice{Kind: SpliceKind(skb)}
				if s.Key, err = d.str(); err != nil {
					return Frame{}, err
				}
				idx, err := d.uvarint()
				if err != nil {
					return Frame{}, err
				}
				s.Index = int(idx)
				if s.HTML, err = d.str(); err != nil {
					return Frame{}, err
				}
				if s.Kind < SpliceAdd || s.Kind > SpliceMove {
					return Frame{}, fmt.Errorf("ui: unknown splice kind %d", s.Kind)
				}
				op.Splices = append(op.Splices, s)
			}
		default:
			return Frame{}, fmt.Errorf("ui: unknown op kind %d", op.Kind)
		}
		f.Ops = append(f.Ops, op)
	}
	return f, nil
}

var errTrunc = errors.New("ui: truncated frame")

func appendU64(b []byte, v uint64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendUvarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}

func appendStr(b []byte, s string) []byte {
	b = appendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// dec is a bounds-checked reader over the frame bytes.
type dec struct {
	b   []byte
	pos int
}

func (d *dec) byte() (byte, bool) {
	if d.pos >= len(d.b) {
		return 0, false
	}
	v := d.b[d.pos]
	d.pos++
	return v, true
}

func (d *dec) u64() (uint64, error) {
	if d.pos+8 > len(d.b) {
		return 0, errTrunc
	}
	v := binary.BigEndian.Uint64(d.b[d.pos : d.pos+8])
	d.pos += 8
	return v, nil
}

func (d *dec) uvarint() (uint64, error) {
	v, n := binary.Uvarint(d.b[d.pos:])
	if n <= 0 {
		return 0, errTrunc
	}
	d.pos += n
	return v, nil
}

func (d *dec) str() (string, error) {
	n, err := d.uvarint()
	if err != nil {
		return "", err
	}
	if d.pos+int(n) > len(d.b) {
		return "", errTrunc
	}
	s := string(d.b[d.pos : d.pos+int(n)])
	d.pos += int(n)
	return s, nil
}
