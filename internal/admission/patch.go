package admission

import (
	"strings"

	"regel.dev/regel/internal/catalog"
)

// ModuleSrc is one module of a submitted patch: its catalog module path (no
// extension, e.g. "app/demo") and its TypeScript source.
type ModuleSrc struct {
	ModuleName string `json:"module_name"`
	Source     string `json:"source"`
}

// Scope is the admission target scope (name_pointer scope_kind/scope_id).
type Scope struct {
	Kind int    `json:"kind"`
	ID   string `json:"id"`
}

// Patch is the admission envelope. Identity and scope are bound from the
// AUTHENTICATED principal at admission time (§2a), never from this body: the
// HTTP/CLI caller passes the authenticated Principal separately to Admit.
type Patch struct {
	Modules []ModuleSrc `json:"modules"`
	// DeclaredCapabilities is the per-definition declared capability set
	// (keyed by full catalog name, e.g. "app/crm/notify"). ADR-07 pin #2: the
	// declared set travels in the envelope, not in source.
	DeclaredCapabilities map[string][]string `json:"declared_capabilities,omitempty"`
	// DefaultDeclared is applied to any admitted def with no per-def entry — the
	// CLI's single --declare list flows here.
	DefaultDeclared []string `json:"default_declared,omitempty"`
	// Tier is the per-definition execution tier (STAGE-A-PLAN pin #7). Stored in
	// the envelope; Stage-A eval reads the tier from the eval request, not from a
	// durable per-def column — named residue.
	Tier map[string]string `json:"tier,omitempty"`
	// Intent is the maintenance-lane discriminant (ADR-07 §4 V6). "" is an
	// ordinary additive admission; "retire" routes a resource's REMOVED fields to
	// the staged maintenance lane (BUILD-C: inline destructive DDL is refused;
	// the retire-intent envelope is the named path that admits without it).
	Intent string `json:"intent,omitempty"`
	// TargetScope is where the pointers move. Empty ⇒ product scope (0, "").
	TargetScope Scope `json:"target_scope"`
	// BaseHashes is the head each pointer-move saw (empty entry / absent ⇒
	// expect-new). Keyed by full catalog name.
	BaseHashes map[string]string `json:"base_hashes,omitempty"`
	// ApprovalToken is the optional one-shot product-scope approval token (ADR-12
	// §6). An agent principal targeting product scope must carry a valid token; the
	// admission transaction validates it against the submitted hashes and consumes
	// it (one-shot CAS) on commit. It is a submission credential, not identity, so
	// it rides the envelope — scope still binds from the authenticated principal.
	ApprovalToken string `json:"approval_token,omitempty"`
	// ReadLog is the optional content-seeder read-log (ADR-07 §1 / ADR-12 §6): the
	// provenance of catalog/resource/condition/audit rows the authoring agent read
	// that reach this patch. Validated against the authenticated principal's scope
	// chain at step 2a. Human/CLI submissions carry none (empty seeder set).
	ReadLog []ReadLogEntry `json:"read_log,omitempty"`
}

// ReadLogEntry is one provenance record an agent submits with a patch. Scope is
// the seeder's own scope; step 2a rejects any scope outside the principal's chain
// (unrepresentable), so the set can never be forged to blame another tenant.
type ReadLogEntry struct {
	SourceKind string `json:"source_kind"` // catalog|resource|condition|audit|external
	SourceRef  string `json:"source_ref"`
	Scope      Scope  `json:"scope"`
	SeededBy   string `json:"seeded_by,omitempty"` // principal; "" ⇒ external/unattributed
}

// Principal is the authenticated identity of a submission (§2a). Grants are
// loaded from grant_row by subject at bind time (Stage A), never trusted from
// the envelope.
type Principal struct {
	ActorKind string        `json:"actor_kind"` // engineer|tenant|agent|system
	ActorID   string        `json:"actor_id"`
	Via       string        `json:"via"` // cli|settings|mcp|git
	Chain     catalog.Chain `json:"-"`
}

// Subject is the grant_row subject key for this principal ("kind:id").
func (p Principal) Subject() string { return p.ActorKind + ":" + p.ActorID }

// declaredFor returns the declared capability set for a def catalog name,
// falling back to the patch-level default. Each entry is normalized so a
// declaration written as the import path (`std/mail.send`) is accepted as the
// stripped capability token (`mail.send`) the verifier and grants speak in —
// the two forms are the same capability (STAGE-F R14 papercut 3).
func (pt Patch) declaredFor(name string) []string {
	raw := pt.DefaultDeclared
	if caps, ok := pt.DeclaredCapabilities[name]; ok {
		raw = caps
	}
	out := make([]string, len(raw))
	for i, c := range raw {
		out[i] = normalizeCapability(c)
	}
	return out
}

// normalizeCapability strips a leading `std/` import-path prefix from a declared
// capability so `std/mail.send` and `mail.send` name the same capability. The
// verifier's named set and the grant table both use the stripped token.
func normalizeCapability(c string) string {
	return strings.TrimPrefix(c, "std/")
}
