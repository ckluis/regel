package pgwire

import (
	"errors"
	"fmt"
)

// PgError is a parsed Postgres ErrorResponse (or NoticeResponse severity). The
// SQLSTATE Code is the load-bearing field: admission and cfr detect
// serialization failures (40001) and exclusion violations (23P01) via
// errors.As on this type.
type PgError struct {
	Severity       string // ERROR, FATAL, PANIC (or localized SeverityUnlocalized when present)
	Code           string // SQLSTATE, e.g. "23P01"
	Message        string
	Detail         string
	Hint           string
	Position       string
	Where          string
	SchemaName     string
	TableName      string
	ColumnName     string
	DataTypeName   string
	ConstraintName string
	File           string
	Line           string
	Routine        string
}

func (e *PgError) Error() string {
	if e.ConstraintName != "" {
		return fmt.Sprintf("pgwire: %s %s: %s (constraint %s)", e.Severity, e.Code, e.Message, e.ConstraintName)
	}
	return fmt.Sprintf("pgwire: %s %s: %s", e.Severity, e.Code, e.Message)
}

// SQLSTATE codes the substrate reasons about by name.
const (
	CodeSerializationFailure = "40001"
	CodeExclusionViolation   = "23P01"
	CodeUniqueViolation      = "23505"
	CodeForeignKeyViolation  = "23503"
	CodeCheckViolation       = "23514"
	CodeInsufficientPrivilege = "42501"
	CodeNotNullViolation     = "23502"
	CodeRaiseException       = "P0001"
)

// IsCode reports whether err (or a wrapped error) is a *PgError with the given
// SQLSTATE.
func IsCode(err error, code string) bool {
	var pe *PgError
	if errors.As(err, &pe) {
		return pe.Code == code
	}
	return false
}

// parseErrorResponse decodes the field-tagged body of an ErrorResponse ('E') or
// NoticeResponse ('N') message into a PgError.
func parseErrorResponse(body []byte) *PgError {
	e := &PgError{}
	i := 0
	for i < len(body) {
		typ := body[i]
		i++
		if typ == 0 {
			break
		}
		start := i
		for i < len(body) && body[i] != 0 {
			i++
		}
		val := string(body[start:i])
		i++ // skip NUL
		switch typ {
		case 'S':
			if e.Severity == "" {
				e.Severity = val
			}
		case 'V':
			e.Severity = val // non-localized severity, preferred
		case 'C':
			e.Code = val
		case 'M':
			e.Message = val
		case 'D':
			e.Detail = val
		case 'H':
			e.Hint = val
		case 'P':
			e.Position = val
		case 'W':
			e.Where = val
		case 's':
			e.SchemaName = val
		case 't':
			e.TableName = val
		case 'c':
			e.ColumnName = val
		case 'd':
			e.DataTypeName = val
		case 'n':
			e.ConstraintName = val
		case 'F':
			e.File = val
		case 'L':
			e.Line = val
		case 'R':
			e.Routine = val
		}
	}
	return e
}

// ErrConnDead is returned when an operation is attempted on a connection that
// has been destroyed by the destroy-on-desync rule.
var ErrConnDead = errors.New("pgwire: connection is dead (destroyed on desync)")

// ErrPoolClosed is returned by Acquire after the pool has been closed.
var ErrPoolClosed = errors.New("pgwire: pool is closed")

// ErrInTransaction is an internal marker: a connection returned to the pool
// mid-transaction is destroyed rather than pooled.
var errInTransaction = errors.New("pgwire: connection returned mid-transaction")
