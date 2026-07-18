package kernel

import (
	"testing"
	"time"
)

// TestParseAsOfGrammar locks the STAGE-F R14 papercut-1 fix: the CLI --as-of and
// HTTP ?as_of= doors share ParseAsOf, which accepts RFC3339 AND the Postgres
// timestamptz text form (space separator, short ±HH offset) a user copy-pastes
// out of psql — and rejects garbage with a grammar-naming error.
func TestParseAsOfGrammar(t *testing.T) {
	// Every form below names the SAME instant: 2026-07-18 16:00:00.123456 UTC.
	want := time.Date(2026, 7, 18, 16, 0, 0, 123456000, time.UTC)
	forms := []string{
		"2026-07-18T16:00:00.123456Z",       // RFC3339, Z, frac
		"2026-07-18T12:00:00.123456-04:00",  // RFC3339, ±HH:MM
		"2026-07-18T12:00:00.123456-04",     // T sep, short offset
		"2026-07-18 12:00:00.123456-04",     // Postgres timestamptz text (the papercut)
		"2026-07-18 16:00:00.123456+00",     // space sep, +00
		"2026-07-18 16:00:00.123456+00:00",  // space sep, full offset
	}
	for _, f := range forms {
		got, err := ParseAsOf(f)
		if err != nil {
			t.Fatalf("ParseAsOf(%q) errored: %v", f, err)
		}
		if !got.Equal(want) {
			t.Fatalf("ParseAsOf(%q) = %s, want %s", f, got.UTC().Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
		}
	}

	// Whole-second forms (no fractional) also parse — proves the frac field is
	// optional, so a coarse pin is legal (papercut 2 is correct behavior).
	if _, err := ParseAsOf("2026-07-18 12:00:00-04"); err != nil {
		t.Fatalf("whole-second PG form must parse: %v", err)
	}

	// Garbage: a grammar-naming error, not a raw layout-mismatch dump.
	_, err := ParseAsOf("not-a-time")
	if err == nil {
		t.Fatal("ParseAsOf(garbage) must error")
	}
	if msg := err.Error(); !contains(msg, "RFC3339") || !contains(msg, "Postgres") {
		t.Fatalf("error must name the accepted grammar, got: %v", msg)
	}
}
