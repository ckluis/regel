package kernel

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

// r9_migrate_std_pair_test.go — the BUILD-F R9 discharge: the epoch-migrate drill
// run across a GENUINELY NEW std pair (a new std-manifest-root), not the unchanged
// pair the Stage-E drills used. The real std delta is std/text.Slug (a type-only
// battery entry, admission.BuildImageEpoch2), which moves the std-manifest-root
// while holding the dispatch attestation constant — so the epoch fence's canonical
// `manifest_root_mismatch` boot refusal fires for THIS new pair (RED, witnessed
// first), and MigrateCommitImage slots the new root through the real migrate
// machinery: dry-run findings-as-rows → all-or-nothing commit → resume on the new
// epoch with exactly-once effects.
//
// It also CAPTURES real parked-continuation frames for the R11 golden-corpus growth
// when REGEL_CAPTURE_R11=1 (a deliberate regen, like the -regen golden generator).

const r9SleepWF = `import { sleep } from "std/wf";
export function w(): number { const acc = 7; sleep(60000); return acc + 35; }`

const r9MailWF = `import { sleep } from "std/wf";
import { send } from "std/mail";
export function w(): string { sleep(60000); send("ops@example.com", "resumed"); return "resumed"; }`

// r9SlugWF only TYPECHECKS against std-N (it imports std/text.Slug, an epoch-2 API)
// — the ADR-08 §3 "a fix that only typechecks against std-N" case. It is REFUSED
// admission under epoch 1 (module unresolved) and ADMITS under epoch 2.
const r9SlugWF = `import type { Slug } from "std/text";
export function slugOf(s: string): Slug { return s; }`

// r9Park admits src (optionally declaring caps), starts it under srv, steps it once
// to park it sleeping, makes its timer immediately due, and returns the id.
func r9Park(t *testing.T, e *reactorEnv, srv *Server, prefix, src string, caps []string) string {
	t.Helper()
	ctx := context.Background()
	var v admission.Verdict
	if len(caps) > 0 {
		for _, c := range caps {
			e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by)
				VALUES ('engineer:dev', $1, '', 'test') ON CONFLICT DO NOTHING`, c)
		}
		v = e.admitDecl(t, src, prefix, caps)
	} else {
		v = e.admit(t, src, prefix, nil)
	}
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("r9Park admit %s: %q %+v", prefix, v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes[prefix+"/w"]
	var id string
	e.withConn(t, func(c *pgwire.Conn) {
		var err error
		id, err = cfr.StartWorkflow(ctx, c, srv.stepEnv(0), srv.Interp(), hash, nil,
			map[string]any{"subject": "op", "operator": true}, cek.TierTrusted)
		if err != nil {
			t.Fatalf("StartWorkflow: %v", err)
		}
	})
	e.withConn(t, func(c *pgwire.Conn) {
		resume := func(st *cek.State, d cek.Delivery, p cek.Principal) cek.Outcome {
			return srv.Interp().Resume(ctx, st, d, p)
		}
		out, claimed, err := cfr.ClaimAndStep(ctx, c, srv.stepEnv(30), srv.Interp(), id, 0, resume)
		if err != nil || !claimed || out.Kind != cek.OutParked {
			t.Fatalf("park step: claimed=%v kind=%d err=%v", claimed, out.Kind, err)
		}
	})
	if s := e.status(t, id); s != "sleeping" {
		t.Fatalf("status after park = %q, want sleeping", s)
	}
	return id
}

// admitWithImage admits src through the real gate under an EXPLICIT std image —
// the seam that lets the drill run admission against the epoch-1 vs epoch-2 std
// surface (the epoch-2 image resolves std/text; the epoch-1 image does not).
func admitWithImage(t *testing.T, pool *pgwire.Pool, src, prefix string, im *admission.Image) admission.Verdict {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release(conn)
	patch := admission.Patch{
		Modules:     []admission.ModuleSrc{{ModuleName: prefix, Source: src}},
		TargetScope: admission.Scope{Kind: 0, ID: ""},
		BaseHashes:  map[string]string{},
	}
	v, err := admission.Admit(ctx, conn, patch,
		admission.Principal{ActorKind: "engineer", ActorID: "dev", Via: "cli"}, im)
	if err != nil {
		// A hard typecheck/resolve error is a legitimate RED (refusal), not a test
		// failure — surface it as a non-admitted verdict so callers can assert on it.
		return admission.Verdict{Outcome: admission.OutcomeRejected, Diagnostics: []admission.Diagnostic{{Message: err.Error()}}}
	}
	return v
}

// TestR9MigrateAcrossRealStdPair is the R9 drill end-to-end.
func TestR9MigrateAcrossRealStdPair(t *testing.T) {
	e := newReactorEnv(t) // genesis'd under the epoch-1 image (the OLD std pair)
	ctx := context.Background()

	e1 := admission.BuildImage()
	e2 := admission.BuildImageEpoch2()
	if e1.ManifestRoot == e2.ManifestRoot {
		t.Fatal("epoch-2 image has the same std-manifest-root — no real std delta")
	}
	t.Logf("R9 std delta: std/text.Slug — old root %s → new root %s (attestation held %s)",
		e1.ManifestRoot[:12], e2.ManifestRoot[:12], e1.Attestation[:12])

	// ---- RED #1 (witnessed first): the OLD migrate machinery cannot slot the new
	// std pair. In a throwaway epoch-1 catalog, the existing MigrateCommit (which
	// copies the CURRENT std pair forward) advances to epoch 2 carrying the OLD
	// root — so the epoch-2 binary, whose embedded root is the NEW one, REFUSES to
	// boot with the canonical `manifest_root_mismatch` (ADR-08 §2). This is the path
	// that fails without MigrateCommitImage — captured red before the green fix.
	eRed := newReactorEnv(t)
	eRed.withConn(t, func(c *pgwire.Conn) {
		if err := admission.MigrateCommit(ctx, c, 2, nil); err != nil {
			t.Fatalf("RED#1 setup: old MigrateCommit to epoch 2: %v", err)
		}
	})
	var br1 *admission.BootRefusal
	eRed.withConn(t, func(c *pgwire.Conn) {
		err := admission.VerifyBoot(ctx, c, e2)
		if err == nil {
			t.Fatal("RED#1: epoch-2 binary booted a catalog the OLD machinery migrated — the fence did not fire")
		}
		if !errors.As(err, &br1) {
			t.Fatalf("RED#1: refusal is not a structured BootRefusal: %v", err)
		}
	})
	if br1.Reason != "manifest_root_mismatch" {
		t.Fatalf("RED#1: boot-refusal reason %q, want manifest_root_mismatch", br1.Reason)
	}
	if br1.Event != "epoch.boot_refused" {
		t.Fatalf("RED#1: event = %q, want epoch.boot_refused", br1.Event)
	}
	j1, _ := json.Marshal(br1)
	t.Logf("RED#1 boot-refusal (epoch-2 binary vs OLD-machinery-migrated catalog): %s", j1)

	// ---- RED #2 (witnessed first): the real std delta provokes an admission
	// refusal the unchanged pair never could — code importing std/text is REFUSED
	// under epoch 1 (the module does not exist in the epoch-1 std surface).
	vRed := admitWithImage(t, e.pool, r9SlugWF, "app/r9slug", e1)
	if vRed.Outcome == admission.OutcomeAdmitted {
		t.Fatal("RED#2: std/text-importing code ADMITTED under epoch 1 — std delta not fenced at the gate")
	}
	redReason := ""
	if len(vRed.Diagnostics) > 0 {
		redReason = vRed.Diagnostics[0].Message
	}
	t.Logf("RED#2 admission refusal under epoch 1 (imports std/text.Slug): outcome=%q reason=%q", vRed.Outcome, redReason)

	// ---- Park REAL workflows under the OLD epoch (real CFR frames).
	idSleep := r9Park(t, e, e.srv, "app/r9sleep", r9SleepWF, nil)
	idMail := r9Park(t, e, e.srv, "app/r9mail", r9MailWF, []string{"mail.send"})
	idCap := r9Park(t, e, e.srv, "app/r9cap", captureWF, nil)
	if ep := e.intScalar(t, `SELECT epoch FROM continuation WHERE id=$1`, idMail); ep != 1 {
		t.Fatalf("mail workflow parked under epoch %d, want 1", ep)
	}

	// R11 capture: dump the real parked frames as golden blobs (gated).
	if os.Getenv("REGEL_CAPTURE_R11") == "1" {
		captureR11(t, e, map[string]string{
			"real_sleep_park":   idSleep,
			"real_mail_park":    idMail,
			"real_capture_park": idCap,
		})
	}

	// ---- Dry-run: findings as ROWS, mutates nothing else (ADR-08 §3).
	var findings []admission.MigrationFinding
	e.withConn(t, func(c *pgwire.Conn) {
		var err error
		findings, err = admission.MigrateDryRun(ctx, c, 2, nil)
		if err != nil {
			t.Fatalf("MigrateDryRun: %v", err)
		}
	})
	nRows := e.intScalar(t, `SELECT count(*) FROM migration_finding WHERE epoch=2`)
	if nRows == 0 || int(nRows) != len(findings) {
		t.Fatalf("dry-run finding rows=%d, returned=%d", nRows, len(findings))
	}
	if n := e.intScalar(t, `SELECT n FROM epoch_current WHERE one=true`); n != 1 {
		t.Fatalf("dry-run advanced the epoch to %d (must mutate nothing)", n)
	}
	t.Logf("dry-run: %d migration_finding rows written, epoch untouched (still 1)", nRows)

	// ---- Commit: slot the NEW std pair in one all-or-nothing txn.
	e.withConn(t, func(c *pgwire.Conn) {
		if err := admission.MigrateCommitImage(ctx, c, 2, e2, nil); err != nil {
			t.Fatalf("MigrateCommitImage: %v", err)
		}
	})
	if n := e.intScalar(t, `SELECT n FROM epoch_current WHERE one=true`); n != 2 {
		t.Fatalf("epoch_current = %d after commit, want 2", n)
	}
	var slotRoot string
	e.withConn(t, func(c *pgwire.Conn) {
		c.QueryRow(ctx, `SELECT std_manifest_root FROM epoch WHERE n=2`, nil, &slotRoot)
	})
	if slotRoot != e2.ManifestRoot {
		t.Fatalf("epoch-2 row root %s != new-image root %s — the new manifest root was NOT slotted", slotRoot, e2.ManifestRoot)
	}
	// The new std NAME POINTER was slotted (its hash is shared with other std types
	// — every type has the opaque `unknown` body — so identity is the name, not the
	// hash). Its absence is exactly what made RED#1 refuse boot.
	if e.intScalar(t, `SELECT count(*) FROM name_pointer WHERE scope_kind=0 AND scope_id='' AND name='std/text/Slug'`) != 1 {
		t.Fatal("std/text/Slug name pointer not catalogued by the migrate")
	}
	t.Logf("commit: epoch 2 live, std_manifest_root slotted to the NEW root %s (std/text/Slug catalogued)", slotRoot[:12])

	// ---- GREEN: the epoch-2 binary now boots; the epoch-1 binary is refused.
	e.withConn(t, func(c *pgwire.Conn) {
		if err := admission.VerifyBoot(ctx, c, e2); err != nil {
			t.Fatalf("GREEN: epoch-2 image refused to boot its own migrated catalog: %v", err)
		}
		if err := admission.VerifyBoot(ctx, c, e1); err == nil {
			t.Fatal("GREEN: epoch-1 image booted the epoch-2 catalog — the fence should refuse the stale binary")
		}
	})
	srv2, err := NewWithImage(ctx, e.pool, e2)
	if err != nil {
		t.Fatalf("boot epoch-2 kernel: %v", err)
	}
	if srv2.Epoch() != 2 {
		t.Fatalf("epoch-2 kernel pinned %d, want 2", srv2.Epoch())
	}
	t.Log("GREEN: epoch-2 kernel booted on the new pair; stale epoch-1 binary refused")

	// ---- Delta-provoked admission GREEN: std/text now admits under epoch 2.
	vGreen := admitWithImage(t, e.pool, r9SlugWF, "app/r9slug2", e2)
	if vGreen.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("GREEN: std/text-importing code refused under epoch 2: %q %+v", vGreen.Outcome, vGreen.Diagnostics)
	}
	t.Log("GREEN: code importing the new std/text.Slug admits under epoch 2 (refused under epoch 1)")

	// ---- Resume on the NEW epoch: correct result + exactly-once mail effect.
	e.exec(t, `UPDATE continuation SET wake = jsonb_build_object('kind','timer','due','2020-01-01T00:00:00.000000Z') WHERE id IN ($1,$2)`, idMail, idSleep)
	r := srv2.StartReactor(ctx, ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	e.waitStatus(t, idMail, "done", 20*time.Second)
	e.waitStatus(t, idSleep, "done", 20*time.Second)

	if got, ok := e.result(t, idMail).StrVal(); !ok || got != "resumed" {
		t.Fatalf("mail workflow resume result = %q (ok=%v), want resumed", got, ok)
	}
	if got := e.result(t, idSleep); got.Tag != cek.TagF64 || got.N != 42 {
		t.Fatalf("sleep workflow resume result = %+v, want 42", got)
	}
	nOut := e.intScalar(t, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, idMail)
	if nOut != 1 {
		t.Fatalf("mail.send effect not exactly-once across the epoch boundary: %d outbox rows, want 1", nOut)
	}
	// Provenance stamp unchanged: resume never re-keyed off the epoch.
	if ep := e.intScalar(t, `SELECT epoch FROM continuation WHERE id=$1`, idMail); ep != 1 {
		t.Fatalf("mail workflow provenance epoch = %d after resume, want still 1", ep)
	}
	t.Logf("RESUME on epoch 2: mail workflow done (result=resumed, outbox=1 exactly-once, provenance stamp 1); " +
		"sleep workflow done (result=42). R9 discharged across a real new std-manifest-root.")
}

// captureR11 writes the given parked continuations' REAL CFR frames to the golden
// corpus as real_*.cfr blobs and prints their covered frame-kind sets, so the
// golden coverage manifest (internal/cfr/testdata/golden/real_coverage.json) can be
// grown to ratchet the monotone floor (R11). Deliberate regen only.
func captureR11(t *testing.T, e *reactorEnv, byName map[string]string) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join("..", "cfr", "testdata", "golden")
	type entry struct {
		Name  string `json:"name"`
		File  string `json:"file"`
		CFR   int    `json:"cfr"`
		Kinds []int  `json:"kinds"`
	}
	var manifest []entry
	for name, id := range byName {
		var framesHex string
		e.withConn(t, func(c *pgwire.Conn) {
			ok, err := c.QueryRow(ctx, `SELECT encode(frames,'hex') FROM continuation WHERE id=$1`, []any{id}, &framesHex)
			if err != nil || !ok {
				t.Fatalf("capture read frames %s: %v", name, err)
			}
		})
		raw, err := hex.DecodeString(framesHex)
		if err != nil {
			t.Fatalf("capture decode hex %s: %v", name, err)
		}
		st, derr := cfr.Decode(raw)
		if derr != nil {
			t.Fatalf("capture: real blob %s does not decode standalone: %v", name, derr)
		}
		cfrVer := int(raw[0])
		kindSet := map[int]bool{}
		for _, f := range st.Kont {
			kindSet[int(f.Kind)] = true
		}
		var kinds []int
		for k := range kindSet {
			kinds = append(kinds, k)
		}
		file := name + ".cfr"
		if err := os.WriteFile(filepath.Join(dir, file), raw, 0o644); err != nil {
			t.Fatalf("capture write %s: %v", file, err)
		}
		manifest = append(manifest, entry{Name: name, File: file, CFR: cfrVer, Kinds: kinds})
		t.Logf("R11 capture: %s -> %s (%d bytes, kinds %v)", name, file, len(raw), kinds)
	}
	out, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "real_coverage.json"), append(out, '\n'), 0o644); err != nil {
		t.Fatalf("capture write real_coverage.json: %v", err)
	}
	t.Logf("R11 capture: wrote real_coverage.json with %d real-shape entries", len(manifest))
}
