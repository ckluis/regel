package rast

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// SchemaVersion is the AST-schema / TLV / normalize version. It travels in the
// address prefix ("r1_…") and the hash domain ("regel-ast/1\n").
const SchemaVersion = 1

// canonEncode serializes a NORMALIZED AST to its one canonical TLV byte
// sequence (ADR-02 §4). The input must already be normalized (Normalize); this
// function performs no reordering. It is total over valid nodes and panics only
// on a structurally impossible (undefined) kind — a programming error.
func canonEncode(n *Node) []byte {
	var b []byte
	encodeInto(&b, n)
	return b
}

func encodeInto(b *[]byte, n *Node) {
	if n == nil {
		n = &Node{Kind: KNone}
	}
	if !valid(n.Kind) {
		panic(fmt.Sprintf("rast: canonEncode of undefined kind %d", n.Kind))
	}
	*b = append(*b, byte(n.Kind))
	s := schema[n.Kind]
	if s.hasStr {
		putUvarint(b, uint64(len(n.Str)))
		*b = append(*b, n.Str...)
	}
	if s.hasU {
		if numAsF64(n.Kind) {
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], n.U)
			*b = append(*b, buf[:]...)
		} else {
			putUvarint(b, n.U)
		}
	}
	if s.hasMag {
		putUvarint(b, uint64(len(n.Mag)))
		*b = append(*b, n.Mag...)
	}
	putUvarint(b, uint64(len(n.Kids)))
	for _, c := range n.Kids {
		encodeInto(b, c)
	}
}

// canonDecode is the inverse of canonEncode: it rebuilds an AST from bytes,
// round-tripping byte-identically. The interpreter uses it to load ASTs from
// catalog rows (ADR-04 §2).
func canonDecode(data []byte) (*Node, error) {
	d := &decoder{buf: data}
	n, err := d.node()
	if err != nil {
		return nil, err
	}
	if d.pos != len(d.buf) {
		return nil, fmt.Errorf("rast: %d trailing bytes after decode", len(d.buf)-d.pos)
	}
	return n, nil
}

type decoder struct {
	buf []byte
	pos int
}

var errTruncated = errors.New("rast: truncated TLV stream")

func (d *decoder) node() (*Node, error) {
	if d.pos >= len(d.buf) {
		return nil, errTruncated
	}
	k := Kind(d.buf[d.pos])
	d.pos++
	if !valid(k) {
		return nil, fmt.Errorf("rast: undefined kind byte %d at %d", k, d.pos-1)
	}
	n := &Node{Kind: k}
	s := schema[k]
	if s.hasStr {
		str, err := d.bytes()
		if err != nil {
			return nil, err
		}
		n.Str = string(str)
	}
	if s.hasU {
		if numAsF64(k) {
			if d.pos+8 > len(d.buf) {
				return nil, errTruncated
			}
			n.U = binary.BigEndian.Uint64(d.buf[d.pos : d.pos+8])
			d.pos += 8
		} else {
			u, err := d.uvarint()
			if err != nil {
				return nil, err
			}
			n.U = u
		}
	}
	if s.hasMag {
		mag, err := d.bytes()
		if err != nil {
			return nil, err
		}
		if len(mag) > 0 {
			n.Mag = mag
		}
	}
	nk, err := d.uvarint()
	if err != nil {
		return nil, err
	}
	if nk > uint64(len(d.buf)-d.pos) {
		return nil, errTruncated // more children claimed than bytes remain
	}
	if nk > 0 {
		n.Kids = make([]*Node, nk)
		for i := range n.Kids {
			c, err := d.node()
			if err != nil {
				return nil, err
			}
			n.Kids[i] = c
		}
	}
	return n, nil
}

func (d *decoder) uvarint() (uint64, error) {
	v, n := binary.Uvarint(d.buf[d.pos:])
	if n <= 0 {
		return 0, errTruncated
	}
	d.pos += n
	return v, nil
}

func (d *decoder) bytes() ([]byte, error) {
	ln, err := d.uvarint()
	if err != nil {
		return nil, err
	}
	if ln > uint64(len(d.buf)-d.pos) {
		return nil, errTruncated
	}
	out := make([]byte, ln)
	copy(out, d.buf[d.pos:d.pos+int(ln)])
	d.pos += int(ln)
	return out, nil
}

func putUvarint(b *[]byte, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	*b = append(*b, tmp[:n]...)
}

// Encode returns the canonical TLV bytes of a normalized AST (exported for the
// CFR/continuation codec and the genesis image builder).
func Encode(n *Node) []byte { return canonEncode(n) }

// Decode rebuilds an AST from canonical TLV bytes (exported for the interpreter
// and CFR loader).
func Decode(data []byte) (*Node, error) { return canonDecode(data) }
