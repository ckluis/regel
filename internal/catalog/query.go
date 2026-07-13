package catalog

import (
	"context"

	"regel.dev/regel/internal/pgwire"
)

// Querier is the minimal surface the catalog helpers need from internal/pgwire.
// Both *pgwire.Conn (a pool-acquired connection or a standalone one) and a
// transaction-bound connection satisfy it, so a helper runs identically inside
// the ADR-03 §5 admission transaction or on a plain read connection.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgwire.Result, error)
	Query(ctx context.Context, sql string, args ...any) (*pgwire.Rows, error)
	QueryRow(ctx context.Context, sql string, args []any, dest ...any) (bool, error)
}

// ptrArg converts an optional *string into an argument encodeArgs accepts: a
// nil pointer becomes an untyped nil (SQL NULL), a set pointer its value.
func ptrArg(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
