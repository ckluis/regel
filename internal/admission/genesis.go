package admission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
)

// Genesis runs the ADR-10 §2 Stage-A bootstrap: it populates the catalog with
// the micro-std image in one transaction (the only gate bypass — the gate ran at
// build over the same bytes) and pins the epoch row. It is idempotent: against a
// populated catalog it verifies the stored manifest/attestation match the running
// binary and refuses (returns an error) on mismatch, without re-inserting.
//
// The substrate DDL (catalog.Bootstrap) must already be applied.
func Genesis(ctx context.Context, conn *pgwire.Conn, im *Image) error {
	var n int
	var storedRoot, storedAttest string
	populated, err := conn.QueryRow(ctx,
		`SELECT n, std_manifest_root, dispatch_attestation FROM epoch WHERE n = $1`,
		[]any{im.Epoch}, &n, &storedRoot, &storedAttest)
	if err != nil {
		return fmt.Errorf("genesis: read epoch: %w", err)
	}
	if populated {
		if storedRoot != im.ManifestRoot || storedAttest != im.Attestation {
			return fmt.Errorf("genesis: epoch %d mismatch — binary std does not match catalog (root %s vs %s)",
				im.Epoch, im.ManifestRoot, storedRoot)
		}
		return VerifyBoot(ctx, conn, im)
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

	hashes := make([]string, 0, len(im.Entries))
	for _, e := range im.Entries {
		hashes = append(hashes, e.Hash)
	}
	var admissionID int64
	if _, err := conn.QueryRow(ctx, `
INSERT INTO admission (actor_kind, actor_id, via, submitted_hashes, verifier_report)
VALUES ('system', 'genesis', 'cli', $1::text[], '{"genesis":true}'::jsonb) RETURNING id`,
		[]any{hashes}, &admissionID); err != nil {
		return fmt.Errorf("genesis: insert admission: %w", err)
	}

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
	for _, e := range im.Entries {
		def := catalog.Def{
			Hash:          e.Hash,
			ASTSchemaVer:  rast.SchemaVersion,
			Kind:          e.CatalogKind,
			AST:           rast.Encode(e.Body),
			CanonicalText: e.CanonicalText,
			AdmissionID:   admissionID,
		}
		if _, err := catalog.InsertDefinition(ctx, conn, def, verify); err != nil {
			return fmt.Errorf("genesis: insert %s: %w", e.CatalogName, err)
		}
		if _, err := catalog.InsertMeta(ctx, conn, catalog.Meta{Hash: e.Hash}); err != nil {
			return fmt.Errorf("genesis: insert meta %s: %w", e.CatalogName, err)
		}
		ptr := catalog.Pointer{
			Name:        e.CatalogName,
			ScopeKind:   0,
			ScopeID:     "",
			Kind:        e.CatalogKind,
			Visibility:  "exported",
			Hash:        e.Hash,
			AdmissionID: admissionID,
		}
		moved, err := catalog.UpsertPointerCAS(ctx, conn, ptr, nil)
		if err != nil {
			return fmt.Errorf("genesis: pointer %s: %w", e.CatalogName, err)
		}
		if !moved {
			return fmt.Errorf("genesis: pointer %s already present (non-empty catalog?)", e.CatalogName)
		}
	}

	if _, err := conn.Exec(ctx, `
INSERT INTO epoch (n, std_manifest_root, dispatch_attestation) VALUES ($1, $2, $3)`,
		im.Epoch, im.ManifestRoot, im.Attestation); err != nil {
		return fmt.Errorf("genesis: insert epoch: %w", err)
	}

	if err := conn.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return VerifyBoot(ctx, conn, im)
}

// VerifyBoot is the standing boot parity check (ADR-10 §2 steps 3-4): it
// recomputes the std-manifest root from the catalog and the dispatch attestation
// from the running binary and compares both to the pinned epoch row. A mismatch
// is a boot refusal — the gate never opens on an unattested dispatch table.
//
// STAGE-A RESIDUE: the dispatch bijection is proven by manifest-root equality
// (catalog std set == binary std set) plus a per-native catalogued check, not by
// decoding every definition's AST to classify NativeBody rows.
func VerifyBoot(ctx context.Context, conn *pgwire.Conn, im *Image) error {
	var storedRoot, storedAttest string
	found, err := conn.QueryRow(ctx,
		`SELECT std_manifest_root, dispatch_attestation FROM epoch WHERE n = $1`,
		[]any{im.Epoch}, &storedRoot, &storedAttest)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("boot: no epoch %d row (run genesis first)", im.Epoch)
	}
	if storedAttest != im.Attestation {
		return fmt.Errorf("boot refused: dispatch attestation mismatch (pinned %s, computed %s)",
			storedAttest, im.Attestation)
	}
	if storedRoot != im.ManifestRoot {
		return fmt.Errorf("boot refused: std-manifest root mismatch (pinned %s, binary %s)",
			storedRoot, im.ManifestRoot)
	}

	// Recompute the manifest root from the catalog's std pointers and compare.
	rows, err := conn.Query(ctx,
		`SELECT name, hash FROM name_pointer WHERE scope_kind = 0 AND scope_id = '' AND name LIKE 'std/%'`)
	if err != nil {
		return err
	}
	var lines []string
	for rows.Next() {
		var name, hash string
		if err := rows.Scan(&name, &hash); err != nil {
			rows.Close()
			return err
		}
		lines = append(lines, name+"="+hash)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	catalogRoot := hex.EncodeToString(sum[:])
	if catalogRoot != im.ManifestRoot {
		return fmt.Errorf("boot refused: catalog std set does not match binary (catalog root %s, binary %s)",
			catalogRoot, im.ManifestRoot)
	}

	// Per-native: every dispatched hash is catalogued (dispatch bijection floor).
	for _, e := range im.Entries {
		if e.Native == nil {
			continue
		}
		var one int
		ok, err := conn.QueryRow(ctx, `SELECT 1 FROM definition WHERE hash = $1`, []any{e.Hash}, &one)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("boot refused: native %s has no catalogued definition %s", e.Intrinsic, e.Hash)
		}
	}
	return nil
}
