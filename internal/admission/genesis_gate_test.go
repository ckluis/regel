package admission

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
)

// genesis_gate_test.go is the BUILD-D D0 genesis gate battery (ADR-10 §2 +
// Red-Path list): two-fresh-DB reproducibility (a), mid-genesis kill ⇒
// empty-or-complete (b), dispatch-bijection boot refusal (c), H_dispatch
// attestation recompute-and-refuse (d), and NativeBody unwritability (e).

// newScratchDB creates a fresh scratch database and applies the substrate DDL
// (catalog.Bootstrap) but does NOT run genesis — the test drives Genesis itself,
// with fault injection where needed.
func newScratchDB(t *testing.T) (*pgwire.Conn, func()) {
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
	db := randName("regel_gate_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create db: %v", err)
	}
	cfg := base
	cfg.Database = db
	conn, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	if err := catalog.Bootstrap(ctx, conn, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	cleanup := func() {
		conn.Close()
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	}
	return conn, cleanup
}

// definitionSnapshot reads the whole definition table as an ordered (hash, ast)
// digest — the §2 kill-test's "SELECT hash, ast FROM definition ORDER BY hash".
func definitionSnapshot(t *testing.T, conn *pgwire.Conn) string {
	t.Helper()
	ctx := ctxT(t)
	rows, err := conn.Query(ctx, `SELECT hash, encode(ast,'hex') FROM definition ORDER BY hash`)
	if err != nil {
		t.Fatalf("snapshot query: %v", err)
	}
	var out string
	for rows.Next() {
		var hash, ast string
		if err := rows.Scan(&hash, &ast); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		out += hash + "|" + ast + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// pointerSnapshot reads the ordered std/ name_pointer set (name=hash), the second
// leg of the §2 parity (definitions + name_pointer + std_manifest root).
func pointerSnapshot(t *testing.T, conn *pgwire.Conn) string {
	t.Helper()
	ctx := ctxT(t)
	rows, err := conn.Query(ctx,
		`SELECT name, hash FROM name_pointer WHERE scope_kind=0 AND scope_id='' ORDER BY name`)
	if err != nil {
		t.Fatalf("pointer query: %v", err)
	}
	var out string
	for rows.Next() {
		var name, hash string
		if err := rows.Scan(&name, &hash); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		out += name + "=" + hash + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// --- (a) two-fresh-DB reproducibility (ADR-10 §2 kill-test) -------------------

// TestGateA_TwoFreshDBReproducibility boots the SAME image against two fresh
// databases and asserts the definition rows (hash, ast), the std name_pointer set,
// and the std_manifest root are byte-identical across both — two fresh databases
// cannot disagree unless the binaries differ (ADR-10 §1 KT-A1).
//
// The projected std/ tree comparison (ADR-09) is NOT re-run here: it is already a
// dedicated release gate — internal/gitproj's two-fold determinism test builds two
// independent stores and asserts byte-identical commit SHAs over the full history
// (gitproj/project_test.go). This test covers the catalog-row + manifest-root leg;
// the projection leg is covered there.
func TestGateA_TwoFreshDBReproducibility(t *testing.T) {
	ctx := ctxT(t)
	im := BuildImage()

	connA, cleanA := newScratchDB(t)
	defer cleanA()
	connB, cleanB := newScratchDB(t)
	defer cleanB()

	if err := Genesis(ctx, connA, im); err != nil {
		t.Fatalf("genesis A: %v", err)
	}
	if err := Genesis(ctx, connB, im); err != nil {
		t.Fatalf("genesis B: %v", err)
	}

	if snapA, snapB := definitionSnapshot(t, connA), definitionSnapshot(t, connB); snapA != snapB {
		t.Fatalf("definition (hash, ast) not byte-identical across two fresh DBs")
	}
	if pa, pb := pointerSnapshot(t, connA), pointerSnapshot(t, connB); pa != pb {
		t.Fatalf("std name_pointer set not byte-identical across two fresh DBs")
	}
	var rootA, rootB string
	if _, err := connA.QueryRow(ctx, `SELECT std_manifest_root FROM epoch WHERE n=1`, nil, &rootA); err != nil {
		t.Fatal(err)
	}
	if _, err := connB.QueryRow(ctx, `SELECT std_manifest_root FROM epoch WHERE n=1`, nil, &rootB); err != nil {
		t.Fatal(err)
	}
	if rootA != rootB || rootA != im.ManifestRoot {
		t.Fatalf("std_manifest_root diverged: A=%s B=%s binary=%s", rootA, rootB, im.ManifestRoot)
	}
	t.Logf("two-fresh-DB parity: %d entries, IDENTICAL (hash,ast)+pointer+manifest-root on both", len(im.Entries))
}

// --- (b) mid-genesis kill ⇒ empty-or-complete (ADR-10 §2) ---------------------

// TestGateB_MidGenesisKillEmptyOrComplete aborts genesis at EVERY statement
// boundary (via the genesisFaultHook seam) and asserts the catalog is empty after
// each abort — never partial — then completes on a clean retry. One scratch DB
// suffices: every aborted genesis rolls its serializable transaction back to
// empty, so the next boundary retries from the same empty state (the §2
// "all-or-nothing: a crash mid-genesis leaves an empty catalog and the next boot
// retries identically"). The sweep terminates when n exceeds the boundary count
// (genesis completes), which is the terminal empty-or-COMPLETE assertion.
func TestGateB_MidGenesisKillEmptyOrComplete(t *testing.T) {
	ctx := ctxT(t)
	im := BuildImage()
	conn, cleanup := newScratchDB(t)
	defer cleanup()
	defer func() { genesisFaultHook = nil }()

	const maxN = 100000 // guard against a hook-placement bug looping forever
	boundaries := 0
	for n := 1; n <= maxN; n++ {
		calls := 0
		genesisFaultHook = func() error {
			calls++
			if calls == n {
				return fmt.Errorf("injected mid-genesis fault at boundary %d", n)
			}
			return nil
		}
		err := Genesis(ctx, conn, im)
		if err != nil {
			// Aborted mid-genesis: the catalog must be EMPTY (zero trace).
			if got := countConn(t, conn, "SELECT count(*) FROM definition"); got != 0 {
				t.Fatalf("boundary %d: %d definitions after abort, want 0 (partial catalog!)", n, got)
			}
			if got := countConn(t, conn, "SELECT count(*) FROM epoch"); got != 0 {
				t.Fatalf("boundary %d: epoch row exists after abort, want none", n)
			}
			boundaries++
			continue
		}
		// n exceeded the boundary count: genesis COMPLETED. Assert complete. The
		// definition table holds one row per DISTINCT hash (the type entries share
		// the opaque `unknown` body, so they collapse to one row); name_pointer
		// holds one row per entry; VerifyBoot is the standing completeness proof.
		genesisFaultHook = nil
		distinct := map[string]bool{}
		for _, e := range im.Entries {
			distinct[e.Hash] = true
		}
		if got := countConn(t, conn, "SELECT count(*) FROM definition"); got != len(distinct) {
			t.Fatalf("clean retry: %d definitions, want %d distinct hashes (incomplete)", got, len(distinct))
		}
		if got := countConn(t, conn, "SELECT count(*) FROM name_pointer WHERE scope_kind=0 AND scope_id='' AND name LIKE 'std/%'"); got != len(im.Entries) {
			t.Fatalf("clean retry: %d std pointers, want %d (incomplete)", got, len(im.Entries))
		}
		if got := countConn(t, conn, "SELECT count(*) FROM epoch WHERE n=1"); got != 1 {
			t.Fatalf("clean retry: epoch row count %d, want 1", got)
		}
		if err := VerifyBoot(ctx, conn, im); err != nil {
			t.Fatalf("clean retry VerifyBoot: %v", err)
		}
		t.Logf("mid-genesis kill: %d statement boundaries swept, all empty-or-complete; retry completed", boundaries)
		return
	}
	t.Fatalf("genesis never completed within %d boundaries (hook placement bug?)", maxN)
}

func countConn(t *testing.T, conn *pgwire.Conn, q string) int {
	t.Helper()
	var n int
	if _, err := conn.QueryRow(context.Background(), q, nil, &n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// --- (c) dispatch bijection boot refusal (ADR-10 §2 step 3) -------------------

// TestGateC_DispatchBijectionBootRefusal proves both legs of the dispatch
// bijection refuse boot: a catalogued native with no registered Go body (strip an
// implementation ⇒ orphan hash), and a registered body with no catalogued entry
// (an extra implementation ⇒ orphan implementation). Each names the orphan hash.
func TestGateC_DispatchBijectionBootRefusal(t *testing.T) {
	im := BuildImage()

	// The real image is in bijection with its own registry — no refusal.
	if err := VerifyDispatchBijection(im, im.Registry()); err != nil {
		t.Fatalf("real image must be in bijection: %v", err)
	}

	// Pick a victim native hash.
	var victim string
	for _, e := range im.Entries {
		if e.Native != nil {
			victim = e.Hash
			break
		}
	}
	if victim == "" {
		t.Fatal("no native entry to strip")
	}

	// Forward leg: a registry MISSING the victim ⇒ orphan-hash boot refusal.
	stripped := cek.NewRegistry()
	for _, e := range im.Entries {
		if e.Native != nil && e.Hash != victim {
			stripped.Register(e.Hash, e.Native)
		}
	}
	err := VerifyDispatchBijection(im, stripped)
	if err == nil {
		t.Fatal("stripped registry must refuse boot (orphan hash)")
	}
	var br *BootRefusal
	if !errors.As(err, &br) {
		t.Fatalf("want *BootRefusal, got %T: %v", err, err)
	}
	if br.Event != "epoch.boot_refused" || br.OrphanHash != victim {
		t.Fatalf("orphan-hash refusal must name %s, got %+v", victim, br)
	}

	// Reverse leg: an EXTRA registered implementation with no catalog entry ⇒
	// orphan-implementation boot refusal naming the extra hash.
	extra := im.Registry()
	const bogus = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	extra.Register(bogus, cek.StdTimeNow)
	err = VerifyDispatchBijection(im, extra)
	if err == nil {
		t.Fatal("extra registered implementation must refuse boot (orphan implementation)")
	}
	if !errors.As(err, &br) {
		t.Fatalf("want *BootRefusal, got %T: %v", err, err)
	}
	if br.Event != "epoch.boot_refused" || br.OrphanHash != bogus {
		t.Fatalf("orphan-implementation refusal must name %s, got %+v", bogus, br)
	}
}

// --- (d) H_dispatch attestation recompute-and-refuse (ADR-10 §2, R1-09) -------

// TestGateD_AttestationRecomputeRefusesOnTamper tampers the pinned epoch
// dispatch_attestation and asserts boot RECOMPUTES H_dispatch from the running
// image and refuses with the structured epoch.boot_refused diagnostic naming
// pinned vs computed — the gate never opens on an unattested dispatch table.
func TestGateD_AttestationRecomputeRefusesOnTamper(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	im := w.im

	const tampered = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := w.conn.Exec(ctx,
		`UPDATE epoch SET dispatch_attestation=$1 WHERE n=1`, tampered); err != nil {
		t.Fatal(err)
	}

	err := VerifyBoot(ctx, w.conn, im)
	if err == nil {
		t.Fatal("VerifyBoot must refuse a tampered dispatch attestation")
	}
	var br *BootRefusal
	if !errors.As(err, &br) {
		t.Fatalf("want structured *BootRefusal, got %T: %v", err, err)
	}
	if br.Event != "epoch.boot_refused" {
		t.Fatalf("event = %q, want epoch.boot_refused", br.Event)
	}
	if br.PinnedHDispatch != tampered {
		t.Fatalf("pinned_h_dispatch = %q, want the tampered value %q", br.PinnedHDispatch, tampered)
	}
	if br.ComputedHDispatch != im.Attestation {
		t.Fatalf("computed_h_dispatch = %q, want the binary's %q", br.ComputedHDispatch, im.Attestation)
	}
	if br.PinnedHDispatch == br.ComputedHDispatch {
		t.Fatal("refusal must name DISTINCT pinned vs computed values")
	}
}

// --- (e) NativeBody unwritability (ADR-10 §1, Red-Path) -----------------------

// TestGateE_NativeBodyUnwritable submits source containing the printed native
// stub form (regelNative("std/money.money") — see rast/print.go) through the LIVE
// admission gate and asserts it is rejected with zero trace. The ADR-01 lowering
// has NO production for KNativeBody, so the printed stub lowers to an ordinary
// call on the free name `regelNative`, which no import resolves — structurally
// unwritable, not merely policy-rejected. Targets a NEW D0 module (std/money).
func TestGateE_NativeBodyUnwritable(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	defsBefore := w.count("SELECT count(*) FROM definition")
	admsBefore := w.count("SELECT count(*) FROM admission")

	// The exact printed native-stub form for a NEW D0 battery (std/money.money).
	src := "export const money = regelNative(\"std/money.money\");\n"
	v, err := admit(ctx, w.conn, src, "app/evil", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 {
		t.Fatal("rejection must carry a diagnostic")
	}
	t.Logf("native-stub rejected by %s / %s: %s",
		v.Diagnostics[0].StageOrVerifier, v.Diagnostics[0].Code, v.Diagnostics[0].Message)

	// Zero trace: no definition, no admission row for the rejected submission.
	if got := w.count("SELECT count(*) FROM definition"); got != defsBefore {
		t.Fatalf("definition rows changed: %d → %d (must be zero trace)", defsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM admission"); got != admsBefore {
		t.Fatalf("admission rows changed: %d → %d (rejected leaves no admission row)", admsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/evil/money'"); got != 0 {
		t.Fatalf("name_pointer for rejected native-stub def exists (%d)", got)
	}
}
