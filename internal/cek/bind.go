package cek

import (
	"fmt"
	"math/big"

	"regel.dev/regel/internal/rast"
)

// bindParams builds a function activation record (ADR-04 §2 E). Every parameter
// binder is flattened, in the lowerer's pre-order over the parameter list, into
// one slot array — matching the flat De Bruijn stack the printer reproduces
// (verified against internal/lower output).
func bindParams(fn *rast.Node, args []Value, closureEnv *Env) (*Env, error) {
	params := fn.Kids[0].Kids
	var slots []Value
	argi := 0
	for _, prm := range params {
		pat := prm.Kids[0]
		if prm.U&1 != 0 { // rest parameter
			rest := &ArrayObj{}
			for ; argi < len(args); argi++ {
				rest.Elems = append(rest.Elems, args[argi])
			}
			if err := bindPattern(pat, arrVal(rest), &slots); err != nil {
				return nil, err
			}
			continue
		}
		v := undef()
		if argi < len(args) {
			v = args[argi]
		}
		argi++
		if v.Tag == TagUndefined && len(prm.Kids) > 2 && !prm.Kids[2].IsNone() {
			if dv, ok := evalConst(prm.Kids[2]); ok {
				v = dv
			}
		}
		if err := bindPattern(pat, v, &slots); err != nil {
			return nil, err
		}
	}
	return pushEnv(closureEnv, slots), nil
}

// bindPattern destructures value v against a binding pattern, appending the bound
// values to *slots in binder pre-order (the collectBinders order the lowerer and
// printer share). It never evaluates general expressions — destructuring a
// computed value is pure — except literal defaults via evalConst.
func bindPattern(pat *rast.Node, v Value, slots *[]Value) error {
	switch pat.Kind {
	case rast.KBindId:
		*slots = append(*slots, v)
		return nil
	case rast.KArrayPat:
		elems := pat.Kids[0].Kids
		var arr []Value
		if v.Tag == TagArray {
			arr = v.arr().Elems
		}
		for i, el := range elems {
			if el.IsNone() { // elision hole: no binder
				continue
			}
			if el.Kind == rast.KRestPat {
				rest := &ArrayObj{}
				if i < len(arr) {
					rest.Elems = append(rest.Elems, arr[i:]...)
				}
				if err := bindPattern(el.Kids[0], arrVal(rest), slots); err != nil {
					return err
				}
				break
			}
			ev := undef()
			if i < len(arr) {
				ev = arr[i]
			}
			if err := bindPattern(el, ev, slots); err != nil {
				return err
			}
		}
		return nil
	case rast.KObjPat:
		props := pat.Kids[0].Kids
		for _, bp := range props { // KBindProp: Kids=[keyNode, pattern, default|None]
			if bp.Kind == rast.KRestPat {
				// object rest: bind remaining keys (Stage-A: shallow copy record)
				if err := bindPattern(bp.Kids[0], v, slots); err != nil {
					return err
				}
				continue
			}
			key, err := patternKey(bp)
			if err != nil {
				return err
			}
			pv := undef()
			if v.Tag == TagRecord {
				if got, ok := v.rec().get(key); ok {
					pv = got
				}
			}
			if pv.Tag == TagUndefined && len(bp.Kids) > 2 && !bp.Kids[2].IsNone() {
				if dv, ok := evalConst(bp.Kids[2]); ok {
					pv = dv
				}
			}
			if err := bindPattern(bp.Kids[1], pv, slots); err != nil {
				return err
			}
		}
		return nil
	case rast.KRestPat:
		return bindPattern(pat.Kids[0], v, slots)
	default:
		return fmt.Errorf("unsupported binding pattern kind %d", pat.Kind)
	}
}

// patternKey extracts a non-computed object-pattern property key.
func patternKey(bp *rast.Node) (string, error) {
	key := bp.Kids[0]
	if bp.U&1 != 0 { // computed
		kv, ok := evalConst(key)
		if !ok {
			return "", fmt.Errorf("computed destructuring key is not constant (Stage A)")
		}
		return propKeyString(kv), nil
	}
	switch key.Kind {
	case rast.KStr, rast.KStrPart, rast.KName:
		return key.Str, nil
	case rast.KNum:
		return numToStr(f64bits(key.U)), nil
	default:
		return "", fmt.Errorf("unsupported destructuring key kind %d", key.Kind)
	}
}

// evalConst is a tiny pure evaluator used ONLY for literal defaults and constant
// keys during binding. It never touches the environment or effects; a
// non-constant input returns ok=false (the caller keeps undefined / errors).
func evalConst(n *rast.Node) (Value, bool) {
	switch n.Kind {
	case rast.KNum:
		return f64(f64bits(n.U)), true
	case rast.KStr:
		return strVal(n.Str), true
	case rast.KBool:
		return boolVal(n.U != 0), true
	case rast.KNull:
		return null(), true
	case rast.KUndefined:
		return undef(), true
	case rast.KBigInt:
		z := new(big.Int).SetBytes(n.Mag)
		if n.U&1 != 0 {
			z.Neg(z)
		}
		return bigVal(z), true
	case rast.KUnary:
		v, ok := evalConst(n.Kids[0])
		if !ok {
			return undef(), false
		}
		r, ok := unaryOp(rast.OpKind(n.U), v)
		return r, ok
	case rast.KBinary:
		a, ok1 := evalConst(n.Kids[0])
		b, ok2 := evalConst(n.Kids[1])
		if !ok1 || !ok2 {
			return undef(), false
		}
		r, ok := binOp(rast.OpKind(n.U), a, b)
		return r, ok
	default:
		return undef(), false
	}
}

func propKeyString(v Value) string {
	if v.Tag == TagStr {
		return v.S
	}
	return toStr(v)
}
