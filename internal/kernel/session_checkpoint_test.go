package kernel

// session_checkpoint_test.go is the ADR-11 §5 checkpoint-write budget gate,
// end-to-end (item 3a): a 20-field-form blur storm asserting writes-per-interaction
// ≤ 1 (the claim→resume→diff→checkpoint loop writes the session row exactly once per
// interaction) and CFR delta ≤ 64 KB per interaction (p95). Recorded as perf_budget
// rows (M4); a regression is red. Item 3b (riding the 50k storm) is asserted in
// session_storm50k_test.go (maxSeq==1 ⇒ ≤1 write/session + session.cfr_delta_bytes.storm50k).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"regel.dev/regel/internal/admission"
)

func TestCheckpointWriteBudget(t *testing.T) {
	se := newSessionEnv(t)

	// A 20-field resource (+ the org policy column).
	const nFields = 20
	var fb strings.Builder
	fb.WriteString(`org: "text"`)
	for i := 1; i <= nFields; i++ {
		fmt.Fprintf(&fb, `, f%02d: "text"`, i)
	}
	src := `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Wide = resource({
  fields: { ` + fb.String() + ` },
  policy: orgScoped,
});
`
	v := se.admit(t, src, "app/wide", nil)
	if v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Wide: %q (%+v)", v.Outcome, v.Diagnostics)
	}

	tbl := "res_" + tblSlug("app/wide/Wide")
	var id int64
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(context.Background(),
			`INSERT INTO `+quoteIdent(tbl)+` (org) VALUES ('acme') RETURNING id`, nil, &id); err != nil {
			t.Fatalf("seed Wide: %v", err)
		}
	})

	ed := se.mount(t, "app/wide/Wide/form/"+fmtID(id), "human:e", "acme")

	// One blur per field = one interaction each. Measure the durable CFR frames-blob
	// growth per interaction (the "CFR delta") and prove step_seq advances by exactly
	// one write per interaction.
	deltas := make([]float64, 0, nFields)
	prev := se.intScalar(t, `SELECT octet_length(frames) FROM continuation WHERE id=$1`, ed.sid)
	for i := 1; i <= nFields; i++ {
		sl := slotForField(t, se.srv, "app/wide/Wide", "form", fmt.Sprintf("f%02d", i))
		r := ed.postEvent("blur", sl, fmt.Sprintf("value-of-field-%02d", i))
		if applied, _ := r["applied"].(bool); !applied {
			t.Fatalf("blur %d not applied: %+v", i, r)
		}
		cur := se.intScalar(t, `SELECT octet_length(frames) FROM continuation WHERE id=$1`, ed.sid)
		d := cur - prev
		if d < 0 {
			d = -d
		}
		deltas = append(deltas, float64(d))
		prev = cur
	}

	// writes-per-interaction ≤ 1: step_seq advanced by EXACTLY one checkpoint per blur.
	seq := se.intScalar(t, `SELECT step_seq FROM continuation WHERE id=$1`, ed.sid)
	if seq != nFields {
		t.Fatalf("step_seq = %d after %d blurs, want %d (exactly 1 checkpoint write per interaction)", seq, nFields, nFields)
	}
	writesPerInteraction := float64(seq) / float64(nFields)

	sort.Float64s(deltas)
	cfrDeltaP95 := deltas[minInt(len(deltas)*95/100, len(deltas)-1)]

	t.Logf("CHECKPOINT BUDGET: %d-field blur storm  writes/interaction=%.2f (budget 1)  "+
		"cfr_delta_p95=%.0f bytes (budget 65536)  deltas=%v", nFields, writesPerInteraction, cfrDeltaP95, deltas)

	writeStormBudget(t, se, "session.writes_per_interaction", 1, writesPerInteraction)
	writeStormBudget(t, se, "session.cfr_delta_bytes_p95", 65536, cfrDeltaP95)
}
