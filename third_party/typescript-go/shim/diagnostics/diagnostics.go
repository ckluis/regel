// Package diagnostics re-exports the diagnostic Category enum so tsx can label
// a diagnostic's severity without importing internal/*. Pure re-export.
package diagnostics

import "github.com/microsoft/typescript-go/internal/diagnostics"

type Category = diagnostics.Category

const (
	CategoryWarning    = diagnostics.CategoryWarning
	CategoryError      = diagnostics.CategoryError
	CategorySuggestion = diagnostics.CategorySuggestion
	CategoryMessage    = diagnostics.CategoryMessage
)
