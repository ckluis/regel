package cek

import (
	"sort"
	"strings"
)

// stdread.go — the row-backed READ natives (STAGE-E D1/D6a): std/identity's
// currentUser/currentOrg (real per-principal lookup, ADR-10 §3) and std/sql's
// typed parameterized query surface (ADR-10 §4). All reach rows through the Host's
// read-only Reader seam; none fabricates data. When no Reader is wired (unit
// tests), an identity read returns null and a sql read faults closed — never a fake.

// --- std/identity: row-backed currentUser / currentOrg (ADR-10 §3, D6a) -------

// StdIdentityCurrentUser returns the evaluating principal's user_account row as a
// record {id, org, email, name, roles}, or null when the principal maps to no
// user. Row-backed: two different principals resolve to two different users (the
// D6a red-path), because the lookup keys on h.Principal.Subject.
func StdIdentityCurrentUser(h *Host, _ []Value) (Value, *NativePark) {
	return identityRead(h, "user")
}

// StdIdentityCurrentOrg returns the evaluating principal's org record {id, name},
// or null when unmapped.
func StdIdentityCurrentOrg(h *Host, _ []Value) (Value, *NativePark) {
	return identityRead(h, "org")
}

func identityRead(h *Host, kind string) (Value, *NativePark) {
	if h.reader == nil {
		return null(), nil // no read seam wired — fail to null, never a fake row
	}
	cols, err := h.reader.Identity(h.ctx, kind, h.Principal.Subject)
	if err != nil {
		return undef(), wfFault("identity.error", err.Error())
	}
	if cols == nil {
		return null(), nil
	}
	return recordFromCols(cols), nil
}

// recordFromCols builds a record from a column map in a DETERMINISTIC key order
// (sorted), so CFR capture of the value round-trips identically across reads.
func recordFromCols(cols map[string]any) Value {
	keys := make([]string, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	r := newRecord()
	for _, k := range keys {
		r.set(k, anyToValue(cols[k]))
	}
	return recVal(r)
}

// --- std/sql: typed parameterized query surface (ADR-10 §4, D1) ---------------

// StdSQLQuery is sql.query(conn, sql, params): a typed, parameterized, SELECT-only
// read against the derived resource tables (ADR-10 §4 "dashboards ride typed
// std/sql queries"). Capability-gated on `sql.query` exactly as std/mail.send
// gates: an ungranted, non-operator caller parks on capability.revoked with NO
// read performed (red-path c). Read-safe by construction: a non-SELECT statement
// is refused at this native boundary and fails closed (red-path b). Runs under the
// eval's as-of read context (h.asOf), propagated to the Reader (red-path: as-of).
func StdSQLQuery(h *Host, args []Value) (Value, *NativePark) {
	// Capability gate (mirrors StdMailSend): ungranted ⇒ capability.revoked park.
	if !h.Principal.IsOperator && !h.Principal.Grants["sql.query"] {
		return undef(), &NativePark{Condition: SignalCondition("capability.revoked",
			[]Restart{
				{Name: "re-grant", Label: "Re-grant sql.query", CapabilityRequired: "operator"},
				{Name: "abort", Label: "Abort"},
			},
			map[string]any{"capability": "sql.query"})}
	}
	// Surface: (conn, sql, params). conn is the std/sql Conn handle (opaque at this
	// floor — the Reader is the kernel pool); sql is a string; params is an array.
	if len(args) < 2 || args[1].Tag != TagStr {
		return undef(), wfFault("sql.arg", "sql.query expects (conn, sql, params)")
	}
	sqlText := args[1].S
	// Read-safe by construction: reject any non-SELECT at the native boundary. This
	// is the runtime leg of ADR-10 §3's "no string SQL is expressible" for reads —
	// a write/DDL statement fails closed here, never reaching Postgres.
	if !isReadOnlySQL(sqlText) {
		return undef(), &NativePark{Condition: SignalCondition("sql.write_refused",
			[]Restart{{Name: "abort", Label: "Abort"}},
			map[string]any{"reason": "sql.query is SELECT-only (read-safe by construction)"})}
	}
	var params []any
	if len(args) >= 3 && args[2].Tag == TagArray {
		for _, el := range args[2].arr().Elems {
			params = append(params, valueToAny(el))
		}
	}
	if h.reader == nil {
		return undef(), wfFault("sql.noreader", "no read seam wired for sql.query")
	}
	rows, err := h.reader.Query(h.ctx, h.asOf, sqlText, params)
	if err != nil {
		return undef(), wfFault("sql.error", err.Error())
	}
	out := &ArrayObj{}
	for _, row := range rows {
		out.Elems = append(out.Elems, recordFromCols(row))
	}
	return arrVal(out), nil
}

// isReadOnlySQL reports whether a statement is a single read-only SELECT. It
// strips leading SQL line/block comments and whitespace, requires a `select`
// prefix, rejects a locking clause (`for update`/`for share` acquire write locks),
// and rejects an embedded statement separator (a non-trailing `;`) so no second
// statement can ride along. Parameters travel as $1 bind values, never string-
// interpolated, so this prefix check plus the closed surface is the read guarantee.
func isReadOnlySQL(s string) bool {
	t := stripSQLLead(s)
	lower := strings.ToLower(t)
	if !strings.HasPrefix(lower, "select ") && !strings.HasPrefix(lower, "select\n") &&
		!strings.HasPrefix(lower, "select\t") {
		return false
	}
	// No trailing/second statement.
	trimmed := strings.TrimRight(t, " \t\n\r")
	trimmed = strings.TrimRight(trimmed, ";")
	if strings.Contains(trimmed, ";") {
		return false
	}
	// No locking clause (a read that takes write locks is not read-safe).
	if strings.Contains(lower, " for update") || strings.Contains(lower, " for share") {
		return false
	}
	return true
}

// stripSQLLead removes leading whitespace and `--`/`/* */` comments so the SELECT
// prefix check cannot be bypassed by a comment before a write statement.
func stripSQLLead(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\n\r")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return ""
		default:
			return s
		}
	}
}
