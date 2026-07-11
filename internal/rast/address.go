package rast

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// Address computes the content address of a NORMALIZED AST (ADR-02 §1, §4):
//
//	digest  = SHA-256( "regel-ast/1\n" ‖ canonEncode(ast) )
//	address = "r1_" + lowercaseCrockfordBase32(digest)   (full 52 chars)
//
// The caller is responsible for normalizing first (lowering does; NormalizeAndAddress
// is the convenience for raw trees). Speed is irrelevant at admission granularity.
func Address(n *Node) string {
	h := sha256.New()
	h.Write(domainBytes)
	h.Write(canonEncode(n))
	var sum [32]byte
	h.Sum(sum[:0])
	return addrPrefix + crockfordEncode(sum[:])
}

// NormalizeAndAddress normalizes a copy of n and returns (normalized, address).
// Used by tests and the property fuzzer whose generated ASTs are not yet sorted.
func NormalizeAndAddress(n *Node) (*Node, string) {
	nn := Normalize(n)
	return nn, Address(nn)
}

const (
	addrPrefix       = "r1_"
	crockfordAlpha   = "0123456789abcdefghjkmnpqrstvwxyz"
	addressBodyChars = 52 // ceil(256/5)
)

var domainBytes = []byte(fmt.Sprintf("regel-ast/%d\n", SchemaVersion))

// crockfordEncode renders 32 bytes (256 bits) as 52 lowercase Crockford base32
// characters, MSB-first, zero-padded on the low bits of the final group. This is
// a fixed-width, untruncated rendering (ADR-02 §4 / STAGE-A pin #1).
func crockfordEncode(b []byte) string {
	var sb strings.Builder
	sb.Grow(addressBodyChars)
	var acc uint32
	var bits uint
	for _, by := range b {
		acc = acc<<8 | uint32(by)
		bits += 8
		for bits >= 5 {
			bits -= 5
			sb.WriteByte(crockfordAlpha[(acc>>bits)&0x1f])
		}
	}
	if bits > 0 { // final 1 leftover bit (256 mod 5 == 1) → pad low with zeros
		sb.WriteByte(crockfordAlpha[(acc<<(5-bits))&0x1f])
	}
	return sb.String()
}

var crockfordDecodeTable = func() [256]int8 {
	var t [256]int8
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(crockfordAlpha); i++ {
		t[crockfordAlpha[i]] = int8(i)
	}
	return t
}()

// DecodeAddress parses an address back to its 32-byte digest, validating the
// prefix and alphabet. Used by Verify.
func DecodeAddress(addr string) ([32]byte, error) {
	var out [32]byte
	if !strings.HasPrefix(addr, addrPrefix) {
		return out, fmt.Errorf("rast: address %q lacks %q prefix", addr, addrPrefix)
	}
	body := addr[len(addrPrefix):]
	if len(body) != addressBodyChars {
		return out, fmt.Errorf("rast: address body %d chars, want %d", len(body), addressBodyChars)
	}
	var acc uint32
	var bits uint
	oi := 0
	for i := 0; i < len(body); i++ {
		v := crockfordDecodeTable[body[i]]
		if v < 0 {
			return out, fmt.Errorf("rast: bad Crockford char %q in address", body[i])
		}
		acc = acc<<5 | uint32(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			if oi >= 32 {
				return out, fmt.Errorf("rast: address decodes to too many bytes")
			}
			out[oi] = byte(acc >> bits)
			oi++
		}
	}
	if oi != 32 {
		return out, fmt.Errorf("rast: address decodes to %d bytes, want 32", oi)
	}
	return out, nil
}

// Verify recomputes the address of n and reports whether it equals addr — the
// re-hash check for ADR-02 §5 guarantee 4 / self-certifying byte-restore (§5.5).
func Verify(n *Node, addr string) bool { return Address(n) == addr }
