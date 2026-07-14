package admission

import (
	"context"
	"math"
	"strings"
	"testing"

	"regel.dev/regel/internal/cek"
	"regel.dev/regel/internal/rast"
)

// conditionalTypeBomb builds a `depth`-deep nested conditional type
// (`0 extends 0 ? … : never`) — the ADR-07 §3 / Red-Path "conditional-type bomb".
// It is syntactically well under MAX_PARSE_DEPTH but its instantiated type graph
// is `depth` deep, so it is the deterministic-type-graph-ceiling's target.
func conditionalTypeBomb(depth int) string {
	var b strings.Builder
	b.WriteString("export type Bomb =\n")
	for i := 0; i < depth; i++ {
		b.WriteString("0 extends 0 ? ")
	}
	b.WriteString("0")
	for i := 0; i < depth; i++ {
		b.WriteString(" : never")
	}
	b.WriteString(";\nexport const probe: number = 1;\n")
	return b.String()
}

// liveEval proves the kernel process is live (never crashed / never stalled): it
// runs a trivial CEK evaluation and asserts it reduces to a value.
func liveEval(t *testing.T) {
	t.Helper()
	const h = "r1_liveprobe"
	src := cek.MapSource{h: &rast.Node{Kind: rast.KNum, U: math.Float64bits(42)}}
	in := cek.New(src, cek.NewRegistry())
	out := in.Run(context.Background(), cek.RunReq{DefHash: h, Tier: cek.TierTrusted})
	if out.Kind != cek.OutDone {
		t.Fatalf("kernel not live: eval outcome %v (err %v)", out.Kind, out.Err)
	}
}

// TestTypecheckBudgetConditionalBomb is the ADR-07 §3 Red-Path: a 200-deep
// conditional-type bomb is refused with a DETERMINISTIC TYPECHECK_BUDGET
// diagnostic naming the offending site; the same submission yields the identical
// refusal twice; the kernel stays live throughout; and the refusal is retrievable
// by its durable id from gate_refusal.
func TestTypecheckBudgetConditionalBomb(t *testing.T) {
	w := setupWorld(t)
	ctx := ctxT(t)
	bomb := conditionalTypeBomb(200)

	liveEval(t) // live before the attack

	v1, err := admit(ctx, w.newConn(), bomb, "app/bomb", engineer("e1"), nil)
	if err != nil {
		t.Fatalf("admit bomb: %v", err)
	}
	liveEval(t) // live during/after the attack (no crash, no stall)

	if v1.Outcome != OutcomeRejected {
		t.Fatalf("bomb outcome = %q, want rejected", v1.Outcome)
	}
	d1 := findDiag(v1, "TYPECHECK_BUDGET")
	if d1 == nil {
		t.Fatalf("no TYPECHECK_BUDGET diagnostic; got %+v", v1.Diagnostics)
	}
	if d1.Loc.Span == "" && !strings.Contains(d1.Message, ":") {
		t.Fatalf("TYPECHECK_BUDGET does not name the offending site: %+v", d1)
	}
	if v1.RefusalID == "" {
		t.Fatalf("no durable refusal_id on a non-green outcome")
	}

	// Determinism: the same submission yields the identical verdict content.
	v2, err := admit(ctx, w.newConn(), bomb, "app/bomb", engineer("e1"), nil)
	if err != nil {
		t.Fatalf("admit bomb again: %v", err)
	}
	d2 := findDiag(v2, "TYPECHECK_BUDGET")
	if d2 == nil {
		t.Fatalf("second run lost TYPECHECK_BUDGET: %+v", v2.Diagnostics)
	}
	if d1.Message != d2.Message || d1.Loc.Span != d2.Loc.Span || d1.Code != d2.Code {
		t.Fatalf("TYPECHECK_BUDGET not deterministic:\n a=%+v\n b=%+v", d1, d2)
	}

	// Refusal retrievable by its durable id from the gate ledger.
	var outcome, blob string
	found, err := w.conn.QueryRow(context.Background(),
		`SELECT outcome, verdict::text FROM gate_refusal WHERE refusal_id = $1`,
		[]any{v1.RefusalID}, &outcome, &blob)
	if err != nil {
		t.Fatalf("retrieve refusal: %v", err)
	}
	if !found {
		t.Fatalf("refusal %s not retrievable by id", v1.RefusalID)
	}
	if outcome != OutcomeRejected || !strings.Contains(blob, "TYPECHECK_BUDGET") {
		t.Fatalf("retrieved refusal wrong: outcome=%q blob=%s", outcome, blob)
	}
}

// findDiag returns the first diagnostic with the given code, or nil.
func findDiag(v Verdict, code string) *Diagnostic {
	for i := range v.Diagnostics {
		if v.Diagnostics[i].Code == code {
			return &v.Diagnostics[i]
		}
	}
	return nil
}
