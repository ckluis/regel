package catalog

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

// Def is one immortal content-addressed definition row (ADR-03 §1 table 1). AST
// carries the canonEncode bytes that hash to Hash; the rest mirror the columns.
type Def struct {
	Hash          string
	ASTSchemaVer  int
	Kind          string
	AST           []byte
	CanonicalText string
	Contracts     string // jsonb text; "" is normalized to "[]"
	Deps          []string
	AdmissionID   int64
}

// Meta is the out-of-hash sidecar (ADR-03 §1 table 2).
type Meta struct {
	Hash      string
	Docstring string
	Comments  string // jsonb text; "" is normalized to "{}"
}

// Pointer is a mutable scoped name pointer (ADR-03 §1 table 3).
type Pointer struct {
	Name        string
	ScopeKind   int
	ScopeID     string
	Kind        string
	Visibility  string // 'exported' | 'private'
	Hash        string
	Overrides   *string
	AdmissionID int64
}

// byteaLiteral renders bytes as a Postgres hex bytea input literal (\x...). The
// wire client sends parameters in text format, so bytea must be hex-encoded.
func byteaLiteral(b []byte) string { return `\x` + hex.EncodeToString(b) }

// decodeBytea parses a hex-format bytea text value (\x...) back to bytes.
func decodeBytea(s string) ([]byte, error) {
	if !strings.HasPrefix(s, `\x`) {
		return nil, fmt.Errorf("catalog: bytea value lacks \\x prefix")
	}
	return hex.DecodeString(s[2:])
}

// InsertDefinition runs the ADR-02 §5 g4 re-hash hook (verify) and, on pass,
// inserts the row content-addressed with ON CONFLICT (hash) DO NOTHING. It
// reports whether a new row was written (false = the content already existed).
// A verify failure aborts before any SQL runs, so a byte/address mismatch never
// reaches the immortal store.
func InsertDefinition(ctx context.Context, q Querier, d Def, verify func(hash string, ast []byte) error) (bool, error) {
	if verify != nil {
		if err := verify(d.Hash, d.AST); err != nil {
			return false, fmt.Errorf("catalog: definition verify failed for %s: %w", d.Hash, err)
		}
	}
	contracts := d.Contracts
	if contracts == "" {
		contracts = "[]"
	}
	deps := d.Deps
	if deps == nil {
		deps = []string{}
	}
	res, err := q.Exec(ctx, `
INSERT INTO definition (hash, ast_schema_ver, kind, ast, canonical_text, contracts, deps, admission_id)
VALUES ($1, $2, $3, $4::bytea, $5, $6::jsonb, $7::text[], $8)
ON CONFLICT (hash) DO NOTHING`,
		d.Hash, d.ASTSchemaVer, d.Kind, byteaLiteral(d.AST), d.CanonicalText, contracts, deps, d.AdmissionID)
	if err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// InsertMeta inserts the out-of-hash sidecar, ON CONFLICT DO NOTHING (an
// existing row's metadata wins, per ADR-03 §5 step 3). Reports whether new.
func InsertMeta(ctx context.Context, q Querier, m Meta) (bool, error) {
	comments := m.Comments
	if comments == "" {
		comments = "{}"
	}
	res, err := q.Exec(ctx, `
INSERT INTO definition_meta (hash, docstring, comments)
VALUES ($1, $2, $3::jsonb)
ON CONFLICT (hash) DO NOTHING`,
		m.Hash, m.Docstring, comments)
	if err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// LoadDefinition reads a definition row by hash. The second return is false when
// no row exists.
func LoadDefinition(ctx context.Context, q Querier, hash string) (Def, bool, error) {
	var d Def
	var astHex string
	ok, err := q.QueryRow(ctx, `
SELECT hash, ast_schema_ver, kind, ast, canonical_text, contracts, deps, admission_id
FROM definition WHERE hash = $1`,
		[]any{hash},
		&d.Hash, &d.ASTSchemaVer, &d.Kind, &astHex, &d.CanonicalText, &d.Contracts, &d.Deps, &d.AdmissionID)
	if err != nil || !ok {
		return Def{}, ok, err
	}
	ast, err := decodeBytea(astHex)
	if err != nil {
		return Def{}, true, err
	}
	d.AST = ast
	return d, true, nil
}

// UpsertPointerCAS performs the ADR-03 §5.7 optimistic compare-and-set on the
// live name pointer. When baseHash is nil the caller expects a brand-new name:
// an insert-if-absent that yields 0 rows (CAS loss) when the name already
// exists. When baseHash is set the pointer is moved only if the current head
// still equals it; a stale base moves 0 rows (a concurrent admission won).
// Either way the returned bool is true iff this call moved the pointer. The I7
// trigger writes history on the insert or the update.
//
// This deliberately does NOT use INSERT ... ON CONFLICT DO UPDATE: on the
// insert-attempt leg of an upsert, the BEFORE INSERT history trigger fires even
// when the row will ultimately be updated, which would open a duplicate history
// window and trip the I4 exclusion. Explicit insert-if-absent (for a new name)
// and UPDATE (for a move) each fire the history trigger exactly once.
func UpsertPointerCAS(ctx context.Context, q Querier, p Pointer, baseHash *string) (bool, error) {
	if baseHash == nil {
		// Insert only if no live winner exists for this exact scope. When one
		// already exists the SELECT yields no row, no trigger fires, and 0 rows
		// are inserted (reported as a CAS loss, not an error).
		res, err := q.Exec(ctx, `
INSERT INTO name_pointer (name, scope_kind, scope_id, kind, visibility, hash, overrides, admission_id)
SELECT $1, $2, $3, $4, $5, $6, $7, $8
WHERE NOT EXISTS (
  SELECT 1 FROM name_pointer WHERE name = $1 AND scope_kind = $2 AND scope_id = $3)`,
			p.Name, p.ScopeKind, p.ScopeID, p.Kind, p.Visibility, p.Hash, ptrArg(p.Overrides), p.AdmissionID)
		if err != nil {
			return false, err
		}
		return res.RowsAffected == 1, nil
	}
	res, err := q.Exec(ctx, `
UPDATE name_pointer
   SET hash = $6, kind = $4, visibility = $5, overrides = $7, admission_id = $8, updated_at = now()
 WHERE name = $1 AND scope_kind = $2 AND scope_id = $3 AND hash = $9`,
		p.Name, p.ScopeKind, p.ScopeID, p.Kind, p.Visibility, p.Hash, ptrArg(p.Overrides), p.AdmissionID, *baseHash)
	if err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}
