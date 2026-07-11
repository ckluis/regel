package pgwire

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// testDSN returns the DSN for the real local Postgres. The default MUST work on
// the build machine (trust auth, role clank). It is not skipped by default.
func testDSN() string {
	if d := os.Getenv("REGEL_PG_TEST_DSN"); d != "" {
		return d
	}
	return "postgres://clank@localhost:5432/postgres"
}

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := ParseDSN(testDSN())
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	return cfg
}

func mustConnect(t *testing.T) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Connect(ctx, testConfig(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestVersion(t *testing.T) {
	c := mustConnect(t)
	defer c.Close()
	var v string
	ok, err := c.QueryRow(ctxT(t), "select version()", nil, &v)
	if err != nil || !ok {
		t.Fatalf("version: ok=%v err=%v", ok, err)
	}
	t.Logf("connected to: %s", v)
}

func TestSimpleAndExtended(t *testing.T) {
	c := mustConnect(t)
	defer c.Close()
	ctx := ctxT(t)

	// simple protocol (multi-statement)
	if _, err := c.ExecSimple(ctx, "SELECT 1; SELECT 2"); err != nil {
		t.Fatalf("ExecSimple: %v", err)
	}
	// extended protocol with params
	var s string
	var n int64
	ok, err := c.QueryRow(ctx, "SELECT $1::text, $2::int8", []any{"hi", int64(42)}, &s, &n)
	if err != nil || !ok {
		t.Fatalf("QueryRow: ok=%v err=%v", ok, err)
	}
	if s != "hi" || n != 42 {
		t.Fatalf("got %q %d", s, n)
	}
}

func TestPreparedReuse(t *testing.T) {
	c := mustConnect(t)
	defer c.Close()
	ctx := ctxT(t)
	sql := "SELECT $1::int8 + 1"
	for i := 0; i < 3; i++ {
		var n int64
		if _, err := c.QueryRow(ctx, sql, []any{int64(i)}, &n); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if n != int64(i)+1 {
			t.Fatalf("iter %d got %d", i, n)
		}
	}
	if len(c.prepared) != 1 {
		t.Fatalf("expected 1 cached statement, got %d", len(c.prepared))
	}
}

func TestTxnBeginCommitRollback(t *testing.T) {
	c := mustConnect(t)
	defer c.Close()
	ctx := ctxT(t)
	if _, err := c.ExecSimple(ctx, "CREATE TEMP TABLE tt(id int primary key, v int)"); err != nil {
		t.Fatal(err)
	}
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if c.TxStatus() != TxInTx {
		t.Fatalf("expected TxInTx, got %c", c.TxStatus())
	}
	if _, err := c.Exec(ctx, "INSERT INTO tt VALUES (1, 10)"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	c.QueryRow(ctx, "SELECT count(*) FROM tt", nil, &count)
	if count != 0 {
		t.Fatalf("rollback left %d rows", count)
	}
	// commit path
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	c.Exec(ctx, "INSERT INTO tt VALUES (2, 20)")
	if err := c.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	c.QueryRow(ctx, "SELECT count(*) FROM tt", nil, &count)
	if count != 1 {
		t.Fatalf("commit gave %d rows", count)
	}
}

func TestTextArrayParse(t *testing.T) {
	c := mustConnect(t)
	defer c.Close()
	var arr []string
	ok, err := c.QueryRow(ctxT(t), `SELECT ARRAY['a','b,c','d"e',NULL]::text[]`, nil, &arr)
	if err != nil || !ok {
		t.Fatalf("array: %v", err)
	}
	want := []string{"a", "b,c", `d"e`, ""}
	if len(arr) != len(want) {
		t.Fatalf("got %v", arr)
	}
	for i := range want {
		if arr[i] != want[i] {
			t.Fatalf("elem %d: got %q want %q (full %v)", i, arr[i], want[i], arr)
		}
	}
}

func TestNullHandling(t *testing.T) {
	c := mustConnect(t)
	defer c.Close()
	ctx := ctxT(t)
	var ns NullString
	var ni NullInt64
	ok, err := c.QueryRow(ctx, "SELECT NULL::text, NULL::int8", nil, &ns, &ni)
	if err != nil || !ok {
		t.Fatalf("null: %v", err)
	}
	if ns.Valid || ni.Valid {
		t.Fatalf("expected NULLs, got %+v %+v", ns, ni)
	}
	// non-null
	c.QueryRow(ctx, "SELECT 'x'::text, 7::int8", nil, &ns, &ni)
	if !ns.Valid || ns.String != "x" || !ni.Valid || ni.Int64 != 7 {
		t.Fatalf("got %+v %+v", ns, ni)
	}
}

func TestPgErrorSQLSTATE(t *testing.T) {
	c := mustConnect(t)
	defer c.Close()
	ctx := ctxT(t)
	c.ExecSimple(ctx, "CREATE TEMP TABLE uq(id int primary key)")
	if _, err := c.Exec(ctx, "INSERT INTO uq VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	_, err := c.Exec(ctx, "INSERT INTO uq VALUES (1)")
	if err == nil {
		t.Fatal("expected unique violation")
	}
	if !IsCode(err, CodeUniqueViolation) {
		t.Fatalf("expected 23505, got %v", err)
	}
	// connection must still be usable (clean resync after query error)
	if c.IsDead() {
		t.Fatal("conn should survive a query-level error")
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping after error: %v", err)
	}
}

func TestSerializationFailure40001(t *testing.T) {
	admin := mustConnect(t)
	defer admin.Close()
	ctx := ctxT(t)
	tbl := "ser_" + randSuffix()
	admin.ExecSimple(ctx, "CREATE TABLE "+tbl+"(id int primary key, v int)")
	defer admin.ExecSimple(context.Background(), "DROP TABLE IF EXISTS "+tbl)
	admin.ExecSimple(ctx, "INSERT INTO "+tbl+" VALUES (1,0)")

	a := mustConnect(t)
	defer a.Close()
	b := mustConnect(t)
	defer b.Close()

	if err := a.BeginSerializable(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, "UPDATE "+tbl+" SET v=v+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err := b.BeginSerializable(ctx); err != nil {
		t.Fatal(err)
	}

	// b's UPDATE blocks on a's row lock; run it async.
	var bErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, bErr = b.Exec(ctx, "UPDATE "+tbl+" SET v=v+1 WHERE id=1")
	}()
	time.Sleep(200 * time.Millisecond) // let b block
	if err := a.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if bErr == nil {
		// b's update may have succeeded; commit would then fail
		bErr = b.Commit(ctx)
	}
	if !IsCode(bErr, CodeSerializationFailure) {
		t.Fatalf("expected 40001, got %v", bErr)
	}
	b.Rollback(ctx)
}
