package kernel

import (
	"fmt"
	"math/big"

	"regel.dev/regel/internal/cek"
)

// ValueToJSON is the exported projection used by the CLI (step-once summary).
func ValueToJSON(v cek.Value) any { return valueToJSON(v) }

// valueToJSON projects a cek.Value to a JSON-marshalable Go value using only the
// exported Value surface (Tag/N/S/Ref). Compound handles (closures, capability
// tokens, opaque) render as a tagged marker string — they are not transportable
// values in Stage A.
func valueToJSON(v cek.Value) any {
	switch v.Tag {
	case cek.TagUndefined, cek.TagNull:
		return nil
	case cek.TagBool:
		return v.N != 0
	case cek.TagF64:
		return v.N
	case cek.TagStr:
		return v.S
	case cek.TagBigInt:
		if z, ok := v.Ref.(*big.Int); ok {
			return z.String()
		}
		return nil
	case cek.TagArray:
		if a, ok := v.Ref.(*cek.ArrayObj); ok {
			out := make([]any, len(a.Elems))
			for i, e := range a.Elems {
				out[i] = valueToJSON(e)
			}
			return out
		}
		return []any{}
	case cek.TagRecord:
		if r, ok := v.Ref.(*cek.RecordObj); ok {
			out := make(map[string]any, len(r.Keys))
			for _, k := range r.Keys {
				out[k] = valueToJSON(r.M[k])
			}
			return out
		}
		return map[string]any{}
	default:
		return fmt.Sprintf("<opaque tag %d>", v.Tag)
	}
}

// jsonToValue builds a cek.Value from a decoded JSON value using only exported
// constructors/fields.
func jsonToValue(a any) cek.Value {
	switch x := a.(type) {
	case nil:
		return cek.NullV()
	case bool:
		return cek.BoolV(x)
	case float64:
		return cek.NumV(x)
	case string:
		return cek.StrV(x)
	case []any:
		elems := make([]cek.Value, len(x))
		for i, e := range x {
			elems[i] = jsonToValue(e)
		}
		return cek.Value{Tag: cek.TagArray, Ref: &cek.ArrayObj{Elems: elems}}
	case map[string]any:
		r := &cek.RecordObj{M: map[string]cek.Value{}}
		for k, val := range x {
			r.Keys = append(r.Keys, k)
			r.M[k] = jsonToValue(val)
		}
		return cek.Value{Tag: cek.TagRecord, Ref: r}
	default:
		return cek.UndefV()
	}
}
