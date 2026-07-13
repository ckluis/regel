package cek

import "strconv"

// getMember reads obj.key over the admitted lattice (own-key semantics; no
// prototype chain — ADR-01). Missing keys read undefined.
func getMember(obj Value, key string) Value {
	switch obj.Tag {
	case TagRecord:
		if v, ok := obj.rec().get(key); ok {
			return v
		}
		return undef()
	case TagArray:
		if key == "length" {
			return f64(float64(len(obj.arr().Elems)))
		}
		if i, ok := arrayIndex(key); ok {
			a := obj.arr().Elems
			if i >= 0 && i < len(a) {
				return a[i]
			}
		}
		return undef()
	case TagStr:
		if key == "length" {
			return f64(float64(len([]rune(obj.S))))
		}
		if i, ok := arrayIndex(key); ok {
			r := []rune(obj.S)
			if i >= 0 && i < len(r) {
				return strVal(string(r[i]))
			}
		}
		return undef()
	default:
		return undef()
	}
}

// getIndex reads obj[idx].
func getIndex(obj, idx Value) Value {
	if obj.Tag == TagArray {
		if i, ok := numIndex(idx); ok {
			a := obj.arr().Elems
			if i >= 0 && i < len(a) {
				return a[i]
			}
			return undef()
		}
	}
	return getMember(obj, propKeyString(idx))
}

// setMember writes obj.key. Arrays grow to fit a numeric key.
func setMember(obj Value, key string, v Value) {
	switch obj.Tag {
	case TagRecord:
		obj.rec().set(key, v)
	case TagArray:
		if i, ok := arrayIndex(key); ok {
			growSet(obj.arr(), i, v)
		}
	}
}

// setIndex writes obj[idx].
func setIndex(obj, idx, v Value) {
	if obj.Tag == TagArray {
		if i, ok := numIndex(idx); ok {
			growSet(obj.arr(), i, v)
			return
		}
	}
	if obj.Tag == TagRecord {
		obj.rec().set(propKeyString(idx), v)
	}
}

func growSet(a *ArrayObj, i int, v Value) {
	if i < 0 {
		return
	}
	for len(a.Elems) <= i {
		a.Elems = append(a.Elems, undef())
	}
	a.Elems[i] = v
}

// hasOwn implements the `in` operator's own-key test.
func hasOwn(key string, obj Value) bool {
	switch obj.Tag {
	case TagRecord:
		_, ok := obj.rec().get(key)
		return ok
	case TagArray:
		if key == "length" {
			return true
		}
		if i, ok := arrayIndex(key); ok {
			return i >= 0 && i < len(obj.arr().Elems)
		}
	}
	return false
}

func arrayIndex(key string) (int, bool) {
	i, err := strconv.Atoi(key)
	if err != nil {
		return 0, false
	}
	return i, true
}

func numIndex(v Value) (int, bool) {
	if v.Tag == TagF64 {
		i := int(v.N)
		if float64(i) == v.N {
			return i, true
		}
	}
	return 0, false
}
