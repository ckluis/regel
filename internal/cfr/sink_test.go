package cfr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestHTTPSinkPerformsRealRequest (STAGE-E D6b): the HTTPSink actually performs
// the outbound call for an http.get intent, and routes a non-http class to its
// fallback. RED evidence: a sink that only recorded intents (the M2 behavior)
// would leave the httptest server's hit counter at 0 — this asserts it reached 1.
func TestHTTPSinkPerformsRealRequest(t *testing.T) {
	var hits int64
	var gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		gotKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(200)
	}))
	defer ts.Close()

	fallback := NewFileSink(t.TempDir())
	sink := NewHTTPSink(fallback)
	ctx := context.Background()

	// http.get → a REAL request lands on the server exactly once.
	in := Intent{DedupKey: "c:1:0", Class: "http.get", Payload: map[string]any{"url": ts.URL}}
	if err := sink.Deliver(ctx, in); err != nil {
		t.Fatalf("http deliver: %v", err)
	}
	if atomic.LoadInt64(&hits) != 1 {
		t.Fatalf("server hits = %d, want 1 (the sink must perform the real request)", hits)
	}
	if gotKey != "c:1:0" {
		t.Fatalf("Idempotency-Key = %q, want the dedup key", gotKey)
	}

	// A non-http class delegates to the fallback file sink.
	mail := Intent{DedupKey: "c:2:0", Class: "mail.send", Payload: map[string]any{"to": "x"}}
	if err := sink.Deliver(ctx, mail); err != nil {
		t.Fatalf("fallback deliver: %v", err)
	}
	if fallback.Delivered("c:2:0") != 1 {
		t.Fatal("mail.send intent was not routed to the fallback file sink")
	}
	if atomic.LoadInt64(&hits) != 1 {
		t.Fatalf("fallback class wrongly hit the HTTP server (hits=%d)", hits)
	}
}

// TestHTTPSinkTransportErrorRetries: a transport failure returns an error so the
// dispatcher leaves the task for retry (never silently lost).
func TestHTTPSinkTransportErrorRetries(t *testing.T) {
	sink := NewHTTPSink(nil)
	in := Intent{DedupKey: "c:1:0", Class: "http.post",
		Payload: map[string]any{"url": "http://127.0.0.1:1/nope", "body": "{}"}}
	if err := sink.Deliver(context.Background(), in); err == nil {
		t.Fatal("expected a transport error for an unreachable url (so the task retries)")
	}
}
