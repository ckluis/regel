package lower

import (
	"strings"
	"testing"
)

// STAGE-E residue #11 (STAGE-D §13.11): a definition that imports two symbols
// which resolve to the SAME content hash must keep BOTH dependency edges. Every
// std TYPE shares the opaque genesis body (internal/admission/image.go), so two
// distinct std types resolve to one hash by design. A deps map keyed by that
// hash collapses the two edges and silently drops one; keying by nominal
// (module, name) identity keeps both.
func TestDepEdgesSurviveHashCollision(t *testing.T) {
	shared := "r1_" + strings.Repeat("a", 52) // one valid address for BOTH types

	res := Module(`import type { User } from "std/identity";
import type { Org } from "std/identity";
export function f(u: User, o: Org): number { return 1; }
`, ModuleContext{ModuleName: "app/coll", Resolve: func(q string) (string, bool) {
		switch q {
		case "std/identity.User", "std/identity.Org":
			return shared, true // collide, as std types do
		}
		return "", false
	}})
	if !res.OK() {
		t.Fatalf("lowering rejected: %v", res.Diagnostics)
	}

	var f *Definition
	for i := range res.Definitions {
		if res.Definitions[i].Name == "f" {
			f = &res.Definitions[i]
		}
	}
	if f == nil {
		t.Fatal("no definition f")
	}

	names := map[string]bool{}
	for _, d := range f.Deps {
		names[d.Module+"."+d.Name] = true
	}
	// Both distinct import edges must survive despite the shared hash. On the
	// pre-fix (hash-keyed) map, one edge is dropped and len(f.Deps) == 1.
	if !names["std/identity.User"] || !names["std/identity.Org"] {
		t.Fatalf("dependency edge dropped on hash collision: deps=%v (want both User and Org)", names)
	}
	if len(f.Deps) != 2 {
		t.Fatalf("len(Deps) = %d, want 2 (both edges); deps=%v", len(f.Deps), names)
	}

	// The regenerated canonical text must re-import both names (the printer
	// regenerates imports from the dep edges).
	text := CanonicalText(*f)
	if !strings.Contains(text, "User") || !strings.Contains(text, "Org") {
		t.Fatalf("canonical text lost an import name:\n%s", text)
	}
}
