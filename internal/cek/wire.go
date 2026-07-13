package cek

// Validity predicates the CFR codec uses to fail closed on an unknown tag, kind,
// or enum value (ADR-05 §2: decode is versioned; unknown tag/kind ⇒ typed error,
// never a panic). These expose the closed sets without leaking internals.

// TagValid reports whether t is a defined Value tag in CFR v1.
func TagValid(t Tag) bool { return t <= TagOpaque }

// FrameKindValid reports whether k is a defined K frame kind in CFR v1.
func FrameKindValid(k FrameKind) bool { return k < frameKindMax }

// ModeValid reports whether m is a defined dispatch mode.
func ModeValid(m Mode) bool { return m <= ModeUnwind }

// SigKindValid reports whether k is a defined signal kind.
func SigKindValid(k SigKind) bool { return k <= SigReturn }

// ParkKindValid reports whether p is a defined park kind (append-only; includes
// ParkWake and ParkFresh, ADR-05 §8).
func ParkKindValid(p ParkKind) bool { return p <= ParkFresh }

// TierValid reports whether t is a defined tier.
func TierValid(t Tier) bool { return t <= TierTrusted }

// NewEnv builds an environment record (exposed for the CFR decoder to rebuild the
// content-shared env heap).
func NewEnv(parent *Env, slots []Value) *Env { return &Env{Parent: parent, Slots: slots} }

// --- exported Value constructors / accessors (kernel + CFR + tests) ----------

// NumV builds a number Value.
func NumV(n float64) Value { return Value{Tag: TagF64, N: n} }

// StrV builds a string Value.
func StrV(s string) Value { return Value{Tag: TagStr, S: s} }

// BoolV builds a boolean Value.
func BoolV(b bool) Value { return boolVal(b) }

// NullV / UndefV build the null / undefined Values.
func NullV() Value  { return null() }
func UndefV() Value { return undef() }

// Num returns the number payload and whether v is a number.
func (v Value) Num() (float64, bool) { return v.N, v.Tag == TagF64 }

// Str returns the string payload and whether v is a string.
func (v Value) StrVal() (string, bool) { return v.S, v.Tag == TagStr }

// Equal reports deep structural equality over the lattice (primitives by value,
// compound recursively) — the ADR-04 §6 "produced values" comparison.
func (v Value) Equal(o Value) bool { return deepEqual(v, o) }

func deepEqual(a, b Value) bool {
	if a.Tag != b.Tag {
		return false
	}
	switch a.Tag {
	case TagUndefined, TagNull:
		return true
	case TagBool:
		return a.asBool() == b.asBool()
	case TagF64:
		return a.N == b.N
	case TagStr, TagCapToken:
		return a.S == b.S
	case TagBigInt:
		return a.big().Cmp(b.big()) == 0
	case TagArray:
		x, y := a.arr().Elems, b.arr().Elems
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if !deepEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case TagRecord:
		x, y := a.rec(), b.rec()
		if len(x.Keys) != len(y.Keys) {
			return false
		}
		for _, k := range x.Keys {
			yv, ok := y.get(k)
			if !ok || !deepEqual(x.M[k], yv) {
				return false
			}
		}
		return true
	default:
		return a.Ref == b.Ref
	}
}
