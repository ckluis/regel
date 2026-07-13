package admission

import (
	"context"
	"testing"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/pgwire"
)

// bootstrapAndGenesis applies the substrate DDL then runs genesis over conn.
func bootstrapAndGenesis(ctx context.Context, conn *pgwire.Conn) error {
	if err := catalog.Bootstrap(ctx, conn, ""); err != nil {
		return err
	}
	return Genesis(ctx, conn, BuildImage())
}

// TestGenesisIdempotentAndAttested verifies genesis populates the catalog, that
// a second genesis is a no-op that passes boot parity, and that the epoch row
// pins the manifest root + dispatch attestation the binary computes (ADR-10 §2).
func TestGenesisIdempotentAndAttested(t *testing.T) {
	w := setupWorld(t) // already runs genesis once
	ctx := ctxT(t)
	im := BuildImage()

	// Every std entry is catalogued as a product-scope pointer.
	for _, e := range im.Entries {
		var one int
		ok, err := w.conn.QueryRow(ctx,
			`SELECT 1 FROM name_pointer WHERE name=$1 AND scope_kind=0 AND scope_id='' AND hash=$2`,
			[]any{e.CatalogName, e.Hash}, &one)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("std entry %s (%s) not catalogued", e.CatalogName, e.Hash)
		}
	}

	// Epoch row pins the binary's roots.
	var root, attest string
	if _, err := w.conn.QueryRow(ctx,
		`SELECT std_manifest_root, dispatch_attestation FROM epoch WHERE n=1`, nil, &root, &attest); err != nil {
		t.Fatal(err)
	}
	if root != im.ManifestRoot {
		t.Fatalf("epoch manifest root %s != binary %s", root, im.ManifestRoot)
	}
	if attest != im.Attestation {
		t.Fatalf("epoch attestation %s != binary %s", attest, im.Attestation)
	}

	// A second genesis is idempotent and passes boot parity.
	defs := w.count("SELECT count(*) FROM definition")
	if err := Genesis(ctx, w.conn, im); err != nil {
		t.Fatalf("second genesis: %v", err)
	}
	if got := w.count("SELECT count(*) FROM definition"); got != defs {
		t.Fatalf("second genesis inserted rows (%d → %d)", defs, got)
	}
	if err := VerifyBoot(ctx, w.conn, im); err != nil {
		t.Fatalf("VerifyBoot: %v", err)
	}
}

// TestBuildImageDeterministic asserts the image hashes are stable across builds
// (the genesis reproducibility floor — two boots of one binary agree).
func TestBuildImageDeterministic(t *testing.T) {
	a := buildImage()
	b := buildImage()
	if a.ManifestRoot != b.ManifestRoot {
		t.Fatalf("manifest root not deterministic: %s vs %s", a.ManifestRoot, b.ManifestRoot)
	}
	if a.Attestation != b.Attestation {
		t.Fatalf("attestation not deterministic")
	}
	for i := range a.Entries {
		if a.Entries[i].Hash != b.Entries[i].Hash {
			t.Fatalf("entry %s hash not deterministic", a.Entries[i].CatalogName)
		}
	}
}
