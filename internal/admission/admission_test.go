package admission

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"regel.dev/regel/internal/pgwire"
)

// --- scratch-DB harness (real PG 16.13, STAGE-A-PLAN pin #4) ------------------

func baseDSN() string {
	if d := os.Getenv("REGEL_PG_TEST_DSN"); d != "" {
		return d
	}
	return "postgres://clank@localhost:5432/postgres"
}

func randName(prefix string) string {
	var b [6]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// world is a bootstrapped + genesis'd scratch database.
type world struct {
	t    *testing.T
	cfg  pgwire.Config
	db   string
	conn *pgwire.Conn
	im   *Image
}

func setupWorld(t *testing.T) *world {
	t.Helper()
	ctx := ctxT(t)
	base, err := pgwire.ParseDSN(baseDSN())
	if err != nil {
		t.Skipf("no test PG: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Skipf("connect admin: %v", err)
	}
	defer admin.Close()

	db := randName("regel_adm_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create db: %v", err)
	}
	cfg := base
	cfg.Database = db
	conn, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	if err := bootstrapAndGenesis(ctx, conn); err != nil {
		t.Fatalf("bootstrap+genesis: %v", err)
	}
	w := &world{t: t, cfg: cfg, db: db, conn: conn, im: BuildImage()}
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

func (w *world) newConn() *pgwire.Conn {
	c, err := pgwire.Connect(context.Background(), w.cfg)
	if err != nil {
		w.t.Fatalf("newConn: %v", err)
	}
	w.t.Cleanup(func() { c.Close() })
	return c
}

func (w *world) count(query string, args ...any) int {
	var n int
	_, err := w.conn.QueryRow(context.Background(), query, args, &n)
	if err != nil {
		w.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func admit(ctx context.Context, conn *pgwire.Conn, src, prefix string, auth Principal, patchMut func(*Patch)) (Verdict, error) {
	p := Patch{
		Modules:     []ModuleSrc{{ModuleName: prefix, Source: src}},
		TargetScope: Scope{Kind: 0, ID: ""},
		BaseHashes:  map[string]string{},
	}
	if patchMut != nil {
		patchMut(&p)
	}
	return Admit(ctx, conn, p, auth, BuildImage())
}

func engineer(id string) Principal { return Principal{ActorKind: "engineer", ActorID: id, Via: "cli"} }

// --- Family-4 (a): V1 red-path — CAP_UNGRANTED, zero trace, refusal persists --

func TestV1CapUngrantedZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	// A type-correct module importing std/mail and calling mail.send.
	src := `import { send } from "std/mail";
export function notify(): void {
  send("a@b.com", "hi");
}
`
	// Principal granted crm.read only.
	if _, err := w.conn.Exec(ctx,
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ($1,'crm.read','','test')`,
		engineer("dev").Subject()); err != nil {
		t.Fatal(err)
	}

	defsBefore := w.count("SELECT count(*) FROM definition")
	admsBefore := w.count("SELECT count(*) FROM admission")
	refBefore := w.count("SELECT count(*) FROM gate_refusal")

	v, err := admit(ctx, w.conn, src, "app/cap", engineer("dev"), func(p *Patch) {
		p.DeclaredCapabilities = map[string][]string{"app/cap/notify": {"crm.read"}}
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", v.Outcome)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "CAP_UNGRANTED" {
		t.Fatalf("want CAP_UNGRANTED diagnostic, got %+v", v.Diagnostics)
	}
	if v.RefusalID == "" {
		t.Fatal("rejected verdict must carry a refusal_id")
	}

	// ZERO TRACE: no definition, no admission row, no pointer.
	if got := w.count("SELECT count(*) FROM definition"); got != defsBefore {
		t.Fatalf("definition rows changed: %d → %d (must be zero trace)", defsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM admission"); got != admsBefore {
		t.Fatalf("admission rows changed: %d → %d (rejected leaves no admission row)", admsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name = 'app/cap/notify'"); got != 0 {
		t.Fatalf("name_pointer for rejected def exists (%d)", got)
	}
	// But the refusal ledger row persists, carrying the verdict.
	if got := w.count("SELECT count(*) FROM gate_refusal"); got != refBefore+1 {
		t.Fatalf("gate_refusal count %d, want %d", got, refBefore+1)
	}
	if got := w.count("SELECT count(*) FROM gate_refusal WHERE refusal_id = $1 AND outcome='rejected'", v.RefusalID); got != 1 {
		t.Fatalf("gate_refusal row for %s not found", v.RefusalID)
	}
}

// --- Family-4 (b): BAN_CLASS rejection through the full gate, no rows ---------

func TestBanClassRejectedZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	defsBefore := w.count("SELECT count(*) FROM definition")

	v, err := admit(ctx, w.conn, "export class Foo {}\n", "app/ban", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", v.Outcome)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "BAN_CLASS" {
		t.Fatalf("want BAN_CLASS, got %+v", v.Diagnostics)
	}
	if got := w.count("SELECT count(*) FROM definition"); got != defsBefore {
		t.Fatalf("definition rows changed on ban (%d → %d)", defsBefore, got)
	}
	if got := w.count("SELECT count(*) FROM gate_refusal WHERE refusal_id=$1", v.RefusalID); got != 1 {
		t.Fatal("missing refusal row")
	}
}

// --- Family-4 (e): tsgo type error → rejected, zero trace --------------------

func TestTypeErrorRejectedZeroTrace(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	defsBefore := w.count("SELECT count(*) FROM definition")

	// number-typed function returning a string.
	src := "export function bad(): number {\n  return \"hello\";\n}\n"
	v, err := admit(ctx, w.conn, src, "app/te", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].StageOrVerifier != "tsgo" {
		t.Fatalf("want tsgo diagnostic, got %+v", v.Diagnostics)
	}
	if got := w.count("SELECT count(*) FROM definition"); got != defsBefore {
		t.Fatalf("definition rows changed on type error (%d → %d)", defsBefore, got)
	}
}

// --- Family-4 (d): idempotent resubmission → already-admitted, no dup rows ---

func TestIdempotentResubmission(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := "export function greet(name: string): string {\n  return \"hi \" + name;\n}\n"

	v1, err := admit(ctx, w.conn, src, "app/idem", engineer("dev"), nil)
	if err != nil || v1.Outcome != OutcomeAdmitted {
		t.Fatalf("first admit: %v / %q", err, v1.Outcome)
	}
	defs := w.count("SELECT count(*) FROM definition")
	ptrs := w.count("SELECT count(*) FROM name_pointer WHERE name='app/idem/greet'")

	v2, err := admit(ctx, w.conn, src, "app/idem", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("second admit: %v", err)
	}
	if v2.Outcome != OutcomeAlreadyAdmitted {
		t.Fatalf("resubmit outcome = %q, want already-admitted", v2.Outcome)
	}
	if got := w.count("SELECT count(*) FROM definition"); got != defs {
		t.Fatalf("definition rows duplicated on resubmit (%d → %d)", defs, got)
	}
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/idem/greet'"); got != ptrs {
		t.Fatalf("pointer rows changed on resubmit (%d → %d)", ptrs, got)
	}
}

// --- Family-4 (c): concurrent same-name → exactly one admitted ---------------

func TestConcurrentSameNameSingleWinner(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)

	srcA := "export function f(): number {\n  return 1;\n}\n"
	srcB := "export function f(): number {\n  return 2;\n}\n"
	connA := w.newConn()
	connB := w.newConn()

	var wg sync.WaitGroup
	var vA, vB Verdict
	var eA, eB error
	wg.Add(2)
	go func() { defer wg.Done(); vA, eA = admit(ctx, connA, srcA, "app/race", engineer("a"), nil) }()
	go func() { defer wg.Done(); vB, eB = admit(ctx, connB, srcB, "app/race", engineer("b"), nil) }()
	wg.Wait()
	if eA != nil || eB != nil {
		t.Fatalf("errors: %v / %v", eA, eB)
	}

	admittedCount := 0
	for _, v := range []Verdict{vA, vB} {
		if v.Outcome == OutcomeAdmitted {
			admittedCount++
		} else if v.Outcome != OutcomeStaleBase && v.Outcome != OutcomeRetryExhausted {
			t.Fatalf("loser outcome = %q, want stale-base/retry-exhausted", v.Outcome)
		}
	}
	if admittedCount != 1 {
		t.Fatalf("admitted count = %d, want exactly 1 (vA=%q vB=%q)", admittedCount, vA.Outcome, vB.Outcome)
	}
	// The catalog has a single live winner for the name.
	if got := w.count("SELECT count(*) FROM name_pointer WHERE name='app/race/f'"); got != 1 {
		t.Fatalf("name_pointer rows for app/race/f = %d, want 1 (no two-headed catalog)", got)
	}
	// History has no overlapping windows (I4) — one open window.
	if got := w.count("SELECT count(*) FROM name_pointer_history WHERE name='app/race/f' AND valid_to IS NULL"); got != 1 {
		t.Fatalf("open history windows = %d, want 1", got)
	}
}

// bootstrapAndGenesis is defined in genesis_bootstrap_test.go to keep this file
// focused on the family-4 assertions.
var _ = bootstrapAndGenesis
