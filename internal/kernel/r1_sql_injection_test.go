package kernel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
)

// r1_sql_injection_test.go — STAGE-F R1: the adversarial fixture FAMILY that proves
// the std/sql.query composition surface (a caller SELECT + $1 bind params, no
// auto-injected policy predicate) cannot be subverted into SQL injection, a write,
// a schema change, cross-policy escape, or privilege escalation. Driven through the
// REAL kernel interpreter + the REAL dbReader against REAL PostgreSQL 16.13.
//
// The surface (ADR-10 §4): sql.query(conn, sql, params) is capability-gated
// (`sql.query`), the sql text is refused at the native boundary unless it is a
// single read-only SELECT (isReadOnlySQL — defense-in-depth string check), and the
// read executes inside a READ ONLY transaction so the ENGINE itself refuses any
// write side effect that slips past the string check (BUILD-F R1, ADR-10 §4).
//
// Fixture classes (25 hostile cases + 2 green controls):
//   A. param-injection (6): a hostile PARAM value cannot escape its $1 bind.
//   B. write/DDL text (8):  UPDATE/DELETE/INSERT/DROP/ALTER/CREATE/TRUNCATE/GRANT
//                           refused at the native boundary (sql.write_refused).
//   C. structural (8):      stacked statements, lock clauses, data-modifying CTE,
//                           comment-hidden writes — all refused before PG.
//   D. engine-enforced (2): SELECT nextval()/setval() PASS the string check but are
//                           real writes; the READ ONLY txn makes PG refuse them.
//                           RED-FIRST: before the BUILD-F fix these WROTE.
//   E. privilege (1):       an ungranted, non-operator caller is refused with no
//                           read performed (capability.revoked).

// r1Env bundles the admitted passthrough query def + the two derived tables.
type r1Env struct {
	e       *reactorEnv
	qhash   string // app/r1q/q — query(c, s, p) passthrough
	widget  string // derived table for Widget
	secret  string // derived table for Secret (the "other tenant" exfil target)
}

// setupR1 admits two resources (Widget, Secret), seeds rows, grants sql.query, and
// admits a passthrough query def whose SQL + params arrive as runtime args (so the
// whole hostile family runs without re-admitting per fixture).
func setupR1(t *testing.T) *r1Env {
	t.Helper()
	e := newReactorEnv(t)

	rsrc := `import { resource } from "std/resource";
export const Widget = resource({ fields: { name: "text", score: "number" } });
export const Secret = resource({ fields: { token: "text" } });`
	if v := e.admit(t, rsrc, "app/r1", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit resources: %q %+v", v.Outcome, v.Diagnostics)
	}
	widget := derivedTable(t, e, "app/r1/Widget")
	secret := derivedTable(t, e, "app/r1/Secret")
	e.exec(t, `INSERT INTO `+quoteIdent(widget)+` (name, score) VALUES ('Acme', 10), ('Beta', 20)`)
	e.exec(t, `INSERT INTO `+quoteIdent(secret)+` (token) VALUES ('TOP-SECRET')`)

	e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('engineer:dev','sql.query','','test')`)

	qsrc := `import { query } from "std/sql";
import type { Conn, Row } from "std/sql";
export function q(c: Conn, s: string, p: (string | number)[]): Row[] {
  return query(c, s, p);
}`
	v := e.admitDecl(t, qsrc, "app/r1q", []string{"sql.query"})
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit passthrough query def: %q %+v", v.Outcome, v.Diagnostics)
	}
	return &r1Env{e: e, qhash: v.Hashes["app/r1q/q"], widget: widget, secret: secret}
}

func derivedTable(t *testing.T, e *reactorEnv, name string) string {
	t.Helper()
	var table string
	e.withConn(t, func(c *pgwire.Conn) {
		if ok, err := c.QueryRow(context.Background(),
			`SELECT table_name FROM derived_resource WHERE resource_name=$1 AND scope_kind=0 AND scope_id=''`,
			[]any{name}, &table); err != nil || !ok {
			t.Fatalf("derived_resource %s ok=%v err=%v", name, ok, err)
		}
	})
	return table
}

// call runs the passthrough query def with an arbitrary sql text + params as an
// operator (the capability grant is bypassed so the SELECT-only / injection guards
// are what is under test, not the capability gate — class E covers that).
func (r *r1Env) call(t *testing.T, sqlText string, params ...any) cek.Outcome {
	t.Helper()
	if params == nil {
		params = []any{}
	}
	argJSON, err := json.Marshal([]any{"conn", sqlText, params})
	if err != nil {
		t.Fatal(err)
	}
	args, err := parseArgs(argJSON)
	if err != nil {
		t.Fatalf("parseArgs %s: %v", argJSON, err)
	}
	return r.e.runAs(r.qhash, args, cek.Principal{Subject: "op", IsOperator: true})
}

// seqLastValue reads the widget id sequence's last_value WITHOUT advancing it (the
// D-class write witness): a real write to the sequence changes this.
func (r *r1Env) seqLastValue(t *testing.T) int64 {
	t.Helper()
	return r.e.intScalar(t, `SELECT last_value FROM pg_sequences
	   WHERE schemaname='public' AND sequencename = split_part(pg_get_serial_sequence($1,'id'),'.',2)`, r.widget)
}

func (r *r1Env) rowCount(t *testing.T, table string) int64 {
	t.Helper()
	return r.e.intScalar(t, `SELECT count(*) FROM `+quoteIdent(table))
}

// TestR1SQLInjectionFixtureFamily is the whole hostile family. Every hostile case
// is REFUSED or CONTAINED; the class-D engine-write cases are witnessed red-first
// in the sibling TestR1EngineWriteWasReachableBeforeFix documentation comment (and
// in evidence-f/r1/red-path.txt, captured before the BUILD-F fix landed).
func TestR1SQLInjectionFixtureFamily(t *testing.T) {
	r := setupR1(t)

	widgetRows0 := r.rowCount(t, r.widget)
	secretRows0 := r.rowCount(t, r.secret)
	seq0 := r.seqLastValue(t)

	// --- green controls: the surface really reads (non-vacuous) -----------------
	t.Run("control/exact-param-match", func(t *testing.T) {
		out := r.call(t, `SELECT id, name FROM `+r.widget+` WHERE name = $1`, "Acme")
		rows := doneRows(t, out)
		if len(rows) != 1 || rows[0]["name"] != "Acme" {
			t.Fatalf("control: want exactly Acme, got %v", rows)
		}
	})
	t.Run("control/numeric-param", func(t *testing.T) {
		out := r.call(t, `SELECT name FROM `+r.widget+` WHERE score = $1`, 20)
		rows := doneRows(t, out)
		if len(rows) != 1 || rows[0]["name"] != "Beta" {
			t.Fatalf("control: want Beta, got %v", rows)
		}
	})

	// --- class A: param-injection — the bind is unbreakable ---------------------
	// Each hostile string is compared literally against name; none matches a real
	// row, so the correct result is ZERO rows. If any param were interpreted as SQL
	// (OR 1=1, UNION, stacked, comment-terminate) we would see rows or an error.
	paramInjection := []struct{ name, param string }{
		{"A1/or-string-true", `' OR '1'='1`},
		{"A2/or-comment", `' OR 1=1 --`},
		{"A3/stacked-drop", `x'; DROP TABLE ` + r.widget + `; --`},
		{"A4/union-exfil", `' UNION SELECT id, token FROM ` + r.secret + ` --`},
		{"A5/quote-comment", `foo'--`},
		{"A6/backslash-quote", `\' OR 1=1`},
	}
	for _, tc := range paramInjection {
		t.Run("A/"+tc.name, func(t *testing.T) {
			out := r.call(t, `SELECT id, name FROM `+r.widget+` WHERE name = $1`, tc.param)
			rows := doneRows(t, out)
			if len(rows) != 0 {
				t.Fatalf("injection param %q leaked %d rows: %v", tc.param, len(rows), rows)
			}
		})
	}

	// --- class B: write / DDL text — refused at the native boundary --------------
	writeText := []struct{ name, sql string }{
		{"B1/update", `UPDATE ` + r.widget + ` SET score = 999`},
		{"B2/delete", `DELETE FROM ` + r.widget},
		{"B3/insert", `INSERT INTO ` + r.widget + ` (name, score) VALUES ('x', 1)`},
		{"B4/drop", `DROP TABLE ` + r.widget},
		{"B5/alter", `ALTER TABLE ` + r.widget + ` ADD COLUMN pwned text`},
		{"B6/create", `CREATE TABLE r1_pwned (x int)`},
		{"B7/truncate", `TRUNCATE ` + r.widget},
		{"B8/grant", `GRANT ALL ON ` + r.widget + ` TO PUBLIC`},
	}
	for _, tc := range writeText {
		t.Run("B/"+tc.name, func(t *testing.T) {
			out := r.call(t, tc.sql)
			mustPark(t, out, "sql.write_refused")
		})
	}

	// --- class C: structural — stacked / lock / CTE / comment-hidden write -------
	structural := []struct{ name, sql string }{
		{"C1/stacked-drop", `SELECT 1; DROP TABLE ` + r.widget},
		{"C2/stacked-update", `SELECT 1; UPDATE ` + r.widget + ` SET score = 0`},
		{"C3/block-comment-write", `/* hi */ UPDATE ` + r.widget + ` SET score = 0`},
		{"C4/line-comment-write", "-- innocuous\nDELETE FROM " + r.widget},
		{"C5/for-update-lock", `SELECT * FROM ` + r.widget + ` FOR UPDATE`},
		{"C6/for-share-lock", `SELECT * FROM ` + r.widget + ` FOR SHARE`},
		{"C7/data-modifying-cte", `WITH x AS (DELETE FROM ` + r.widget + ` RETURNING id) SELECT id FROM x`},
		{"C8/semicolon-in-comment", `SELECT 1 /* ; */`},
	}
	for _, tc := range structural {
		t.Run("C/"+tc.name, func(t *testing.T) {
			out := r.call(t, tc.sql)
			mustPark(t, out, "sql.write_refused")
		})
	}

	// --- class D: engine-enforced write side effects (RED-FIRST) ----------------
	// nextval()/setval() are SELECT-prefixed and pass isReadOnlySQL, but they WRITE
	// (advance/reset a sequence). Before BUILD-F the non-as-of path did NOT wrap the
	// read in a READ ONLY transaction, so these actually mutated the sequence. Now
	// the READ ONLY transaction makes PostgreSQL refuse them (sql.error / "read-only
	// transaction"), and the sequence is untouched.
	engineWrite := []struct{ name, sql string }{
		{"D1/nextval", `SELECT nextval(pg_get_serial_sequence('` + r.widget + `','id'))`},
		{"D2/setval", `SELECT setval(pg_get_serial_sequence('` + r.widget + `','id'), 999999)`},
	}
	for _, tc := range engineWrite {
		t.Run("D/"+tc.name, func(t *testing.T) {
			out := r.call(t, tc.sql)
			if out.Kind != cek.OutParked || out.Condition == nil || out.Condition.Class != "sql.error" {
				t.Fatalf("engine-write %q must be refused by the read-only txn (sql.error), got kind=%v cond=%+v",
					tc.sql, out.Kind, out.Condition)
			}
			if msg := parkErrText(out); !strings.Contains(strings.ToLower(msg), "read-only") {
				t.Fatalf("engine-write %q: want a read-only-transaction refusal, got %q", tc.sql, msg)
			}
			if got := r.seqLastValue(t); got != seq0 {
				t.Fatalf("engine-write %q ADVANCED the sequence %d -> %d (a real write got through)", tc.sql, seq0, got)
			}
		})
	}

	// --- class E: privilege — ungranted non-operator caller reads nothing --------
	t.Run("E/ungranted-caller-refused", func(t *testing.T) {
		argJSON, _ := json.Marshal([]any{"conn", `SELECT id, name FROM ` + r.widget, []any{}})
		args, _ := parseArgs(argJSON)
		out := r.e.runAs(r.qhash, args, cek.Principal{Subject: "user:eve", Grants: map[string]bool{}})
		mustPark(t, out, "capability.revoked")
	})

	// --- final integrity: nothing wrote, nothing dropped, no escape -------------
	if got := r.rowCount(t, r.widget); got != widgetRows0 {
		t.Fatalf("Widget row count changed %d -> %d — a hostile fixture wrote", widgetRows0, got)
	}
	if got := r.rowCount(t, r.secret); got != secretRows0 {
		t.Fatalf("Secret row count changed %d -> %d", secretRows0, got)
	}
	if got := r.seqLastValue(t); got != seq0 {
		t.Fatalf("Widget id sequence changed %d -> %d — a write side effect got through", seq0, got)
	}
	// The would-be-created table from B6 must not exist.
	if got := r.e.intScalar(t, `SELECT count(*) FROM information_schema.tables WHERE table_name='r1_pwned'`); got != 0 {
		t.Fatalf("B6/create leaked a table into the schema")
	}
	// The would-be-added column from B5 must not exist.
	if got := r.e.intScalar(t,
		`SELECT count(*) FROM information_schema.columns WHERE table_name=$1 AND column_name='pwned'`, r.widget); got != 0 {
		t.Fatalf("B5/alter leaked a column")
	}
}

// doneRows asserts an OutDone outcome and returns its rows as []map[string]any.
func doneRows(t *testing.T, out cek.Outcome) []map[string]any {
	t.Helper()
	if out.Kind != cek.OutDone {
		cls := ""
		if out.Condition != nil {
			cls = out.Condition.Class
		}
		t.Fatalf("expected OutDone, got kind=%v class=%q", out.Kind, cls)
	}
	arr, ok := ValueToJSON(out.Value).([]any)
	if !ok {
		t.Fatalf("expected a row array, got %v", ValueToJSON(out.Value))
	}
	rows := make([]map[string]any, 0, len(arr))
	for _, r := range arr {
		m, _ := r.(map[string]any)
		rows = append(rows, m)
	}
	return rows
}

func mustPark(t *testing.T, out cek.Outcome, class string) {
	t.Helper()
	if out.Kind != cek.OutParked || out.Condition == nil || out.Condition.Class != class {
		gotCls := ""
		if out.Condition != nil {
			gotCls = out.Condition.Class
		}
		t.Fatalf("expected park %q, got kind=%v class=%q", class, out.Kind, gotCls)
	}
}

func parkErrText(out cek.Outcome) string {
	if out.Condition == nil {
		return ""
	}
	if v, ok := out.Condition.Payload["error"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
