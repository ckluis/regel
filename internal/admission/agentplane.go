package admission

import (
	"context"
	"sort"
	"time"

	"regel.dev/regel/internal/catalog"
)

// agentplane.go is the admission-side of the ADR-12 agent plane: the §6 patch
// scope policy (overlay self-serve; product by one-shot human approval token) and
// the token mint/consume. It is enforced INSIDE the one gate (no second pipeline):
// checkScopePolicy runs before any row is written (zero trace on refusal), the
// token is consumed inside the admission transaction, and both are shared by every
// door (HTTP, CLI, MCP).

// approvalToken is a validated one-shot product-scope approval (ADR-12 §6).
type approvalToken struct {
	Token    string
	MintedBy string
}

// agentOverlayScope is an agent principal's own overlay (sandbox org) scope: the
// only scope its patches may self-serve. Product requires a token; any other scope
// is unreachable (ADR-03 §3 overlay isolation).
func agentOverlayScope(auth Principal) Scope {
	return Scope{Kind: 2, ID: auth.Chain.OrgID}
}

// AgentOverlayScope is the exported form the MCP door uses to default a patch's
// target scope to the agent's own overlay (scope binds from the principal, §6).
func AgentOverlayScope(auth Principal) Scope { return agentOverlayScope(auth) }

// isProductScope reports whether s is the product scope (kind 0, "").
func isProductScope(s Scope) bool { return s.Kind == 0 && s.ID == "" }

// sortedHashes is the deterministic content-hash list of a lowered patch, the form
// approval tokens bind to and the refusal ledger records.
func sortedHashes(lowered []loweredDef) []string {
	out := make([]string, 0, len(lowered))
	for _, ld := range lowered {
		out = append(out, ld.Def.Hash)
	}
	sort.Strings(out)
	return out
}

// checkScopePolicy enforces ADR-12 §6 for an agent principal. Non-agent principals
// are unaffected (engineers/tenants/system keep the Stage-A scope semantics). It
// returns a validated token to consume (product scope), a refusal Diagnostic
// (escalation / dead token / out-of-reach scope), or nothing (overlay self-serve).
func checkScopePolicy(ctx context.Context, q catalog.Querier, patch Patch, auth Principal, scope Scope, lowered []loweredDef) (*approvalToken, *Diagnostic, error) {
	if auth.ActorKind != "agent" {
		return nil, nil, nil
	}
	overlay := agentOverlayScope(auth)

	// Overlay self-serve: the agent targets its own sandbox org scope.
	if overlay.ID != "" && scope.Kind == overlay.Kind && scope.ID == overlay.ID {
		return nil, nil, nil
	}

	// Product scope requires a one-shot approval token (default-deny product).
	if isProductScope(scope) {
		if patch.ApprovalToken == "" {
			d := capEscalationDiag(auth, scope,
				"agent principal may not self-serve a product-scope patch; a human product-write holder must approve it (one-shot token). Escalation without a token is refused and audited")
			return nil, &d, nil
		}
		tok, diag, err := validateApprovalToken(ctx, q, patch.ApprovalToken, auth, scope, sortedHashes(lowered))
		if err != nil {
			return nil, nil, err
		}
		if diag != nil {
			return nil, diag, nil
		}
		return tok, nil, nil
	}

	// Any other scope is unreachable for an agent (another tenant's overlay, a
	// package/team/user scope it does not own) — refused as an escalation.
	d := capEscalationDiag(auth, scope,
		"agent principal may target only its own overlay (sandbox org) scope or, with approval, product scope; the requested scope is out of reach")
	return nil, &d, nil
}

// validateApprovalToken loads and checks a token against the submission (ADR-12
// §6): unexpired, unconsumed, minted for THIS agent, authorizing THIS scope, and
// bound to EXACTLY these content hashes. Any failure is a dead token ⇒ refusal.
func validateApprovalToken(ctx context.Context, q catalog.Querier, token string, auth Principal, scope Scope, hashes []string) (*approvalToken, *Diagnostic, error) {
	var mintedBy, mintedFor, scopeAttempted string
	var boundHashes []string
	var expired, consumed bool
	found, err := q.QueryRow(ctx, `
SELECT minted_by, minted_for, scope_attempted, bound_hashes,
       (expires_at <= now()) AS expired, (consumed_by IS NOT NULL) AS consumed
FROM approval_token WHERE token = $1`,
		[]any{token}, &mintedBy, &mintedFor, &scopeAttempted, &boundHashes, &expired, &consumed)
	if err != nil {
		return nil, nil, err
	}
	reject := func(msg string) (*approvalToken, *Diagnostic, error) {
		d := Diagnostic{
			StageOrVerifier: "V1", Code: "APPROVAL_INVALID", Severity: "error",
			Subject: auth.Subject(),
			Message: "product-scope approval token is invalid: " + msg,
			Fix:     "obtain a fresh approval from a human product-write holder for the current patch hashes and resubmit",
		}
		return nil, &d, nil
	}
	if !found {
		return reject("no such token")
	}
	if expired {
		return reject("token expired — re-approval required")
	}
	if consumed {
		return reject("token already consumed (replay) — re-approval required")
	}
	if mintedFor != auth.Subject() {
		return reject("token was minted for a different principal")
	}
	if scopeAttempted != scopeKey(scope) {
		return reject("token authorizes a different scope")
	}
	if !sameStringSet(boundHashes, hashes) {
		return reject("bound content hashes no longer match the submission (drift) — re-approval required")
	}
	return &approvalToken{Token: token, MintedBy: mintedBy}, nil, nil
}

// consumeApprovalToken CAS-consumes a token inside the admission txn (one-shot). It
// reports whether THIS admission won the consume (0 rows ⇒ already consumed).
func consumeApprovalToken(ctx context.Context, q catalog.Querier, token string, admissionID int64) (bool, error) {
	res, err := q.Exec(ctx,
		`UPDATE approval_token SET consumed_by=$2, consumed_at=now() WHERE token=$1 AND consumed_by IS NULL`,
		token, admissionID)
	if err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// approvalReplayDiag is the refusal when a token loses the consume CAS (replay).
func approvalReplayDiag() Diagnostic {
	return Diagnostic{
		StageOrVerifier: "V1", Code: "APPROVAL_REPLAY", Severity: "error",
		Message: "the approval token was already consumed by another admission (replay); the submission was refused",
		Fix:     "obtain a fresh approval from a human product-write holder and resubmit",
	}
}

// capEscalationDiag is the CAP_UNGRANTED refusal an agent scope-escalation lands
// (ADR-12 §6): the refusal-ledger row it produces names principal, scope, hashes.
func capEscalationDiag(auth Principal, scope Scope, msg string) Diagnostic {
	return Diagnostic{
		StageOrVerifier: "V1", Code: "CAP_UNGRANTED", Severity: "error",
		Subject: auth.Subject(),
		Message: "scope escalation refused (" + scopeKey(scope) + "): " + msg,
		Fix:     "target your own overlay scope, or have a human approve a product-scope patch (one-shot token)",
	}
}

// scopeKey is the "kind:id" refusal/token scope key.
func scopeKey(s Scope) string {
	return itoaAdm(s.Kind) + ":" + s.ID
}

// sameStringSet reports set equality of two string slices (order-independent).
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// --- approval token mint (ADR-12 §6/§7: the human-gated mint path) ------------

// MintApprovalToken mints a one-shot product-scope approval token bound to the
// exact submitted hashes (ADR-12 §6). The minter must hold the product-write
// capability (checked against grant_row); the token is minted FOR the named agent
// principal, authorizing the given scope, expiring after ttl. Exposed as the
// operator/CLI `regel approve` door — the ONLY place a product-write token is born.
func MintApprovalToken(ctx context.Context, q catalog.Querier, minter, forAgent string, scope Scope, hashes []string, ttl time.Duration) (string, error) {
	sorted := append([]string(nil), hashes...)
	sort.Strings(sorted)
	token := NewUUID()
	if _, err := q.Exec(ctx, `
INSERT INTO approval_token (token, bound_hashes, minted_by, minted_for, scope_attempted, expires_at)
VALUES ($1, $2::text[], $3, $4, $5, now() + make_interval(secs => $6))`,
		token, sorted, minter, forAgent, scopeKey(scope), int(ttl.Seconds())); err != nil {
		return "", err
	}
	return token, nil
}

// HoldsProductWrite reports whether a principal holds the product-write capability
// grant (the mint precondition — ADR-12 §6).
func HoldsProductWrite(ctx context.Context, q catalog.Querier, subject string) (bool, error) {
	var one int
	ok, err := q.QueryRow(ctx,
		`SELECT 1 FROM grant_row WHERE subject=$1 AND capability='product.write'`,
		[]any{subject}, &one)
	return ok, err
}
