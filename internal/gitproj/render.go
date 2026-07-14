package gitproj

import "strings"

// render.go builds a projected file body from immortal catalog rows (ADR-09 §1 /
// BUILD-C item 3): the leading JSDoc docstring (definition_meta.docstring, stored
// WITH its /** */ delimiters, out-of-hash per ADR-02 §2) prepended to the
// definition's canonical_text (definition.canonical_text — already a complete,
// compiling module with imports regenerated from deps by rast.PrintModule). The
// body is a pure function of the content hash, so identical hashes ⇒ identical
// blobs (content-addressed rename fidelity, BUILD-C item 6).

// renderFile returns the projected bytes for a definition. docstring may be empty
// (std entries and undocumented defs). The docstring block is isolated as the sole
// leading block above the byte-identical canonical text, so a docstring difference
// is a diff of that block alone (BUILD-C item 4).
func renderFile(docstring, canonicalText string) []byte {
	var b strings.Builder
	if docstring != "" {
		b.WriteString(strings.TrimRight(docstring, "\n"))
		b.WriteByte('\n')
	}
	b.WriteString(canonicalText)
	if !strings.HasSuffix(canonicalText, "\n") {
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
