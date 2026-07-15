package admission

import (
	"context"
	"time"

	"regel.dev/regel/internal/pgwire"
	"regel.dev/regel/internal/ui"
)

// masking.go is the BUILD-D increment D2 render-path masking runtime (ADR-11 §8):
// the DB-backed bridge between the pure ui.MaskCtx and the catalog's grant + vault
// substrate. It is the layer that decides, per masking-leaf slot, whether the
// render principal holds a LIVE reveal grant for (resource, subject, field) — and,
// when it does, recovers the plaintext from the vault and writes a reveal_audit row.
//
// Invariant (ADR-11 §8): plaintext never enters the slot snapshot. The pure layer
// stores the mask token (plus the grant scope when revealed); the plaintext this
// resolver returns rides only the transient frame value. Grant expiry re-masks at
// the next render because the resolver simply stops returning the plaintext.

// RevealCapability is the grant_row.capability a render-path reveal grant carries.
// The schema's reveal_grant_human_only CHECK forbids an agent principal from holding
// it, so vault plaintext is structurally unreachable from the agent plane.
const RevealCapability = "pii.reveal"

// revealScope is the grant_row.scope encoding for a reveal grant: the exact
// (resource, subject, field) triple the grant authorizes.
func revealScope(resource, subject, field string) string {
	return resource + "|" + subject + "|" + field
}

// BuildMaskCtx constructs a ui.MaskCtx for one render principal. It preloads the
// principal's live (unexpired) reveal-grant scopes, then returns a resolver that:
//   - denies (mask token) when no live grant scopes (resource, subject, field);
//   - on a live grant, recovers plaintext via VaultReveal and writes a reveal_audit
//     row, returning (plaintext, grant scope, true).
// A crypto-shredded subject reveals nothing (VaultReveal fails closed), so the
// resolver falls back to the mask token even under a live grant.
func BuildMaskCtx(ctx context.Context, conn *pgwire.Conn, principal string) (*ui.MaskCtx, error) {
	live, err := loadRevealScopes(ctx, conn, principal)
	if err != nil {
		return nil, err
	}
	return &ui.MaskCtx{
		Principal: principal,
		Reveal: func(resource, subject, field string) (string, string, bool) {
			scope := revealScope(resource, subject, field)
			if !live[scope] {
				return "", "", false
			}
			pt, ok, err := VaultReveal(ctx, conn, resource, subject, field)
			if err != nil || !ok {
				return "", "", false // shredded / missing ciphertext ⇒ stay masked
			}
			// A revealed materialization is durably audited (the ACT, not the value).
			if _, aerr := conn.Exec(ctx,
				`INSERT INTO reveal_audit (resource, subject_id, field, principal, grant_scope)
				 VALUES ($1,$2,$3,$4,$5)`,
				resource, subject, field, principal, scope); aerr != nil {
				return "", "", false // fail closed if the audit write fails
			}
			return pt, scope, true
		},
	}, nil
}

// loadRevealScopes reads the principal's live reveal-grant scopes (expiry honored).
func loadRevealScopes(ctx context.Context, conn *pgwire.Conn, principal string) (map[string]bool, error) {
	rows, err := conn.Query(ctx,
		`SELECT scope FROM grant_row
		 WHERE capability=$1 AND subject=$2 AND (expires_at IS NULL OR expires_at > now())`,
		RevealCapability, principal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return nil, err
		}
		out[scope] = true
	}
	return out, rows.Err()
}

// MintRevealGrant inserts (or refreshes) a reveal grant for a HUMAN principal over
// exactly one (resource, subject, field), expiring at expiresAt (zero ⇒ no expiry).
// A helper for D3's approval flow and the D2 masking tests; the schema CHECK rejects
// an agent subject at the DB boundary.
func MintRevealGrant(ctx context.Context, conn *pgwire.Conn, principal, resource, subject, field string, expiresAt time.Time, grantedBy string) error {
	scope := revealScope(resource, subject, field)
	var exp any
	if !expiresAt.IsZero() {
		exp = expiresAt
	}
	_, err := conn.Exec(ctx,
		`INSERT INTO grant_row (subject, capability, scope, expires_at, granted_by)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (subject, capability, scope) DO UPDATE
		   SET expires_at=EXCLUDED.expires_at, granted_by=EXCLUDED.granted_by`,
		principal, RevealCapability, scope, exp, grantedBy)
	return err
}
