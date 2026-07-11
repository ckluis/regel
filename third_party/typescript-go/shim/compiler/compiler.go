// Package compiler re-exports the tsgo Program/CompilerHost surface tsx needs to
// construct a fresh in-memory Program per admission and pull its diagnostics.
// Pure re-export; the diagnostic-collection methods (GetSyntacticDiagnostics,
// GetBindDiagnostics, GetSemanticDiagnostics, GetProgramDiagnostics,
// GetGlobalDiagnostics, SourceFiles, …) come free as methods on *Program.
package compiler

import "github.com/microsoft/typescript-go/internal/compiler"

type (
	Program        = compiler.Program
	ProgramOptions = compiler.ProgramOptions
	CompilerHost   = compiler.CompilerHost
)

// NewCompilerHost builds a CompilerHost over a vfs.FS. Pass a nil
// extendedConfigCache and nil trace for the hermetic tsx host (no project
// references, no tracing).
var NewCompilerHost = compiler.NewCompilerHost

// NewProgram constructs a fresh Program (fresh checker state) from options.
var NewProgram = compiler.NewProgram
