package pgwire

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// fieldDesc is one column of a RowDescription.
type fieldDesc struct {
	name     string
	tableOID uint32
	colAttr  int16
	typeOID  uint32
	typeLen  int16
	typeMod  int32
	format   int16 // 0 = text (always, in v1)
}

// Result is the outcome of a non-row-returning statement (or the tag of a
// row-returning one after iteration).
type Result struct {
	Tag          string // e.g. "INSERT 0 1", "UPDATE 1"
	RowsAffected int64
}

// Rows is a forward-only cursor over a query result. Not safe for concurrent
// use. Always drained/closed by the Conn before the connection is reusable.
type Rows struct {
	conn   *Conn
	fields []fieldDesc
	// current row: one []byte per column, nil == SQL NULL.
	vals    [][]byte
	err     error
	done    bool
	result  Result
	scanErr error
	cancel  func()
}

// Columns returns the column names.
func (r *Rows) Columns() []string {
	cols := make([]string, len(r.fields))
	for i, f := range r.fields {
		cols[i] = f.name
	}
	return cols
}

// Err returns any error that terminated iteration.
func (r *Rows) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.scanErr
}

// Result returns the CommandComplete tag once iteration is finished.
func (r *Rows) Result() Result { return r.result }

// rawValues exposes the current row's raw column bytes (nil == NULL) for
// typed helpers. Valid only after a successful Next.
func (r *Rows) rawValues() [][]byte { return r.vals }

// Scan copies the current row's columns into dest. Supported dest element
// types: *string, *int64, *int, *bool, *float64, *time.Time, *[]string,
// *[]byte, *any, and pointers to the sql-null wrappers below.
func (r *Rows) Scan(dest ...any) error {
	if r.vals == nil {
		return fmt.Errorf("pgwire: Scan called with no current row")
	}
	if len(dest) != len(r.vals) {
		return fmt.Errorf("pgwire: Scan expected %d dest, got %d", len(r.vals), len(dest))
	}
	for i, d := range dest {
		if err := scanValue(r.vals[i], r.fields[i].typeOID, d); err != nil {
			r.scanErr = fmt.Errorf("pgwire: column %d (%s): %w", i, r.fields[i].name, err)
			return r.scanErr
		}
	}
	return nil
}

// Null wrappers ---------------------------------------------------------------

type NullString struct {
	String string
	Valid  bool
}
type NullInt64 struct {
	Int64 int64
	Valid bool
}
type NullTime struct {
	Time  time.Time
	Valid bool
}

func scanValue(raw []byte, typeOID uint32, dest any) error {
	switch d := dest.(type) {
	case *NullString:
		if raw == nil {
			*d = NullString{}
			return nil
		}
		*d = NullString{String: string(raw), Valid: true}
		return nil
	case *NullInt64:
		if raw == nil {
			*d = NullInt64{}
			return nil
		}
		v, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return err
		}
		*d = NullInt64{Int64: v, Valid: true}
		return nil
	case *NullTime:
		if raw == nil {
			*d = NullTime{}
			return nil
		}
		t, err := parseTimestamp(string(raw))
		if err != nil {
			return err
		}
		*d = NullTime{Time: t, Valid: true}
		return nil
	}

	if raw == nil {
		// NULL into a non-null dest: only *any and *[]byte accept it.
		switch d := dest.(type) {
		case *any:
			*d = nil
			return nil
		case *[]byte:
			*d = nil
			return nil
		case *[]string:
			*d = nil
			return nil
		default:
			return fmt.Errorf("cannot scan NULL into %T", dest)
		}
	}
	s := string(raw)
	switch d := dest.(type) {
	case *string:
		*d = s
	case *[]byte:
		b := make([]byte, len(raw))
		copy(b, raw)
		*d = b
	case *int64:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		*d = v
	case *int:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		*d = int(v)
	case *bool:
		*d = s == "t" || s == "true" || s == "1"
	case *float64:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*d = v
	case *time.Time:
		t, err := parseTimestamp(s)
		if err != nil {
			return err
		}
		*d = t
	case *[]string:
		arr, err := parseTextArray(s)
		if err != nil {
			return err
		}
		*d = arr
	case *any:
		*d = s
	default:
		return fmt.Errorf("unsupported Scan dest %T", dest)
	}
	return nil
}

// parseTimestamp parses Postgres text timestamp / timestamptz formats.
func parseTimestamp(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999999Z07",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05Z07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC3339Nano,
		time.RFC3339,
	}
	var lastErr error
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse timestamp %q: %w", s, lastErr)
}

// parseTextArray parses a one-dimensional Postgres array text literal such as
// {a,b,"c,d",NULL} into a []string. A bare NULL element becomes "".
func parseTextArray(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return []string{}, nil
	}
	if s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("pgwire: malformed array literal %q", s)
	}
	inner := s[1 : len(s)-1]
	var out []string
	var b strings.Builder
	i := 0
	for i < len(inner) {
		c := inner[i]
		switch {
		case c == '"':
			i++
			for i < len(inner) {
				if inner[i] == '\\' && i+1 < len(inner) {
					b.WriteByte(inner[i+1])
					i += 2
					continue
				}
				if inner[i] == '"' {
					i++
					break
				}
				b.WriteByte(inner[i])
				i++
			}
		case c == ',':
			out = append(out, b.String())
			b.Reset()
			i++
		default:
			// unquoted token; read to next comma
			start := i
			for i < len(inner) && inner[i] != ',' {
				i++
			}
			tok := inner[start:i]
			if tok == "NULL" {
				b.WriteString("")
			} else {
				b.WriteString(tok)
			}
		}
	}
	out = append(out, b.String())
	return out, nil
}

// encodeTextArray renders a []string as a Postgres array literal for a text[]
// parameter.
func encodeTextArray(vals []string) string {
	if len(vals) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range v {
			if r == '"' || r == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
