package admission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
)

// genesisFaultHook is a TEST-ONLY fault-injection seam (ADR-10 §2 kill-test): when
// non-nil it is called at every genesis statement boundary, and a non-nil return
// aborts the transaction. The deferred Rollback then proves the all-or-nothing
// guarantee — a mid-genesis kill leaves an empty-or-complete catalog, never a
// partial one. Production leaves this nil; the sweep test in genesis_gate_test.go
// installs a counter that aborts after the Nth boundary. Tests run sequentially,
// so a plain package var needs no lock.
var genesisFaultHook func() error

func genesisFault() error {
	if genesisFaultHook != nil {
		return genesisFaultHook()
	}
	return nil
}

// BootRefusal is the ADR-08 §2 structured, machine-parseable boot-refuse
// diagnostic (event "epoch.boot_refused"), extended per ADR-10 §2 R1-09 with the
// pinned-vs-computed H_dispatch pair for the attestation-mismatch cause. It is the
// error VerifyBoot returns so an epoch incident stays observable as data — the
// gate never opens on an unattested or drifted dispatch table.
type BootRefusal struct {
	Event               string `json:"event"`
	Reason              string `json:"reason"`
	RequiredEpoch       int    `json:"required_epoch"`
	BinaryManifestRoot  string `json:"binary_manifest_root,omitempty"`
	CatalogManifestRoot string `json:"catalog_manifest_root,omitempty"`
	PinnedHDispatch     string `json:"pinned_h_dispatch,omitempty"`
	ComputedHDispatch   string `json:"computed_h_dispatch,omitempty"`
	OrphanHash          string `json:"orphan_hash,omitempty"`
	Detail              string `json:"detail,omitempty"`
	TS                  string `json:"ts"`
	Action              string `json:"action"`
}

func (e *BootRefusal) Error() string {
	b, _ := json.Marshal(e)
	return "boot refused: " + string(b)
}

func newBootRefusal(reason string, epoch int) *BootRefusal {
	return &BootRefusal{
		Event:         "epoch.boot_refused",
		Reason:        reason,
		RequiredEpoch: epoch,
		TS:            time.Now().UTC().Format(time.RFC3339),
		Action:        "refused_boot",
	}
}

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
	if err := genesisFault(); err != nil {
		return err
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
		if err := genesisFault(); err != nil {
			return err
		}
		if _, err := catalog.InsertMeta(ctx, conn, catalog.Meta{Hash: e.Hash}); err != nil {
			return fmt.Errorf("genesis: insert meta %s: %w", e.CatalogName, err)
		}
		if err := genesisFault(); err != nil {
			return err
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
		if err := genesisFault(); err != nil {
			return err
		}
	}

	if err := genesisFault(); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO epoch (n, std_manifest_root, dispatch_attestation) VALUES ($1, $2, $3)`,
		im.Epoch, im.ManifestRoot, im.Attestation); err != nil {
		return fmt.Errorf("genesis: insert epoch: %w", err)
	}
	if err := genesisFault(); err != nil {
		return err
	}

	// ADR-08 §2: pin the live epoch fence row in the same transaction as the epoch.
	if _, err := conn.Exec(ctx, `
INSERT INTO epoch_current (one, n) VALUES (true, $1)
ON CONFLICT (one) DO UPDATE SET n = EXCLUDED.n`, im.Epoch); err != nil {
		return fmt.Errorf("genesis: pin epoch_current: %w", err)
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
	// H_dispatch attestation: recompute from the running image (im.Attestation is
	// computed at build over the binary's own dispatch table) and compare to the
	// value pinned in the epoch row. A mismatch is a tampered/swapped dispatch
	// table — the structured epoch.boot_refused diagnostic names pinned vs computed
	// (ADR-10 §2 R1-09, ADR-08 §2). The gate never opens on an unattested table.
	if storedAttest != im.Attestation {
		br := newBootRefusal("dispatch_attestation_mismatch", im.Epoch)
		br.PinnedHDispatch = storedAttest
		br.ComputedHDispatch = im.Attestation
		br.Detail = "the running dispatch table does not match the epoch-pinned attestation"
		return br
	}
	if storedRoot != im.ManifestRoot {
		br := newBootRefusal("manifest_root_mismatch", im.Epoch)
		br.CatalogManifestRoot = storedRoot
		br.BinaryManifestRoot = im.ManifestRoot
		br.Detail = "the epoch-pinned std-manifest root does not match the binary"
		return br
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
		br := newBootRefusal("catalog_manifest_root_mismatch", im.Epoch)
		br.CatalogManifestRoot = catalogRoot
		br.BinaryManifestRoot = im.ManifestRoot
		br.Detail = "the catalog std set does not match the binary"
		return br
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
			br := newBootRefusal("dispatch_orphan_hash", im.Epoch)
			br.OrphanHash = e.Hash
			br.Detail = "native " + e.Intrinsic + " has no catalogued definition"
			return br
		}
	}

	// Dispatch bijection (ADR-10 §2 step 3): the running native registry and the
	// catalogued NativeBody set must be in bijection — no orphan hash, no orphan
	// implementation. The registry the kernel runs is im.Registry(), so this
	// exercises the check on the shipped image; the gate test drives the tampered
	// registries that prove it REFUSES. (Residue: the two are built from one source,
	// so production is structurally in bijection — the check is the standing proof.)
	if err := VerifyDispatchBijection(im, im.Registry()); err != nil {
		return err
	}
	return nil
}

// VerifyDispatchBijection asserts the running native dispatch registry and the
// image's catalogued NativeBody set are in BIJECTION (ADR-10 §2 step 3: "asserts
// every catalogued NativeBody hash has a registered implementation, and vice
// versa"). A catalogued native with no registered Go body is an orphan hash; a
// registered body with no catalogued entry is an orphan implementation. Either
// refuses boot, naming the orphan hash — the binary is not a trust root taken on
// faith.
func VerifyDispatchBijection(im *Image, reg *cek.Registry) error {
	// Forward leg: every catalogued native has a registered implementation.
	for _, e := range im.Entries {
		if e.Native == nil {
			continue
		}
		if !reg.Has(e.Hash) {
			br := newBootRefusal("dispatch_orphan_hash", im.Epoch)
			br.OrphanHash = e.Hash
			br.Detail = "catalogued native " + e.Intrinsic + " has no registered Go implementation"
			return br
		}
	}
	// Reverse leg: every registered implementation has a catalogued native entry.
	catalogued := map[string]bool{}
	for _, e := range im.Entries {
		if e.Native != nil {
			catalogued[e.Hash] = true
		}
	}
	for _, h := range reg.Hashes() {
		if !catalogued[h] {
			br := newBootRefusal("dispatch_orphan_implementation", im.Epoch)
			br.OrphanHash = h
			br.Detail = "registered native has no catalogued definition"
			return br
		}
	}
	return nil
}
