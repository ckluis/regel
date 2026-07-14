package gitproj

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/pgwire"
)

// harness_test.go is the gitproj scratch-DB harness (real PG, the ADR-03 pattern):
// a bootstrapped + genesis'd database, a small app-admission helper, and the real
// git-binary oracle (test-only usage — git verifies the bare repo we build).

func baseDSN() string {
	if d := os.Getenv("REGEL_PG_TEST_DSN"); d != "" {
		return d
	}
	return "postgres://clank@localhost:5432/postgres"
}

func randName(prefix string) string {
	var b [6]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type world struct {
	t    *testing.T
	cfg  pgwire.Config
	db   string
	conn *pgwire.Conn
	im   *admission.Image
}

func setupWorld(t *testing.T) *world {
	t.Helper()
	ctx := ctxT(t)
	base, err := pgwire.ParseDSN(baseDSN())
	if err != nil {
		t.Skipf("no test PG: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Skipf("connect admin: %v", err)
	}
	defer admin.Close()

	db := randName("regel_gp_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create db: %v", err)
	}
	cfg := base
	cfg.Database = db
	conn, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	im := admission.BuildImage()
	if err := catalog.Bootstrap(ctx, conn, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := admission.Genesis(ctx, conn, im); err != nil {
		t.Fatalf("genesis: %v", err)
	}
	w := &world{t: t, cfg: cfg, db: db, conn: conn, im: im}
	t.Cleanup(func() {
		conn.Close()
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	})
	return w
}

func (w *world) count(query string, args ...any) int {
	var n int
	_, err := w.conn.QueryRow(context.Background(), query, args, &n)
	if err != nil {
		w.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func engineer(id string) admission.Principal {
	return admission.Principal{ActorKind: "engineer", ActorID: id, Via: "cli"}
}

// admitApp admits one app module through the REAL pipeline and fails the test on a
// non-green outcome. Returns the Verdict (its Hashes name each admitted def).
func (w *world) admitApp(ctx context.Context, module, source string, mut func(*admission.Patch)) admission.Verdict {
	w.t.Helper()
	p := admission.Patch{
		Modules:     []admission.ModuleSrc{{ModuleName: module, Source: source}},
		TargetScope: admission.Scope{Kind: 0, ID: ""},
		BaseHashes:  map[string]string{},
	}
	if mut != nil {
		mut(&p)
	}
	v, err := admission.Admit(ctx, w.conn, p, engineer("dev"), w.im)
	if err != nil {
		w.t.Fatalf("admit %s: %v", module, err)
	}
	if v.Outcome != admission.OutcomeAdmitted && v.Outcome != admission.OutcomeAlreadyAdmitted {
		w.t.Fatalf("admit %s: outcome %q, diags=%+v", module, v.Outcome, v.Diagnostics)
	}
	return v
}

// headHash reads the live head hash of a product-scope name.
func (w *world) headHash(ctx context.Context, name string) string {
	var h string
	ok, err := w.conn.QueryRow(ctx,
		`SELECT hash FROM name_pointer WHERE name=$1 AND scope_kind=0 AND scope_id=''`, []any{name}, &h)
	if err != nil || !ok {
		w.t.Fatalf("headHash %s: ok=%v err=%v", name, ok, err)
	}
	return h
}

// --- the real-git oracle (test-only) -----------------------------------------

// gitOracle runs `git --git-dir=<repo> <args...>` and returns combined output.
func gitOracle(t *testing.T, repoDir string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"--git-dir=" + repoDir}, args...)
	cmd := exec.Command("git", full...)
	// A hermetic environment so the oracle never reads the developer's git config.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// requireGit skips the oracle assertions if git is unavailable.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available for the oracle")
	}
}

// removeObject deletes a loose object from the store (simulating object-level
// mangle); the fold must reconstruct it.
func removeObject(t *testing.T, repo *BareRepo, oid string) {
	t.Helper()
	path := filepath.Join(repo.Dir(), "objects", oid[:2], oid[2:])
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove object %s: %v", oid, err)
	}
}

// blobOidAt returns the blob oid at a repo path on main (via the git oracle).
func blobOidAt(t *testing.T, repo *BareRepo, path string) string {
	t.Helper()
	out, err := gitOracle(t, repo.Dir(), "ls-tree", "main", path)
	if err != nil {
		t.Fatalf("ls-tree %s: %v\n%s", path, err, out)
	}
	// Format: "<mode> blob <oid>\t<path>"
	fields := strings.Fields(out)
	if len(fields) < 3 || fields[1] != "blob" {
		t.Fatalf("unexpected ls-tree output for %s: %q", path, out)
	}
	return fields[2]
}

// fsckClean asserts `git fsck --full` reports no corruption (dangling objects are
// informational and ignored).
func fsckClean(t *testing.T, repoDir string) {
	t.Helper()
	out, err := gitOracle(t, repoDir, "fsck", "--full", "--no-progress")
	if err != nil {
		t.Fatalf("git fsck failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "dangling ") || strings.HasPrefix(line, "Checking") {
			continue
		}
		t.Fatalf("git fsck reported a problem: %q\n(full output)\n%s", line, out)
	}
}
