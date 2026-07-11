// Package vfs re-exports the tsgo virtual-filesystem interface and the
// in-memory FromMap constructor tsx uses to back the hermetic module host over
// exactly the L0/L1/L2 world map — no disk, no clock beyond file mtimes derived
// from the map. Pure re-export.
package vfs

import (
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

type (
	FS      = vfs.FS
	Entries = vfs.Entries
)

// FromMap builds an in-memory vfs.FS from a map of normalized absolute path →
// file contents (string). Paths must be rooted and normalized (e.g.
// "/app/crm/deal.ts"). This is the only filesystem tsx ever hands the checker.
func FromMap(m map[string]string, useCaseSensitiveFileNames bool) FS {
	return vfstest.FromMap(m, useCaseSensitiveFileNames)
}
