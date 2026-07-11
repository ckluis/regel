// Package locale re-exports the default locale used to render diagnostic
// messages deterministically. Pure re-export.
package locale

import "github.com/microsoft/typescript-go/internal/locale"

type Locale = locale.Locale

// Default is the locale tsx uses to localize diagnostic messages; using a fixed
// locale keeps message text a pure function of the diagnostic (hermeticity).
var Default = locale.Default
