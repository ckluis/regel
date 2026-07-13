package cfr

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// uuid4 generates a random RFC-4122 v4 UUID string.
func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// byteaLiteral renders bytes as a Postgres hex bytea input literal (\x…).
func byteaLiteral(b []byte) string { return `\x` + hex.EncodeToString(b) }

// decodeBytea parses a hex-format bytea text value (\x…) back to bytes.
func decodeBytea(s string) ([]byte, error) {
	if len(s) < 2 || s[0] != '\\' || s[1] != 'x' {
		return nil, fmt.Errorf("cfr: bytea value lacks \\x prefix")
	}
	return hex.DecodeString(s[2:])
}

// hexDecode parses a bare hex string (no \x prefix) as produced by SQL
// encode(col,'hex'). Empty input decodes to an empty slice.
func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }

// nullable maps "" to SQL NULL, else the string.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func jsonOrEmpty(m map[string]any) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseArgs(s string) map[string]any {
	if s == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}
