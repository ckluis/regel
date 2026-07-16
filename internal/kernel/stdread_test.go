package kernel

import (
	"context"
	"testing"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/pgwire"
)

// stdread_test.go — STAGE-E D1 (std/sql typed parameterized query) + D6a
// (row-backed identity) red-paths, driven through the REAL kernel interpreter with
// the dbReader wired.

// seedUser inserts a user_account row backing a principal subject.
func (e *reactorEnv) seedUser(t *testing.T, subject, userID, org, email, name string) {
	t.Helper()
	e.exec(t, `INSERT INTO user_account (subject, user_id, org_id, org_name, email, display_name, roles)
	           VALUES ($1,$2,$3,$4,$5,$6,'member')`,
		subject, userID, org, org+" Inc", email, name)
}

// runAs runs a resolved def under a chosen principal (bypassing the eval door's
// hardcoded operator subject) so a red-path can eval as two different principals.
func (e *reactorEnv) runAs(hash string, args []cek.Value, p cek.Principal) cek.Outcome {
	return e.srv.Interp().Run(context.Background(), cek.RunReq{
		DefHash: hash, Args: args, Tier: cek.TierTrusted, Principal: p,
	})
}

// TestIdentityRowBacked (D6a): identity.currentUser() is a REAL per-principal read
// of user_account — two different principals return two different users.
//
// RED evidence: with currentUser wired to nativeStub (its Stage-D state) the eval
// returns undefined for BOTH principals and the two results are identical — this
// test's "alice != bob" assertion fails. The row-backed native is the control.
func TestIdentityRowBacked(t *testing.T) {
	e := newReactorEnv(t)
	e.seedUser(t, "user:alice", "U-alice", "org1", "alice@ex.com", "Alice")
	e.seedUser(t, "user:bob", "U-bob", "org2", "bob@ex.com", "Bob")

	src := `import { currentUser } from "std/identity";
import type { User } from "std/identity";
export function whoami(): User { return currentUser(); }`
	v := e.admit(t, src, "app/id", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q %+v", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/id/whoami"]

	outA := e.runAs(hash, nil, cek.Principal{Subject: "user:alice"})
	outB := e.runAs(hash, nil, cek.Principal{Subject: "user:bob"})
	if outA.Kind != cek.OutDone || outB.Kind != cek.OutDone {
		t.Fatalf("eval outcomes: A=%v B=%v", outA.Kind, outB.Kind)
	}
	ja := ValueToJSON(outA.Value)
	jb := ValueToJSON(outB.Value)
	ma, _ := ja.(map[string]any)
	mb, _ := jb.(map[string]any)
	if ma == nil || mb == nil {
		t.Fatalf("currentUser returned non-records: A=%v B=%v", ja, jb)
	}
	if ma["id"] != "U-alice" || mb["id"] != "U-bob" {
		t.Fatalf("row-backed identity wrong: A.id=%v B.id=%v (want U-alice/U-bob)", ma["id"], mb["id"])
	}
	if ma["id"] == mb["id"] || ma["org"] == mb["org"] {
		t.Fatalf("two principals resolved to the SAME user (stub behavior): A=%v B=%v", ma, mb)
	}

	// An unmapped principal reads back null — no hardcoded/fabricated identity.
	outC := e.runAs(hash, nil, cek.Principal{Subject: "user:nobody"})
	if outC.Kind != cek.OutDone {
		t.Fatalf("unmapped eval outcome: %v", outC.Kind)
	}
	if jc := ValueToJSON(outC.Value); jc != nil {
		t.Fatalf("unmapped principal must read null, got %v", jc)
	}
}

// TestSQLQueryTypedParameterized (D1): the typed parameterized query surface end
// to end from admitted TS — (a) a SELECT with a $1 param returns the matching
// rows; (b) a non-SELECT statement is refused (read-safe by construction); (c) an
// ungranted, non-operator caller is refused on the capability.
//
// RED evidence: (a) with std/sql.query still a fixture stub the eval returns
// undefined, not the row array — the "1 row, name=Acme" assertion fails. (b) drop
// the isReadOnlySQL guard and the UPDATE reaches the reader (a write) instead of a
// sql.write_refused park — the OutParked assertion fails. (c) drop the capability
// gate and the ungranted caller reads rows instead of parking capability.revoked.
func TestSQLQueryTypedParameterized(t *testing.T) {
	e := newReactorEnv(t)

	// Admit a resource so a real derived table exists, and seed two rows.
	rsrc := `import { resource } from "std/resource";
export const Widget = resource({ fields: { name: "text", score: "number" } });`
	if v := e.admit(t, rsrc, "app/d1", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit resource: %q %+v", v.Outcome, v.Diagnostics)
	}
	var table string
	e.withConn(t, func(c *pgwire.Conn) {
		if ok, err := c.QueryRow(context.Background(),
			`SELECT table_name FROM derived_resource WHERE resource_name='app/d1/Widget' AND scope_kind=0 AND scope_id=''`,
			nil, &table); err != nil || !ok {
			t.Fatalf("derived_resource ok=%v err=%v", ok, err)
		}
	})
	e.exec(t, `INSERT INTO `+quoteIdent(table)+` (name, score) VALUES ('Acme', 10), ('Beta', 20)`)

	// The submitting engineer must hold sql.query (V1 declare+grant) to admit a
	// def carrying the capability-bearing call.
	e.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('engineer:dev','sql.query','','test')`)

	// Admit a def that queries the derived table with a $1 param.
	qsrc := `import { query } from "std/sql";
import type { Conn, Row } from "std/sql";
export function findByName(c: Conn, n: string): Row[] {
  return query(c, "SELECT name, score FROM ` + table + ` WHERE name = $1", [n]);
}`
	v := e.admitDecl(t, qsrc, "app/d1q", []string{"sql.query"})
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit query def: %q %+v", v.Outcome, v.Diagnostics)
	}
	hash := v.Hashes["app/d1q/findByName"]
	args, err := parseArgs([]byte(`["conn","Acme"]`))
	if err != nil {
		t.Fatal(err)
	}

	// (a) SELECT with $1 works e2e (operator eval bypasses the grant).
	out := e.runAs(hash, args, cek.Principal{Subject: "op", IsOperator: true})
	if out.Kind != cek.OutDone {
		t.Fatalf("(a) query outcome %v, want done", out.Kind)
	}
	rows, ok := ValueToJSON(out.Value).([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("(a) want exactly 1 row for name=Acme, got %v", ValueToJSON(out.Value))
	}
	row0, _ := rows[0].(map[string]any)
	if row0["name"] != "Acme" || row0["score"] != "10" {
		t.Fatalf("(a) wrong row: %v", row0)
	}

	// (c) ungranted, non-operator caller is refused on the capability.
	outC := e.runAs(hash, args, cek.Principal{Subject: "user:eve", Grants: map[string]bool{}})
	if outC.Kind != cek.OutParked || outC.Condition == nil || outC.Condition.Class != "capability.revoked" {
		t.Fatalf("(c) ungranted call must park capability.revoked, got %v %+v", outC.Kind, outC.Condition)
	}

	// granted caller works.
	outG := e.runAs(hash, args, cek.Principal{Subject: "user:g", Grants: map[string]bool{"sql.query": true}})
	if outG.Kind != cek.OutDone {
		t.Fatalf("granted call outcome %v, want done", outG.Kind)
	}

	// (b) a non-SELECT statement is refused, read-safe by construction.
	usrc := `import { query } from "std/sql";
import type { Conn, Row } from "std/sql";
export function evil(c: Conn): Row[] {
  return query(c, "UPDATE ` + table + ` SET score = 999", []);
}`
	uv := e.admitDecl(t, usrc, "app/d1u", []string{"sql.query"})
	if uv.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit update def: %q %+v", uv.Outcome, uv.Diagnostics)
	}
	uhash := uv.Hashes["app/d1u/evil"]
	uargs, _ := parseArgs([]byte(`["conn"]`))
	outU := e.runAs(uhash, uargs, cek.Principal{Subject: "op", IsOperator: true})
	if outU.Kind != cek.OutParked || outU.Condition == nil || outU.Condition.Class != "sql.write_refused" {
		t.Fatalf("(b) non-SELECT must be refused (sql.write_refused), got %v %+v", outU.Kind, outU.Condition)
	}
	// And the UPDATE never executed — the score is untouched.
	if got := e.intScalar(t, `SELECT count(*) FROM `+quoteIdent(table)+` WHERE score=999`); got != 0 {
		t.Fatalf("(b) refused UPDATE still mutated %d rows", got)
	}
}
