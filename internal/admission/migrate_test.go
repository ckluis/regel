package admission

import (
	"context"
	"errors"
	"strings"
	"testing"

	"regel.dev/regel/internal/catalog"
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

// TestOverlayScopeCanaryReLower is the BUILD-F R8 discharge: the pipeline leg
// re-lowers OVERLAY-scoped app defs, not just product-scope ones. It witnesses the
// pre-fix BLINDNESS first (the old scope-filtered query never inspects the overlay
// def), then proves the extended leg CATCHES a canonical_text drift on that overlay
// def that the encoder leg is structurally blind to — all on real PG through the
// same WorldRehashCanary door the CLI drives.
func TestOverlayScopeCanaryReLower(t *testing.T) {
	ctx, e, cleanup := setupMigrateDB(t)
	defer cleanup()

	// Admit an OVERLAY-scoped def: an agent self-serving a def in its own sandbox
	// org scope (scope_kind=2, scope_id="org1"). This def is reachable ONLY through
	// the overlay pointer — no product pointer references its hash.
	ov, err := admit(ctx, e.conn,
		`export function ov(): number { return 99; }`, "app/ov",
		agent("a1", catalog.Chain{OrgID: "org1"}),
		func(p *Patch) { p.TargetScope = Scope{Kind: 2, ID: "org1"} })
	if err != nil || ov.Outcome != OutcomeAdmitted {
		t.Fatalf("admit overlay def: %v (%q) diags=%+v", err, ov.Outcome, ov.Diagnostics)
	}
	ovHash := ov.Hashes["app/ov/ov"]
	if ovHash == "" {
		t.Fatalf("overlay def hash missing: %+v", ov.Hashes)
	}
	// Confirm it really landed at overlay scope, not product.
	var sk int
	var sid string
	if _, err := e.conn.QueryRow(ctx,
		`SELECT scope_kind, scope_id FROM name_pointer WHERE name='app/ov/ov'`, nil, &sk, &sid); err != nil {
		t.Fatalf("read overlay pointer: %v", err)
	}
	if sk != 2 || sid != "org1" {
		t.Fatalf("overlay pointer at scope %d:%s, want 2:org1", sk, sid)
	}

	// GREEN over the healthy overlay state (both legs).
	if findings, err := WorldRehashCanary(ctx, e.conn, BuildImage()); err != nil {
		t.Fatalf("canary: %v", err)
	} else if len(findings) != 0 {
		t.Fatalf("canary not green on a clean overlay corpus: %+v", findings)
	}

	// BLINDNESS PROOF (permanent): the PRE-FIX pipeline-leg query filtered
	// scope_kind=0 AND scope_id='' — so it never even SELECTs the overlay def. The
	// overlay hash is absent from the old set (while the product def IS present),
	// so the old leg was structurally blind to any overlay drift.
	oldRows, err := e.conn.Query(ctx, `
SELECT d.hash FROM name_pointer p JOIN definition d ON d.hash = p.hash
 WHERE p.scope_kind = 0 AND p.scope_id = '' AND p.name NOT LIKE 'std/%'`)
	if err != nil {
		t.Fatalf("old-shape query: %v", err)
	}
	oldSet := map[string]bool{}
	for oldRows.Next() {
		var h string
		if err := oldRows.Scan(&h); err != nil {
			oldRows.Close()
			t.Fatalf("scan old set: %v", err)
		}
		oldSet[h] = true
	}
	if err := oldRows.Err(); err != nil {
		t.Fatalf("old-shape rows: %v", err)
	}
	if oldSet[ovHash] {
		t.Fatal("overlay def is in the OLD product-only pipeline set — blindness premise false")
	}
	if !oldSet[e.wHash] {
		t.Fatal("product def missing from the OLD pipeline set — sanity check failed")
	}

	// TAMPER the overlay def's canonical_text ONLY (leave the stored AST intact):
	// the text now re-lowers to a DIFFERENT address. The encoder leg (AST→hash) is
	// structurally blind to this — only the pipeline leg (canonical_text→hash) sees
	// it. This is the exact text↔AST seam drift ADR-02 §5 names as the #1 risk.
	if _, err := e.conn.Exec(ctx,
		`UPDATE definition SET canonical_text = $2 WHERE hash = $1`,
		ovHash, `export function ov(): number { return 7; }`); err != nil {
		t.Fatalf("tamper canonical_text: %v", err)
	}

	// CAUGHT: the extended pipeline leg now re-lowers the overlay def and the
	// scrubber trips — a finding naming the overlay hash, the pipeline leg, and the
	// overlay scope. The encoder leg stays silent (the AST is genuinely unchanged),
	// so the catch is attributable SOLELY to the new overlay pipeline coverage.
	findings, err := WorldRehashCanary(ctx, e.conn, BuildImage())
	if err != nil {
		t.Fatalf("canary post-tamper: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("canary stayed silent on a tampered overlay canonical_text — R8 blindness NOT closed")
	}
	var pipelineHit, encoderHit bool
	for _, f := range findings {
		if f.Hash == ovHash && f.Leg == "pipeline" {
			pipelineHit = true
			if f.Scope != "2:org1" {
				t.Fatalf("pipeline finding names scope %q, want 2:org1", f.Scope)
			}
		}
		if f.Hash == ovHash && f.Leg == "encoder" {
			encoderHit = true
		}
	}
	if !pipelineHit {
		t.Fatalf("canary did not catch the overlay def on the pipeline leg: %+v", findings)
	}
	if encoderHit {
		t.Fatalf("encoder leg fired on an untampered AST — the catch must be the pipeline leg alone: %+v", findings)
	}

	// HEAL: restore the true canonical_text; the canary returns to green.
	if _, err := e.conn.Exec(ctx,
		`UPDATE definition SET canonical_text = $2 WHERE hash = $1`,
		ovHash, `export function ov(): number { return 99; }`); err != nil {
		t.Fatalf("heal canonical_text: %v", err)
	}
	if findings, err := WorldRehashCanary(ctx, e.conn, BuildImage()); err != nil {
		t.Fatalf("canary post-heal: %v", err)
	} else if len(findings) != 0 {
		t.Fatalf("canary not green after healing the overlay def: %+v", findings)
	}
}
