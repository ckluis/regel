package cek

import (
	"math"
	"math/big"
	"strconv"

	"regel.dev/regel/internal/rast"
)

// binOp applies a binary operator (arithmetic/comparison/bitwise) with JS
// semantics on the admitted lattice. Logical && || ?? are short-circuit and are
// handled in the step loop, not here. Returns (value, ok); ok=false ⇒ a type
// combination the dialect should never admit (fail closed).
func binOp(op rast.OpKind, a, b Value) (Value, bool) {
	switch op {
	case rast.OpAdd:
		return opAdd(a, b)
	case rast.OpSub:
		return numArith(a, b, func(x, y float64) float64 { return x - y },
			func(x, y *big.Int) *big.Int { return new(big.Int).Sub(x, y) })
	case rast.OpMul:
		return numArith(a, b, func(x, y float64) float64 { return x * y },
			func(x, y *big.Int) *big.Int { return new(big.Int).Mul(x, y) })
	case rast.OpDiv:
		return numArith(a, b, func(x, y float64) float64 { return x / y },
			func(x, y *big.Int) *big.Int {
				if y.Sign() == 0 {
					return nil
				}
				return new(big.Int).Quo(x, y)
			})
	case rast.OpMod:
		return numArith(a, b, jsMod,
			func(x, y *big.Int) *big.Int {
				if y.Sign() == 0 {
					return nil
				}
				return new(big.Int).Rem(x, y)
			})
	case rast.OpExp:
		return numArith(a, b, math.Pow,
			func(x, y *big.Int) *big.Int { return new(big.Int).Exp(x, y, nil) })
	case rast.OpBitAnd, rast.OpBitOr, rast.OpBitXor, rast.OpShl, rast.OpShr, rast.OpUShr:
		return bitOp(op, a, b)
	case rast.OpLt, rast.OpGt, rast.OpLe, rast.OpGe:
		return relOp(op, a, b)
	case rast.OpEqEq, rast.OpEqEqEq:
		return boolVal(strictEq(a, b)), true
	case rast.OpNeEq, rast.OpNeEqEq:
		return boolVal(!strictEq(a, b)), true
	default:
		return undef(), false
	}
}

func opAdd(a, b Value) (Value, bool) {
	switch {
	case a.Tag == TagStr || b.Tag == TagStr:
		return strVal(toStr(a) + toStr(b)), true
	case a.Tag == TagBigInt && b.Tag == TagBigInt:
		return bigVal(new(big.Int).Add(a.big(), b.big())), true
	default:
		x, ok1 := toNum(a)
		y, ok2 := toNum(b)
		if !ok1 || !ok2 {
			return undef(), false
		}
		return f64(x + y), true
	}
}

func numArith(a, b Value, fn func(x, y float64) float64, bfn func(x, y *big.Int) *big.Int) (Value, bool) {
	if a.Tag == TagBigInt && b.Tag == TagBigInt {
		r := bfn(a.big(), b.big())
		if r == nil {
			return undef(), false // bigint division by zero
		}
		return bigVal(r), true
	}
	x, ok1 := toNum(a)
	y, ok2 := toNum(b)
	if !ok1 || !ok2 {
		return undef(), false
	}
	return f64(fn(x, y)), true
}

// jsMod implements the ECMAScript % operator (result takes the sign of the
// dividend; NaN/Inf rules follow Go's math.Mod which matches JS for finite).
func jsMod(x, y float64) float64 { return math.Mod(x, y) }

func bitOp(op rast.OpKind, a, b Value) (Value, bool) {
	if a.Tag == TagBigInt && b.Tag == TagBigInt {
		x, y := a.big(), b.big()
		switch op {
		case rast.OpBitAnd:
			return bigVal(new(big.Int).And(x, y)), true
		case rast.OpBitOr:
			return bigVal(new(big.Int).Or(x, y)), true
		case rast.OpBitXor:
			return bigVal(new(big.Int).Xor(x, y)), true
		case rast.OpShl:
			return bigVal(new(big.Int).Lsh(x, uint(y.Int64()))), true
		case rast.OpShr:
			return bigVal(new(big.Int).Rsh(x, uint(y.Int64()))), true
		}
		return undef(), false
	}
	x, ok1 := toNum(a)
	y, ok2 := toNum(b)
	if !ok1 || !ok2 {
		return undef(), false
	}
	xi := toInt32(x)
	switch op {
	case rast.OpBitAnd:
		return f64(float64(xi & toInt32(y))), true
	case rast.OpBitOr:
		return f64(float64(xi | toInt32(y))), true
	case rast.OpBitXor:
		return f64(float64(xi ^ toInt32(y))), true
	case rast.OpShl:
		return f64(float64(xi << (toUint32(y) & 31))), true
	case rast.OpShr:
		return f64(float64(xi >> (toUint32(y) & 31))), true
	case rast.OpUShr:
		return f64(float64(toUint32(x) >> (toUint32(y) & 31))), true
	}
	return undef(), false
}

func relOp(op rast.OpKind, a, b Value) (Value, bool) {
	// String vs string: lexical (code-point) order. Stage-A residue: JS uses
	// UTF-16 code-unit order; identical for the BMP used in tests.
	if a.Tag == TagStr && b.Tag == TagStr {
		return boolVal(cmpStr(op, a.S, b.S)), true
	}
	if a.Tag == TagBigInt && b.Tag == TagBigInt {
		c := a.big().Cmp(b.big())
		return boolVal(cmpInt(op, c)), true
	}
	x, ok1 := toNum(a)
	y, ok2 := toNum(b)
	if !ok1 || !ok2 {
		return undef(), false
	}
	switch op {
	case rast.OpLt:
		return boolVal(x < y), true
	case rast.OpGt:
		return boolVal(x > y), true
	case rast.OpLe:
		return boolVal(x <= y), true
	case rast.OpGe:
		return boolVal(x >= y), true
	}
	return undef(), false
}

func cmpStr(op rast.OpKind, a, b string) bool {
	switch op {
	case rast.OpLt:
		return a < b
	case rast.OpGt:
		return a > b
	case rast.OpLe:
		return a <= b
	case rast.OpGe:
		return a >= b
	}
	return false
}

func cmpInt(op rast.OpKind, c int) bool {
	switch op {
	case rast.OpLt:
		return c < 0
	case rast.OpGt:
		return c > 0
	case rast.OpLe:
		return c <= 0
	case rast.OpGe:
		return c >= 0
	}
	return false
}

// strictEq implements === over the lattice: primitives by value, compound by
// reference identity.
func strictEq(a, b Value) bool {
	if a.Tag != b.Tag {
		return false
	}
	switch a.Tag {
	case TagUndefined, TagNull:
		return true
	case TagBool:
		return a.asBool() == b.asBool()
	case TagF64:
		return a.N == b.N // NaN !== NaN falls out naturally
	case TagStr:
		return a.S == b.S
	case TagBigInt:
		return a.big().Cmp(b.big()) == 0
	case TagCapToken:
		return a.S == b.S
	default:
		return a.Ref == b.Ref // pointer identity for array/record/closure/opaque
	}
}

// unaryOp applies a prefix unary operator (not / && short-circuit handled apart).
func unaryOp(op rast.OpKind, v Value) (Value, bool) {
	switch op {
	case rast.OpPos:
		x, ok := toNum(v)
		return f64(x), ok
	case rast.OpNeg:
		if v.Tag == TagBigInt {
			return bigVal(new(big.Int).Neg(v.big())), true
		}
		x, ok := toNum(v)
		return f64(-x), ok
	case rast.OpNot:
		return boolVal(!truthy(v)), true
	case rast.OpBitNot:
		if v.Tag == TagBigInt {
			return bigVal(new(big.Int).Not(v.big())), true
		}
		x, ok := toNum(v)
		if !ok {
			return undef(), false
		}
		return f64(float64(^toInt32(x))), true
	}
	return undef(), false
}

// --- coercions --------------------------------------------------------------

func toNum(v Value) (float64, bool) {
	switch v.Tag {
	case TagF64:
		return v.N, true
	case TagBool:
		return v.N, true
	case TagStr:
		if v.S == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(v.S, 64)
		if err != nil {
			return math.NaN(), true
		}
		return f, true
	case TagNull:
		return 0, true
	case TagUndefined:
		return math.NaN(), true
	default:
		return math.NaN(), false
	}
}

func toStr(v Value) string {
	switch v.Tag {
	case TagStr:
		return v.S
	case TagUndefined:
		return "undefined"
	case TagNull:
		return "null"
	case TagBool:
		if v.asBool() {
			return "true"
		}
		return "false"
	case TagF64:
		return numToStr(v.N)
	case TagBigInt:
		return v.big().String()
	case TagArray:
		out := ""
		for i, e := range v.arr().Elems {
			if i > 0 {
				out += ","
			}
			if e.Tag != TagUndefined && e.Tag != TagNull {
				out += toStr(e)
			}
		}
		return out
	case TagRecord:
		return "[object Object]"
	default:
		return "[object Object]"
	}
}

// numToStr renders a number the way JS String(n) does for the cases the tests
// need (integers without a decimal point, shortest round-tripping otherwise).
func numToStr(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// f64bits decodes a KNum.U IEEE-754 bit pattern (ADR-02 §2) to a float64.
func f64bits(u uint64) float64 { return math.Float64frombits(u) }

func newBig() *big.Int { return new(big.Int) }
func bigOne() *big.Int { return big.NewInt(1) }

func toInt32(f float64) int32 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int32(uint32(int64(math.Trunc(f))))
}

func toUint32(f float64) uint32 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return uint32(int64(math.Trunc(f)))
}
