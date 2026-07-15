package cek

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"time"
)

// stdworld.go carries the Stage-D std/ world natives (ADR-10 §3 SHIP batteries):
// the tier-1 UI component constructors (§7), money (§5 decimal, no float), crypto
// AEAD (§3 intrinsic-only), and the http/time/log capability + read/sink natives.
// Each is dispatched by definition hash from the genesis image (admission.Image).
//
// Design invariants held here:
//   - every value a native returns is built from the EXISTING encodable tags
//     (record/array/string/bigint), so CFR capture of std-produced state (a UI
//     tree, a money value) round-trips unchanged — no new V5 lattice tag (§3).
//   - no float ever represents money: the value is a minor-units bigint plus a
//     currency string, in a record (§5 "decimal + currency, no implicit coercion").
//   - no key material is a dialect value: crypto keys are referenced by an opaque
//     token string and derived inside the native (§3 "no key material as a
//     dialect value; keys live in external KMS").

// --- std/ui: the closed 25 tier-1 semantic components (ADR-10 §7) -------------

// UITier1 is the closed tier-1 component roster in the ADR-10 §7 declared order.
// Each name becomes a std/ui native constructor (UINative) returning a plain
// {component, props, children} record. The set is closed: derivation totality
// (V6) rests on every field type mapping to a member of this roster.
var UITier1 = []string{
	"page", "section", "stack", "grid", "nav", "heading", "text", "label",
	"badge", "money", "datetime", "avatar", "icon", "link", "button", "field",
	"select", "checkbox", "dialog", "card", "list", "table", "alert", "spinner",
	"empty",
}

// MaskingLeaves marks the six value-binding masking leaves (ADR-10 §7 ◆): the
// ONLY component sites where pii masking lives, and nowhere else. D2 owns the
// runtime masking behavior; this table is the queryable Go surface later
// increments (and V2's render-path coverage claim) key on to enumerate the finite
// set of masking-obligation sites.
var MaskingLeaves = map[string]bool{
	"text":   true,
	"badge":  true,
	"money":  true,
	"avatar": true,
	"field":  true,
	"table":  true,
}

// UINative returns the native constructor for tier-1 component `name`. The node
// shape is the record {component:<name>, props:<record>, children:<array>}:
// props defaults to an empty record and children to an empty array, so a bare
// call is a well-formed node. Masking is not applied here (D2 owns it); the
// constructor is a pure value builder over the encodable lattice.
func UINative(name string) NativeFn {
	return func(_ *Host, args []Value) (Value, *NativePark) {
		props := recVal(newRecord())
		if len(args) >= 1 && args[0].Tag == TagRecord {
			props = args[0]
		}
		children := arrVal(&ArrayObj{})
		if len(args) >= 2 && args[1].Tag == TagArray {
			children = args[1]
		}
		r := newRecord()
		r.set("component", strVal(name))
		r.set("props", props)
		r.set("children", children)
		return recVal(r), nil
	}
}

// --- std/money: decimal money, no float (ADR-10 §5) ---------------------------

// StdMoney is the money(minorUnits, currency) constructor: a decimal amount as a
// minor-units bigint plus an ISO currency string, in a record. NO float ever
// touches the value — a float argument is truncated to an integer minor-units
// count, never carried as a float (§5 "no implicit coercion").
func StdMoney(_ *Host, args []Value) (Value, *NativePark) {
	minor := big.NewInt(0)
	if len(args) >= 1 {
		switch args[0].Tag {
		case TagBigInt:
			minor = new(big.Int).Set(args[0].big())
		case TagF64:
			minor = big.NewInt(int64(args[0].N))
		}
	}
	cur := ""
	if len(args) >= 2 && args[1].Tag == TagStr {
		cur = args[1].S
	}
	r := newRecord()
	r.set("minorUnits", bigVal(minor))
	r.set("currency", strVal(cur))
	return recVal(r), nil
}

// StdMoneyFormat renders a money record as "<currency> <major>.<cc>" over exactly
// two minor digits — a minimal, locale-free formatter (locale rules ride the §5
// semantic type at derivation; this is the intrinsic floor). Non-money input
// yields the empty string.
func StdMoneyFormat(_ *Host, args []Value) (Value, *NativePark) {
	if len(args) < 1 || args[0].Tag != TagRecord {
		return strVal(""), nil
	}
	r := args[0].rec()
	minorV, ok := r.get("minorUnits")
	if !ok || minorV.Tag != TagBigInt {
		return strVal(""), nil
	}
	curV, _ := r.get("currency")
	cur := ""
	if curV.Tag == TagStr {
		cur = curV.S
	}
	minor := minorV.big()
	neg := minor.Sign() < 0
	abs := new(big.Int).Abs(minor)
	hundred := big.NewInt(100)
	major := new(big.Int)
	rem := new(big.Int)
	major.DivMod(abs, hundred, rem)
	cents := rem.Int64()
	sign := ""
	if neg {
		sign = "-"
	}
	out := cur
	if out != "" {
		out += " "
	}
	out += sign + major.String() + "." + twoDigits(cents)
	return strVal(out), nil
}

func twoDigits(n int64) string {
	if n < 10 {
		return "0" + itoa(int(n))
	}
	return itoa(int(n))
}

// --- std/crypto: intrinsic-only AEAD (ADR-10 §3) ------------------------------

// aeadKey derives a 32-byte AES key from an opaque key token. The token is the
// only thing the dialect ever names; the key material never becomes a Value. A
// SHA-256 of the token stands in for the KMS-backed KDF at this floor (§3 "vetted
// AEAD+KDF only; keys live in external KMS").
func aeadKey(token string) [32]byte { return sha256.Sum256([]byte(token)) }

// StdAeadSeal is aeadSeal(keyToken, plaintext) → hex(nonce‖ciphertext) using
// AES-256-GCM with a fresh random nonce. A malformed argument yields undefined
// (fail-closed: no partial seal).
func StdAeadSeal(_ *Host, args []Value) (Value, *NativePark) {
	if len(args) < 2 || args[0].Tag != TagStr || args[1].Tag != TagStr {
		return undef(), nil
	}
	key := aeadKey(args[0].S)
	gcm, err := newGCM(key)
	if err != nil {
		return undef(), nil
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return undef(), nil
	}
	ct := gcm.Seal(nonce, nonce, []byte(args[1].S), nil)
	return strVal(hex.EncodeToString(ct)), nil
}

// StdAeadOpen is aeadOpen(keyToken, hexCiphertext) → plaintext, or undefined on
// any authentication or decode failure (fail-closed).
func StdAeadOpen(_ *Host, args []Value) (Value, *NativePark) {
	if len(args) < 2 || args[0].Tag != TagStr || args[1].Tag != TagStr {
		return undef(), nil
	}
	raw, err := hex.DecodeString(args[1].S)
	if err != nil {
		return undef(), nil
	}
	key := aeadKey(args[0].S)
	gcm, err := newGCM(key)
	if err != nil {
		return undef(), nil
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return undef(), nil
	}
	pt, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return undef(), nil
	}
	return strVal(string(pt)), nil
}

func newGCM(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// --- std/http: outbound capability natives (ADR-10 §3, effect class external) -

// StdHTTPGet / StdHTTPPost are the outbound HTTP capability natives. Like
// mail.send they RECORD an external effect intent rather than perform real I/O at
// this floor (the dispatcher delivers per ADR-05/06); the outbound call is a
// capability (V2 treats it as a sink), so an ungranted principal fails closed on
// a capability.revoked park with NO effect recorded.
func StdHTTPGet(h *Host, args []Value) (Value, *NativePark)  { return httpCall(h, "http.get", args) }
func StdHTTPPost(h *Host, args []Value) (Value, *NativePark) { return httpCall(h, "http.post", args) }

func httpCall(h *Host, capName string, args []Value) (Value, *NativePark) {
	if !h.Principal.IsOperator && !h.Principal.Grants[capName] {
		return undef(), &NativePark{Condition: SignalCondition("capability.revoked",
			[]Restart{
				{Name: "re-grant", Label: "Re-grant " + capName, CapabilityRequired: "operator"},
				{Name: "abort", Label: "Abort"},
			},
			map[string]any{"capability": capName})}
	}
	url := ""
	if len(args) > 0 {
		url = toStr(args[0])
	}
	h.RecordEffect(capName, map[string]any{"url": url})
	r := newRecord()
	r.set("intent", strVal(capName))
	r.set("url", strVal(url))
	return recVal(r), nil
}

// --- std/time (read) and std/log (external sink) ------------------------------

// StdTimeNow reads the wall clock and returns unix milliseconds. Effect class
// read (ADR-10 §6): the kernel performs it inline, no checkpoint — re-execution
// after a crash is safe (the value is a fresh read, not a re-fired effect).
func StdTimeNow(_ *Host, _ []Value) (Value, *NativePark) {
	return f64(float64(time.Now().UnixMilli())), nil
}

// StdLogWrite records a log.write sink intent (effect class external, ADR-10 §6):
// the log sink is in V2's sink set, so a pii value reaching it unmasked is a V2
// reject over the CALLER's AST — the native itself is a plain effect recorder.
func StdLogWrite(h *Host, args []Value) (Value, *NativePark) {
	msg := ""
	if len(args) > 0 {
		msg = toStr(args[0])
	}
	h.RecordEffect("log.write", map[string]any{"message": msg})
	return undef(), nil
}
