package gitproj

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/pgwire"
)

// inbound.go is the ADR-09 §4 inbound door as kernel machinery: a git-submission
// entry point that takes a branch's changed files (path → content), maps the
// verified git identity to a catalog principal, converts them to an ADR-07 Patch
// via the shared name→path INVERSE (catalog.NameFromPath), and runs the REAL
// admission pipeline — dry-run for a PR check (reuses admission.DryRun, a full txn
// then ROLLBACK), the real transaction on merge (admission.Admit, via='git'). One
// gate, three doors; rejection through git is a Verdict like everywhere else.

// Submission is one branch's changed files plus the verified committer identity.
type Submission struct {
	// Files maps a repo-relative projection path (e.g. "app/demo/greet.ts") to its
	// TypeScript content.
	Files map[string]string
	// Email is the VERIFIED git committer identity (the forge authenticates it;
	// here it is trusted input). It maps to a catalog principal via git_identity.
	Email string
	// Bases maps a catalog name to the head hash the PR saw when opened (ADR-07
	// pointer-move base). Absent ⇒ expect-new. The merge-time CAS makes a moved
	// base a stale-base Verdict — the merge side door is impossible.
	Bases map[string]string
}

// DryRun runs the ADR-07 pipeline as a PR check: the full transaction, then
// ROLLBACK, returning the Verdict for the forge to render as a required status
// check (reuses admission.DryRun — the ONE dry-run path, shared with MCP
// commit:false). main never moves.
func DryRun(ctx context.Context, conn *pgwire.Conn, sub Submission, im *admission.Image) (admission.Verdict, error) {
	auth, patch, refusal, ok, err := prepare(ctx, conn, sub)
	if err != nil {
		return admission.Verdict{}, err
	}
	if !ok {
		return refusal, nil
	}
	return admission.DryRun(ctx, conn, patch, auth, im)
}

// Merge runs the ADR-07 pipeline as a real admission (admission.Admit, via='git').
// On ACCEPT the projector advances main to the canonical commit derived from the
// new admission row — the landed bytes are the PRINTER's, not the pusher's
// (normalization-on-merge). On REJECT the transaction rolls back and main never
// moves. mirror may be nil (no mirror configured).
func Merge(ctx context.Context, conn *pgwire.Conn, sub Submission, im *admission.Image, mirror *Mirror) (admission.Verdict, error) {
	auth, patch, refusal, ok, err := prepare(ctx, conn, sub)
	if err != nil {
		return admission.Verdict{}, err
	}
	if !ok {
		return refusal, nil
	}
	v, err := admission.Admit(ctx, conn, patch, auth, im)
	if err != nil {
		return admission.Verdict{}, err
	}
	if mirror != nil && (v.Outcome == admission.OutcomeAdmitted || v.Outcome == admission.OutcomeAlreadyAdmitted) {
		if _, err := mirror.Advance(ctx, conn); err != nil {
			return admission.Verdict{}, err
		}
	}
	return v, nil
}

// prepare resolves the identity and builds the Patch. When the identity is unmapped
// it returns ok=false with a rejected Verdict (a gate_refusal row, no admission) —
// rejected at scope-bind, refusal only (ADR-09 §4 / Red-Path "Identity mapping").
func prepare(ctx context.Context, conn *pgwire.Conn, sub Submission) (admission.Principal, admission.Patch, admission.Verdict, bool, error) {
	auth, scope, known, err := resolveGitIdentity(ctx, conn, sub.Email)
	if err != nil {
		return admission.Principal{}, admission.Patch{}, admission.Verdict{}, false, err
	}
	if !known {
		// BUILD-C RED (increment C6): the identity gate is not yet built — an
		// unmapped identity binds a default principal and proceeds. GREEN rejects it
		// at scope-bind with a refusal row.
		auth = admission.Principal{ActorKind: "engineer", ActorID: "anonymous", Via: "git"}
		scope = admission.Scope{Kind: 0, ID: ""}
	}
	patch := buildPatch(sub, scope)
	return auth, patch, admission.Verdict{}, true, nil
}

// resolveGitIdentity re-reads git_identity for the committer email on every request
// (rotation, like agent_key). A missing or revoked identity is unknown.
func resolveGitIdentity(ctx context.Context, conn *pgwire.Conn, email string) (admission.Principal, admission.Scope, bool, error) {
	var kind, id, scopeID string
	var scopeKind int
	var revoked bool
	found, err := conn.QueryRow(ctx, `
SELECT actor_kind, actor_id, scope_kind, scope_id, revoked
FROM git_identity WHERE email = $1`,
		[]any{email}, &kind, &id, &scopeKind, &scopeID, &revoked)
	if err != nil || !found || revoked {
		return admission.Principal{}, admission.Scope{}, false, err
	}
	p := admission.Principal{ActorKind: kind, ActorID: id, Via: "git"}
	switch scopeKind {
	case 1:
		p.Chain = catalog.Chain{PackageID: scopeID}
	case 2:
		p.Chain = catalog.Chain{OrgID: scopeID}
	case 3:
		p.Chain = catalog.Chain{TeamID: scopeID}
	case 4:
		p.Chain = catalog.Chain{UserID: scopeID}
	}
	return p, admission.Scope{Kind: scopeKind, ID: scopeID}, true, nil
}

// buildPatch converts the changed files to an ADR-07 Patch. Each projected file is
// a single-definition module (the projection is one file per definition), so it is
// submitted as one ModuleSrc whose module path is the file's catalog name minus its
// last segment (the shared name→path inverse). Non-projection paths are ignored.
func buildPatch(sub Submission, scope admission.Scope) admission.Patch {
	patch := admission.Patch{
		TargetScope: scope,
		BaseHashes:  map[string]string{},
	}
	paths := make([]string, 0, len(sub.Files))
	for p := range sub.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths) // deterministic module order
	for _, p := range paths {
		name, ok := catalog.NameFromPath(p)
		if !ok {
			continue
		}
		module := moduleOf(name)
		if module == "" {
			continue
		}
		patch.Modules = append(patch.Modules, admission.ModuleSrc{ModuleName: module, Source: sub.Files[p]})
	}
	for name, base := range sub.Bases {
		patch.BaseHashes[name] = base
	}
	return patch
}

// moduleOf returns a catalog name's module path (the name minus its last segment):
// "app/demo/greet" → "app/demo". "" if the name has no module prefix.
func moduleOf(name string) string {
	i := strings.LastIndexByte(name, '/')
	if i <= 0 {
		return ""
	}
	return name[:i]
}

// refuseUnknownIdentity writes the scope-bind refusal for an unmapped git identity:
// a gate_refusal row (rejected, principal "git:<email>"), no admission row. The
// returned Verdict carries the durable refusal id.
func refuseUnknownIdentity(ctx context.Context, conn *pgwire.Conn, sub Submission) (admission.Verdict, error) {
	names := make([]string, 0, len(sub.Files))
	for p := range sub.Files {
		if n, ok := catalog.NameFromPath(p); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	principal := "git:" + sub.Email
	v := admission.Verdict{
		Outcome:  admission.OutcomeRejected,
		Hashes:   map[string]string{},
		Stages:   []admission.Stage{{Stage: "scope-bind", Status: "fail"}},
		Seeders:  []admission.Seeder{},
		Diagnostics: []admission.Diagnostic{{
			StageOrVerifier: "scope-bind", Code: "IDENTITY_UNMAPPED", Severity: "error",
			Subject: principal,
			Message: "the git committer identity " + sub.Email + " maps to no catalog principal; a push from an unmapped identity is rejected at scope-bind (no admission)",
			Fix:     "bind the git identity to a catalog principal (git_identity) before pushing, or push under a mapped identity",
		}},
		RefusalID: admission.NewUUID(),
	}
	if err := writeIdentityRefusal(ctx, conn, principal, names, &v); err != nil {
		return admission.Verdict{}, err
	}
	return v, nil
}

// writeIdentityRefusal persists the durable gate_refusal ledger row for the
// scope-bind identity refusal (the same refusal ledger every door writes; ADR-03
// §1 table 6). No admission row is written — the refusal is the only trace.
func writeIdentityRefusal(ctx context.Context, conn *pgwire.Conn, principal string, names []string, v *admission.Verdict) error {
	blob, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO gate_refusal (refusal_id, principal, scope_attempted, submitted_hashes, outcome, verdict)
VALUES ($1, $2, $3, $4::text[], $5, $6::jsonb)`,
		v.RefusalID, principal, "git", names, v.Outcome, string(blob))
	return err
}
