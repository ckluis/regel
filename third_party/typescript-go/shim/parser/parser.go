// Package parser re-exports tsgo's single-file parse entry point. Pure re-export.
package parser

import "github.com/microsoft/typescript-go/internal/parser"

// ParseSourceFile parses one file to a *ast.SourceFile, attaching parse
// diagnostics to the returned node. No typecheck, no I/O.
var ParseSourceFile = parser.ParseSourceFile
