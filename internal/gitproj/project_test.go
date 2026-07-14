package gitproj

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// --- shared app fixtures (pure functions; import only std or nothing) ----------

const incSrc = `export function inc(x: number): number {
  return (x + 1);
}
`

const dblSrc = `export function dbl(x: number): number {
  return (x * 2);
}
`

const greetSrc = `export function greet(name: string): string {
  return name;
}
`

// seedLedger admits a few app modules so the fold has real history beyond genesis.
func seedLedger(ctx context.Context, w *world) {
	w.admitApp(ctx, "app/math", incSrc, nil)
	w.admitApp(ctx, "app/math2", dblSrc, nil)
	w.admitApp(ctx, "app/text", greetSrc, nil)
}

// --- Red-path: byte-identical SHAs + determinism release gate ------------------

// TestDeterminismReleaseGate folds the SAME ledger range twice with independent
// fresh stores (two isolated in-process folds — the "two machines" of ADR-09 §2)
// and asserts byte-identical commit SHAs over the FULL history. It PRINTS the SHAs
// (Stage-C gate evidence).
func TestDeterminismReleaseGate(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	seedLedger(ctx, w)

	r1, err := Fold(ctx, w.conn, newMemStore(), Config{})
	if err != nil {
		t.Fatalf("fold A: %v", err)
	}
	r2, err := Fold(ctx, w.conn, newMemStore(), Config{})
	if err != nil {
		t.Fatalf("fold B: %v", err)
	}

	if r1.Head == "" {
		t.Fatal("empty projection head — the fold produced no commit")
	}
	if r1.Head != r2.Head {
		t.Fatalf("DETERMINISM VIOLATION: heads differ\n  A=%s\n  B=%s", r1.Head, r2.Head)
	}
	if len(r1.Commits) != len(r2.Commits) {
		t.Fatalf("commit count differs: %d vs %d", len(r1.Commits), len(r2.Commits))
	}
	for i := range r1.Commits {
		if r1.Commits[i] != r2.Commits[i] {
			t.Fatalf("commit %d differs: %s vs %s", i, r1.Commits[i], r2.Commits[i])
		}
	}

	t.Logf("two-fold determinism: %d commits, IDENTICAL head SHA on both machines", len(r1.Commits))
	t.Logf("  machine A head SHA = %s", r1.Head)
	t.Logf("  machine B head SHA = %s", r2.Head)
	for i, c := range r1.Commits {
		t.Logf("  commit[%d] = %s", i, c)
	}

	// Re-folding from an empty store reproduces the WHOLE history byte-identically.
	r3, err := Fold(ctx, w.conn, newMemStore(), Config{})
	if err != nil {
		t.Fatalf("fold C: %v", err)
	}
	if r3.Head != r1.Head {
		t.Fatalf("re-projection from scratch diverged: %s vs %s", r3.Head, r1.Head)
	}
}

// TestGitOracleVerifiesRepo folds into a real bare repo and verifies it with the
// stock git binary (test-only oracle): git fsck is clean and git log parses the
// full history. Byte-identical SHAs means git object ids any client can check, so
// the filesystem fold must match the in-memory fold exactly.
func TestGitOracleVerifiesRepo(t *testing.T) {
	requireGit(t)
	w := setupWorld(t)
	ctx := ctxT(t)
	seedLedger(ctx, w)

	mem, err := Fold(ctx, w.conn, newMemStore(), Config{})
	if err != nil {
		t.Fatalf("mem fold: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "mirror.git")
	repo, err := OpenBare(dir)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	fs, err := Fold(ctx, w.conn, repo, Config{})
	if err != nil {
		t.Fatalf("fs fold: %v", err)
	}
	if fs.Head != mem.Head {
		t.Fatalf("filesystem fold head %s != memory fold head %s", fs.Head, mem.Head)
	}
	if err := repo.setMain(fs.Head); err != nil {
		t.Fatalf("set main: %v", err)
	}

	fsckClean(t, repo.Dir())

	out, err := gitOracle(t, repo.Dir(), "log", "--format=%H", "main")
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	logged := nonEmptyLines(out)
	if len(logged) != len(fs.Commits) {
		t.Fatalf("git log saw %d commits, fold produced %d\n%s", len(logged), len(fs.Commits), out)
	}
	// git log lists newest-first; our commit list is oldest-first.
	if logged[0] != fs.Head {
		t.Fatalf("git log head %s != fold head %s", logged[0], fs.Head)
	}
	t.Logf("git fsck clean; git log parsed %d commits; head %s", len(logged), fs.Head)

	// The projected tree carries the app files at their name→path locations.
	tree, err := gitOracle(t, repo.Dir(), "ls-tree", "-r", "--name-only", "main")
	if err != nil {
		t.Fatalf("git ls-tree: %v\n%s", err, tree)
	}
	for _, want := range []string{"app/math/inc.ts", "app/math2/dbl.ts", "app/text/greet.ts", ".regel/catalog.lock", "std/mail/send.ts"} {
		if !strings.Contains(tree, want) {
			t.Fatalf("projected tree missing %q\n%s", want, tree)
		}
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}
