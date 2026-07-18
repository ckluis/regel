package admission

import (
	"reflect"
	"sort"
	"testing"
)

// TestDeclaredForNormalizesImportPath locks the STAGE-F R14 papercut-3 fix: a
// capability declared as its import path (`std/mail.send`) is accepted as the
// stripped token (`mail.send`) the V1 verifier and grant table speak in, so
// `--declare std/mail.send` and `--declare mail.send` name the same capability.
func TestDeclaredForNormalizesImportPath(t *testing.T) {
	pt := Patch{
		DefaultDeclared: []string{"std/mail.send", "sql.query"},
		DeclaredCapabilities: map[string][]string{
			"app/x/perDef": {"std/http.get"},
		},
	}

	got := append([]string(nil), pt.declaredFor("app/x/anything")...) // falls to default
	sort.Strings(got)
	want := []string{"mail.send", "sql.query"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("declaredFor(default) = %v, want %v (std/ prefix must be stripped)", got, want)
	}

	perDef := pt.declaredFor("app/x/perDef")
	if len(perDef) != 1 || perDef[0] != "http.get" {
		t.Fatalf("declaredFor(perDef) = %v, want [http.get]", perDef)
	}

	if normalizeCapability("std/mail.send") != "mail.send" {
		t.Fatalf("normalizeCapability must strip a leading std/")
	}
	if normalizeCapability("mail.send") != "mail.send" {
		t.Fatalf("normalizeCapability must leave a bare token unchanged")
	}
}
