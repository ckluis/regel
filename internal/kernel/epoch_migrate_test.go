package kernel

import (
	"context"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
)

const sleepWF = `import { sleep } from "std/wf";
export function w(): number { sleep(60000); return 42; }`

// captureWF binds a number live ACROSS the await, so the parked state holds a
// substantive lattice tag (TagF64) for the O4 needs-hold red-path.
const captureWF = `import { sleep } from "std/wf";
export function w(): number { const n = 7; sleep(60000); return n; }`

// parkOne admits sleepWF, starts it under the given kernel, and steps it once so
// it parks sleeping. Returns the continuation id.
func parkOne(t *testing.T, e *reactorEnv, srv *Server, prefix string) string {
	return parkSrc(t, e, srv, prefix, sleepWF)
}

// parkSrc is parkOne with an explicit workflow source.
func parkSrc(t *testing.T, e *reactorEnv, srv *Server, prefix, src string) string {
	t.Helper()
	ctx := context.Background()
	v := e.admit(t, src, prefix, nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q", v.Outcome)
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

func (e *reactorEnv) dryRun(t *testing.T, target int, ban []cek.Tag) []admission.MigrationFinding {
	t.Helper()
	var out []admission.MigrationFinding
	e.withConn(t, func(c *pgwire.Conn) {
		var err error
		out, err = admission.MigrateDryRun(context.Background(), c, target, ban)
		if err != nil {
			t.Fatalf("MigrateDryRun: %v", err)
		}
	})
	return out
}

func (e *reactorEnv) commit(t *testing.T, target int, ban []cek.Tag) error {
	t.Helper()
	var err error
	e.withConn(t, func(c *pgwire.Conn) {
		err = admission.MigrateCommit(context.Background(), c, target, ban)
	})
	return err
}

// TestMigrateDryRunFindingsNoMutation is ADR-08 red-path (a): dry-run on a world
// with a parked continuation produces findings ROWS and mutates NOTHING else.
func TestMigrateDryRunFindingsNoMutation(t *testing.T) {
	e := newReactorEnv(t)
	id := parkOne(t, e, e.srv, "app/dry")

	defsBefore := e.intScalar(t, `SELECT count(*) FROM definition`)
	seqBefore := e.intScalar(t, `SELECT step_seq FROM continuation WHERE id=$1`, id)

	findings := e.dryRun(t, 2, nil)

	// Findings rows exist and include our continuation as 'ok'.
	var okForCont bool
	for _, f := range findings {
		if f.Scope == "continuation" && f.Subject == id && f.Rule == "ok" {
			okForCont = true
		}
	}
	if !okForCont {
		t.Fatalf("dry-run did not classify the parked continuation ok: %+v", findings)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM migration_finding WHERE epoch=2`); n == 0 {
		t.Fatal("dry-run wrote no migration_finding rows")
	}
	// MUTATED NOTHING: epoch, continuation, definitions all untouched.
	if n := e.intScalar(t, `SELECT n FROM epoch_current WHERE one=true`); n != 1 {
		t.Fatalf("epoch_current = %d after dry-run, want 1 (untouched)", n)
	}
	if s := e.status(t, id); s != "sleeping" {
		t.Fatalf("continuation status = %q after dry-run, want sleeping (untouched)", s)
	}
	if seq := e.intScalar(t, `SELECT step_seq FROM continuation WHERE id=$1`, id); seq != seqBefore {
		t.Fatalf("step_seq moved during dry-run: %d → %d", seqBefore, seq)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM definition`); n != defsBefore {
		t.Fatalf("definition count moved during dry-run: %d → %d", defsBefore, n)
	}
	var one int
	e.withConn(t, func(c *pgwire.Conn) {
		if found, _ := c.QueryRow(context.Background(), `SELECT 1 FROM epoch WHERE n=2`, nil, &one); found {
			t.Fatal("dry-run created an epoch-2 row")
		}
	})
}

// TestMigrateUndecodableBlocksCommit is ADR-08 red-path (c): a continuation whose
// CFR blob no longer decodes is `undecodable`, and that BLOCKS --commit
// fail-closed — the epoch does not advance (which is also the all-or-nothing
// proof: nothing persisted).
func TestMigrateUndecodableBlocksCommit(t *testing.T) {
	e := newReactorEnv(t)
	id := parkOne(t, e, e.srv, "app/undec")

	// Corrupt the parked frames (truncate to one byte) — the immortal-store REVOKE
	// is on the definition table; continuation is mutable, and the scratch owner
	// writes it here to simulate CFR corruption.
	e.exec(t, `UPDATE continuation SET frames = '\x00'::bytea WHERE id=$1`, id)

	findings := e.dryRun(t, 2, nil)
	var undec bool
	for _, f := range findings {
		if f.Subject == id && f.Rule == "undecodable" {
			undec = true
		}
	}
	if !undec {
		t.Fatalf("dry-run did not flag the corrupt continuation undecodable: %+v", findings)
	}

	if err := e.commit(t, 2, nil); err == nil {
		t.Fatal("commit was NOT blocked by the undecodable continuation")
	}
	if n := e.intScalar(t, `SELECT n FROM epoch_current WHERE one=true`); n != 1 {
		t.Fatalf("epoch advanced to %d despite a blocking finding, want 1", n)
	}
}

// TestMigrateO4NeedsHoldBlocksCommit is ADR-08 §4 O4: a sleeping continuation
// holding a to-be-banned lattice tag blocks --commit with the continuation
// enumerated; without the ban the same commit lands.
func TestMigrateO4NeedsHoldBlocksCommit(t *testing.T) {
	e := newReactorEnv(t)
	id := parkSrc(t, e, e.srv, "app/o4", captureWF)

	// Discover a tag the parked state actually holds, then ban it.
	var ban cek.Tag
	var found bool
	e.withConn(t, func(c *pgwire.Conn) {
		var hexFrames string
		ok, err := c.QueryRow(context.Background(), `SELECT encode(frames,'hex') FROM continuation WHERE id=$1`, []any{id}, &hexFrames)
		if err != nil || !ok {
			t.Fatalf("read frames: %v", err)
		}
		raw := make([]byte, len(hexFrames)/2)
		for i := 0; i < len(raw); i++ {
			var b int
			_, _ = fscanHexByte(hexFrames[2*i:2*i+2], &b)
			raw[i] = byte(b)
		}
		st, derr := cfr.Decode(raw)
		if derr != nil {
			t.Fatalf("decode: %v", derr)
		}
		for tag := range cfr.StateTags(st) {
			if tag != 0 { // skip TagUndef — pick a substantive tag
				ban = tag
				found = true
				break
			}
		}
	})
	if !found {
		t.Skip("parked state held only the trivial tag; nothing substantive to ban")
	}

	// With the ban: refused, continuation enumerated as needs-hold.
	findings := e.dryRun(t, 2, []cek.Tag{ban})
	var needsHold bool
	for _, f := range findings {
		if f.Subject == id && f.Rule == "needs-hold" {
			needsHold = true
		}
	}
	if !needsHold {
		t.Fatalf("O4: banned-tag continuation not flagged needs-hold: %+v", findings)
	}
	if err := e.commit(t, 2, []cek.Tag{ban}); err == nil {
		t.Fatal("O4: commit was NOT blocked by the needs-hold continuation")
	}
	if n := e.intScalar(t, `SELECT n FROM epoch_current WHERE one=true`); n != 1 {
		t.Fatalf("O4: epoch advanced despite needs-hold, want 1 got %d", n)
	}

	// Without the ban: the same commit lands (the narrowing was the only blocker).
	if err := e.commit(t, 2, nil); err != nil {
		t.Fatalf("commit without the ban failed: %v", err)
	}
	if n := e.intScalar(t, `SELECT n FROM epoch_current WHERE one=true`); n != 2 {
		t.Fatalf("epoch did not advance to 2 after an unblocked commit, got %d", n)
	}
}

// TestTwoEpochStrandedImpossibility is ADR-08's stranded-continuation red-path
// across TWO boundaries: a continuation parked before epoch N survives to N+2 and
// resumes to the IDENTICAL result an undisturbed run gives — never silently
// stranded. Uses the real `migrate N --commit` machinery for both flips.
func TestTwoEpochStrandedImpossibility(t *testing.T) {
	e := newReactorEnv(t)
	id := parkOne(t, e, e.srv, "app/two")

	// Provenance stamp at rest: epoch 1.
	if ep := e.intScalar(t, `SELECT epoch FROM continuation WHERE id=$1`, id); ep != 1 {
		t.Fatalf("parked epoch stamp = %d, want 1", ep)
	}
	// Make the 60s timer immediately due (fixed past UTC instant).
	e.exec(t, `UPDATE continuation SET wake = jsonb_build_object('kind','timer','due','2020-01-01T00:00:00.000000Z') WHERE id=$1`, id)

	// Two real epoch flips: 1 → 2 → 3. Parked BEFORE epoch 2; must reach epoch 3.
	if err := e.commit(t, 2, nil); err != nil {
		t.Fatalf("migrate 2 --commit: %v", err)
	}
	if err := e.commit(t, 3, nil); err != nil {
		t.Fatalf("migrate 3 --commit: %v", err)
	}

	// A fresh kernel boots pinned to epoch 3 (VerifyBoot tolerates the copied rows,
	// which carry the immortal n=1 pair).
	ctx := context.Background()
	srv3, err := New(ctx, e.pool)
	if err != nil {
		t.Fatalf("boot epoch-3 kernel: %v", err)
	}
	if srv3.Epoch() != 3 {
		t.Fatalf("epoch-3 kernel pinned %d, want 3", srv3.Epoch())
	}
	r := srv3.StartReactor(ctx, ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	e.waitStatus(t, id, "done", 15*time.Second)
	got := e.result(t, id)
	if got.Tag != cek.TagF64 || got.N != 42 {
		t.Fatalf("two-epoch resume result = %+v, want 42 (identical to undisturbed run)", got)
	}
	// Provenance stamp unchanged: resume never keyed off the epoch.
	if ep := e.intScalar(t, `SELECT epoch FROM continuation WHERE id=$1`, id); ep != 1 {
		t.Fatalf("post-resume epoch stamp = %d, want still 1", ep)
	}
	t.Logf("TWO-EPOCH STRANDED-IMPOSSIBILITY: parked under epoch 1, survived migrate 1→2→3, "+
		"resumed on an epoch-3 kernel against the ORIGINAL def_hash, result=42, provenance stamp stayed 1")
}

// TestBadEpochRevertHoldsDependents is Deliverable 4 / L1 backing: a bad epoch is
// reverted (a new epoch carrying the prior-good pair), and every dependent bound
// to the bad epoch is HELD FAIL-CLOSED — an epoch_hold row + a 'condition' status
// that the reactor never resumes against the reverted world.
func TestBadEpochRevertHoldsDependents(t *testing.T) {
	e := newReactorEnv(t)
	ctx := context.Background()

	// Ship the "bad" epoch 2, then boot a kernel pinned to it.
	if err := e.commit(t, 2, nil); err != nil {
		t.Fatalf("migrate 2 --commit (bad epoch): %v", err)
	}
	srv2, err := New(ctx, e.pool)
	if err != nil {
		t.Fatalf("boot epoch-2 kernel: %v", err)
	}
	if srv2.Epoch() != 2 {
		t.Fatalf("epoch-2 kernel pinned %d, want 2", srv2.Epoch())
	}
	// Park a continuation UNDER epoch 2 (stamped epoch=2 → bound to the bad epoch).
	id := parkOne(t, e, srv2, "app/rev")
	if ep := e.intScalar(t, `SELECT epoch FROM continuation WHERE id=$1`, id); ep != 2 {
		t.Fatalf("continuation epoch stamp = %d, want 2 (bound to bad epoch)", ep)
	}

	// REVERT: epoch 3 carrying epoch-1's prior-good pair, holding the dependent.
	var held []string
	e.withConn(t, func(c *pgwire.Conn) {
		held, err = admission.RevertEpoch(ctx, c, 3, 1)
		if err != nil {
			t.Fatalf("RevertEpoch: %v", err)
		}
	})
	var inHeld bool
	for _, h := range held {
		if h == id {
			inHeld = true
		}
	}
	if !inHeld {
		t.Fatalf("revert did not hold the bad-epoch dependent %s: held=%v", id, held)
	}

	// DDL-backed hold state is visible as rows, fail-closed.
	if n := e.intScalar(t, `SELECT count(*) FROM epoch_hold WHERE continuation_id=$1 AND released_at IS NULL`, id); n != 1 {
		t.Fatalf("expected 1 active epoch_hold row for %s, got %d", id, n)
	}
	if s := e.status(t, id); s != "condition" {
		t.Fatalf("held continuation status = %q, want condition (fenced from the reactor)", s)
	}
	if n := e.intScalar(t, `SELECT n FROM epoch_current WHERE one=true`); n != 3 {
		t.Fatalf("epoch_current = %d after revert, want 3", n)
	}
	// The revert epoch carries epoch-1's manifest root (rolling back = rolling
	// forward to the prior binary pair).
	var r1root, r3root string
	e.withConn(t, func(c *pgwire.Conn) {
		c.QueryRow(ctx, `SELECT std_manifest_root FROM epoch WHERE n=1`, nil, &r1root)
		c.QueryRow(ctx, `SELECT std_manifest_root FROM epoch WHERE n=3`, nil, &r3root)
	})
	if r1root == "" || r1root != r3root {
		t.Fatalf("revert epoch 3 root %q does not carry epoch-1 root %q", r3root, r1root)
	}

	// Boot an epoch-3 kernel and run its reactor: the held dependent is NOT resumed
	// against the reverted world — it stays 'condition', no result appears.
	srv3, err := New(ctx, e.pool)
	if err != nil {
		t.Fatalf("boot epoch-3 kernel: %v", err)
	}
	rr := srv3.StartReactor(ctx, ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer rr.Stop()
	time.Sleep(400 * time.Millisecond)
	if s := e.status(t, id); s != "condition" {
		t.Fatalf("held continuation was resumed (status=%q) against the reverted world — NOT fail-closed", s)
	}
	if n := e.intScalar(t, `SELECT count(*) FROM continuation WHERE id=$1 AND result IS NOT NULL`, id); n != 0 {
		t.Fatal("held continuation produced a result — it ran against the reverted world")
	}
	t.Logf("BAD-EPOCH REVERT: dependent %s bound to bad epoch 2 HELD fail-closed (epoch_hold + condition), "+
		"epoch reverted to epoch-1 pair as epoch 3, held work never resumed", id[:12])
}

// fscanHexByte parses a 2-char hex string to an int (avoids importing fmt.Sscanf
// noise into the hot decode loop above).
func fscanHexByte(s string, out *int) (int, error) {
	v := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= int(c - '0')
		case c >= 'a' && c <= 'f':
			v |= int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= int(c-'A') + 10
		}
	}
	*out = v
	return 1, nil
}
