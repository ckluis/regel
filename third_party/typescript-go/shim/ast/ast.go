// Package ast is a pure re-export shim over the fork's internal/ast package.
// It exposes the tsgo AST surface (Node, Kind, SourceFile, Diagnostic, the
// concrete node structs, and the common node predicates) to packages outside
// this module — chiefly regel.dev/regel/internal/tsx and internal/lower — which
// cannot import internal/* across the module boundary. Zero logic lives here:
// every declaration is an alias or a func value that forwards to internal/ast.
//
// The bulk mechanical aliases live in the generated files in this directory:
//   - kind_generated_shim.go     — every ast.Kind constant
//   - nodetypes_generated_shim.go — every struct a Node.As*() accessor returns
package ast

import "github.com/microsoft/typescript-go/internal/ast"

// Core node/type surface. As*() accessors come free as methods on *Node.
type (
	Node                    = ast.Node
	NodeList                = ast.NodeList
	Kind                    = ast.Kind
	SourceFile              = ast.SourceFile
	Diagnostic              = ast.Diagnostic
	Symbol                  = ast.Symbol
	NodeFlags               = ast.NodeFlags
	ModifierFlags           = ast.ModifierFlags
	SourceFileParseOptions  = ast.SourceFileParseOptions
	ExternalModuleIndicatorOptions = ast.ExternalModuleIndicatorOptions
	Visitor                 = ast.Visitor
	Statement               = ast.Statement
	Expression              = ast.Expression
	Modifier                = ast.Modifier
	DeclarationName         = ast.DeclarationName
)

// Diagnostic ordering used by the checker; re-exported so tsx sorts identically.
var CompareDiagnostics = ast.CompareDiagnostics

// Common node predicates the lowering pass reaches for. This is not exhaustive —
// most traversal goes through Node.Kind() plus the As*() accessors — but the
// frequently-used guards are surfaced so lowering need not compare Kind by hand.
var (
	IsFunctionDeclaration  = ast.IsFunctionDeclaration
	IsVariableStatement    = ast.IsVariableStatement
	IsVariableDeclaration  = ast.IsVariableDeclaration
	IsIdentifier           = ast.IsIdentifier
	IsCallExpression       = ast.IsCallExpression
	IsPropertyAccessExpression = ast.IsPropertyAccessExpression
	IsImportDeclaration    = ast.IsImportDeclaration
	IsExportDeclaration    = ast.IsExportDeclaration
	IsStringLiteral        = ast.IsStringLiteral
	IsBlock                = ast.IsBlock
	IsArrowFunction        = ast.IsArrowFunction
	IsExpressionStatement  = ast.IsExpressionStatement
)
