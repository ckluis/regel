package cfr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/lower"
	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/rast"
)

// --- DB harness (mirrors internal/catalog test pattern) ---------------------

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

type dbEnv struct {
	t    *testing.T
	conn *pgwire.Conn
	db   string
	base pgwire.Config
}

func setupDB(t *testing.T) *dbEnv {
	t.Helper()
	ctx := ctxT(t)
	base, err := pgwire.ParseDSN(baseDSN())
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	db := randName("regel_cfr_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create db: %v", err)
	}
	cfg := base
	cfg.Database = db
	conn, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	if err := catalog.Bootstrap(ctx, conn, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	e := &dbEnv{t: t, conn: conn, db: db, base: base}
	t.Cleanup(func() {
		conn.Close()
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	})
	return e
}

func (e *dbEnv) newConn(t *testing.T) *pgwire.Conn {
	t.Helper()
	cfg := e.base
	cfg.Database = e.db
	c, err := pgwire.Connect(ctxT(t), cfg)
	if err != nil {
		t.Fatalf("newConn: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// catSource loads definition ASTs from the catalog (ADR-03 immortal store).
type catSource struct {
	ctx  context.Context
	conn catalog.Querier
}

func (s catSource) Load(hash string) (*rast.Node, error) {
	d, ok, err := catalog.LoadDefinition(s.ctx, s.conn, hash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	return rast.Decode(d.AST)
}

func mkAdmission(t *testing.T, ctx context.Context, q catalog.Querier) int64 {
	t.Helper()
	var id int64
	ok, err := q.QueryRow(ctx, `
INSERT INTO admission (actor_kind, actor_id, via, submitted_hashes, verifier_report)
VALUES ('system','test','cli',$1::text[],'{}'::jsonb) RETURNING id`,
		[]any{[]string{}}, &id)
	if err != nil || !ok {
		t.Fatalf("mkAdmission: %v", err)
	}
	return id
}

// insertDef inserts a definition row content-addressed by its AST.
func insertDef(t *testing.T, ctx context.Context, q catalog.Querier, body *rast.Node, deps []string, admID int64) string {
	t.Helper()
	norm := rast.Normalize(body)
	hash := rast.Address(norm)
	_, err := catalog.InsertDefinition(ctx, q, catalog.Def{
		Hash: hash, ASTSchemaVer: 1, Kind: "function",
		AST: rast.Encode(norm), CanonicalText: "canon", Deps: deps, AdmissionID: admID,
	}, nil)
	if err != nil {
		t.Fatalf("insertDef: %v", err)
	}
	return hash
}

// seed lowers a module, inserts its defs, and builds an interp over the catalog.
func (e *dbEnv) seed(t *testing.T, source string, resolve lower.Resolver, reg *cek.Registry) (*cek.Interp, map[string]string) {
	t.Helper()
	ctx := ctxT(t)
	admID := mkAdmission(t, ctx, e.conn)
	r := lower.Module(source, lower.ModuleContext{ModuleName: "app/test", Resolve: resolve})
	if !r.OK() {
		t.Fatalf("lower: %v", r.Diagnostics)
	}
	names := map[string]string{}
	for _, d := range r.Definitions {
		var deps []string
		for _, dep := range d.Deps {
			deps = append(deps, dep.Hash)
		}
		insertDef(t, ctx, e.conn, d.Body, deps, admID)
		names[d.Name] = d.Hash
	}
	in := cek.New(catSource{ctx: ctx, conn: e.conn}, reg)
	return in, names
}

const burnProgram = `export function burn(n: number): number {
  let acc = 0;
  for (let i = 0; i < n; i++) { acc = acc + i * 3 - 1; }
  return acc;
}`

// --- family 3 (RED-FIRST): fuel exhaustion parks cleanly with the right rows --

func TestParkRowsFuel(t *testing.T) {
	e := setupDB(t)
	ctx := ctxT(t)
	in, names := e.seed(t, burnProgram, nil, nil)
	hash := names["burn"]

	o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: []cek.Value{cek.NumV(100000)}, Tier: cek.TierSandbox, Fuel: 500})
	if o.Kind != cek.OutParked {
		t.Fatalf("expected Parked, got kind=%d", o.Kind)
	}
	contID, condID, err := Park(ctx, e.conn, ParkReq{
		State: o.State, Kind: "workflow", RootDefHash: hash,
		Class: o.Condition.Class, Payload: o.Condition.Payload, Restarts: o.Condition.Restarts,
	})
	if err != nil {
		t.Fatalf("Park: %v", err)
	}

	// Assertion (written first, red before Park existed): the rows are present.
	var status string
	if ok, err := e.conn.QueryRow(ctx, `SELECT status FROM continuation WHERE id=$1`, []any{contID}, &status); err != nil || !ok {
		t.Fatalf("continuation row: ok=%v err=%v", ok, err)
	}
	if status != "condition" {
		t.Fatalf("continuation status = %q, want 'condition'", status)
	}
	var class string
	if ok, err := e.conn.QueryRow(ctx, `SELECT class FROM durable_condition WHERE id=$1`, []any{condID}, &class); err != nil || !ok {
		t.Fatalf("durable_condition row: ok=%v err=%v", ok, err)
	}
	if class != "fuel.exhausted" {
		t.Fatalf("condition class = %q, want 'fuel.exhausted'", class)
	}
	rows, err := e.conn.Query(ctx, `SELECT name FROM restart WHERE condition_id=$1 ORDER BY name`, condID)
	if err != nil {
		t.Fatalf("restart query: %v", err)
	}
	var got []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		got = append(got, n)
	}
	if len(got) != 2 || got[0] != "abort" || got[1] != "grant-fuel" {
		t.Fatalf("restarts = %v, want [abort grant-fuel]", got)
	}
}

// --- family 3: grant-fuel resume yields the identical result ------------------

func TestFamily3GrantFuelIdentical(t *testing.T) {
	e := setupDB(t)
	ctx := ctxT(t)
	in, names := e.seed(t, burnProgram, nil, nil)
	hash := names["burn"]
	arg := []cek.Value{cek.NumV(2000)}

	ref := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierTrusted})
	if ref.Kind != cek.OutDone {
		t.Fatalf("reference kind=%d", ref.Kind)
	}

	o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierSandbox, Fuel: 777})
	if o.Kind != cek.OutParked {
		t.Fatalf("expected Parked, got %d", o.Kind)
	}
	contID, condID, err := Park(ctx, e.conn, ParkReq{
		State: o.State, Kind: "workflow", RootDefHash: hash,
		Class: o.Condition.Class, Restarts: o.Condition.Restarts,
	})
	if err != nil {
		t.Fatalf("Park: %v", err)
	}

	if err := PickRestart(ctx, e.conn, condID, "grant-fuel",
		map[string]any{"fuel": 1 << 30}, "operator", []string{"operator"}); err != nil {
		t.Fatalf("PickRestart: %v", err)
	}

	out, claimed, err := ClaimAndResume(ctx, e.conn, contID, 0, kernelUUID(),
		func(st *cek.State, ch cek.RestartChoice) cek.Outcome { return in.Resume(ctx, st, cek.Delivery{Restart: &ch}, cek.Principal{IsOperator: true}) })
	if err != nil {
		t.Fatalf("ClaimAndResume: %v", err)
	}
	if !claimed {
		t.Fatal("expected claim")
	}
	if out.Kind != cek.OutDone || !out.Value.Equal(ref.Value) {
		t.Fatalf("resumed %+v (kind %d) != reference %+v", out.Value, out.Kind, ref.Value)
	}
	var status string
	e.conn.QueryRow(ctx, `SELECT status FROM continuation WHERE id=$1`, []any{contID}, &status)
	if status != "done" {
		t.Fatalf("continuation status = %q, want 'done'", status)
	}
}

// --- ADR-05 test 4b: corrupt CFR fails closed into step.failed ---------------

func TestCorruptCFRDB(t *testing.T) {
	e := setupDB(t)
	ctx := ctxT(t)
	in, names := e.seed(t, burnProgram, nil, nil)
	hash := names["burn"]

	o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: []cek.Value{cek.NumV(2000)}, Tier: cek.TierSandbox, Fuel: 400})
	if o.Kind != cek.OutParked {
		t.Fatalf("expected Parked")
	}
	contID, condID, err := Park(ctx, e.conn, ParkReq{
		State: o.State, Kind: "workflow", RootDefHash: hash,
		Class: o.Condition.Class, Restarts: o.Condition.Restarts,
	})
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	// Corrupt the stored frames blob (bit flip near the end).
	blob, _ := Encode(o.State)
	blob[len(blob)-2] ^= 0x40
	if _, err := e.conn.Exec(ctx, `UPDATE continuation SET frames=$2::bytea WHERE id=$1`,
		contID, byteaLiteral(blob)); err != nil {
		t.Fatalf("corrupt update: %v", err)
	}
	if err := PickRestart(ctx, e.conn, condID, "grant-fuel",
		map[string]any{"fuel": 1 << 30}, "operator", []string{"operator"}); err != nil {
		t.Fatalf("PickRestart: %v", err)
	}
	_, claimed, err := ClaimAndResume(ctx, e.conn, contID, 0, kernelUUID(),
		func(st *cek.State, ch cek.RestartChoice) cek.Outcome { return in.Resume(ctx, st, cek.Delivery{Restart: &ch}, cek.Principal{IsOperator: true}) })
	if !claimed {
		t.Fatal("expected claim (the CAS wins before decode)")
	}
	if err == nil {
		t.Fatal("expected a typed decode error, got nil")
	}
	// The failure is recorded: continuation failed + a step.failed condition.
	var status string
	e.conn.QueryRow(ctx, `SELECT status FROM continuation WHERE id=$1`, []any{contID}, &status)
	if status != "failed" {
		t.Fatalf("continuation status = %q, want 'failed'", status)
	}
	var n int
	e.conn.QueryRow(ctx, `SELECT count(*) FROM durable_condition WHERE continuation_id=$1 AND class='step.failed'`,
		[]any{contID}, &n)
	if n != 1 {
		t.Fatalf("step.failed conditions = %d, want 1", n)
	}
}

// --- ADR-05 test 5: double-resume CAS (sequential + concurrent) ---------------

func TestDoubleResumeCAS(t *testing.T) {
	e := setupDB(t)
	ctx := ctxT(t)
	in, names := e.seed(t, burnProgram, nil, nil)
	hash := names["burn"]

	park := func() (string, string) {
		o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: []cek.Value{cek.NumV(2000)}, Tier: cek.TierSandbox, Fuel: 500})
		if o.Kind != cek.OutParked {
			t.Fatalf("expected Parked")
		}
		c, cond, err := Park(ctx, e.conn, ParkReq{
			State: o.State, Kind: "workflow", RootDefHash: hash,
			Class: o.Condition.Class, Restarts: o.Condition.Restarts,
		})
		if err != nil {
			t.Fatalf("Park: %v", err)
		}
		if err := PickRestart(ctx, e.conn, cond, "grant-fuel",
			map[string]any{"fuel": 1 << 30}, "operator", []string{"operator"}); err != nil {
			t.Fatalf("PickRestart: %v", err)
		}
		return c, cond
	}

	t.Run("sequential", func(t *testing.T) {
		contID, _ := park()
		resume := func(st *cek.State, ch cek.RestartChoice) cek.Outcome { return in.Resume(ctx, st, cek.Delivery{Restart: &ch}, cek.Principal{IsOperator: true}) }
		_, c1, err := ClaimAndResume(ctx, e.conn, contID, 0, kernelUUID(), resume)
		if err != nil || !c1 {
			t.Fatalf("first claim: claimed=%v err=%v", c1, err)
		}
		_, c2, err := ClaimAndResume(ctx, e.conn, contID, 0, kernelUUID(), resume)
		if err != nil {
			t.Fatalf("second claim err: %v", err)
		}
		if c2 {
			t.Fatal("second claim with the same seenSeq must be refused")
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		contID, _ := park()
		c1 := e.newConn(t)
		c2 := e.newConn(t)
		in1 := cek.New(catSource{ctx: ctx, conn: c1}, nil)
		in2 := cek.New(catSource{ctx: ctx, conn: c2}, nil)
		var wg sync.WaitGroup
		var claims [2]bool
		var errs [2]error
		run := func(i int, db *pgwire.Conn, ii *cek.Interp) {
			defer wg.Done()
			_, claimed, err := ClaimAndResume(ctx, db, contID, 0, kernelUUID(),
				func(st *cek.State, ch cek.RestartChoice) cek.Outcome { return ii.Resume(ctx, st, cek.Delivery{Restart: &ch}, cek.Principal{IsOperator: true}) })
			claims[i] = claimed
			errs[i] = err
		}
		wg.Add(2)
		go run(0, c1, in1)
		go run(1, c2, in2)
		wg.Wait()
		n := 0
		for i := 0; i < 2; i++ {
			if errs[i] != nil {
				t.Logf("goroutine %d err (acceptable if serialization loss): %v", i, errs[i])
			}
			if claims[i] {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("exactly one goroutine must claim, got %d", n)
		}
	})
}

// --- ADR-05 test 6: the resumed run fires no re-fired effect ------------------

func TestEffectFiredOnce(t *testing.T) {
	e := setupDB(t)
	ctx := ctxT(t)

	// A native 'std/fx.tick' that increments a shared counter; the program calls
	// it once BEFORE the fuel-park point.
	nb := rast.Normalize(&rast.Node{Kind: rast.KNativeBody, Str: "std/fx.tick",
		Kids: []*rast.Node{{Kind: rast.TKeyword, Str: "unknown"}}})
	nativeHash := rast.Address(nb)
	admID := mkAdmission(t, ctx, e.conn)
	if _, err := catalog.InsertDefinition(ctx, e.conn, catalog.Def{
		Hash: nativeHash, ASTSchemaVer: 1, Kind: "function",
		AST: rast.Encode(nb), CanonicalText: "native", AdmissionID: admID,
	}, nil); err != nil {
		t.Fatalf("insert native def: %v", err)
	}

	var counter int
	reg := cek.NewRegistry()
	reg.Register(nativeHash, func(h *cek.Host, args []cek.Value) (cek.Value, *cek.NativePark) {
		counter++
		return cek.UndefV(), nil
	})
	resolve := func(name string) (string, bool) {
		if name == "std/fx.tick" {
			return nativeHash, true
		}
		return "", false
	}

	src := `import { tick } from "std/fx";
export function work(n: number): number {
  tick();
  let acc = 0;
  for (let i = 0; i < n; i++) { acc = acc + i; }
  return acc;
}`
	in, names := e.seed(t, src, resolve, reg)
	hash := names["work"]

	// Park after the tick() effect fired (fuel burns during the loop).
	o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: []cek.Value{cek.NumV(2000)}, Tier: cek.TierSandbox, Fuel: 400})
	if o.Kind != cek.OutParked {
		t.Fatalf("expected Parked, got %d", o.Kind)
	}
	if counter != 1 {
		t.Fatalf("effect count before resume = %d, want 1", counter)
	}
	contID, condID, err := Park(ctx, e.conn, ParkReq{
		State: o.State, Kind: "workflow", RootDefHash: hash,
		Class: o.Condition.Class, Restarts: o.Condition.Restarts,
	})
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if err := PickRestart(ctx, e.conn, condID, "grant-fuel",
		map[string]any{"fuel": 1 << 30}, "operator", []string{"operator"}); err != nil {
		t.Fatalf("PickRestart: %v", err)
	}
	out, _, err := ClaimAndResume(ctx, e.conn, contID, 0, kernelUUID(),
		func(st *cek.State, ch cek.RestartChoice) cek.Outcome { return in.Resume(ctx, st, cek.Delivery{Restart: &ch}, cek.Principal{IsOperator: true}) })
	if err != nil {
		t.Fatalf("ClaimAndResume: %v", err)
	}
	if out.Kind != cek.OutDone {
		t.Fatalf("resume kind=%d", out.Kind)
	}
	if counter != 1 {
		t.Fatalf("effect fired %d times across park+resume, want exactly 1", counter)
	}
}

// --- process-restart resume: resume from rows on a fresh connection -----------

func TestProcessRestartResume(t *testing.T) {
	e := setupDB(t)
	ctx := ctxT(t)
	in, names := e.seed(t, burnProgram, nil, nil)
	hash := names["burn"]
	arg := []cek.Value{cek.NumV(2000)}
	ref := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierTrusted})

	o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: arg, Tier: cek.TierSandbox, Fuel: 900})
	if o.Kind != cek.OutParked {
		t.Fatalf("expected Parked")
	}
	contID, condID, err := Park(ctx, e.conn, ParkReq{
		State: o.State, Kind: "workflow", RootDefHash: hash,
		Class: o.Condition.Class, Restarts: o.Condition.Restarts,
	})
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if err := PickRestart(ctx, e.conn, condID, "grant-fuel",
		map[string]any{"fuel": 1 << 30}, "operator", []string{"operator"}); err != nil {
		t.Fatalf("PickRestart: %v", err)
	}

	// Simulate a fresh kernel: a NEW connection + a NEW interp built only from
	// catalog rows (no in-process state carried over).
	fresh := e.newConn(t)
	freshIn := cek.New(catSource{ctx: ctx, conn: fresh}, nil)
	out, claimed, err := ClaimAndResume(ctx, fresh, contID, 0, kernelUUID(),
		func(st *cek.State, ch cek.RestartChoice) cek.Outcome { return freshIn.Resume(ctx, st, cek.Delivery{Restart: &ch}, cek.Principal{IsOperator: true}) })
	if err != nil || !claimed {
		t.Fatalf("fresh resume: claimed=%v err=%v", claimed, err)
	}
	if out.Kind != cek.OutDone || !out.Value.Equal(ref.Value) {
		t.Fatalf("fresh-kernel resume %+v != reference %+v", out.Value, ref.Value)
	}
}

// --- torn write (test 7 minimal): a rolled-back park leaves zero rows ---------

func TestTornWriteRollsBack(t *testing.T) {
	e := setupDB(t)
	ctx := ctxT(t)
	in, names := e.seed(t, burnProgram, nil, nil)
	hash := names["burn"]
	o := in.Run(ctx, cek.RunReq{DefHash: hash, Args: []cek.Value{cek.NumV(2000)}, Tier: cek.TierSandbox, Fuel: 500})
	if o.Kind != cek.OutParked {
		t.Fatalf("expected Parked")
	}
	blob, _ := Encode(o.State)
	contID := uuid4()
	condID := uuid4()

	// Begin the park transaction, write the continuation + condition, then crash
	// (rollback) before commit — the injected torn write.
	if err := e.conn.BeginSerializable(ctx); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := e.conn.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal, step_seq)
VALUES ($1,'workflow',$2,1,1,$3::bytea,$4::jsonb,'condition','{}'::jsonb,0)`,
		contID, hash, byteaLiteral(blob), `{"kind":"manual","condition":"`+condID+`"}`); err != nil {
		t.Fatalf("insert continuation: %v", err)
	}
	if _, err := e.conn.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload)
VALUES ($1,$2,'fuel.exhausted','{}'::jsonb)`, condID, contID); err != nil {
		t.Fatalf("insert condition: %v", err)
	}
	if err := e.conn.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Zero rows survive: no continuation, no condition, no restarts.
	for _, q := range []struct {
		sql string
		arg string
	}{
		{`SELECT count(*) FROM continuation WHERE id=$1`, contID},
		{`SELECT count(*) FROM durable_condition WHERE id=$1`, condID},
		{`SELECT count(*) FROM restart WHERE condition_id=$1`, condID},
	} {
		var n int
		if _, err := e.conn.QueryRow(ctx, q.sql, []any{q.arg}, &n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Fatalf("torn write left %d rows for %q", n, q.sql)
		}
	}
}

func kernelUUID() string { return uuid4() }
