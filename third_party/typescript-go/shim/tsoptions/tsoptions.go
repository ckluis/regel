// Package tsoptions re-exports ParsedCommandLine and its direct constructor, so
// tsx can build a command line from a CompilerOptions + root file list without a
// tsconfig on disk. Pure re-export.
package tsoptions

import "github.com/microsoft/typescript-go/internal/tsoptions"

type ParsedCommandLine = tsoptions.ParsedCommandLine

// NewParsedCommandLine builds a ParsedCommandLine directly from compiler options
// and a root file-name list (no tsconfig file involved).
var NewParsedCommandLine = tsoptions.NewParsedCommandLine
