package tsx

import (
	"strings"
	"testing"
)

func nestedConditional(depth int) string {
	var b strings.Builder
	b.WriteString("export type T =\n")
	for i := 0; i < depth; i++ {
		b.WriteString("0 extends 0 ? ")
	}
	b.WriteString("0")
	for i := 0; i < depth; i++ {
		b.WriteString(" : never")
	}
	b.WriteString(";\n")
	return b.String()
}

// TestTypeGraphBudgetDepth: a deep conditional nest breaches the depth ceiling
// deterministically and names a site; an ordinary annotation does not.
func TestTypeGraphBudgetDepth(t *testing.T) {
	pr, err := Parse("/bomb.ts", nestedConditional(200))
	if err != nil {
		t.Fatal(err)
	}
	b := CheckTypeGraphBudget(pr.SourceFile)
	if b == nil {
		t.Fatalf("200-deep conditional not caught")
	}
	if b.Kind != "depth" || b.Measured <= TypeGraphDepthCeiling {
		t.Fatalf("wrong breach: %+v", b)
	}
	if !strings.Contains(b.Site, "/bomb.ts:") {
		t.Fatalf("site not named: %q", b.Site)
	}

	// Determinism: identical breach on a re-parse.
	pr2, _ := Parse("/bomb.ts", nestedConditional(200))
	b2 := CheckTypeGraphBudget(pr2.SourceFile)
	if b2 == nil || *b != *b2 {
		t.Fatalf("budget not deterministic: %+v vs %+v", b, b2)
	}
}

func TestTypeGraphBudgetOrdinaryPasses(t *testing.T) {
	src := "export const f = (x: number, y: Array<{ a: string; b: number }>): number => x;\n"
	pr, err := Parse("/ok.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	if b := CheckTypeGraphBudget(pr.SourceFile); b != nil {
		t.Fatalf("ordinary code flagged: %+v", b)
	}
}
