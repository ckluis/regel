// Package core re-exports the fork's internal/core compiler-options surface:
// CompilerOptions plus the Tristate / ScriptTarget / ModuleKind /
// ModuleResolutionKind / ModuleDetectionKind constants tsx sets on the locked
// config, and Version() for the TS7.x sanity check. Pure re-export, zero logic.
package core

import "github.com/microsoft/typescript-go/internal/core"

type (
	CompilerOptions      = core.CompilerOptions
	Tristate             = core.Tristate
	ScriptTarget         = core.ScriptTarget
	ModuleKind           = core.ModuleKind
	ModuleResolutionKind = core.ModuleResolutionKind
	ModuleDetectionKind  = core.ModuleDetectionKind
	TextRange            = core.TextRange
	TextPos              = core.TextPos
	ScriptKind           = core.ScriptKind
)

// GetScriptKindFromFileName maps a file name to its ScriptKind (e.g. .ts → TS).
var GetScriptKindFromFileName = core.GetScriptKindFromFileName

const (
	TSUnknown = core.TSUnknown
	TSFalse   = core.TSFalse
	TSTrue    = core.TSTrue

	ScriptTargetES2022 = core.ScriptTargetES2022
	ScriptTargetESNext = core.ScriptTargetESNext
	ScriptTargetLatest = core.ScriptTargetLatest

	ModuleKindESNext   = core.ModuleKindESNext
	ModuleKindNode16   = core.ModuleKindNode16
	ModuleKindNodeNext = core.ModuleKindNodeNext

	ModuleResolutionKindNode16   = core.ModuleResolutionKindNode16
	ModuleResolutionKindNodeNext = core.ModuleResolutionKindNodeNext
	ModuleResolutionKindBundler  = core.ModuleResolutionKindBundler

	ModuleDetectionKindForce = core.ModuleDetectionKindForce
)

// Version returns the vendored tsgo/TypeScript version string (e.g. "7.1.0-dev").
func Version() string { return core.Version() }

// VersionMajorMinor returns e.g. "7.1".
func VersionMajorMinor() string { return core.VersionMajorMinor() }
