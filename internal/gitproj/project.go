package gitproj

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"regel.dev/regel/internal/catalog"
)

// project.go is the ADR-09 §2 deterministic fold: `project(ledger prefix) → commit
// SHA`, a pure function of catalog + ledger rows and nothing fresh. Two kernels
// folding the same catalog emit byte-identical objects and SHAs; re-projecting
// from an empty store reproduces the entire history. The fold reads ONLY catalog
// rows (never the in-process image), so std/ mirror rows project through the same
// path as app/ definitions (BUILD-C item 3).

// Config parameterizes the fold. Epoch is stamped into catalog.lock (single-epoch
// at Stage C).
type Config struct {
	Epoch int
}

func (c Config) epoch() int {
	if c.Epoch == 0 {
		return 1
	}
	return c.Epoch
}

// Result is the outcome of a fold: the head commit SHA (or "" for an empty ledger)
// and the ordered list of every commit SHA produced (oldest first).
type Result struct {
	Head    string
	Commits []string
}

// ptr is one projected name pointer as of some admission.
type ptr struct {
	name       string
	hash       string
	visibility string
}

// defRow is the immortal content a projected file body needs.
type defRow struct {
	kind          string
	canonicalText string
	docstring     string
}

// Fold folds the whole admission ledger into store and returns the head SHA and
// the commit list. It emits one commit per PROJECTING admission row — an admission
// that moves a product/package/std pointer (BUILD-C item 1); overlay-only and
// no-op admission rows change no projected byte and contribute no commit.
func Fold(ctx context.Context, q catalog.Querier, store objectStore, cfg Config) (Result, error) {
	defs, err := loadDefs(ctx, q)
	if err != nil {
		return Result{}, err
	}
	byAdm, err := loadProjectedHistory(ctx, q)
	if err != nil {
		return Result{}, err
	}
	admins, err := loadAdmissions(ctx, q)
	if err != nil {
		return Result{}, err
	}

	state := map[string]ptr{} // name → live projected pointer
	var res Result
	parent := ""
	var prevTree string

	for _, adm := range admins {
		moves := byAdm[adm.id]
		if len(moves) == 0 {
			continue // this admission touched no projected pointer (overlay / no-op)
		}
		// Apply this admission's pointer moves (id order ⇒ later windows win).
		for _, m := range moves {
			state[m.name] = m
		}
		tree, err := buildTree(store, state, defs, cfg.epoch())
		if err != nil {
			return Result{}, err
		}
		if tree == prevTree && parent != "" {
			// No projected change (e.g. a move that re-set an identical hash). The
			// ledger still audits it; the code history does not fork.
			continue
		}
		commit, err := writeCommit(store, commitSpec{
			tree:      tree,
			parent:    parent,
			author:    authorIdent(adm),
			committer: projectorIdent(adm),
			message:   commitMessage(adm, moves),
		})
		if err != nil {
			return Result{}, err
		}
		res.Commits = append(res.Commits, commit)
		parent = commit
		prevTree = tree
	}
	res.Head = parent
	return res, nil
}

// buildTree materializes the projected file set for a catalog state and writes the
// tree (with all its blobs + subtrees), returning the root tree oid.
func buildTree(store objectStore, state map[string]ptr, defs map[string]defRow, epoch int) (string, error) {
	root := newTreeNode()
	// .regel/catalog.lock — the sorted parity manifest name → (hash, kind, epoch).
	lockBlob, err := writeBlob(store, catalogLock(state, defs, epoch))
	if err != nil {
		return "", err
	}
	root.insert(".regel/catalog.lock", lockBlob)

	for name, p := range state {
		d, ok := defs[p.hash]
		if !ok {
			return "", fmt.Errorf("gitproj: projected name %q references unknown hash %q", name, p.hash)
		}
		blob, err := writeBlob(store, renderFile(d.docstring, d.canonicalText))
		if err != nil {
			return "", err
		}
		root.insert(catalog.NamePath(name), blob)
	}
	return root.write(store)
}

// catalogLock renders the deterministic parity manifest: one sorted line per
// projected name, `name<TAB>hash<TAB>kind<TAB>epoch`. Any checkout can verify its
// file bytes against these hashes offline; carries names + hashes only (ADR-09 §5).
func catalogLock(state map[string]ptr, defs map[string]defRow, epoch int) []byte {
	lines := make([]string, 0, len(state))
	for name, p := range state {
		kind := defs[p.hash].kind
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%d", name, p.hash, kind, epoch))
	}
	sort.Strings(lines)
	return []byte(strings.Join(lines, "\n") + "\n")
}

// commitMessage derives a commit message purely from ledger data (BUILD-C item 1).
func commitMessage(adm admissionRow, moves []ptr) string {
	sorted := append([]ptr(nil), moves...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	var b strings.Builder
	fmt.Fprintf(&b, "regel admission #%d (via %s)\n\n", adm.id, adm.via)
	for _, m := range sorted {
		fmt.Fprintf(&b, "%s %s\n", m.name, m.hash)
	}
	return b.String()
}

// authorIdent is the admission principal as a stable synthetic git identity
// (ADR-09 §2): name "<actor_kind>:<actor_id>", email "<actor_id>@regel", timestamp
// pinned to the admission row's created_at — never projection wall clock.
func authorIdent(adm admissionRow) ident {
	return ident{
		name:  adm.actorKind + ":" + adm.actorID,
		email: adm.actorID + "@regel",
		unix:  adm.createdUnix,
		tz:    "+0000",
	}
}

// projectorIdent is the fixed committer identity, timestamp pinned identically.
func projectorIdent(adm admissionRow) ident {
	return ident{name: "regel-projector", email: "projector@regel", unix: adm.createdUnix, tz: "+0000"}
}
