package kernel

import (
	"fmt"
	"time"
)

// ParseAsOf parses an as-of instant leniently across the forms an operator
// actually holds in hand, so the CLI `--as-of` and the HTTP `?as_of=` doors
// agree on exactly the same grammar (STAGE-F R14 papercut 1).
//
// Accepted: RFC3339 with the `T` separator and a `Z` or `±HH:MM` offset (the
// canonical form), AND Postgres's own timestamptz text output — a space
// separator and a short `±HH` offset — which is what a user copy-pastes
// straight out of `psql` (`select now();` → `2026-07-18 00:14:45.760823-04`).
// A fractional-seconds field is accepted after the seconds in every layout
// (Go's parser folds it in even when the layout omits it). Returns a plain,
// grammar-naming error on no match rather than the raw layout-mismatch text.
func ParseAsOf(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,                // T sep, Z or ±HH:MM  (frac auto on parse)
		"2006-01-02T15:04:05Z07",    // T sep, short offset (Z / +00 / -04)
		"2006-01-02 15:04:05Z07:00", // space sep, full offset
		"2006-01-02 15:04:05Z07",    // space sep, short offset (Postgres timestamptz text)
	}
	var lastErr error
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	_ = lastErr
	return time.Time{}, fmt.Errorf("not an as-of instant %q: want RFC3339 "+
		"(2006-01-02T15:04:05Z / ...±HH:MM) or Postgres timestamptz text "+
		"(2006-01-02 15:04:05-04)", s)
}
