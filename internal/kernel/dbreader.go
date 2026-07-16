package kernel

import (
	"context"
	"fmt"
	"time"

	"regel.dev/regel/internal/pgwire"
)

// dbreader.go — the kernel's cek.Reader implementation (STAGE-E D1/D6a): the
// read-only DB seam std natives (identity.currentUser/currentOrg, std/sql.query)
// reach rows through. It is SELECT-only by construction: Identity issues a fixed
// parameterized read of user_account; Query executes a caller SELECT the native
// has already proven read-only (isReadOnlySQL) with $1 bind params, honoring the
// eval's as-of as a consistent read snapshot. It NEVER writes.
type dbReader struct{ pool *pgwire.Pool }

// Identity reads the evaluating principal's user or org record from user_account,
// keyed on the CFR principal subject. Returns nil for an unmapped principal — no
// hardcoded identity (D6a: two principals read two different users, or null).
func (d *dbReader) Identity(ctx context.Context, kind, subject string) (map[string]any, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer d.pool.Release(conn)

	var userID, orgID, orgName, email, name, roles pgwire.NullString
	found, err := conn.QueryRow(ctx,
		`SELECT user_id, org_id, org_name, email, display_name, roles
		   FROM user_account WHERE subject=$1`,
		[]any{subject}, &userID, &orgID, &orgName, &email, &name, &roles)
	if err != nil || !found {
		return nil, err
	}
	switch kind {
	case "org":
		if orgID.String == "" {
			return nil, nil
		}
		return map[string]any{"id": orgID.String, "name": orgName.String}, nil
	default: // "user"
		return map[string]any{
			"id":    userID.String,
			"org":   orgID.String,
			"email": email.String,
			"name":  name.String,
			"roles": roles.String,
		}, nil
	}
}

// Query runs a parameterized SELECT (already proven read-only at the native
// boundary) and returns rows as column maps. Every column is read as text (the
// derived resource tables' display form, mirroring the erf read path's ::text
// scan) so the result is uniform and CFR-encodable. When asOf is set, the read
// runs inside a REPEATABLE READ snapshot — the eval's consistent read context
// (data reads stay live under the policy horizon, as the erf read path does; asOf
// pins the snapshot boundary).
func (d *dbReader) Query(ctx context.Context, asOf *time.Time, sql string, params []any) ([]map[string]any, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer d.pool.Release(conn)

	snapshot := asOf != nil
	if snapshot {
		if _, err := conn.Exec(ctx, `BEGIN ISOLATION LEVEL REPEATABLE READ, READ ONLY`); err != nil {
			return nil, err
		}
	}
	rows, qerr := conn.Query(ctx, sql, params...)
	if qerr != nil {
		if snapshot {
			_, _ = conn.Exec(ctx, `ROLLBACK`)
		}
		return nil, qerr
	}
	cols := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		cells := make([]pgwire.NullString, len(cols))
		dest := make([]any, len(cols))
		for i := range cells {
			dest[i] = &cells[i]
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			if snapshot {
				_, _ = conn.Exec(ctx, `ROLLBACK`)
			}
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			if cells[i].Valid {
				m[c] = cells[i].String
			} else {
				m[c] = nil
			}
		}
		out = append(out, m)
	}
	err = rows.Err()
	rows.Close()
	if snapshot {
		if _, cerr := conn.Exec(ctx, `COMMIT`); cerr != nil && err == nil {
			err = cerr
		}
	}
	if err != nil {
		return nil, fmt.Errorf("sql.query: %w", err)
	}
	return out, nil
}
