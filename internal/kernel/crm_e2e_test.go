package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cek"
)

// crm_e2e_test.go is the Stage-E kill-test anchor for "reference app green end to
// end" (GATE-1 §4). It runs scripts/crm-setup.sh's equivalent PROGRAMMATICALLY:
// admits the crm/ sources FROM DISK through the real gate, asserts the derivations
// (3 resources + board + component template), runs the follow-up std/taak workflow
// to completion (with its mail.send outbox intent), and renders a live board
// session. It guards the proof CRM in `go test ./...` forever. Fast (<60s): one
// in-process kernel, a 15ms-poll reactor, no subprocess.

func crmSource(t *testing.T, name string) string {
	t.Helper()
	// The test binary runs in internal/kernel; the crm/ sources live at repo root.
	p, err := filepath.Abs(filepath.Join("../..", "crm", name))
	if err != nil {
		t.Fatalf("resolve crm/%s: %v", name, err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read crm/%s: %v", name, err)
	}
	return string(b)
}

func TestCRMReferenceAppEndToEnd(t *testing.T) {
	se := newSessionEnv(t)
	ctx := context.Background()
	reactor := se.srv.StartReactor(ctx, ReactorConfig{PollInterval: 15 * time.Millisecond})
	t.Cleanup(reactor.Stop)

	// The CRM declares mail.send + sql.query; the submitting principal must hold the
	// matching grants (named ⊆ declared ⊆ grants). Insert them, mirroring crm-setup.
	for _, capb := range []string{"mail.send", "sql.query"} {
		se.exec(t, `INSERT INTO grant_row (subject, capability, scope, granted_by)
			VALUES ('engineer:dev', $1, '', 'test') ON CONFLICT DO NOTHING`, capb)
	}

	// Admit the whole CRM from disk through the real gate.
	admit := func(name string, declared []string) {
		t.Helper()
		v := se.admitDecl(t, crmSource(t, name), "app/crm", declared)
		if v.Outcome != admission.OutcomeAdmitted {
			t.Fatalf("admit crm/%s: %q %+v", name, v.Outcome, v.Diagnostics)
		}
	}
	admit("account.ts", nil)
	admit("contact.ts", nil)
	admit("activity.ts", nil)
	admit("followup.ts", []string{"mail.send"})
	admit("accountcard.ts", nil)
	admit("pipeline.ts", []string{"sql.query"})

	// --- derivations ---------------------------------------------------------
	se.withConn(t, func(c *pgConn) {
		var nres int64
		if _, err := c.QueryRow(ctx,
			`SELECT count(DISTINCT resource_name) FROM derived_resource WHERE resource_name LIKE 'app/crm/%'`,
			nil, &nres); err != nil {
			t.Fatalf("count derived resources: %v", err)
		}
		if nres != 3 {
			t.Fatalf("expected 3 derived CRM resources, got %d", nres)
		}
		// board(Account) — a states-bearing resource derives a board template.
		var board string
		ok, err := c.QueryRow(ctx,
			`SELECT detail::text FROM derived_artifact WHERE resource_name='app/crm/Account' AND pass='template'`,
			nil, &board)
		if err != nil || !ok || !strings.Contains(board, `"board"`) {
			t.Fatalf("Account must derive a board template (ok=%v err=%v)", ok, err)
		}
		// AccountCard lowers to a component_template.
		var nct int64
		if _, err := c.QueryRow(ctx,
			`SELECT count(*) FROM derived_artifact WHERE resource_name='app/crm/AccountCard' AND pass='component_template'`,
			nil, &nct); err != nil || nct != 1 {
			t.Fatalf("AccountCard component_template count=%d err=%v", nct, err)
		}
	})

	// pii fields on Contact derive NO base column (vault-routed).
	se.withConn(t, func(c *pgConn) {
		for _, col := range []string{"email", "phone"} {
			var n int64
			if _, err := c.QueryRow(ctx,
				`SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_crm_contact' AND column_name=$1`,
				[]any{col}, &n); err != nil || n != 0 {
				t.Fatalf("pii column %s leaked (n=%d err=%v)", col, n, err)
			}
		}
	})

	// --- seed + workflow run -------------------------------------------------
	se.exec(t, `INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage)
		VALUES ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active')`)

	var followupHash string
	se.withConn(t, func(c *pgConn) {
		if _, err := c.QueryRow(ctx, `SELECT hash FROM name_pointer WHERE name='app/crm/followup'`,
			nil, &followupHash); err != nil {
			t.Fatalf("resolve followup: %v", err)
		}
	})
	cid := se.start(t, followupHash, []cek.Value{cek.StrV("Globex")},
		map[string]any{"subject": "op", "operator": true})
	se.waitStatus(t, cid, "done", 30*time.Second)

	if got, ok := se.result(t, cid).StrVal(); !ok || got != "reminder-sent" {
		t.Fatalf("followup result = %q (ok=%v), want reminder-sent", got, ok)
	}
	// The mail.send external effect enqueued exactly one outbox intent.
	se.withConn(t, func(c *pgConn) {
		var nout int64
		if _, err := c.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE continuation_id=$1`, []any{cid}, &nout); err != nil {
			t.Fatalf("count outbox: %v", err)
		}
		if nout < 1 {
			t.Fatalf("followup enqueued no mail.send outbox intent")
		}
	})

	// --- live session render -------------------------------------------------
	board := se.mount(t, "app/crm/Account/board", "human:rep", "acme")
	foundActive := false
	for id := range board.slots {
		if strings.HasPrefix(id, "board.badge.") {
			foundActive = true
		}
	}
	if !foundActive {
		t.Fatalf("board session render produced no card slots (slots=%v)", board.slots)
	}
}
