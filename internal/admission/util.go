package admission

import (
	"crypto/rand"
	"fmt"
	"strings"

	"regel.dev/regel/internal/rast"
)

// NewUUID mints an RFC-4122 v4 UUID string (gate_refusal PK, minted before the
// refusal is returned — ADR-07 R1-08; also the kernel lease-owner id).
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// qualifiedToCatalogName maps a lowering resolver key "module/path.exportedName"
// (e.g. "std/mail.send") to a catalog name ("std/mail/send"): the final "."
// separating module from export becomes "/".
func qualifiedToCatalogName(qualified string) string {
	i := strings.LastIndex(qualified, ".")
	if i < 0 {
		return qualified
	}
	return qualified[:i] + "/" + qualified[i+1:]
}

// catalogKind maps a rast DefKind to the definition.kind / name_pointer.kind
// column value (ADR-03 §1 CHECK set). STAGE-A RESIDUE: DefValue maps to
// 'function' — the reference product's top-level consts are function values;
// a distinct value kind is not in the Stage-A CHECK set.
func catalogKind(k rast.DefKind) string {
	switch k {
	case rast.DefType, rast.DefInterface:
		return "type"
	default: // DefFunc, DefValue, DefNative
		return "function"
	}
}
