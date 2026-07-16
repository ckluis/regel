package m5eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"regel.dev/regel/internal/pgwire"
)

// harness_test.go stands up a real DB + regel binary for the gate tests. The
// deterministic (DB-only) tests here run in ordinary CI when PG is present and
// SKIP cleanly otherwise. The LLM orchestrator (m5llm_test.go) is additionally
// gated on REGEL_M5_LLM=1 so a CI-without-LLM run stays green and honest.

func baseDSN() string {
	if d := os.Getenv("REGEL_PG_TEST_DSN"); d != "" {
		return d
	}
	return "postgres://clank@localhost:5432/postgres"
}

func randDB() string {
	var b [6]byte
	rand.Read(b[:])
	return "regel_m5_" + hex.EncodeToString(b[:])
}

// gworld is a bootstrapped scratch DB with a bound agent key + built binary.
type gworld struct {
	t       *testing.T
	dsn     string
	binPath string
	conn    *pgwire.Conn
	epoch   int
	subject string
	scope   string
}

var builtBin string

func buildRegel(t *testing.T) string {
	t.Helper()
	if builtBin != "" {
		return builtBin
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "regel-m5")
	// The temp dir is per-test; cache across tests by leaking one build into a
	// stable path under the OS temp root.
	stable := filepath.Join(os.TempDir(), "regel-m5-eval-bin")
	cmd := exec.Command("go", "build", "-o", stable, "./cmd/regel")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build regel: %v\n%s", err, out)
	}
	_ = bin
	builtBin = stable
	return stable
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// gate/m5eval → repo root is two levels up.
	wd, _ := os.Getwd()
	return filepath.Dir(filepath.Dir(wd))
}

func setupGate(t *testing.T) *gworld {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base, err := pgwire.ParseDSN(baseDSN())
	if err != nil {
		t.Skipf("no test PG: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Skipf("connect admin PG: %v", err)
	}
	db := randDB()
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		admin.Close()
		t.Fatalf("create db: %v", err)
	}
	admin.Close()

	bin := buildRegel(t)
	cfg := base
	cfg.Database = db
	dsn := dsnFor(baseDSN(), db)

	run := func(args ...string) {
		c := exec.Command(bin, args...)
		c.Env = append([]string{"REGEL_PG_DSN=" + dsn}, os.Environ()...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("regel %v: %v\n%s", args, err, out)
		}
	}
	run("migrate-db")
	run("genesis")
	subject := "agent:a1"
	run("agent-key", "--key", "m5-agent", "--actor", "a1", "--scope-id", "org1")

	conn, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	epoch, err := CurrentEpoch(ctx, conn)
	if err != nil {
		t.Fatalf("epoch: %v", err)
	}
	w := &gworld{t: t, dsn: dsn, binPath: bin, conn: conn, epoch: epoch, subject: subject, scope: "org.org1"}
	t.Cleanup(func() {
		conn.Close()
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	})
	return w
}

// dsnFor rewrites the database in a DSN.
func dsnFor(dsn, db string) string {
	cfg, err := pgwire.ParseDSN(dsn)
	if err != nil {
		return dsn
	}
	u := "postgres://"
	if cfg.User != "" {
		u += cfg.User + "@"
	}
	u += cfg.Host
	if cfg.Port != "" {
		u += ":" + cfg.Port
	}
	return u + "/" + db
}

func (w *gworld) startMCP() *MCPSession {
	w.t.Helper()
	ctx := context.Background()
	sess, err := StartMCP(ctx, w.binPath, "m5-agent", w.dsn)
	if err != nil {
		w.t.Fatalf("start mcp: %v", err)
	}
	w.t.Cleanup(sess.Close)
	return sess
}
