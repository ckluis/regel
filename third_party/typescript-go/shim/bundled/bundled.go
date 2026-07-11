// Package bundled re-exports the embedded lib.d.ts overlay. WrapFS overlays the
// bundled libs onto an in-memory FS at LibPath(); passing LibPath() as the
// CompilerHost's DefaultLibraryPath lets the checker load the standard library
// entirely from the embedded FS — no disk. Pure re-export.
package bundled

import "github.com/microsoft/typescript-go/internal/bundled"

// WrapFS overlays the embedded lib.d.ts files onto the given FS.
var WrapFS = bundled.WrapFS

// LibPath returns the embedded-scheme directory holding the bundled lib files.
var LibPath = bundled.LibPath
