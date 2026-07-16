package main

// vaultput_test.go — STAGE-E D12 red-path for the `regel vault-put` CLI door.
//
// RED evidence: with the `case "vault-put"` arm removed from main()'s switch (or
// cmdVaultPut deleted), the built binary answers `vault-put` with
// "regel: unknown command \"vault-put\"" and exit 2 — TestVaultPutCLIDoor fails at
// the `run(... "vault-put" ...)` step. The control (the CLI door calling the REAL
// admission.VaultPut over stdin) is what turns it green. The test also proves the
// secret is passed on STDIN, never argv: the args slice handed to the process
// carries no plaintext, yet the ciphertext round-trips through VaultReveal.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/pgwire"
)

func testBaseDSN() string {
	if d := os.Getenv("REGEL_PG_TEST_DSN"); d != "" {
		return d
	}
	return "postgres://clank@localhost:5432/postgres"
}

// TestVaultPutCLIDoor drives the compiled binary end-to-end: migrate → genesis →
// admit a resource with a pii field → insert a base row → `vault-put` the secret
// over stdin → confirm the ciphertext round-trips through the real VaultReveal.
func TestVaultPutCLIDoor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	base, err := pgwire.ParseDSN(testBaseDSN())
	if err != nil {
		t.Skipf("no test PG: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Skipf("connect admin: %v", err)
	}
	defer admin.Close()

	var b [6]byte
	rand.Read(b[:])
	db := "regel_vp_" + hex.EncodeToString(b[:])
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() {
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	})

	cfg := base
	cfg.Database = db
	dsn := "postgres://" + cfg.User + "@" + cfg.Host + ":" + cfg.Port + "/" + db

	// Build the binary once.
	bin := filepath.Join(t.TempDir(), "regel")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	env := append(os.Environ(), "REGEL_PG_DSN="+dsn)
	run := func(stdin string, args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		return out.String(), err
	}

	if out, err := run("", "migrate-db"); err != nil {
		t.Fatalf("migrate-db: %v\n%s", err, out)
	}
	if out, err := run("", "genesis"); err != nil {
		t.Fatalf("genesis: %v\n%s", err, out)
	}

	// Admit a resource with a pii field so a derived table + vault key exist.
	resSrc := `import { resource } from "std/resource";
export const Contact = resource({ fields: { name: "text", email: "pii:email" } });`
	resFile := filepath.Join(t.TempDir(), "contact.ts")
	if err := os.WriteFile(resFile, []byte(resSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run("", "admit", resFile, "--name-prefix", "app/vp", "--actor", "engineer:dev"); err != nil {
		t.Fatalf("admit: %v\n%s", err, out)
	}

	// Insert a base row and grab its id + the derived physical table name.
	conn, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var table string
	if ok, err := conn.QueryRow(ctx,
		`SELECT table_name FROM derived_resource WHERE resource_name='app/vp/Contact' AND scope_kind=0 AND scope_id=''`,
		nil, &table); err != nil || !ok {
		t.Fatalf("derived_resource lookup ok=%v err=%v", ok, err)
	}
	var id int64
	if _, err := conn.QueryRow(ctx,
		`INSERT INTO `+quoteTable(table)+` (name) VALUES ('Ada') RETURNING id`, nil, &id); err != nil {
		t.Fatal(err)
	}
	subj := itoa64(id)

	// THE DOOR: secret on stdin, never argv.
	const secret = "ada@example.com"
	out, err := run(secret, "vault-put", "--resource", "app/vp/Contact", "--subject", subj, "--field", "email", "--scope", "product")
	if err != nil {
		t.Fatalf("vault-put: %v\n%s", err, out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("vault-put stdout leaked the plaintext: %q", out)
	}

	// The ciphertext round-trips through the REAL VaultReveal (same AEAD).
	pt, ok, err := admission.VaultReveal(ctx, conn, table, subj, "email")
	if err != nil || !ok || pt != secret {
		t.Fatalf("VaultReveal = %q ok=%v err=%v, want %q — CLI must seal via the real VaultPut", pt, ok, err, secret)
	}

	// And the pii field never derived a base column (vault-routed only).
	var emailCols int64
	if _, err := conn.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_name=$1 AND column_name='email'`,
		[]any{table}, &emailCols); err != nil {
		t.Fatal(err)
	}
	if emailCols != 0 {
		t.Fatal("pii field email must not derive a base column")
	}
}

func quoteTable(t string) string { return `"` + strings.ReplaceAll(t, `"`, `""`) + `"` }

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
