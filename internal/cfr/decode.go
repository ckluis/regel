package cfr

import (
	"encoding/binary"
	"math"
	"math/big"

	"regel.dev/regel/internal/cek"
)

func mathFloat64bits(f float64) uint64 { return math.Float64bits(f) }

// Decode rebuilds a machine State from a CFR blob. It is versioned and total: an
// unknown version/tag/frame-kind or a truncation returns a typed error wrapping
// ErrCFR, never a panic and never a partial state (ADR-05 §6 test 4b).
func Decode(data []byte) (st *cek.State, err error) {
	defer func() {
		if r := recover(); r != nil {
			st = nil
			err = decodeErr("panic during decode: %v", r)
		}
	}()

	d := &decoder{buf: data}
	ver, e := d.byteE()
	if e != nil {
		return nil, e
	}
	if ver != FormatVersion {
		return nil, decodeErr("unknown format version %d", ver)
	}

	// Object heap: allocate shells, then fill (two passes support forward refs
	// and cycles).
	n, e := d.uvarintE()
	if e != nil {
		return nil, e
	}
	if n > uint64(len(d.buf)) {
		return nil, decodeErr("object count %d exceeds buffer", n)
	}
	d.shells = make([]any, n)
	types := make([]byte, n)
	for i := uint64(0); i < n; i++ {
		t, e := d.byteE()
		if e != nil {
			return nil, e
		}
		types[i] = t
		switch t {
		case objEnv:
			d.shells[i] = &cek.Env{}
		case objArray:
			d.shells[i] = &cek.ArrayObj{}
		case objRecord:
			d.shells[i] = &cek.RecordObj{M: map[string]cek.Value{}}
		default:
			return nil, decodeErr("unknown heap object type %d", t)
		}
	}
	for i := uint64(0); i < n; i++ {
		if e := d.fillObject(types[i], d.shells[i]); e != nil {
			return nil, e
		}
	}

	out := &cek.State{}
	if out.DefHash, e = d.strE(); e != nil {
		return nil, e
	}
	if out.Path, e = d.pathE(); e != nil {
		return nil, e
	}
	mode, e := d.byteE()
	if e != nil {
		return nil, e
	}
	if !cek.ModeValid(cek.Mode(mode)) {
		return nil, decodeErr("invalid mode %d", mode)
	}
	out.Mode = cek.Mode(mode)
	pk, e := d.byteE()
	if e != nil {
		return nil, e
	}
	if !cek.ParkKindValid(cek.ParkKind(pk)) {
		return nil, decodeErr("invalid park kind %d", pk)
	}
	out.ParkKind = cek.ParkKind(pk)
	tier, e := d.byteE()
	if e != nil {
		return nil, e
	}
	if !cek.TierValid(cek.Tier(tier)) {
		return nil, decodeErr("invalid tier %d", tier)
	}
	out.Tier = cek.Tier(tier)
	if out.FuelSteps, e = d.svarintE(); e != nil {
		return nil, e
	}
	if out.FuelAlloc, e = d.svarintE(); e != nil {
		return nil, e
	}
	if out.Val, e = d.valueE(); e != nil {
		return nil, e
	}
	sk, e := d.byteE()
	if e != nil {
		return nil, e
	}
	if !cek.SigKindValid(cek.SigKind(sk)) {
		return nil, decodeErr("invalid signal kind %d", sk)
	}
	out.Sig.Kind = cek.SigKind(sk)
	if out.Sig.Val, e = d.valueE(); e != nil {
		return nil, e
	}
	if out.Env, e = d.envRefE(); e != nil {
		return nil, e
	}

	fc, e := d.uvarintE()
	if e != nil {
		return nil, e
	}
	if fc > uint64(len(d.buf)) {
		return nil, decodeErr("frame count %d exceeds buffer", fc)
	}
	out.Kont = make([]*cek.Frame, fc)
	for i := uint64(0); i < fc; i++ {
		f, e := d.frameE()
		if e != nil {
			return nil, e
		}
		out.Kont[i] = f
	}
	if d.pos != len(d.buf) {
		return nil, decodeErr("%d trailing bytes", len(d.buf)-d.pos)
	}
	return out, nil
}

func (d *decoder) fillObject(t byte, shell any) error {
	switch t {
	case objEnv:
		env := shell.(*cek.Env)
		parent, e := d.envRefE()
		if e != nil {
			return e
		}
		env.Parent = parent
		ns, e := d.uvarintE()
		if e != nil {
			return e
		}
		if ns > uint64(len(d.buf)) {
			return decodeErr("env slot count too large")
		}
		env.Slots = make([]cek.Value, ns)
		for i := range env.Slots {
			if env.Slots[i], e = d.valueE(); e != nil {
				return e
			}
		}
	case objArray:
		a := shell.(*cek.ArrayObj)
		ne, e := d.uvarintE()
		if e != nil {
			return e
		}
		if ne > uint64(len(d.buf)) {
			return decodeErr("array length too large")
		}
		a.Elems = make([]cek.Value, ne)
		for i := range a.Elems {
			if a.Elems[i], e = d.valueE(); e != nil {
				return e
			}
		}
	case objRecord:
		r := shell.(*cek.RecordObj)
		nk, e := d.uvarintE()
		if e != nil {
			return e
		}
		if nk > uint64(len(d.buf)) {
			return decodeErr("record key count too large")
		}
		r.Keys = make([]string, nk)
		for i := range r.Keys {
			k, e := d.strE()
			if e != nil {
				return e
			}
			v, e := d.valueE()
			if e != nil {
				return e
			}
			r.Keys[i] = k
			r.M[k] = v
		}
	}
	return nil
}

func (d *decoder) frameE() (*cek.Frame, error) {
	f := &cek.Frame{}
	k, e := d.byteE()
	if e != nil {
		return nil, e
	}
	if !cek.FrameKindValid(cek.FrameKind(k)) {
		return nil, decodeErr("invalid frame kind %d", k)
	}
	f.Kind = cek.FrameKind(k)
	if f.Path, e = d.pathE(); e != nil {
		return nil, e
	}
	idx, e := d.svarintE()
	if e != nil {
		return nil, e
	}
	f.Idx = int(idx)
	aux, e := d.svarintE()
	if e != nil {
		return nil, e
	}
	f.Aux = int(aux)
	nv, e := d.uvarintE()
	if e != nil {
		return nil, e
	}
	if nv > uint64(len(d.buf)) {
		return nil, decodeErr("frame vals count too large")
	}
	f.Vals = make([]cek.Value, nv)
	for i := range f.Vals {
		if f.Vals[i], e = d.valueE(); e != nil {
			return nil, e
		}
	}
	if f.Env, e = d.envRefE(); e != nil {
		return nil, e
	}
	if f.OuterEnv, e = d.envRefE(); e != nil {
		return nil, e
	}
	if f.RetDef, e = d.strE(); e != nil {
		return nil, e
	}
	if f.RetPath, e = d.pathE(); e != nil {
		return nil, e
	}
	if f.RetEnv, e = d.envRefE(); e != nil {
		return nil, e
	}
	if f.Obj, e = d.valueE(); e != nil {
		return nil, e
	}
	if f.Key, e = d.strE(); e != nil {
		return nil, e
	}
	if f.IdxVal, e = d.valueE(); e != nil {
		return nil, e
	}
	hasPend, e := d.byteE()
	if e != nil {
		return nil, e
	}
	if hasPend == 1 {
		sk, e := d.byteE()
		if e != nil {
			return nil, e
		}
		if !cek.SigKindValid(cek.SigKind(sk)) {
			return nil, decodeErr("invalid pending signal kind %d", sk)
		}
		pv, e := d.valueE()
		if e != nil {
			return nil, e
		}
		f.Pend = &cek.Signal{Kind: cek.SigKind(sk), Val: pv}
	}
	return f, nil
}

// --- decoder primitives -----------------------------------------------------

type decoder struct {
	buf    []byte
	pos    int
	shells []any
}

func (d *decoder) byteE() (byte, error) {
	if d.pos >= len(d.buf) {
		return 0, decodeErr("truncated: expected byte at %d", d.pos)
	}
	b := d.buf[d.pos]
	d.pos++
	return b, nil
}

func (d *decoder) uvarintE() (uint64, error) {
	v, n := binary.Uvarint(d.buf[d.pos:])
	if n <= 0 {
		return 0, decodeErr("truncated uvarint at %d", d.pos)
	}
	d.pos += n
	return v, nil
}

func (d *decoder) svarintE() (int64, error) {
	v, n := binary.Varint(d.buf[d.pos:])
	if n <= 0 {
		return 0, decodeErr("truncated varint at %d", d.pos)
	}
	d.pos += n
	return v, nil
}

func (d *decoder) f64E() (float64, error) {
	if d.pos+8 > len(d.buf) {
		return 0, decodeErr("truncated f64 at %d", d.pos)
	}
	bits := binary.BigEndian.Uint64(d.buf[d.pos : d.pos+8])
	d.pos += 8
	return math.Float64frombits(bits), nil
}

func (d *decoder) bytesE() ([]byte, error) {
	ln, e := d.uvarintE()
	if e != nil {
		return nil, e
	}
	if ln > uint64(len(d.buf)-d.pos) {
		return nil, decodeErr("truncated bytes (want %d) at %d", ln, d.pos)
	}
	out := make([]byte, ln)
	copy(out, d.buf[d.pos:d.pos+int(ln)])
	d.pos += int(ln)
	return out, nil
}

func (d *decoder) strE() (string, error) {
	b, e := d.bytesE()
	if e != nil {
		return "", e
	}
	return string(b), nil
}

func (d *decoder) pathE() (cek.Path, error) {
	ln, e := d.uvarintE()
	if e != nil {
		return nil, e
	}
	if ln > uint64(len(d.buf)-d.pos) {
		return nil, decodeErr("truncated path (want %d) at %d", ln, d.pos)
	}
	if ln == 0 {
		return nil, nil
	}
	p := make(cek.Path, ln)
	for i := range p {
		v, e := d.uvarintE()
		if e != nil {
			return nil, e
		}
		if v > 0xFFFF {
			return nil, decodeErr("path index %d out of range", v)
		}
		p[i] = uint16(v)
	}
	return p, nil
}

func (d *decoder) objRefE() (any, error) {
	ref, e := d.uvarintE()
	if e != nil {
		return nil, e
	}
	if ref == 0 {
		return nil, nil
	}
	idx := int(ref - 1)
	if idx < 0 || idx >= len(d.shells) {
		return nil, decodeErr("object ref %d out of range", ref)
	}
	return d.shells[idx], nil
}

func (d *decoder) envRefE() (*cek.Env, error) {
	o, e := d.objRefE()
	if e != nil {
		return nil, e
	}
	if o == nil {
		return nil, nil
	}
	env, ok := o.(*cek.Env)
	if !ok {
		return nil, decodeErr("env ref points to non-env object")
	}
	return env, nil
}

func (d *decoder) valueE() (cek.Value, error) {
	tb, e := d.byteE()
	if e != nil {
		return cek.Value{}, e
	}
	if !cek.TagValid(cek.Tag(tb)) {
		return cek.Value{}, decodeErr("invalid value tag %d", tb)
	}
	tag := cek.Tag(tb)
	v := cek.Value{Tag: tag}
	switch tag {
	case cek.TagUndefined, cek.TagNull:
	case cek.TagBool:
		b, e := d.byteE()
		if e != nil {
			return cek.Value{}, e
		}
		v.N = float64(b)
	case cek.TagF64:
		if v.N, e = d.f64E(); e != nil {
			return cek.Value{}, e
		}
	case cek.TagStr, cek.TagCapToken:
		if v.S, e = d.strE(); e != nil {
			return cek.Value{}, e
		}
	case cek.TagBigInt:
		sign, e := d.byteE()
		if e != nil {
			return cek.Value{}, e
		}
		mag, e := d.bytesE()
		if e != nil {
			return cek.Value{}, e
		}
		z := new(big.Int).SetBytes(mag)
		if sign == 1 {
			z.Neg(z)
		}
		v.Ref = z
	case cek.TagArray:
		o, e := d.objRefE()
		if e != nil {
			return cek.Value{}, e
		}
		a, ok := o.(*cek.ArrayObj)
		if !ok {
			return cek.Value{}, decodeErr("array value ref is not an array")
		}
		v.Ref = a
	case cek.TagRecord:
		o, e := d.objRefE()
		if e != nil {
			return cek.Value{}, e
		}
		r, ok := o.(*cek.RecordObj)
		if !ok {
			return cek.Value{}, decodeErr("record value ref is not a record")
		}
		v.Ref = r
	case cek.TagClosure:
		defHash, e := d.strE()
		if e != nil {
			return cek.Value{}, e
		}
		p, e := d.pathE()
		if e != nil {
			return cek.Value{}, e
		}
		env, e := d.envRefE()
		if e != nil {
			return cek.Value{}, e
		}
		v.Ref = &cek.ClosureObj{DefHash: defHash, Path: p, Env: env}
	case cek.TagOpaque:
		codec, e := d.strE()
		if e != nil {
			return cek.Value{}, e
		}
		data, e := d.bytesE()
		if e != nil {
			return cek.Value{}, e
		}
		v.Ref = &cek.OpaqueObj{Codec: codec, Data: data}
	}
	return v, nil
}
