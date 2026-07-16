package admission

import (
	"context"
	"errors"
	"strings"
	"testing"

	"regel.dev/regel/internal/pgwire"
)

var errSeededKill = errors.New("seeded mid-commit kill")

// setupMigrateDB gives a bootstrapped + genesis'd scratch DB with one admitted
// app definition, ready for the epoch-migration + canary red-paths.
func setupMigrateDB(t *testing.T) (context.Context, *migrateEnv, func()) {
	t.Helper()
	conn, cleanup := newScratchDB(t)
	ctx := ctxT(t)
	if err := Genesis(ctx, conn, BuildImage()); err != nil {
		cleanup()
		t.Fatalf("genesis: %v", err)
	}
	v, err := admit(ctx, conn, `export function w(): number { return 42; }`, "app/t", engineer("dev"), nil)
	if err != nil || v.Outcome != OutcomeAdmitted {
		cleanup()
		t.Fatalf("admit: %v (%q)", err, v.Outcome)
	}
	return ctx, &migrateEnv{conn: conn, wHash: v.Hashes["app/t/w"]}, cleanup
}

type migrateEnv struct {
	conn  *pgwire.Conn
	wHash string
}

func (e *migrateEnv) epochNow(t *testing.T) int {
	t.Helper()
	var n int
	if _, err := e.conn.QueryRow(context.Background(), `SELECT n FROM epoch_current WHERE one=true`, nil, &n); err != nil {
		t.Fatalf("epochNow: %v", err)
	}
	return n
}

func (e *migrateEnv) epochRowExists(t *testing.T, n int) bool {
	t.Helper()
	var one int
	found, err := e.conn.QueryRow(context.Background(), `SELECT 1 FROM epoch WHERE n=$1`, []any{n}, &one)
	if err != nil {
		t.Fatalf("epochRowExists: %v", err)
	}
	return found
}

// TestMigrateCommitAtomicityKill is ADR-08 red-path "commit atomicity": a kill
// mid `--commit` leaves the epoch row, manifest, and flip ALL absent — the fleet
// never observes a half-epoch. The fault seam aborts after the epoch row +
// std_manifest are inserted but before the fence flips; the deferred rollback
// must undo everything.
func TestMigrateCommitAtomicityKill(t *testing.T) {
	ctx, e, cleanup := setupMigrateDB(t)
	defer cleanup()

	migrateFaultHook = func() error { return errSeededKill }
	defer func() { migrateFaultHook = nil }()

	err := MigrateCommit(ctx, e.conn, 2, nil)
	if err == nil {
		t.Fatal("expected the seeded mid-commit kill to surface")
	}
	// ALL-OR-NOTHING: no epoch-2 row, no std_manifest for 2, fence still on 1.
	if e.epochRowExists(t, 2) {
		t.Fatal("epoch 2 row present after a killed commit — half-epoch leaked")
	}
	if n := e.epochNow(t); n != 1 {
		t.Fatalf("epoch_current = %d after killed commit, want 1 (untouched)", n)
	}
	var man int
	if _, err := e.conn.QueryRow(ctx, `SELECT count(*) FROM std_manifest WHERE epoch=2`, nil, &man); err != nil {
		t.Fatal(err)
	}
	if man != 0 {
		t.Fatalf("std_manifest for epoch 2 has %d rows after killed commit, want 0", man)
	}

	// The retry (fault cleared) lands cleanly — atomicity did not corrupt state.
	migrateFaultHook = nil
	if err := MigrateCommit(ctx, e.conn, 2, nil); err != nil {
		t.Fatalf("clean re-commit after killed attempt: %v", err)
	}
	if n := e.epochNow(t); n != 2 {
		t.Fatalf("epoch_current = %d after clean commit, want 2", n)
	}
}

// TestWorldRehashCanaryGreenThenTamper is Deliverable 5's red-path: the canary is
// GREEN on a real corpus (both legs), and a TAMPERED stored AST row makes it
// SCREAM — a non-empty finding on the encoder leg naming the address.
func TestWorldRehashCanaryGreenThenTamper(t *testing.T) {
	ctx, e, cleanup := setupMigrateDB(t)
	defer cleanup()

	// GREEN: every stored def rehashes to its address, and the app def re-lowers.
	findings, err := WorldRehashCanary(ctx, e.conn, BuildImage())
	if err != nil {
		t.Fatalf("canary: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("canary not green on a clean corpus: %+v", findings)
	}

	// TAMPER: flip a byte of the app def's stored AST. (The scratch DB owner
	// retains UPDATE; the immortal-store REVOKE is from the kernel role, not owner.)
	if _, err := e.conn.Exec(ctx,
		`UPDATE definition SET ast = set_byte(ast, 1, (get_byte(ast,1) # 255)) WHERE hash=$1`, e.wHash); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	findings, err = WorldRehashCanary(ctx, e.conn, BuildImage())
	if err != nil {
		t.Fatalf("canary post-tamper: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("canary stayed silent on a tampered AST — the scrubber did not trip")
	}
	var hit bool
	for _, f := range findings {
		if f.Hash == e.wHash && f.Leg == "encoder" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("canary did not name the tampered address on the encoder leg: %+v", findings)
	}
	if !strings.HasPrefix(e.wHash, "r1_") {
		t.Fatalf("unexpected hash form %q", e.wHash)
	}
}
