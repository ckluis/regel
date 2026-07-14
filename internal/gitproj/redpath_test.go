package gitproj

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"regel.dev/regel/internal/admission"
)

// redpath_test.go covers the ADR-09 §Red-Path suite (each a named test): merge
// side door impossible, force-push mangle self-heal, projection leak, rename
// fidelity, round-trip, identity mapping, and docstring edit.

func bindGitIdentity(ctx context.Context, w *world, email, actorKind, actorID string, scopeKind int, scopeID string) {
	w.t.Helper()
	if _, err := w.conn.Exec(ctx, `
INSERT INTO git_identity (email, actor_kind, actor_id, scope_kind, scope_id)
VALUES ($1, $2, $3, $4, $5)`, email, actorKind, actorID, scopeKind, scopeID); err != nil {
		w.t.Fatalf("bind git identity: %v", err)
	}
}

func hasCode(v admission.Verdict, code string) bool {
	for _, d := range v.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

// --- Red-path: identity mapping ------------------------------------------------

// TestIdentityMappingUnmapped: a push from a git identity with no catalog principal
// is rejected at scope-bind — no admission row beyond the audit of the refusal.
func TestIdentityMappingUnmapped(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	admsBefore := w.count("SELECT count(*) FROM admission")
	sub := Submission{
		Files: map[string]string{"app/ghost/x.ts": incSrc},
		Email: "nobody@stranger.example",
	}
	v, err := Merge(ctx, w.conn, sub, w.im, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if v.Outcome != admission.OutcomeRejected {
		t.Fatalf("unmapped identity: outcome %q, want rejected", v.Outcome)
	}
	if !hasCode(v, "IDENTITY_UNMAPPED") {
		t.Fatalf("want IDENTITY_UNMAPPED, got %+v", v.Diagnostics)
	}
	if v.RefusalID == "" {
		t.Fatal("refusal id must be minted for an identity refusal")
	}
	// No admission row was written; exactly one refusal row records it.
	if got := w.count("SELECT count(*) FROM admission"); got != admsBefore {
		t.Fatalf("unmapped push wrote an admission row (%d → %d)", admsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM gate_refusal WHERE principal=$1", "git:nobody@stranger.example"); got != 1 {
		t.Fatalf("want exactly one identity refusal row, got %d", got)
	}
	// The dry-run door refuses identically.
	dv, err := DryRun(ctx, w.conn, sub, w.im)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dv.Outcome != admission.OutcomeRejected || !hasCode(dv, "IDENTITY_UNMAPPED") {
		t.Fatalf("dry-run unmapped: %q %+v", dv.Outcome, dv.Diagnostics)
	}
}

// --- Red-path: merge side door impossible --------------------------------------

// TestMergeSideDoorImpossible: a PR whose dry-run shows green but whose merge-time
// admission rejects (base moved underneath) leaves main unmoved and returns a
// stale-base Verdict — no forge sequence lands unverified code on main.
func TestMergeSideDoorImpossible(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	bindGitIdentity(ctx, w, "dev@corp.example", "engineer", "gitdev", 0, "")

	// Base v1 admitted through the normal door.
	v1 := `export function f(x: number): number {
  return (x + 1);
}
`
	w.admitApp(ctx, "app/side", v1, nil)
	base := w.headHash(ctx, "app/side/f")

	// A mirror advanced to the pre-merge head.
	mirror, err := NewMirror(filepath.Join(t.TempDir(), "m.git"), Config{})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	preHead, err := mirror.Advance(ctx, w.conn)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}

	// The PR proposes v2 with the base it saw. Dry-run is GREEN (base matches).
	v2 := `export function f(x: number): number {
  return (x + 2);
}
`
	sub := Submission{
		Files: map[string]string{"app/side/f.ts": v2},
		Email: "dev@corp.example",
		Bases: map[string]string{"app/side/f": base},
	}
	dv, err := DryRun(ctx, w.conn, sub, w.im)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dv.Outcome != admission.OutcomeAdmitted || !dv.DryRun {
		t.Fatalf("dry-run should be green + rolled back, got %q dryRun=%v", dv.Outcome, dv.DryRun)
	}

	// Base moves underneath: a competing admission lands v3 on the same name.
	v3 := `export function f(x: number): number {
  return (x + 3);
}
`
	w.admitApp(ctx, "app/side", v3, func(p *admission.Patch) {
		p.BaseHashes["app/side/f"] = base
	})

	// Merge-time admission with the now-STALE base ⇒ stale-base; main never moves.
	mv, err := Merge(ctx, w.conn, sub, w.im, mirror)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if mv.Outcome != admission.OutcomeStaleBase {
		t.Fatalf("merge outcome %q, want stale-base (%+v)", mv.Outcome, mv.Diagnostics)
	}
	after, err := mirror.Repo().readMain()
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	if after != preHead {
		t.Fatalf("main moved on a rejected merge: pre=%s post=%s", preHead, after)
	}
}

// --- Red-path: force-push mangle ⇒ detected, force-restored, audited -----------

// TestForcePushMangleSelfHeal: force-push garbage to the mirror's main (and drop a
// reachable object); the next projection detects the SHA mismatch, force-restores
// from the recomputed image, and writes an audit row. No admission consumed the
// mangled state.
func TestForcePushMangleSelfHeal(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	seedLedger(ctx, w)

	mirror, err := NewMirror(filepath.Join(t.TempDir(), "m.git"), Config{})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	head, err := mirror.Advance(ctx, w.conn)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	admsBefore := w.count("SELECT count(*) FROM admission")
	auditBefore := w.count("SELECT count(*) FROM projection_audit")

	// Mangle: a stolen credential force-pushes a garbage SHA onto main and an
	// object is corrupted/removed underneath it.
	garbage := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if err := mirror.Repo().setMain(garbage); err != nil {
		t.Fatalf("mangle ref: %v", err)
	}
	victim := head // the head commit object itself — delete it to prove reconstruction
	removeObject(t, mirror.Repo(), victim)
	if mirror.Repo().hasObject(victim) {
		t.Fatal("victim object should be gone after mangle")
	}

	// Next projection self-heals.
	restored, err := mirror.Advance(ctx, w.conn)
	if err != nil {
		t.Fatalf("advance (heal): %v", err)
	}
	if restored != head {
		t.Fatalf("healed head %s != original %s", restored, head)
	}
	main, err := mirror.Repo().readMain()
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	if main != head {
		t.Fatalf("main not restored: %s (want %s)", main, head)
	}
	if !mirror.Repo().hasObject(victim) {
		t.Fatal("deleted object was not reconstructed by the fold")
	}
	// Exactly one force-restore audit row naming the mangled + restored SHAs.
	if got := w.count("SELECT count(*) FROM projection_audit WHERE event='force-restore'"); got != auditBefore+1 {
		t.Fatalf("want one new force-restore audit row, got delta %d", got-auditBefore)
	}
	if got := w.count("SELECT count(*) FROM projection_audit WHERE detail->>'mangled_sha'=$1 AND detail->>'restored_sha'=$2", garbage, head); got != 1 {
		t.Fatalf("audit row does not record the mangled→restored transition")
	}
	// No admission consumed the mangled state.
	if got := w.count("SELECT count(*) FROM admission"); got != admsBefore {
		t.Fatalf("self-heal wrote an admission row (%d → %d)", admsBefore, got)
	}

	requireGit(t)
	fsckClean(t, mirror.Repo().Dir())
}

// --- Red-path: projection leak -------------------------------------------------

// TestProjectionLeak: the projected tree for a full catalog contains zero grant
// rows, tenant identifiers, or overlay content; and a PII-literal patch is rejected
// at V2, so no projected byte can ever contain it.
func TestProjectionLeak(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// A normal product def (projected).
	w.admitApp(ctx, "app/pub", greetSrc, nil)

	// A grant row carrying a distinctive secret capability marker.
	if _, err := w.conn.Exec(ctx,
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ($1,$2,'','test')`,
		"engineer:dev", "SECRETCAP_LEAKMARKER"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// An OVERLAY-scope (org) definition — tenant-private, never projected. Its body
	// carries a marker that must not appear in any projected object.
	overlaySrc := `export function tenant(): string {
  return "OVERLAYLEAK_acme_secret";
}
`
	ov := admission.Patch{
		Modules:     []admission.ModuleSrc{{ModuleName: "app/overlaymod", Source: overlaySrc}},
		TargetScope: admission.Scope{Kind: 2, ID: "acme"},
		BaseHashes:  map[string]string{},
	}
	ovv, err := admission.Admit(ctx, w.conn, ov, engineer("dev"), w.im)
	if err != nil {
		t.Fatalf("overlay admit: %v", err)
	}
	if ovv.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("overlay admit: %q %+v", ovv.Outcome, ovv.Diagnostics)
	}

	// A PII-literal patch — rejected at V2, so it never enters the projection.
	piiSrc := `import type { Vault } from "std/pii";
import { mask } from "std/pii";
export function leak(): string {
  const ssn: Vault<string> = "SSN-999-LEAKMARKER";
  return mask(ssn);
}
`
	pv, err := admission.Admit(ctx, w.conn,
		admission.Patch{Modules: []admission.ModuleSrc{{ModuleName: "app/piileak", Source: piiSrc}}, TargetScope: admission.Scope{Kind: 0}, BaseHashes: map[string]string{}},
		engineer("dev"), w.im)
	if err != nil {
		t.Fatalf("pii admit: %v", err)
	}
	if pv.Outcome != admission.OutcomeRejected || !hasCode(pv, "PII_LITERAL") {
		t.Fatalf("pii-literal must be rejected at V2 (PII_LITERAL), got %q %+v", pv.Outcome, pv.Diagnostics)
	}

	// Fold and scan EVERY object for the forbidden markers.
	store := newMemStore()
	if _, err := Fold(ctx, w.conn, store, Config{}); err != nil {
		t.Fatalf("fold: %v", err)
	}
	forbidden := []string{"SECRETCAP_LEAKMARKER", "OVERLAYLEAK_acme_secret", "SSN-999-LEAKMARKER", "acme"}
	for oid, framed := range store.objs {
		s := string(framed)
		for _, bad := range forbidden {
			if strings.Contains(s, bad) {
				t.Fatalf("projection leak: object %s contains %q", oid, bad)
			}
		}
	}
	// Sanity: the public def DID project (the scan is not vacuously clean).
	found := false
	for _, framed := range store.objs {
		if strings.Contains(string(framed), "app/pub/greet") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("public def app/pub/greet was not projected — scan would be vacuous")
	}
}

// --- Red-path: rename fidelity -------------------------------------------------

// TestRenameFidelity: a pointer-only rename (same body under a new name) projects
// as a git rename — the blob SHA is unchanged and catalog.lock shows the same hash.
func TestRenameFidelity(t *testing.T) {
	requireGit(t)
	w := setupWorld(t)
	ctx := ctxT(t)

	body := `export function thing(n: number): number {
  return (n + 7);
}
`
	// Same body under two names ⇒ same content hash (content-addressed).
	w.admitApp(ctx, "app/orig", body, nil)
	w.admitApp(ctx, "app/renamed", body, nil)
	h1 := w.headHash(ctx, "app/orig/thing")
	h2 := w.headHash(ctx, "app/renamed/thing")
	if h1 != h2 {
		t.Fatalf("identical body must share a hash: %s vs %s", h1, h2)
	}

	dir := filepath.Join(t.TempDir(), "m.git")
	repo, err := OpenBare(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	res, err := Fold(ctx, w.conn, repo, Config{})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if err := repo.setMain(res.Head); err != nil {
		t.Fatalf("set main: %v", err)
	}

	origBlob := blobOidAt(t, repo, "app/orig/thing.ts")
	renamedBlob := blobOidAt(t, repo, "app/renamed/thing.ts")
	if origBlob != renamedBlob {
		t.Fatalf("rename changed the blob SHA: %s vs %s (git would not see a rename)", origBlob, renamedBlob)
	}

	// catalog.lock records both names at the SAME hash.
	lock, err := gitOracle(t, dir, "show", "main:.regel/catalog.lock")
	if err != nil {
		t.Fatalf("show lock: %v\n%s", err, lock)
	}
	if !strings.Contains(lock, "app/orig/thing\t"+h1) || !strings.Contains(lock, "app/renamed/thing\t"+h1) {
		t.Fatalf("catalog.lock does not show both names at hash %s:\n%s", h1, lock)
	}
	t.Logf("rename fidelity: both names share blob %s and hash %s", origBlob, h1)
}

// --- Red-path: round-trip ------------------------------------------------------

// TestRoundTrip: read every projected app file back through the gate — each unit
// short-circuits as already-admitted; the repo and image agree hash-for-hash.
func TestRoundTrip(t *testing.T) {
	requireGit(t)
	w := setupWorld(t)
	ctx := ctxT(t)
	bindGitIdentity(ctx, w, "rt@corp.example", "engineer", "rtdev", 0, "")
	seedLedger(ctx, w)

	dir := filepath.Join(t.TempDir(), "m.git")
	repo, err := OpenBare(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	res, err := Fold(ctx, w.conn, repo, Config{})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if err := repo.setMain(res.Head); err != nil {
		t.Fatalf("set main: %v", err)
	}

	// "Clone" the repo: read every projected app/*.ts file.
	tree, err := gitOracle(t, dir, "ls-tree", "-r", "--name-only", "main")
	if err != nil {
		t.Fatalf("ls-tree: %v\n%s", err, tree)
	}
	files := map[string]string{}
	for _, p := range nonEmptyLines(tree) {
		if !strings.HasPrefix(p, "app/") || !strings.HasSuffix(p, ".ts") {
			continue
		}
		content, err := gitOracle(t, dir, "show", "main:"+p)
		if err != nil {
			t.Fatalf("show %s: %v\n%s", p, err, content)
		}
		files[p] = content
	}
	if len(files) == 0 {
		t.Fatal("no app files projected to resubmit")
	}

	// Resubmit them all unchanged through the REAL gate.
	mirror, err := NewMirror(dir, Config{})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	beforeHead, _ := mirror.Repo().readMain()
	v, err := Merge(ctx, w.conn, Submission{Files: files, Email: "rt@corp.example"}, w.im, mirror)
	if err != nil {
		t.Fatalf("resubmit merge: %v", err)
	}
	if v.Outcome != admission.OutcomeAlreadyAdmitted {
		t.Fatalf("round-trip resubmit must be already-admitted, got %q (%+v)", v.Outcome, v.Diagnostics)
	}
	afterHead, _ := mirror.Repo().readMain()
	if afterHead != beforeHead {
		t.Fatalf("a no-op round-trip moved main: %s → %s", beforeHead, afterHead)
	}
	t.Logf("round-trip: %d projected app files all resubmit as already-admitted", len(files))
}

// --- Red-path: docstring edit --------------------------------------------------

// TestDocstringEdit: a JSDoc-only edit re-hashes to the same address ⇒
// already-admitted, the catalog.lock hash is unchanged, and the projected file
// isolates the docstring as the sole leading block above byte-identical
// canonical_text (metadata is immortal/first-wins, so the docstring bound at first
// admission is the projected one).
func TestDocstringEdit(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	withDoc := `/** original doc block */
export function d(x: number): number {
  return (x + 1);
}
`
	v1 := w.admitApp(ctx, "app/doc", withDoc, nil)
	h := v1.Hashes["app/doc/d"]
	if h == "" {
		t.Fatalf("no hash for app/doc/d: %+v", v1.Hashes)
	}

	// A JSDoc-only edit: same body, different docstring ⇒ SAME hash, already-admitted.
	editedDoc := `/** EDITED doc block — totally different words */
export function d(x: number): number {
  return (x + 1);
}
`
	v2 := w.admitApp(ctx, "app/doc", editedDoc, nil)
	if v2.Outcome != admission.OutcomeAlreadyAdmitted {
		t.Fatalf("docstring-only edit must be already-admitted, got %q", v2.Outcome)
	}
	if v2.Hashes["app/doc/d"] != h {
		t.Fatalf("docstring edit changed the hash: %s → %s", h, v2.Hashes["app/doc/d"])
	}

	// The projected file = the ORIGINAL docstring block ⊕ the byte-identical
	// canonical_text (immortal first-wins metadata; docstring is out-of-hash). Read
	// both immortal columns and reconstruct exactly what the projector renders.
	var canon, storedDoc string
	if _, err := w.conn.QueryRow(ctx, `
SELECT d.canonical_text, COALESCE(m.docstring,'')
FROM definition d LEFT JOIN definition_meta m ON m.hash=d.hash WHERE d.hash=$1`,
		[]any{h}, &canon, &storedDoc); err != nil {
		t.Fatalf("read def+meta: %v", err)
	}
	if !strings.Contains(storedDoc, "original doc block") {
		t.Fatalf("immortal docstring is not the first-admitted one: %q", storedDoc)
	}
	store := newMemStore()
	if _, err := Fold(ctx, w.conn, store, Config{}); err != nil {
		t.Fatalf("fold: %v", err)
	}
	projected := findBlobContaining(store, "original doc block")
	if projected == "" {
		t.Fatal("projected docstring file not found")
	}
	// The projected content is exactly the projector's render: docstring block ⊕
	// canonical_text.
	expect := string(renderFile(storedDoc, canon))
	if projected != expect {
		t.Fatalf("projected docstring file mismatch:\n--- got ---\n%q\n--- want ---\n%q", projected, expect)
	}
	if strings.Contains(projected, "EDITED doc block") {
		t.Fatal("projection leaked the rejected (never-stored) edited docstring — metadata is not immortal")
	}
	// The code portion below the docstring block is byte-identical to canonical_text
	// (the docstring is isolated as the sole leading block; the code is verbatim).
	if !strings.Contains(projected, canon) || !strings.HasPrefix(projected, strings.TrimRight(storedDoc, "\n")) {
		t.Fatal("canonical text is not preserved verbatim below the docstring block")
	}
	t.Logf("docstring edit: hash unchanged (%s), already-admitted, projection keeps the immortal docstring", h)
}

// findBlobContaining returns the content of the first stored object (the memStore
// holds raw, unframed object content) that contains marker — for a distinctive file
// marker that is the blob body.
func findBlobContaining(store *memStore, marker string) string {
	for _, content := range store.objs {
		if s := string(content); strings.Contains(s, marker) {
			return s
		}
	}
	return ""
}
