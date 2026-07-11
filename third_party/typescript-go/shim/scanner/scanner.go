// Package scanner re-exports the position→line/character helper tsx uses to
// turn a diagnostic byte position into a 1-based line/column. Pure re-export.
package scanner

import "github.com/microsoft/typescript-go/internal/scanner"

// GetECMALineAndUTF16CharacterOfPosition returns the 0-based line and UTF-16
// character offset for a byte position in a source file.
var GetECMALineAndUTF16CharacterOfPosition = scanner.GetECMALineAndUTF16CharacterOfPosition

// GetLeadingCommentRanges iterates the comment ranges in text following pos —
// used by internal/lower to strip comments/docstrings into the ADR-02 §2
// sidecar. Pure re-export.
var GetLeadingCommentRanges = scanner.GetLeadingCommentRanges
