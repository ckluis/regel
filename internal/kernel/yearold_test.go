package kernel

import (
	"context"
	"fmt"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
)

// TestYearOldResume is ADR-05 Red-Path Test 2: a workflow parked long ago (its
// rows backdated 400 days, its timer `due` a fixed past UTC instant) resumes to
// completion after the catalog has advanced one epoch AND its definition head has
// moved — against its ORIGINAL def_hash, via the append-only CFR reader. The
// continuation.epoch stamp is pure provenance: it reads 1 at rest and the resume
// (driven by an epoch-2 kernel) never keys off it.
func TestYearOldResume(t *testing.T) {
	e := newReactorEnv(t) // epoch 1 pinned; e.srv is an epoch-1 kernel
	ctx := context.Background()

	mk := func(ret int) string {
		return fmt.Sprintf(`import { sleep } from "std/wf";
export function w(): number { sleep(60000); return %d; }`, ret)
	}
	v1 := e.admit(t, mk(42), "app/year", nil)
	if v1.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit v1: %q (%+v)", v1.Outcome, v1.Diagnostics)
	}
	h1 := v1.Hashes["app/year/w"]
	id := e.start(t, h1, nil, map[string]any{"subject": "op", "operator": true})

	// Drive one step: parks on the 60s timer.
	if out, claimed := e.stepOnce(t, id); !claimed || out.Kind != cek.OutParked {
		t.Fatalf("park step: claimed=%v kind=%d", claimed, out.Kind)
	}
	if s := e.status(t, id); s != "sleeping" {
		t.Fatalf("status after park = %q, want sleeping", s)
	}
	// Provenance stamp at rest: epoch 1.
	if ep := e.intScalar(t, `SELECT epoch FROM continuation WHERE id=$1`, id); ep != 1 {
		t.Fatalf("parked continuation.epoch = %d, want 1 (provenance)", ep)
	}

	// Age it a year and a bit; rewrite the timer `due` to a fixed past UTC instant
	// (fixed-width ISO, lexicographically <= now → index-served as immediately due).
	e.exec(t, `
UPDATE continuation
   SET created_at = now() - interval '400 days',
       updated_at = now() - interval '400 days',
       wake = jsonb_build_object('kind','timer','due','2020-01-01T00:00:00.000000Z')
 WHERE id=$1`, id)

	// Advance the catalog one epoch: copy the epoch-1 manifest/attestation into a
	// new epoch-2 row and flip the live fence. (A real narrowing epoch would carry
	// its own roots; copying keeps VerifyBoot — which checks the n=1 row — green.)
	e.exec(t, `
INSERT INTO epoch (n, std_manifest_root, dispatch_attestation)
SELECT 2, std_manifest_root, dispatch_attestation FROM epoch WHERE n=1`)
	e.exec(t, `UPDATE epoch_current SET n=2 WHERE one=true`)

	// Re-admit the definition so the catalog HEAD moves (new hash); the parked
	// continuation must ignore it and resume against h1.
	v2 := e.admit(t, mk(99), "app/year", map[string]string{"app/year/w": h1})
	if v2.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("re-admit v2: %q (%+v)", v2.Outcome, v2.Diagnostics)
	}
	if v2.Hashes["app/year/w"] == h1 {
		t.Fatalf("re-admit did not move the head (still %s)", h1)
	}

	// Boot a NEW kernel: it reads epoch_current at boot and pins epoch 2. VerifyBoot
	// tolerates the copied epoch-2 row (it verifies against the immortal n=1 row).
	srv2, err := New(ctx, e.pool)
	if err != nil {
		t.Fatalf("boot epoch-2 kernel: %v", err)
	}
	if srv2.Epoch() != 2 {
		t.Fatalf("epoch-2 kernel pinned epoch %d, want 2", srv2.Epoch())
	}

	// Resume via the epoch-2 kernel's reactor. The timer is past-due → scanned →
	// drained → completes against h1's frames with the ORIGINAL result.
	r := srv2.StartReactor(ctx, ReactorConfig{PollInterval: 15 * time.Millisecond})
	defer r.Stop()

	e.waitStatus(t, id, "done", 15*time.Second)
	got := e.result(t, id)
	if got.Tag != cek.TagF64 || got.N != 42 {
		t.Fatalf("year-old resume result = %+v, want 42 (original h1 semantics, not the moved head)", got)
	}
	// Provenance stamp unchanged: resume never wrote epoch, never keyed off it.
	if ep := e.intScalar(t, `SELECT epoch FROM continuation WHERE id=$1`, id); ep != 1 {
		t.Fatalf("post-resume continuation.epoch = %d, want still 1", ep)
	}
	age := e.intScalar(t, `SELECT (EXTRACT(DAY FROM now()-created_at))::bigint FROM continuation WHERE id=$1`, id)
	t.Logf("YEAR-OLD RESUME: id=%s rest_age=%d days, epoch_current advanced 1→2, head moved %s→%s, "+
		"resumed against ORIGINAL def_hash, result=%d, provenance epoch stamp stayed 1",
		id, age, h1[:12], v2.Hashes["app/year/w"][:12], int64(got.N))
}
