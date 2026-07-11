// Package tspath re-exports the path primitives tsx needs to build a
// ParsedCommandLine and a CompilerHost over an in-memory world. Pure re-export.
package tspath

import "github.com/microsoft/typescript-go/internal/tspath"

type (
	Path                = tspath.Path
	ComparePathsOptions = tspath.ComparePathsOptions
)

var (
	ToPath        = tspath.ToPath
	NormalizePath = tspath.NormalizePath
	GetDirectoryPath = tspath.GetDirectoryPath
)
