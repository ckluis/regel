package admission

import (
	"context"
	"fmt"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/cfr"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
)

// migrate.go — the ADR-08 §3 `migrate N` machinery: a dry-run that writes
// FINDINGS AS ROWS and mutates nothing else, an all-or-nothing `--commit` that
// advances the epoch in one SERIALIZABLE transaction, and the §6a revert motion
// that HOLDS dependents bound to a bad epoch fail-closed (L1).
//
// MigrateCommit carries an UNCHANGED std pair forward (an app-deploy epoch, or a
// revert to a prior pair). MigrateCommitImage (BUILD-F R9) is the sibling that
// slots a GENUINELY NEW std pair: it admits the std delta and writes the epoch-N
// image's own manifest root + attestation, discharging the R9 residue — the
// migrate machinery exercised across a real dialect/std change, not just an epoch
// bump over a fixed std set. Both are the streng atomic-epoch shape (findings →
// one commit → fence).

// migrateFaultHook is a TEST-ONLY fault-injection seam (ADR-08 red-path "commit
// atomicity"): when non-nil it is called inside the --commit transaction AFTER
// the epoch row + std_manifest are inserted but BEFORE the fence flips; a non-nil
// return aborts, and the deferred Rollback proves all-or-nothing — the fleet never
// observes a half-epoch. Production leaves this nil.
var migrateFaultHook func() error

func migrateFault() error {
	if migrateFaultHook != nil {
		return migrateFaultHook()
	}
	return nil
}

// MigrationFinding is one ADR-08 §3 dry-run finding row (the 400-breaks operator
// work queue). Rule ∈ {ok, needs-hold, undecodable}; a needs-hold or undecodable
// finding without a resolution BLOCKS --commit (fail-closed).
type MigrationFinding struct {
	Epoch   int    `json:"epoch"`
	Scope   string `json:"scope"`
	Subject string `json:"subject"`
	Rule    string `json:"rule"`
	Loc     string `json:"loc,omitempty"`
	Message string `json:"message,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// MigrateDryRun re-runs the epoch-`target` compatibility check over the whole
// world and writes the result as migration_finding ROWS. It mutates NOTHING else
// — no definitions move, no continuation is touched, the epoch is not advanced
// (ADR-08 §3, red-path a). It is repeatable: each run rewrites the target epoch's
// finding set. banTags is the set of Value tags the target epoch would REMOVE
// from the serializable lattice (O4); a sleeping continuation holding one is a
// `needs-hold` finding.
func MigrateDryRun(ctx context.Context, conn *pgwire.Conn, target int, banTags []cek.Tag) ([]MigrationFinding, error) {
	cur, err := currentEpoch(ctx, conn)
	if err != nil {
		return nil, err
	}
	if target <= cur {
		return nil, fmt.Errorf("migrate: target epoch %d is not ahead of the live epoch %d", target, cur)
	}
	findings, err := scanFindings(ctx, conn, target, banTags)
	if err != nil {
		return nil, err
	}

	// The ONLY mutation: replace the target epoch's finding set. Everything else
	// — definitions, continuations, the epoch pointer — is untouched.
	if err := conn.BeginSerializable(ctx); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = conn.Rollback(ctx)
		}
	}()
	if _, err := conn.Exec(ctx, `DELETE FROM migration_finding WHERE epoch=$1`, target); err != nil {
		return nil, err
	}
	for _, f := range findings {
		if _, err := conn.Exec(ctx, `
INSERT INTO migration_finding (epoch, scope, subject, rule, loc, message, fix)
VALUES ($1,$2,$3,$4,$5,$6,$7)`, f.Epoch, f.Scope, f.Subject, f.Rule, f.Loc, f.Message, f.Fix); err != nil {
			return nil, err
		}
	}
	if err := conn.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	return findings, nil
}

// scanFindings classifies every sleeping/parked continuation and every definition
// under the target epoch, WITHOUT any mutation. A continuation whose CFR blob no
// longer decodes is `undecodable` (O3 fail-closed); one holding a to-be-banned tag
// is `needs-hold` (O4); otherwise `ok`. Definitions are checked by the encoder leg
// (decode-by-hash): an undecodable stored AST is a release blocker.
func scanFindings(ctx context.Context, conn *pgwire.Conn, target int, banTags []cek.Tag) ([]MigrationFinding, error) {
	banned := map[cek.Tag]bool{}
	for _, t := range banTags {
		banned[t] = true
	}
	var out []MigrationFinding

	rows, err := conn.Query(ctx, `
SELECT id::text, encode(frames,'hex') FROM continuation
 WHERE status IN ('sleeping','ready','condition','running')`)
	if err != nil {
		return nil, err
	}
	type cont struct{ id, framesHex string }
	var conts []cont
	for rows.Next() {
		var c cont
		if err := rows.Scan(&c.id, &c.framesHex); err != nil {
			rows.Close()
			return nil, err
		}
		conts = append(conts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range conts {
		frames, derr := hexToBytes(c.framesHex)
		if derr != nil {
			out = append(out, MigrationFinding{Epoch: target, Scope: "continuation", Subject: c.id,
				Rule: "undecodable", Message: "frames column is not decodable hex: " + derr.Error(),
				Fix: "quarantine + restore from CFR fixture / physical backup (ADR-05 §8)"})
			continue
		}
		st, cerr := cfr.Decode(frames)
		if cerr != nil {
			out = append(out, MigrationFinding{Epoch: target, Scope: "continuation", Subject: c.id,
				Rule: "undecodable", Message: cerr.Error(),
				Fix: "quarantine; a readerless CFR blob blocks the epoch (append-only readers, ADR-05 §8)"})
			continue
		}
		held := ""
		if len(banned) > 0 {
			for tag := range cfr.StateTags(st) {
				if banned[tag] {
					held = fmt.Sprintf("holds banned lattice tag %d", tag)
					break
				}
			}
		}
		if held != "" {
			out = append(out, MigrationFinding{Epoch: target, Scope: "continuation", Subject: c.id,
				Rule: "needs-hold", Message: held,
				Fix: "resolve the sleeping continuation before commit (O4, ADR-08 §4)"})
			continue
		}
		out = append(out, MigrationFinding{Epoch: target, Scope: "continuation", Subject: c.id, Rule: "ok"})
	}

	// Definition encoder leg: every stored AST must still decode (O1/O3).
	drows, err := conn.Query(ctx, `SELECT hash, encode(ast,'hex') FROM definition`)
	if err != nil {
		return nil, err
	}
	type defrow struct{ hash, astHex string }
	var defs []defrow
	for drows.Next() {
		var d defrow
		if err := drows.Scan(&d.hash, &d.astHex); err != nil {
			drows.Close()
			return nil, err
		}
		defs = append(defs, d)
	}
	if err := drows.Err(); err != nil {
		return nil, err
	}
	for _, d := range defs {
		if bad := checkDefEncoderLeg(d.hash, d.astHex); bad != "" {
			out = append(out, MigrationFinding{Epoch: target, Scope: "definition", Subject: d.hash,
				Rule: "undecodable", Message: bad,
				Fix: "restore the stored AST (self-certifying byte-restore, ADR-02 §5.5)"})
		}
	}
	return out, nil
}

// MigrateCommit advances the live epoch to `target` in ONE all-or-nothing
// SERIALIZABLE transaction (ADR-08 §3). It re-runs the O4 enumeration INSIDE the
// transaction (TOCTOU closed, R1-05) and REFUSES if any continuation is
// undecodable or holds a banned tag — fail-closed. The epoch row carries the
// current (unchanged) std pair; std_manifest membership for the new epoch is
// materialized from the live std pointers; epoch_current flips and NOTIFY epoch
// publishes the signal, all in the same commit.
func MigrateCommit(ctx context.Context, conn *pgwire.Conn, target int, banTags []cek.Tag) error {
	if err := conn.BeginSerializable(ctx); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = conn.Rollback(ctx)
		}
	}()

	var cur int
	if _, err := conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &cur); err != nil {
		return err
	}
	if target <= cur {
		return fmt.Errorf("migrate --commit: target %d is not ahead of the live epoch %d (concurrent migrate?)", target, cur)
	}
	var exists int
	if found, err := conn.QueryRow(ctx, `SELECT 1 FROM epoch WHERE n=$1`, []any{target}, &exists); err != nil {
		return err
	} else if found {
		return fmt.Errorf("migrate --commit: epoch %d already exists", target)
	}

	// AUTHORITATIVE O4 enumeration, in-transaction (ADR-08 §4). A block here is
	// fail-closed: the flip does not land while any continuation is unresolvable.
	blockers, err := scanFindings(ctx, conn, target, banTags)
	if err != nil {
		return err
	}
	for _, f := range blockers {
		if f.Rule == "undecodable" || f.Rule == "needs-hold" {
			return fmt.Errorf("migrate --commit REFUSED (fail-closed): %s %s is %s — %s",
				f.Scope, f.Subject, f.Rule, f.Message)
		}
	}

	// Insert the epoch row carrying the CURRENT (unchanged) std pair.
	if _, err := conn.Exec(ctx, `
INSERT INTO epoch (n, std_manifest_root, dispatch_attestation, supersedes)
SELECT $1, std_manifest_root, dispatch_attestation, $2 FROM epoch WHERE n=$2`, target, cur); err != nil {
		return err
	}
	// Materialize std_manifest membership for the new epoch from the live std set.
	if _, err := conn.Exec(ctx, `
INSERT INTO std_manifest (epoch, hash)
SELECT $1, hash FROM name_pointer
 WHERE scope_kind=0 AND scope_id='' AND name LIKE 'std/%'
ON CONFLICT DO NOTHING`, target); err != nil {
		return err
	}
	// Commit-atomicity fault seam: a kill here must leave NO half-epoch.
	if err := migrateFault(); err != nil {
		return err
	}
	// Flip the fence row + publish the signal — the O5 guard every kernel reads.
	if _, err := conn.Exec(ctx, `UPDATE epoch_current SET n=$1 WHERE one=true`, target); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`NOTIFY epoch, '%d'`, target)); err != nil {
		return err
	}
	if err := conn.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// MigrateCommitImage is MigrateCommit for a REAL std change (BUILD-F R9): it
// advances the live epoch to `target` AND slots the new epoch's (std-manifest-root,
// dispatch-attestation) pair carried by `newImage`, admitting the std DELTA — every
// newImage std entry not already catalogued — as ordinary immortal rows inside the
// SAME all-or-nothing SERIALIZABLE transaction (ADR-08 §3: "insert std-N mirror
// rows + std_manifest + epoch row" as one commit). Unlike MigrateCommit (which
// carries the current unchanged pair forward), this is the path the R9 residue
// named: the migrate machinery exercised across a genuinely new std-manifest-root.
//
// The same O4 in-transaction enumeration fences it fail-closed. After commit,
// VerifyBoot(newImage) recomputes the catalog std-manifest-root over the now-larger
// std pointer set and matches newImage.ManifestRoot — proving the delta was really
// slotted; an old-image binary refuses boot on the manifest-root mismatch.
func MigrateCommitImage(ctx context.Context, conn *pgwire.Conn, target int, newImage *Image, banTags []cek.Tag) error {
	if newImage.Epoch != target {
		return fmt.Errorf("migrate --commit: newImage epoch %d != target %d", newImage.Epoch, target)
	}
	if err := conn.BeginSerializable(ctx); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = conn.Rollback(ctx)
		}
	}()

	var cur int
	if _, err := conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &cur); err != nil {
		return err
	}
	if target <= cur {
		return fmt.Errorf("migrate --commit: target %d is not ahead of the live epoch %d (concurrent migrate?)", target, cur)
	}
	var exists int
	if found, err := conn.QueryRow(ctx, `SELECT 1 FROM epoch WHERE n=$1`, []any{target}, &exists); err != nil {
		return err
	} else if found {
		return fmt.Errorf("migrate --commit: epoch %d already exists", target)
	}

	// AUTHORITATIVE O4 enumeration, in-transaction (ADR-08 §4). Fail-closed.
	blockers, err := scanFindings(ctx, conn, target, banTags)
	if err != nil {
		return err
	}
	for _, f := range blockers {
		if f.Rule == "undecodable" || f.Rule == "needs-hold" {
			return fmt.Errorf("migrate --commit REFUSED (fail-closed): %s %s is %s — %s",
				f.Scope, f.Subject, f.Rule, f.Message)
		}
	}

	// Admit the std DELTA as ordinary immortal rows (a prepared std re-admission,
	// ADR-08 §3), under one migration admission row. Every newImage std entry whose
	// hash is not already catalogued lands here; unchanged std keeps its address
	// (content-addressing, ADR-02 §6 — nothing is re-hashed).
	verify := func(hash string, ast []byte) error {
		nn, derr := rast.Decode(ast)
		if derr != nil {
			return derr
		}
		if !rast.Verify(nn, hash) {
			return fmt.Errorf("hash mismatch for %s", hash)
		}
		return nil
	}
	// The delta is keyed on the NAME POINTER, not the hash: every std TYPE shares
	// the opaque `unknown` genesis body, so a new std type reuses an existing
	// definition hash (InsertDefinition ON CONFLICT DO NOTHING) but still needs a
	// fresh std/<mod>/<name> pointer. Keying on the definition hash would miss it.
	var delta []*Entry
	for _, e := range newImage.Entries {
		var one int
		found, qerr := conn.QueryRow(ctx,
			`SELECT 1 FROM name_pointer WHERE scope_kind=0 AND scope_id='' AND name=$1`, []any{e.CatalogName}, &one)
		if qerr != nil {
			return qerr
		}
		if !found {
			delta = append(delta, e)
		}
	}
	if len(delta) > 0 {
		hashes := make([]string, 0, len(delta))
		for _, e := range delta {
			hashes = append(hashes, e.Hash)
		}
		var admissionID int64
		if _, err := conn.QueryRow(ctx, `
INSERT INTO admission (actor_kind, actor_id, via, submitted_hashes, verifier_report)
VALUES ('system', 'migrate', 'cli', $1::text[], '{"migrate_std_delta":true}'::jsonb) RETURNING id`,
			[]any{hashes}, &admissionID); err != nil {
			return fmt.Errorf("migrate: insert admission: %w", err)
		}
		for _, e := range delta {
			def := catalog.Def{
				Hash:          e.Hash,
				ASTSchemaVer:  rast.SchemaVersion,
				Kind:          e.CatalogKind,
				AST:           rast.Encode(e.Body),
				CanonicalText: e.CanonicalText,
				AdmissionID:   admissionID,
			}
			if _, err := catalog.InsertDefinition(ctx, conn, def, verify); err != nil {
				return fmt.Errorf("migrate: insert %s: %w", e.CatalogName, err)
			}
			if _, err := catalog.InsertMeta(ctx, conn, catalog.Meta{Hash: e.Hash}); err != nil {
				return fmt.Errorf("migrate: insert meta %s: %w", e.CatalogName, err)
			}
			moved, err := catalog.UpsertPointerCAS(ctx, conn, catalog.Pointer{
				Name:        e.CatalogName,
				ScopeKind:   0,
				ScopeID:     "",
				Kind:        e.CatalogKind,
				Visibility:  "exported",
				Hash:        e.Hash,
				AdmissionID: admissionID,
			}, nil)
			if err != nil {
				return fmt.Errorf("migrate: pointer %s: %w", e.CatalogName, err)
			}
			if !moved {
				return fmt.Errorf("migrate: pointer %s already present", e.CatalogName)
			}
		}
	}

	// Insert the epoch row carrying the NEW std pair (root + attestation from the
	// epoch-N image) — the R9 delta vs MigrateCommit, which copies the current pair.
	if _, err := conn.Exec(ctx, `
INSERT INTO epoch (n, std_manifest_root, dispatch_attestation, supersedes)
VALUES ($1, $2, $3, $4)`, target, newImage.ManifestRoot, newImage.Attestation, cur); err != nil {
		return err
	}
	// Materialize std_manifest membership for the new epoch from the (now-updated)
	// live std pointer set — it now includes the delta.
	if _, err := conn.Exec(ctx, `
INSERT INTO std_manifest (epoch, hash)
SELECT $1, hash FROM name_pointer
 WHERE scope_kind=0 AND scope_id='' AND name LIKE 'std/%'
ON CONFLICT DO NOTHING`, target); err != nil {
		return err
	}
	if err := migrateFault(); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `UPDATE epoch_current SET n=$1 WHERE one=true`, target); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`NOTIFY epoch, '%d'`, target)); err != nil {
		return err
	}
	if err := conn.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// RevertEpoch executes the ADR-08 §6a revert motion: a NEW epoch row `target`
// (= bad+1) that carries the prior-good `revertTo` pair and supersedes the bad
// epoch, landed in one SERIALIZABLE commit. Every dependent bound to the bad
// epoch — a continuation stepped or parked under it — is HELD FAIL-CLOSED
// (L1): an epoch_hold row is written and its status flips to 'condition' so the
// reactor never resumes it against the reverted world. Returns the ids held.
func RevertEpoch(ctx context.Context, conn *pgwire.Conn, target, revertTo int) ([]string, error) {
	if err := conn.BeginSerializable(ctx); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = conn.Rollback(ctx)
		}
	}()

	var bad int
	if _, err := conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &bad); err != nil {
		return nil, err
	}
	if target <= bad {
		return nil, fmt.Errorf("revert: target %d must be ahead of the bad epoch %d", target, bad)
	}
	var okRoot, okAttest string
	found, err := conn.QueryRow(ctx,
		`SELECT std_manifest_root, dispatch_attestation FROM epoch WHERE n=$1`,
		[]any{revertTo}, &okRoot, &okAttest)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("revert: prior-good epoch %d not found", revertTo)
	}
	// Revert constraint (ADR-08 §6a): a pure revert to a prior BINARY is sound only
	// while nothing depends on what only the bad epoch can read. This build never
	// bumps r<n> within the epoch machinery, so the blast query below is the whole
	// check; a real r-bump revert would carry the additive readers instead.

	if _, err := conn.Exec(ctx, `
INSERT INTO epoch (n, std_manifest_root, dispatch_attestation, supersedes)
VALUES ($1, $2, $3, $4)`, target, okRoot, okAttest, bad); err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO std_manifest (epoch, hash) SELECT $1, hash FROM std_manifest WHERE epoch=$2
ON CONFLICT DO NOTHING`, target, revertTo); err != nil {
		return nil, err
	}

	// HOLD the blast: continuations bound to the bad epoch (stamped with it, or
	// stepped since it released) that are still live. Fail-closed.
	//
	// R10 (BUILD-F, ADR-08 §6a / ADR-13 §3 `epoch.hold_fence_ms`): the hold is
	// SET-BASED, not a per-dependent round trip. A dependents-heavy revert (a busy
	// tenant with thousands of parked/live continuations bound to the bad epoch) must
	// fence in O(1) round trips inside the single SERIALIZABLE commit, not O(N) — the
	// per-row loop this replaced made 2N round trips and blew the fence-cost budget at
	// scale (witnessed red in evidence-f/r10/). The predicate below is the blast
	// closure; it is evaluated identically for the id enumeration (return value), the
	// bulk INSERT, and the bulk UPDATE — the flip to 'condition' happens only in the
	// UPDATE, so all three see the same set.
	const blastPredicate = `status IN ('sleeping','ready','condition','running')
   AND (epoch = $1
        OR updated_at >= (SELECT created_at FROM epoch WHERE n=$1))`

	rows, err := conn.Query(ctx, `SELECT id::text FROM continuation WHERE `+blastPredicate, bad)
	if err != nil {
		return nil, err
	}
	var held []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		held = append(held, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(held) > 0 {
		// One INSERT ... SELECT over the blast closure — every held dependent gets its
		// fail-closed epoch_hold audit row in a single statement.
		if _, err := conn.Exec(ctx, `
INSERT INTO epoch_hold (continuation_id, bad_epoch, revert_epoch, reason)
SELECT id, $1, $2, $3 FROM continuation WHERE `+blastPredicate+`
ON CONFLICT (continuation_id, bad_epoch) DO NOTHING`,
			bad, target, fmt.Sprintf("bound to reverted epoch %d", bad)); err != nil {
			return nil, err
		}
		// One UPDATE flips the whole blast set to 'condition' — the reactor never
		// resumes them against the reverted world.
		if _, err := conn.Exec(ctx,
			`UPDATE continuation SET status='condition', updated_at=now() WHERE `+blastPredicate, bad); err != nil {
			return nil, err
		}
	}

	if _, err := conn.Exec(ctx, `UPDATE epoch_current SET n=$1 WHERE one=true`, target); err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`NOTIFY epoch, '%d'`, target)); err != nil {
		return nil, err
	}
	if err := conn.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	return held, nil
}

func currentEpoch(ctx context.Context, conn *pgwire.Conn) (int, error) {
	var n int
	found, err := conn.QueryRow(ctx, `SELECT n FROM epoch_current WHERE one=true`, nil, &n)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("no live epoch (run genesis first)")
	}
	return n, nil
}
