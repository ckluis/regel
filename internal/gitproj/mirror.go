package gitproj

import (
	"context"
	"encoding/json"

	"regel.dev/regel/internal/catalog"
)

// mirror.go is the ADR-09 §3 stored mirror with self-healing restore. The
// projection is COMPUTED (a pure fold; no writable repo state can drift from the
// image) but SERVED from a stored bare repo. On every projection the mirror's main
// SHA is compared to the computed head; a mismatch that is not a legitimate prior
// projection (a force-push mangle) is force-restored from the recomputed fold and
// audited. The mirror is a cache; the image is truth; "the repo lags or lies" has
// no durable representation beyond one projection_audit row.

// Mirror couples a bare repo to the fold. Its Advance is wired BOTH into the kernel
// serve path (post-admission hook) and the `regel project` CLI command.
type Mirror struct {
	repo *BareRepo
	cfg  Config
}

// NewMirror opens (creating if absent) the bare mirror at dir.
func NewMirror(dir string, cfg Config) (*Mirror, error) {
	repo, err := OpenBare(dir)
	if err != nil {
		return nil, err
	}
	return &Mirror{repo: repo, cfg: cfg}, nil
}

// Repo exposes the underlying bare repo (the --git-dir for a git client / oracle).
func (m *Mirror) Repo() *BareRepo { return m.repo }

// Advance folds the ledger, self-heals the object store + main ref, and returns the
// computed head. It is idempotent and safe to call after every admission or on
// demand: an unchanged ledger reproduces the same head and moves nothing.
//
// The fold ALWAYS rewrites every reachable object (put is idempotent and recreates
// any missing/deleted object), so the object store self-heals by reconstruction.
// The main ref is then reconciled:
//   - main == head            → up to date, nothing written;
//   - main == "" or a prior   → a normal fast-forward advance (no audit);
//     projected commit
//   - main == a foreign SHA   → a force-push mangle: force-restore + audit row.
func (m *Mirror) Advance(ctx context.Context, q catalog.Querier) (string, error) {
	res, err := Fold(ctx, q, m.repo, m.cfg)
	if err != nil {
		return "", err
	}
	cur, err := m.repo.readMain()
	if err != nil {
		return "", err
	}
	if cur == res.Head {
		return res.Head, nil // mirror already agrees with the image
	}
	// BUILD-C RED (increment C6): a normal fast-forward from a known prior
	// projection advances main. The SELF-HEAL of a foreign (force-push-mangled) ref
	// and its projection_audit row are NOT yet built — a mangled main is left as-is.
	if cur == "" || inCommits(cur, res.Commits) {
		if err := m.repo.setMain(res.Head); err != nil {
			return "", err
		}
	}
	_ = q
	return res.Head, nil
}

// inCommits reports whether sha is one of the commits the current fold produced —
// i.e., a legitimate prior projection the mirror is fast-forwarding from.
func inCommits(sha string, commits []string) bool {
	for _, c := range commits {
		if c == sha {
			return true
		}
	}
	return false
}

// writeAudit appends one projection_audit row (ADR-03 §1 table 10 / ADR-09 §3).
func writeAudit(ctx context.Context, q catalog.Querier, event string, detail map[string]string) error {
	blob, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx,
		`INSERT INTO projection_audit (event, detail) VALUES ($1, $2::jsonb)`,
		event, string(blob))
	return err
}
