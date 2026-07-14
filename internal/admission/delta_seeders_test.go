package admission

import (
	"context"
	"strings"
	"testing"

	"regel.dev/regel/internal/catalog"
)

func agent(id string, chain catalog.Chain) Principal {
	return Principal{ActorKind: "agent", ActorID: id, Via: "mcp", Chain: chain}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// --- Blast-radius delta present + honest on a GREEN verdict (R1-04) -----------
// A widening patch that adds an egress capability AND a newly-sunk (masked) pii
// field: the green Verdict delta names both under added_vs_base.
func TestDeltaNamesWideningOnGreen(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	if _, err := w.conn.Exec(ctx,
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ($1,'mail.send','','test')`,
		engineer("dev").Subject()); err != nil {
		t.Fatal(err)
	}
	src := `import { send } from "std/mail";
import type { Vault } from "std/pii";
import { mask } from "std/pii";
export function notify(owner: Vault<string>): string {
  send("a@b.com", "hi");
  return mask(owner);
}
`
	v, err := admit(ctx, w.conn, src, "app/widen", engineer("dev"), func(p *Patch) {
		p.DeclaredCapabilities = map[string][]string{"app/widen/notify": {"mail.send"}}
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if !contains(v.Delta.Capabilities.AddedVsBase, "mail.send") {
		t.Fatalf("delta must name mail.send under capabilities.added_vs_base: %+v", v.Delta.Capabilities)
	}
	if !contains(v.Delta.PIISurface.AddedVsBase, "owner") {
		t.Fatalf("delta must name owner under pii_surface.added_vs_base: %+v", v.Delta.PIISurface)
	}
	// The full green Verdict is retrievable from admission.verifier_report.
	var outcome, report string
	if _, err := w.conn.QueryRow(ctx,
		`SELECT verifier_report->>'outcome', verifier_report::text FROM admission WHERE id=$1`,
		[]any{v.AdmissionID}, &outcome, &report); err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeAdmitted {
		t.Fatalf("verifier_report outcome = %q, want admitted", outcome)
	}
	if !strings.Contains(report, "mail.send") {
		t.Fatalf("stored verdict missing the delta: %s", report)
	}
	// The delta is persisted to the admission row's verdict_delta column too.
	if got := w.count("SELECT count(*) FROM admission WHERE id=$1 AND verdict_delta IS NOT NULL", v.AdmissionID); got != 1 {
		t.Fatalf("verdict_delta not persisted")
	}
}

// A no-op re-admission has an empty added_vs_base everywhere.
func TestDeltaNoopEmptyAddedVsBase(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := "export function greet(name: string): string {\n  return \"hi \" + name;\n}\n"
	if v1, err := admit(ctx, w.conn, src, "app/noop", engineer("dev"), nil); err != nil || v1.Outcome != OutcomeAdmitted {
		t.Fatalf("first admit: %v / %q", err, v1.Outcome)
	}
	v2, err := admit(ctx, w.conn, src, "app/noop", engineer("dev"), nil)
	if err != nil {
		t.Fatalf("second admit: %v", err)
	}
	if v2.Outcome != OutcomeAlreadyAdmitted {
		t.Fatalf("outcome = %q, want already-admitted", v2.Outcome)
	}
	if len(v2.Delta.Capabilities.AddedVsBase) != 0 || len(v2.Delta.PIISurface.AddedVsBase) != 0 || len(v2.Delta.DDLSurface.AddedVsBase) != 0 {
		t.Fatalf("no-op re-admission must add nothing vs base: %+v", v2.Delta)
	}
}

// --- Content-seeder attribution (R1-04) ---------------------------------------

// An agent seeder outside the principal's scope chain is rejected (unrepresentable).
func TestSeederOutOfChainRejected(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := "export function f(): number {\n  return 1;\n}\n"
	d, a, r := w.snapshot()
	v, err := admit(ctx, w.conn, src, "app/seedbad", agent("a1", catalog.Chain{OrgID: "org1"}), func(p *Patch) {
		p.ReadLog = []ReadLogEntry{{
			SourceKind: "resource", SourceRef: "app/other/Deal",
			Scope: Scope{Kind: 2, ID: "org2"}, SeededBy: "agent:a1",
		}}
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Diagnostics) == 0 || v.Diagnostics[0].Code != "SEEDER_OUT_OF_CHAIN" {
		t.Fatalf("want SEEDER_OUT_OF_CHAIN, got %+v", v.Diagnostics)
	}
	assertZeroTrace(t, w, v, d, a, r, "app/seedbad/f")
}

// An external-effect seeder (no resolvable principal) is recorded 'unattributed',
// never dropped; an in-chain seeder is recorded with its principal.
func TestSeederUnattributedRecorded(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := "export function f(): number {\n  return 1;\n}\n"
	v, err := admit(ctx, w.conn, src, "app/seedok", agent("a1", catalog.Chain{OrgID: "org1"}), func(p *Patch) {
		// The agent self-serves at its OWN overlay scope (ADR-12 §6 realized: an
		// agent may not self-serve product; seeder attribution is scope-independent).
		p.TargetScope = Scope{Kind: 2, ID: "org1"}
		p.ReadLog = []ReadLogEntry{
			{SourceKind: "external", SourceRef: "upstream:fail-text", Scope: Scope{Kind: 0, ID: ""}, SeededBy: ""},
			{SourceKind: "resource", SourceRef: "app/x/Deal", Scope: Scope{Kind: 0, ID: ""}, SeededBy: "agent:a1"},
		}
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Seeders) != 2 {
		t.Fatalf("want 2 seeders recorded, got %+v", v.Seeders)
	}
	var sawUnattr, sawPrincipal bool
	for _, s := range v.Seeders {
		if s.SourceKind == "external" && s.SeededBy == "unattributed" {
			sawUnattr = true
		}
		if s.SourceKind == "resource" && s.SeededBy == "agent:a1" {
			sawPrincipal = true
		}
	}
	if !sawUnattr || !sawPrincipal {
		t.Fatalf("seeders not attributed correctly: %+v", v.Seeders)
	}
	// Persisted to the admission row.
	if got := w.count("SELECT jsonb_array_length(seeders) FROM admission WHERE id=$1", v.AdmissionID); got != 2 {
		t.Fatalf("admission.seeders length = %d, want 2", got)
	}
}

// A human/CLI submission carries an empty seeder set even if a read-log is present.
func TestSeederHumanEmpty(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	src := "export function f(): number {\n  return 1;\n}\n"
	v, err := admit(ctx, w.conn, src, "app/human", engineer("dev"), func(p *Patch) {
		p.ReadLog = []ReadLogEntry{{SourceKind: "resource", SourceRef: "app/x/Deal", Scope: Scope{Kind: 0}, SeededBy: "engineer:dev"}}
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if v.Outcome != OutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted; diags=%+v", v.Outcome, v.Diagnostics)
	}
	if len(v.Seeders) != 0 {
		t.Fatalf("human/CLI submission must carry an empty seeder set, got %+v", v.Seeders)
	}
	if got := w.count("SELECT jsonb_array_length(seeders) FROM admission WHERE id=$1", v.AdmissionID); got != 0 {
		t.Fatalf("admission.seeders length = %d, want 0", got)
	}
}

var _ = context.Background
