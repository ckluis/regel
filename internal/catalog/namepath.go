package catalog

import "strings"

// namepath.go is the ONE name→path function (ADR-07 §2 / ADR-09 §1): a total,
// injective map from a catalog name to its repo-relative file path. It has two
// consumers and must live in exactly one place so they can never disagree about
// layout — the ADR-07 tsgo module host (internal/admission buildTypecheckWorld)
// and the ADR-09 git projector. The host prepends "/" to address its in-memory
// world; the projector uses the path repo-relative under the tree root.
//
// A definition's catalog name is "<module>/<Name>" (e.g. "app/crm/deal/total" or
// "std/mail/send"); its path is that name plus the ".ts" extension.

const tsExt = ".ts"

// NamePath maps a catalog name to its repo-relative projection path, e.g.
// "app/crm/deal/total" → "app/crm/deal/total.ts". Total and injective.
func NamePath(name string) string { return name + tsExt }

// NameFromPath is the inverse: it recovers the catalog name from a projection
// path (a leading "/" is tolerated so the tsgo world's "/name.ts" round-trips).
// ok is false for any path that is not a projected definition file.
func NameFromPath(path string) (name string, ok bool) {
	p := strings.TrimPrefix(path, "/")
	if !strings.HasSuffix(p, tsExt) {
		return "", false
	}
	return strings.TrimSuffix(p, tsExt), true
}
