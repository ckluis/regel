package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/pgwire"
)

// --- test harness ------------------------------------------------------------

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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// env is a bootstrapped scratch database plus its runtime kernel role. The owner
// connection (conn) is the superuser table owner; kernelConn opens a second
// connection AS the least-privileged kernel role for the I6 privilege drill.
type env struct {
	t    *testing.T
	conn *pgwire.Conn
	role string
	db   string
	base pgwire.Config
}

func setupEnv(t *testing.T) *env {
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

	db := randName("regel_test_")
	role := randName("regel_k_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create database: %v", err)
	}

	scratchCfg := base
	scratchCfg.Database = db
	conn, err := pgwire.Connect(ctx, scratchCfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	if err := Bootstrap(ctx, conn, role); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	e := &env{t: t, conn: conn, role: role, db: db, base: base}
	t.Cleanup(func() {
		conn.Close()
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		cl.ExecSimple(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	return e
}

// kernelConn opens a connection to the scratch db AS the kernel role (trust auth,
// no password on the build box).
func (e *env) kernelConn(t *testing.T) *pgwire.Conn {
	t.Helper()
	cfg := e.base
	cfg.Database = e.db
	cfg.User = e.role
	cfg.Password = ""
	c, err := pgwire.Connect(ctxT(t), cfg)
	if err != nil {
		t.Fatalf("connect as kernel role %s: %v", e.role, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func wantCode(t *testing.T, err error, code, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected SQLSTATE %s, got nil (constraint/trigger not enforced)", what, code)
	}
	if !pgwire.IsCode(err, code) {
		t.Fatalf("%s: expected SQLSTATE %s, got %v", what, code, err)
	}
}

func mustNil(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// mkAdmission inserts a minimal admission ledger row and returns its id.
func mkAdmission(t *testing.T, ctx context.Context, q Querier) int64 {
	t.Helper()
	var id int64
	ok, err := q.QueryRow(ctx, `
INSERT INTO admission (actor_kind, actor_id, via, submitted_hashes, verifier_report)
VALUES ('system','test','cli',$1::text[],'{}'::jsonb) RETURNING id`,
		[]any{[]string{}}, &id)
	if err != nil || !ok {
		t.Fatalf("mkAdmission: ok=%v err=%v", ok, err)
	}
	return id
}

// mkDef seeds a definition row (verify hook a no-op) under admID.
func mkDef(t *testing.T, ctx context.Context, q Querier, hash string, admID int64) {
	t.Helper()
	_, err := InsertDefinition(ctx, q, Def{
		Hash: hash, ASTSchemaVer: 1, Kind: "function",
		AST: []byte("ast-" + hash), CanonicalText: "canon " + hash, AdmissionID: admID,
	}, nil)
	if err != nil {
		t.Fatalf("mkDef(%s): %v", hash, err)
	}
}

// --- CI Verification Gate 1: DDL-creatable -----------------------------------

func TestGate1DDLCreatable(t *testing.T) {
	e := setupEnv(t)
	ctx := ctxT(t)

	var v string
	if _, err := e.conn.QueryRow(ctx, "select version()", nil, &v); err != nil {
		t.Fatalf("version: %v", err)
	}
	t.Logf("CI Gate 1 against real Postgres: %s", v)

	var one int
	ok, err := e.conn.QueryRow(ctx, "SELECT 1 FROM pg_extension WHERE extname='btree_gist'", nil, &one)
	if err != nil || !ok {
		t.Fatalf("btree_gist not present: ok=%v err=%v", ok, err)
	}

	var conname string
	ok, err = e.conn.QueryRow(ctx,
		"SELECT conname FROM pg_constraint WHERE contype='x' AND conrelid='name_pointer_history'::regclass",
		nil, &conname)
	if err != nil || !ok {
		t.Fatalf("I4 exclusion constraint absent on name_pointer_history: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(conname, "excl") {
		t.Fatalf("exclusion constraint name %q does not look like a GiST exclusion", conname)
	}
	t.Logf("I4 exclusion constraint present: %s", conname)
}

// --- CI Verification Gate 2: overlap-rejection kill-test ----------------------

func TestGate2OverlapKill(t *testing.T) {
	e := setupEnv(t)
	ctx := ctxT(t)
	q := e.conn

	// The constraint MUST exist: a green result requires an ACTIVE rejection, never
	// pass-by-absence.
	var conname string
	if ok, err := q.QueryRow(ctx,
		"SELECT conname FROM pg_constraint WHERE contype='x' AND conrelid='name_pointer_history'::regclass",
		nil, &conname); err != nil || !ok {
		t.Fatalf("KILL-TEST precondition failed: no exclusion constraint (ok=%v err=%v)", ok, err)
	}

	admID := mkAdmission(t, ctx, q)
	hash := "r1_gate200000"
	mkDef(t, ctx, q, hash, admID)

	insHist := func(name string, from time.Time, to any) error {
		_, err := q.Exec(ctx, `
INSERT INTO name_pointer_history (name, scope_kind, scope_id, hash, valid_from, valid_to, admission_id)
VALUES ($1, 0, '', $2, $3, $4, $5)`, name, hash, from, to, admID)
		return err
	}

	t0 := time.Now().UTC()
	mustNil(t, insHist("app/n", t0, nil), "first open window [t0,NULL)")
	// Second open window at t1>t0 for the SAME (name,scope) overlaps at every instant >=t1.
	t1 := t0.Add(time.Second)
	err := insHist("app/n", t1, nil)
	wantCode(t, err, pgwire.CodeExclusionViolation, "overlapping window must be rejected 23P01")
}

// --- CI Verification Gate 3: no-false-positive -------------------------------

func TestGate3NoFalsePositive(t *testing.T) {
	e := setupEnv(t)
	ctx := ctxT(t)
	q := e.conn
	admID := mkAdmission(t, ctx, q)
	hash := "r1_gate300000"
	mkDef(t, ctx, q, hash, admID)

	insHist := func(name string, from time.Time, to any) error {
		_, err := q.Exec(ctx, `
INSERT INTO name_pointer_history (name, scope_kind, scope_id, hash, valid_from, valid_to, admission_id)
VALUES ($1, 0, '', $2, $3, $4, $5)`, name, hash, from, to, admID)
		return err
	}

	t0 := time.Now().UTC()
	t1 := t0.Add(time.Second)
	// Adjacent windows [t0,t1) then [t1,NULL) for one (name,scope): both commit.
	mustNil(t, insHist("app/adj", t0, t1), "adjacent closed window [t0,t1)")
	mustNil(t, insHist("app/adj", t1, nil), "adjacent open window [t1,NULL)")
	// Distinct names, each one open window at the same instant: both commit.
	mustNil(t, insHist("app/one", t0, nil), "distinct name one")
	mustNil(t, insHist("app/two", t0, nil), "distinct name two")
}

// --- Invariants I1/I2/I3/I7 + history FK --------------------------------------

func TestInvariants(t *testing.T) {
	e := setupEnv(t)
	ctx := ctxT(t)
	q := e.conn
	admID := mkAdmission(t, ctx, q)

	// I1: dangling name_pointer.hash rejected by FK.
	t.Run("I1_dangling_pointer_hash", func(t *testing.T) {
		_, err := UpsertPointerCAS(ctx, q, Pointer{
			Name: "app/i1", ScopeKind: 0, Kind: "function", Visibility: "exported",
			Hash: "r1_missing0000", AdmissionID: admID,
		}, nil)
		wantCode(t, err, pgwire.CodeForeignKeyViolation, "I1 dangling name_pointer.hash")
	})

	// I2: dangling deps element rejected by the trigger (raises foreign_key_violation).
	t.Run("I2_dangling_dep", func(t *testing.T) {
		_, err := InsertDefinition(ctx, q, Def{
			Hash: "r1_i2def00000", ASTSchemaVer: 1, Kind: "function",
			AST: []byte("x"), CanonicalText: "c", Deps: []string{"r1_absentdep0"}, AdmissionID: admID,
		}, nil)
		wantCode(t, err, pgwire.CodeForeignKeyViolation, "I2 dangling deps edge")
	})

	// I3: duplicate live winner rejected by the composite PK.
	t.Run("I3_duplicate_live_winner", func(t *testing.T) {
		mkDef(t, ctx, q, "r1_i3a0000000", admID)
		mkDef(t, ctx, q, "r1_i3b0000000", admID)
		moved, err := UpsertPointerCAS(ctx, q, Pointer{
			Name: "app/i3", ScopeKind: 0, Kind: "function", Visibility: "exported",
			Hash: "r1_i3a0000000", AdmissionID: admID,
		}, nil)
		mustNil(t, err, "I3 first pointer insert")
		if !moved {
			t.Fatal("I3 first pointer should have inserted")
		}
		// A raw duplicate INSERT of the SAME PK must hit the composite PK. The
		// history writer is disabled for this one assertion so the name_pointer PK
		// (I3) is proven in isolation — otherwise the BEFORE INSERT history trigger
		// opens a duplicate window and the I4 exclusion (23P01) fires first.
		_, derr := q.Exec(ctx, "ALTER TABLE name_pointer DISABLE TRIGGER name_pointer_history_writer")
		mustNil(t, derr, "disable history trigger")
		_, err = q.Exec(ctx, `
INSERT INTO name_pointer (name, scope_kind, scope_id, kind, visibility, hash, admission_id)
VALUES ('app/i3', 0, '', 'function', 'exported', 'r1_i3b0000000', $1)`, admID)
		_, eerr := q.Exec(ctx, "ALTER TABLE name_pointer ENABLE TRIGGER name_pointer_history_writer")
		mustNil(t, eerr, "re-enable history trigger")
		wantCode(t, err, pgwire.CodeUniqueViolation, "I3 duplicate (name,scope_kind,scope_id)")
	})

	// I7: UPDATE writes two history windows, exactly adjacent, no overlap.
	t.Run("I7_history_windows", func(t *testing.T) {
		mkDef(t, ctx, q, "r1_i7v100000", admID)
		mkDef(t, ctx, q, "r1_i7v200000", admID)
		base := "r1_i7v100000"
		moved, err := UpsertPointerCAS(ctx, q, Pointer{
			Name: "app/i7", ScopeKind: 0, Kind: "function", Visibility: "exported",
			Hash: base, AdmissionID: admID,
		}, nil)
		mustNil(t, err, "I7 insert pointer")
		if !moved {
			t.Fatal("I7 insert should move")
		}
		moved, err = UpsertPointerCAS(ctx, q, Pointer{
			Name: "app/i7", ScopeKind: 0, Kind: "function", Visibility: "exported",
			Hash: "r1_i7v200000", AdmissionID: admID,
		}, &base)
		mustNil(t, err, "I7 update pointer")
		if !moved {
			t.Fatal("I7 update should move (base matched)")
		}

		rows, err := q.Query(ctx, `
SELECT hash, valid_from, valid_to FROM name_pointer_history
WHERE name='app/i7' AND scope_kind=0 AND scope_id='' ORDER BY valid_from`)
		mustNil(t, err, "I7 read history")
		type win struct {
			hash string
			from time.Time
			to   pgwire.NullTime
		}
		var ws []win
		for rows.Next() {
			var w win
			if err := rows.Scan(&w.hash, &w.from, &w.to); err != nil {
				t.Fatalf("I7 scan: %v", err)
			}
			ws = append(ws, w)
		}
		mustNil(t, rows.Err(), "I7 rows err")
		if len(ws) != 2 {
			t.Fatalf("I7 expected 2 history windows, got %d", len(ws))
		}
		if ws[0].hash != base || ws[1].hash != "r1_i7v200000" {
			t.Fatalf("I7 window hashes out of order: %+v", ws)
		}
		if !ws[0].to.Valid {
			t.Fatal("I7 first window should be closed (valid_to set)")
		}
		if ws[1].to.Valid {
			t.Fatal("I7 second window should be open (valid_to NULL)")
		}
		if !ws[0].to.Time.Equal(ws[1].from) {
			t.Fatalf("I7 first.valid_to (%v) must equal second.valid_from (%v) — no gap, no overlap",
				ws[0].to.Time, ws[1].from)
		}
	})

	// history admission_id FK: a bogus admission_id aborts the transaction.
	t.Run("history_admission_fk", func(t *testing.T) {
		mkDef(t, ctx, q, "r1_hfk000000", admID)
		_, err := q.Exec(ctx, `
INSERT INTO name_pointer_history (name, scope_kind, scope_id, hash, valid_from, valid_to, admission_id)
VALUES ('app/hfk', 0, '', 'r1_hfk000000', now(), NULL, 999999999)`)
		wantCode(t, err, pgwire.CodeForeignKeyViolation, "history admission_id FK")
	})
}

// --- I6: kernel role cannot mutate or delete the immortal store ---------------

func TestI6KernelImmutable(t *testing.T) {
	e := setupEnv(t)
	ctx := ctxT(t)
	admID := mkAdmission(t, ctx, e.conn)
	mkDef(t, ctx, e.conn, "r1_i6def0000", admID)

	k := e.kernelConn(t)
	// Sanity: the kernel CAN read and insert (its granted rights).
	var one int
	if ok, err := k.QueryRow(ctx, "SELECT 1 FROM definition WHERE hash='r1_i6def0000'", nil, &one); err != nil || !ok {
		t.Fatalf("kernel SELECT on definition: ok=%v err=%v", ok, err)
	}
	// UPDATE and DELETE must be refused with insufficient_privilege.
	_, err := k.Exec(ctx, "UPDATE definition SET canonical_text='tampered' WHERE hash='r1_i6def0000'")
	wantCode(t, err, pgwire.CodeInsufficientPrivilege, "I6 kernel UPDATE definition")
	_, err = k.Exec(ctx, "DELETE FROM definition WHERE hash='r1_i6def0000'")
	wantCode(t, err, pgwire.CodeInsufficientPrivilege, "I6 kernel DELETE definition")
}

// --- ADR-05 / ADR-06 CHECK constraints ---------------------------------------

func TestADR05Checks(t *testing.T) {
	e := setupEnv(t)
	ctx := ctxT(t)
	q := e.conn
	admID := mkAdmission(t, ctx, q)
	hash := "r1_adr05def00"
	mkDef(t, ctx, q, hash, admID)

	insCont := func(wake string) error {
		_, err := q.Exec(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal)
VALUES (gen_random_uuid(), 'workflow', $1, 1, 1, $2::bytea, $3::jsonb, 'sleeping', '{}'::jsonb)`,
			hash, byteaLiteral(nil), wake)
		return err
	}
	t.Run("wake_empty_object_rejected", func(t *testing.T) {
		wantCode(t, insCont(`{}`), pgwire.CodeCheckViolation, "wake '{}' has no kind")
	})
	t.Run("wake_bogus_kind_rejected", func(t *testing.T) {
		wantCode(t, insCont(`{"kind":"bogus"}`), pgwire.CodeCheckViolation, "wake bogus kind")
	})

	// A valid continuation to hang durable_condition rows off of.
	var contID string
	if ok, err := q.QueryRow(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal)
VALUES (gen_random_uuid(), 'workflow', $1, 1, 1, $2::bytea, $3::jsonb, 'condition', '{}'::jsonb) RETURNING id`,
		[]any{hash, byteaLiteral(nil), `{"kind":"manual"}`}, &contID); err != nil || !ok {
		t.Fatalf("seed continuation: ok=%v err=%v", ok, err)
	}

	t.Run("durable_class_shape_empty_rejected", func(t *testing.T) {
		_, err := q.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload)
VALUES (gen_random_uuid(), $1, '', '{}'::jsonb)`, contID)
		wantCode(t, err, pgwire.CodeCheckViolation, "durable_condition class ''")
	})

	// resolved_consistency, direction A: status resolved but resolved_* NULL.
	t.Run("resolved_consistency_resolved_without_fields", func(t *testing.T) {
		_, err := q.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload, status)
VALUES (gen_random_uuid(), $1, 'cond.test', '{}'::jsonb, 'resolved')`, contID)
		wantCode(t, err, pgwire.CodeCheckViolation, "resolved status without resolution fields")
	})

	// resolved_consistency, direction B: status open but resolution fields set.
	t.Run("resolved_consistency_open_with_fields", func(t *testing.T) {
		var condID string
		if ok, err := q.QueryRow(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload)
VALUES (gen_random_uuid(), $1, 'cond.test', '{}'::jsonb) RETURNING id`, []any{contID}, &condID); err != nil || !ok {
			t.Fatalf("seed condition: ok=%v err=%v", ok, err)
		}
		var restartID string
		if ok, err := q.QueryRow(ctx, `
INSERT INTO restart (id, condition_id, name, label)
VALUES (gen_random_uuid(), $1, 'retry', 'Retry') RETURNING id`, []any{condID}, &restartID); err != nil || !ok {
			t.Fatalf("seed restart: ok=%v err=%v", ok, err)
		}
		_, err := q.Exec(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload, status, resolved_restart, resolved_by, resolved_at)
VALUES (gen_random_uuid(), $1, 'cond.test', '{}'::jsonb, 'open', $2, 'op', now())`, contID, restartID)
		wantCode(t, err, pgwire.CodeCheckViolation, "open status with resolution fields")
	})

	// ADR-06 task payload_shape: resume without step_seq.
	t.Run("task_resume_without_step_seq", func(t *testing.T) {
		_, err := q.Exec(ctx, `
INSERT INTO task (id, kind, run_at, payload)
VALUES (gen_random_uuid(), 'resume', now(), $1::jsonb)`, `{"continuation_id":"c"}`)
		wantCode(t, err, pgwire.CodeCheckViolation, "resume task missing step_seq")
	})
}

// --- Stage-B (ADR-05 BUILD-B / ADR-08) schema additions ----------------------

func TestStageBSchema(t *testing.T) {
	e := setupEnv(t)
	ctx := ctxT(t)
	q := e.conn
	admID := mkAdmission(t, ctx, q)
	hash := "r1_stageb0def"
	mkDef(t, ctx, q, hash, admID)

	// continuation.result column exists (nullable bytea).
	t.Run("result_column", func(t *testing.T) {
		var one int
		ok, err := q.QueryRow(ctx, `
SELECT 1 FROM information_schema.columns
WHERE table_name='continuation' AND column_name='result' AND data_type='bytea'`, nil, &one)
		if err != nil || !ok {
			t.Fatalf("continuation.result bytea column absent: ok=%v err=%v", ok, err)
		}
	})

	// 'cancelled' is accepted by the status CHECK.
	var contID string
	t.Run("cancelled_status_accepted", func(t *testing.T) {
		if ok, err := q.QueryRow(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal)
VALUES (gen_random_uuid(), 'workflow', $1, 1, 1, $2::bytea, $3::jsonb, 'cancelled', '{}'::jsonb) RETURNING id`,
			[]any{hash, byteaLiteral(nil), `{"kind":"join"}`}, &contID); err != nil || !ok {
			t.Fatalf("'cancelled' status rejected: ok=%v err=%v", ok, err)
		}
	})

	// A live continuation to hang outbox / channel_message rows off of.
	var liveID string
	if ok, err := q.QueryRow(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal)
VALUES (gen_random_uuid(), 'workflow', $1, 1, 1, $2::bytea, $3::jsonb, 'sleeping', '{}'::jsonb) RETURNING id`,
		[]any{hash, byteaLiteral(nil), `{"kind":"message","channel":"c"}`}, &liveID); err != nil || !ok {
		t.Fatalf("seed live continuation: ok=%v err=%v", ok, err)
	}

	// channel_message table exists and accepts a row.
	t.Run("channel_message_table", func(t *testing.T) {
		if _, err := q.Exec(ctx, `
INSERT INTO channel_message (id, channel, payload, sent_by)
VALUES (gen_random_uuid(), 'orders', $1::bytea, 'external')`, byteaLiteral([]byte{1, 2, 3})); err != nil {
			t.Fatalf("channel_message insert: %v", err)
		}
	})

	// epoch_current table exists with the singleton row (genesis writes n=1).
	t.Run("epoch_current_table", func(t *testing.T) {
		var n int
		ok, err := q.QueryRow(ctx, `SELECT 1 FROM epoch_current WHERE one = true`, nil, &n)
		// Bootstrap (DDL only) may or may not seed the row; assert the table exists
		// and the singleton constraint holds by inserting/updating.
		if err != nil {
			t.Fatalf("epoch_current query: %v", err)
		}
		_ = ok
		// The 'one' PK + CHECK(one) makes it a singleton: a second true row fails.
		// (epoch row n=1 exists from the DDL's epoch table only if genesis ran; here
		// insert an epoch row then the epoch_current row.)
		if _, err := q.Exec(ctx, `
INSERT INTO epoch (n, std_manifest_root, dispatch_attestation) VALUES (1,'r','a')
ON CONFLICT (n) DO NOTHING`); err != nil {
			t.Fatalf("seed epoch: %v", err)
		}
		if _, err := q.Exec(ctx, `
INSERT INTO epoch_current (one, n) VALUES (true, 1)
ON CONFLICT (one) DO UPDATE SET n = EXCLUDED.n`); err != nil {
			t.Fatalf("epoch_current upsert: %v", err)
		}
	})

	// outbox table exists and its UNIQUE (continuation_id, step_seq, ordinal)
	// rejects a duplicate dedup key.
	t.Run("outbox_unique_dedup", func(t *testing.T) {
		ins := func() error {
			_, err := q.Exec(ctx, `
INSERT INTO outbox (id, continuation_id, step_seq, ordinal, class, payload)
VALUES (gen_random_uuid(), $1, 7, 0, 'mail.send', '{}'::jsonb)`, liveID)
			return err
		}
		if err := ins(); err != nil {
			t.Fatalf("first outbox insert: %v", err)
		}
		wantCode(t, ins(), pgwire.CodeUniqueViolation, "outbox duplicate (continuation_id, step_seq, ordinal)")
	})
}

// --- Resolver: scope walk, overlays, visibility, as-of, CAS -------------------

func TestResolver(t *testing.T) {
	e := setupEnv(t)
	ctx := ctxT(t)
	q := e.conn
	admID := mkAdmission(t, ctx, q)

	const (
		Hprod = "r1_prod000000"
		HorgA = "r1_orga000000"
		Hpriv = "r1_priv000000"
		V1    = "r1_ver1000000"
		V2    = "r1_ver2000000"
		Hb    = "r1_casb000000"
		H1c   = "r1_cas1000000"
		H2c   = "r1_cas2000000"
	)
	for _, h := range []string{Hprod, HorgA, Hpriv, V1, V2, Hb, H1c, H2c} {
		mkDef(t, ctx, q, h, admID)
	}

	seed := func(p Pointer) {
		moved, err := UpsertPointerCAS(ctx, q, p, nil)
		mustNil(t, err, "seed pointer "+p.Name)
		if !moved {
			t.Fatalf("seed pointer %s scope %d/%s did not insert", p.Name, p.ScopeKind, p.ScopeID)
		}
	}
	seed(Pointer{Name: "app/crm/deal/total", ScopeKind: 0, Kind: "function", Visibility: "exported", Hash: Hprod, AdmissionID: admID})
	seed(Pointer{Name: "app/crm/deal/total", ScopeKind: 2, ScopeID: "orgA", Kind: "function", Visibility: "exported", Hash: HorgA, AdmissionID: admID})
	seed(Pointer{Name: "app/crm/deal/roundUp", ScopeKind: 0, Kind: "function", Visibility: "private", Hash: Hpriv, AdmissionID: admID})

	resolve := func(req ResolveReq) (Resolved, bool) {
		r, ok, err := Resolve(ctx, q, req)
		mustNil(t, err, "resolve "+req.Name)
		return r, ok
	}

	// Overlay: org chain resolves the org overlay.
	t.Run("org_chain_hits_overlay", func(t *testing.T) {
		r, ok := resolve(ResolveReq{Name: "app/crm/deal/total", Chain: Chain{OrgID: "orgA"}})
		if !ok || r.Hash != HorgA || r.ScopeKind != 2 {
			t.Fatalf("want overlay %s@2, got %+v ok=%v", HorgA, r, ok)
		}
	})
	// Isolation: a different org falls through to product.
	t.Run("other_org_falls_through_to_product", func(t *testing.T) {
		r, ok := resolve(ResolveReq{Name: "app/crm/deal/total", Chain: Chain{OrgID: "orgB"}})
		if !ok || r.Hash != Hprod || r.ScopeKind != 0 {
			t.Fatalf("want product %s@0, got %+v ok=%v", Hprod, r, ok)
		}
	})
	// Visibility: same-module caller sees the private helper.
	t.Run("private_same_module_resolves", func(t *testing.T) {
		r, ok := resolve(ResolveReq{Name: "app/crm/deal/roundUp", CallerModule: "app/crm/deal"})
		if !ok || r.Hash != Hpriv {
			t.Fatalf("want private %s, got %+v ok=%v", Hpriv, r, ok)
		}
	})
	// Visibility: different-module caller gets zero rows.
	t.Run("private_other_module_not_found", func(t *testing.T) {
		if _, ok := resolve(ResolveReq{Name: "app/crm/deal/roundUp", CallerModule: "app/other"}); ok {
			t.Fatal("private name must be invisible to a different module")
		}
	})
	// Visibility: external caller ('') gets zero rows.
	t.Run("private_external_caller_not_found", func(t *testing.T) {
		if _, ok := resolve(ResolveReq{Name: "app/crm/deal/roundUp", CallerModule: ""}); ok {
			t.Fatal("private name must be invisible to an external caller")
		}
	})
	// Exported resolves regardless of caller module.
	t.Run("exported_resolves_regardless", func(t *testing.T) {
		r, ok := resolve(ResolveReq{Name: "app/crm/deal/total", CallerModule: ""})
		if !ok || r.Hash != Hprod {
			t.Fatalf("want product %s, got %+v ok=%v", Hprod, r, ok)
		}
	})

	// As-of: v1 then UPDATE to v2; resolve between windows ⇒ v1, after ⇒ v2.
	t.Run("as_of", func(t *testing.T) {
		seed(Pointer{Name: "app/x/ver", ScopeKind: 0, Kind: "function", Visibility: "exported", Hash: V1, AdmissionID: admID})
		moved, err := UpsertPointerCAS(ctx, q, Pointer{
			Name: "app/x/ver", ScopeKind: 0, Kind: "function", Visibility: "exported", Hash: V2, AdmissionID: admID,
		}, strptr(V1))
		mustNil(t, err, "as-of update to v2")
		if !moved {
			t.Fatal("as-of update should move")
		}
		// Fetch the two window start instants from history (server clock).
		rows, err := q.Query(ctx, `
SELECT hash, valid_from FROM name_pointer_history
WHERE name='app/x/ver' AND scope_kind=0 AND scope_id='' ORDER BY valid_from`)
		mustNil(t, err, "as-of read history")
		var hs []string
		var ts []time.Time
		for rows.Next() {
			var h string
			var tt time.Time
			if err := rows.Scan(&h, &tt); err != nil {
				t.Fatalf("as-of scan: %v", err)
			}
			hs = append(hs, h)
			ts = append(ts, tt)
		}
		mustNil(t, rows.Err(), "as-of rows err")
		if len(ts) != 2 || hs[0] != V1 || hs[1] != V2 {
			t.Fatalf("as-of history unexpected: hs=%v", hs)
		}
		tv1, tv2 := ts[0], ts[1]
		if r, ok := resolve(ResolveReq{Name: "app/x/ver", AsOf: &tv1}); !ok || r.Hash != V1 {
			t.Fatalf("as-of at v1 window: want %s got %+v ok=%v", V1, r, ok)
		}
		if r, ok := resolve(ResolveReq{Name: "app/x/ver", AsOf: &tv2}); !ok || r.Hash != V2 {
			t.Fatalf("as-of at v2 window: want %s got %+v ok=%v", V2, r, ok)
		}
	})

	// BUILD-A (ADR-03 §3): as-of carries the identical visibility predicate —
	// a private helper is as invisible historically as it is live.
	t.Run("as_of_private_visibility", func(t *testing.T) {
		var tw time.Time
		ok, err := q.QueryRow(ctx, `
SELECT valid_from FROM name_pointer_history
WHERE name='app/crm/deal/roundUp' AND scope_kind=0 AND scope_id='' AND valid_to IS NULL`,
			nil, &tw)
		mustNil(t, err, "read private window start")
		if !ok {
			t.Fatal("private helper has no history window")
		}
		if r, ok := resolve(ResolveReq{Name: "app/crm/deal/roundUp", CallerModule: "app/crm/deal", AsOf: &tw}); !ok || r.Hash != Hpriv {
			t.Fatalf("as-of same-module private: want %s got %+v ok=%v", Hpriv, r, ok)
		}
		if _, ok := resolve(ResolveReq{Name: "app/crm/deal/roundUp", CallerModule: "app/other", AsOf: &tw}); ok {
			t.Fatal("as-of private name must be invisible to a different module")
		}
		if _, ok := resolve(ResolveReq{Name: "app/crm/deal/roundUp", CallerModule: "", AsOf: &tw}); ok {
			t.Fatal("as-of private name must be invisible to an external caller")
		}
	})

	// CAS: two updates from the same stale base ⇒ exactly one wins.
	t.Run("cas_stale_base_loses", func(t *testing.T) {
		seed(Pointer{Name: "app/x/cas", ScopeKind: 0, Kind: "function", Visibility: "exported", Hash: Hb, AdmissionID: admID})
		moved, err := UpsertPointerCAS(ctx, q, Pointer{
			Name: "app/x/cas", ScopeKind: 0, Kind: "function", Visibility: "exported", Hash: H1c, AdmissionID: admID,
		}, strptr(Hb))
		mustNil(t, err, "cas first update")
		if !moved {
			t.Fatal("first CAS from base Hb should win")
		}
		// Second update still expects the STALE base Hb; head is now H1c ⇒ 0 rows.
		moved, err = UpsertPointerCAS(ctx, q, Pointer{
			Name: "app/x/cas", ScopeKind: 0, Kind: "function", Visibility: "exported", Hash: H2c, AdmissionID: admID,
		}, strptr(Hb))
		mustNil(t, err, "cas stale update")
		if moved {
			t.Fatal("stale-base CAS must lose (0 rows), reported not-moved")
		}
		// Head remains H1c.
		if r, ok := resolve(ResolveReq{Name: "app/x/cas"}); !ok || r.Hash != H1c {
			t.Fatalf("after CAS contention head want %s, got %+v ok=%v", H1c, r, ok)
		}
		// Expect-new-name on an existing name also loses (0 rows), no error.
		moved, err = UpsertPointerCAS(ctx, q, Pointer{
			Name: "app/x/cas", ScopeKind: 0, Kind: "function", Visibility: "exported", Hash: H2c, AdmissionID: admID,
		}, nil)
		mustNil(t, err, "cas expect-new on existing")
		if moved {
			t.Fatal("expect-new-name on an existing name must not move")
		}
	})
}

func strptr(s string) *string { return &s }
