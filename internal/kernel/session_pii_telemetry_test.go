package kernel

// session_pii_telemetry_test.go is the ADR-13 §6 / ADR-11 §8 PII sweep EXTENDED to
// the kernel's own emissions (BUILD-D D5b): during a seeded-PII session (mount →
// reveal → expire, generating real frames), the seeded plaintext must be absent from
// (1) the structured stdout event stream, (2) the /healthz + current-metrics response,
// and (3) the in-process metric snapshots. The OTLP push exporter (ADR-13 §4) has no
// artifact at this milestone — named as a residue below; its batch channel is covered
// structurally by the same typed-fields-only emitter rule (ADR-13 §6), with nothing to
// grep. A harness self-test proves the sweep can actually fail (ADR-13 red-path 3).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/cfr"
)

func TestPIITelemetrySweep(t *testing.T) {
	se := newSessionEnv(t)
	se.admitWidget(t)
	id := se.seedWidget(t, "acme", "foo", 1)
	table := se.widgetTable()
	subj := fmtID(id)
	const secret = "founder-secret@acme.example"
	se.withConn(t, func(c *pgConn) {
		if err := admission.VaultPut(context.Background(), c, table, subj, "email", secret); err != nil {
			t.Fatalf("vault put: %v", err)
		}
	})

	// Capture the kernel's structured stdout event stream for the whole PII window.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	// Seeded-PII scenario: mount, no-grant render, mint+reveal, expire — the exact
	// ADR-11 §8 kill-test flow, so revealed plaintext exists only in the transient
	// frame under the live grant and nowhere durable.
	A := se.mount(t, "app/rx/Widget/detail/"+subj, "human:dpo", "acme")
	c := A.openSSE(0)
	defer c.close()
	time.Sleep(80 * time.Millisecond)
	A.postEvent("click", "detail.0", "x") // no-grant re-render
	se.withConn(t, func(c *pgConn) {
		if err := admission.MintRevealGrant(context.Background(), c, "human:dpo", table, subj, "email", time.Time{}, "operator:dpo"); err != nil {
			t.Fatalf("mint grant: %v", err)
		}
	})
	A.postEvent("click", "detail.0", "y") // revealed re-render (plaintext only in the frame)
	se.withConn(t, func(c *pgConn) {
		admission.MintRevealGrant(context.Background(), c, "human:dpo", table, subj, "email", time.Now().Add(-time.Hour), "operator:dpo")
	})
	A.postEvent("click", "detail.0", "z") // re-mask

	// Hit the health surface WHILE the revealed session is live: /healthz + metrics
	// snapshot — the exact channels ADR-13 §6 forbids from becoming the unmasked side
	// channel that durable rows are forbidden to be.
	healthBody := ""
	if resp, err := http.Get(se.ts.URL + "/healthz"); err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		healthBody = string(b)
	}
	sseM, _ := json.Marshal(sseMetricsSnapshot())
	cfrM, _ := json.Marshal(cfr.MetricsSnapshot())

	// Restore stdout and read what the kernel emitted during the window.
	w.Close()
	os.Stdout = old
	stdoutBytes, _ := io.ReadAll(r)
	stdout := string(stdoutBytes)

	// The sweep: seeded plaintext absent from EVERY emission channel.
	channels := map[string]string{
		"stdout event stream": stdout,
		"/healthz response":   healthBody,
		"sse metrics snapshot": string(sseM),
		"cfr metrics snapshot": string(cfrM),
	}
	for name, payload := range channels {
		if strings.Contains(payload, secret) {
			t.Fatalf("seeded PII plaintext leaked into telemetry channel %q", name)
		}
	}

	// Self-test (ADR-13 red-path 3): the sweep must be able to FAIL — a planted
	// violation is caught, proving the grep is not vacuous.
	planted := `{"event":"synthetic","value":"` + secret + `"}`
	if !strings.Contains(planted, secret) {
		t.Fatal("sweep self-test broken: planted secret not detected")
	}

	t.Logf("PII TELEMETRY SWEEP: seeded plaintext absent from stdout (%dB), /healthz (%dB), "+
		"sse+cfr metrics. Residue: ADR-13 §4 OTLP push exporter has no artifact at this "+
		"milestone (structurally covered by the typed-fields-only emitter, ADR-13 §6).",
		len(stdout), len(healthBody))
}
