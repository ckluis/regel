package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
)

// r3_asof_data_test.go is the STAGE-F R3 anchor: as-of ROW DATA reconstruction.
// Stage-E's ?as_of= mount rolled SCHEMA/BEHAVIOR back (the template a resource had
// at an instant) but served the CURRENT row DATA — the residue. R3 reconstructs the
// row values that were live at asOf from the non-pii trigger history (a generic
// point-in-time query over <table>_history) and renders them behind the same mount.
//
// This anchor pins three things a real PG proves: (1) a detail mount at a pre-change
// boundary reconstructs the OLD values while a live mount shows the new ones;
// (2) a row DELETEd after the boundary still reconstructs as-of, and is absent live;
// (3) as-of over a pii-bearing resource cannot resurrect a pii value — the pii column
// is structurally absent from history, so the as-of render is masked exactly as live
// (the end-to-end vault-put/shred-across-as-of proof is scenario-d2-asof-data.sh).

func r3LeadRes() string {
	return `import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Lead = resource({
  fields: { org: "text", name: "text", score: "number", email: "pii:email" },
  policy: orgScoped,
});
`
}

func TestR3AsOfRowDataReconstruction(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()

	if v := se.admit(t, r3LeadRes(), "app/r3", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Lead: %q %+v", v.Outcome, v.Diagnostics)
	}
	tbl := quoteIdent("res_" + tblSlug("app/r3/Lead"))

	// Seed two rows before the boundary: Ada (score 111) and Bee (score 222).
	var adaID, beeID int64
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(ctx,
			`INSERT INTO `+tbl+` (org,name,score) VALUES ('acme','Ada',111) RETURNING id`, nil, &adaID); err != nil {
			t.Fatalf("seed Ada: %v", err)
		}
		if _, err := c.QueryRow(ctx,
			`INSERT INTO `+tbl+` (org,name,score) VALUES ('acme','Bee',222) RETURNING id`, nil, &beeID); err != nil {
			t.Fatalf("seed Bee: %v", err)
		}
	})

	// Boundary T0 — the pre-change instant.
	var t0 time.Time
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(ctx, `SELECT now()`, nil, &t0); err != nil {
			t.Fatalf("read boundary: %v", err)
		}
	})
	time.Sleep(5 * time.Millisecond)

	// Post-T0 DATA changes: UPDATE Ada (name+score), DELETE Bee. Both write history.
	se.withConn(t, func(c *pgConn) {
		if _, err := c.Exec(ctx, `UPDATE `+tbl+` SET name='Zeta', score=999 WHERE id=$1`, adaID); err != nil {
			t.Fatalf("update Ada: %v", err)
		}
		if _, err := c.Exec(ctx, `DELETE FROM `+tbl+` WHERE id=$1`, beeID); err != nil {
			t.Fatalf("delete Bee: %v", err)
		}
	})

	detail := func(id int64, asOf *time.Time) string {
		t.Helper()
		res, err := se.srv.mountSession(ctx, "app/r3/Lead/detail/"+fmtID(id), "human:e", "acme", "", asOf)
		if err != nil {
			t.Fatalf("mount detail %d (asOf=%v): %v", id, asOf, err)
		}
		return res.HTML
	}

	// (1) Ada: live shows the NEW values, as-of T0 reconstructs the OLD ones.
	live := detail(adaID, nil)
	if !strings.Contains(live, "Zeta") || !strings.Contains(live, "999") {
		t.Fatalf("live detail must show current data (Zeta/999); HTML=%s", live)
	}
	past := detail(adaID, &t0)
	if strings.Contains(past, "Zeta") || strings.Contains(past, "999") {
		t.Fatalf("as-of T0 detail LEAKED current data (Zeta/999) — reconstruction failed; HTML=%s", past)
	}
	if !strings.Contains(past, "Ada") || !strings.Contains(past, "111") {
		t.Fatalf("as-of T0 detail must reconstruct historical data (Ada/111); HTML=%s", past)
	}

	// (2) Bee: deleted after T0. Live mount is absent (empty skeleton, no name); as-of
	// T0 reconstructs the row from its DELETE history (the last-live value).
	liveBee := detail(beeID, nil)
	if strings.Contains(liveBee, "Bee") || strings.Contains(liveBee, "222") {
		t.Fatalf("live detail of a deleted row must not show its data; HTML=%s", liveBee)
	}
	pastBee := detail(beeID, &t0)
	if !strings.Contains(pastBee, "Bee") || !strings.Contains(pastBee, "222") {
		t.Fatalf("as-of T0 detail must reconstruct a row deleted after T0 (Bee/222); HTML=%s", pastBee)
	}

	// (3) A table (list) mount as-of T0 reconstructs the pre-change roster: Ada (not
	// Zeta) AND Bee (still present at T0); a live table shows Zeta and omits Bee.
	table := func(asOf *time.Time) string {
		t.Helper()
		res, err := se.srv.mountSession(ctx, "app/r3/Lead/table", "human:e", "acme", "", asOf)
		if err != nil {
			t.Fatalf("mount table (asOf=%v): %v", asOf, err)
		}
		return res.HTML
	}
	lt := table(nil)
	if !strings.Contains(lt, "Zeta") || strings.Contains(lt, "Bee") {
		t.Fatalf("live table must show Zeta and omit deleted Bee; HTML=%s", lt)
	}
	pt := table(&t0)
	if strings.Contains(pt, "Zeta") {
		t.Fatalf("as-of table LEAKED current data (Zeta); HTML=%s", pt)
	}
	if !strings.Contains(pt, "Ada") || !strings.Contains(pt, "Bee") {
		t.Fatalf("as-of table must reconstruct the pre-change roster (Ada + Bee); HTML=%s", pt)
	}
}

// TestR3AsOfPIIStaysMasked pins the PII invariant across as-of: the pii `email`
// column is structurally absent from history, so an as-of mount has nothing to read
// and renders the mask — a value sealed and then changed can never surface as
// plaintext through a pre-change as-of. (Real vault-put + shred is the scenario.)
func TestR3AsOfPIIStaysMasked(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()
	if v := se.admit(t, r3LeadRes(), "app/r3p", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit Lead: %q %+v", v.Outcome, v.Diagnostics)
	}
	tbl := quoteIdent("res_" + tblSlug("app/r3p/Lead"))

	// The pii column must not exist on the history table at all (history-excludes-PII).
	var histHasEmail bool
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM information_schema.columns
			   WHERE table_name=$1 AND column_name='email')`,
			[]any{"res_" + tblSlug("app/r3p/Lead") + "_history"}, &histHasEmail); err != nil {
			t.Fatalf("check hist column: %v", err)
		}
	})
	if histHasEmail {
		t.Fatalf("pii column 'email' must NOT exist on the history table (as-of could resurrect it)")
	}

	var id int64
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(ctx,
			`INSERT INTO `+tbl+` (org,name,score) VALUES ('acme','Ada',1) RETURNING id`, nil, &id); err != nil {
			t.Fatalf("seed: %v", err)
		}
	})
	var t0 time.Time
	se.withConn(t, func(c *pgConn) {
		_, _ = c.QueryRow(ctx, `SELECT now()`, nil, &t0)
	})
	time.Sleep(5 * time.Millisecond)
	se.withConn(t, func(c *pgConn) {
		if _, err := c.Exec(ctx, `UPDATE `+tbl+` SET score=2 WHERE id=$1`, id); err != nil {
			t.Fatalf("update: %v", err)
		}
	})

	res, err := se.srv.mountSession(ctx, "app/r3p/Lead/detail/"+fmtID(id), "human:e", "acme", "", &t0)
	if err != nil {
		t.Fatalf("mount as-of: %v", err)
	}
	// The mask glyph is present (the email slot renders masked), and no plaintext-ish
	// value could ever appear — there was never a base/history column to reconstruct.
	if !strings.Contains(res.HTML, "••••") {
		t.Fatalf("as-of pii render must carry the mask glyph; HTML=%s", res.HTML)
	}
}
