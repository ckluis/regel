// Pure re-exports of the modifier/function-flag surface the lowering pass reads,
// added for regel.dev/regel/internal/lower. Zero logic: every declaration is an
// alias or a func value forwarding to internal/ast. (The common ModifierFlags /
// NodeFlags constants live in ast.go; this file adds only the extras the grammar
// gate needs — generator detection, the remaining member modifiers, and the
// numeric-literal radix token flags.)

package ast

import "github.com/microsoft/typescript-go/internal/ast"

// FunctionFlags and GetFunctionFlags detect async / generator function-likes.
type FunctionFlags = ast.FunctionFlags

const (
	FunctionFlagsNormal         = ast.FunctionFlagsNormal
	FunctionFlagsGenerator      = ast.FunctionFlagsGenerator
	FunctionFlagsAsync          = ast.FunctionFlagsAsync
	FunctionFlagsInvalid        = ast.FunctionFlagsInvalid
	FunctionFlagsAsyncGenerator = ast.FunctionFlagsAsyncGenerator
)

var GetFunctionFlags = ast.GetFunctionFlags

// HasSyntacticModifier reports a syntactic modifier (export/async/readonly/…).
var HasSyntacticModifier = ast.HasSyntacticModifier

// Remaining member modifiers the gate compares against (class accessors, static,
// abstract, accessibility) beyond those already in ast.go.
const (
	ModifierFlagsStatic    = ast.ModifierFlagsStatic
	ModifierFlagsAccessor  = ast.ModifierFlagsAccessor
	ModifierFlagsAbstract  = ast.ModifierFlagsAbstract
	ModifierFlagsPublic    = ast.ModifierFlagsPublic
	ModifierFlagsPrivate   = ast.ModifierFlagsPrivate
	ModifierFlagsProtected = ast.ModifierFlagsProtected
)

// TokenFlags surface: the numeric-literal radix bits lowering reads to
// reconstruct canonical scalar values.
type TokenFlags = ast.TokenFlags

const (
	TokenFlagsHexSpecifier      = ast.TokenFlagsHexSpecifier
	TokenFlagsBinarySpecifier   = ast.TokenFlagsBinarySpecifier
	TokenFlagsOctalSpecifier    = ast.TokenFlagsOctalSpecifier
	TokenFlagsContainsSeparator = ast.TokenFlagsContainsSeparator
	TokenFlagsScientific        = ast.TokenFlagsScientific
)

// A few more node predicates the gate reaches for.
var (
	IsBindingElement       = ast.IsBindingElement
	IsComputedPropertyName = ast.IsComputedPropertyName
	IsTypeReferenceNode    = ast.IsTypeReferenceNode
	IsToken                = ast.IsToken
)
