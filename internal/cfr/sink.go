package cfr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sink.go — the real cfr.DeliverySink implementations (STAGE-E D6b, ADR-06 §5).
// The dispatcher (reactor) owns effectively-once via the outbox delivered_at CAS
// (ADR-05 §7); a sink MUST be idempotent under Intent.DedupKey so a crash-retry
// between the sink call and the mark does not duplicate the real-world effect.
//
//   - FileSink   — a hermetic file/dir spool: the SAFE LOCAL default wired by
//                  `regel serve`, so demos perform no real network I/O. Idempotent
//                  by construction (O_EXCL keyed on the dedup key: a redelivered
//                  intent finds its file already present and is a no-op).
//   - HTTPSink   — performs the REAL outbound request for http.get/http.post
//                  intents (net/http), delegating any other class to a Fallback.
//
// Neither adds a Go dependency (net/http + os are stdlib).

// FileSink spools each delivered intent as one JSON file under Dir, named by a
// sanitized dedup key. It is the safe local delivery the serving kernel uses by
// default: mail.send / http.* / log.write intents land as inspectable files, never
// as real network effects. Idempotent under DedupKey — a redelivery whose file
// already exists returns nil without writing a second artifact, so the real
// delivered set is exactly-once even though Deliver may be called twice.
type FileSink struct {
	Dir string

	mu       sync.Mutex
	failNext map[string]int // test-only crash injection: DedupKey → forced failures
}

// NewFileSink builds a FileSink spooling into dir (created if absent).
func NewFileSink(dir string) *FileSink {
	return &FileSink{Dir: dir, failNext: map[string]int{}}
}

// FailOnce arms the sink to return an error the next time it sees dedupKey — a
// stand-in for a crash between the sink call and the delivered_at mark (test hook,
// mirrors RecordingSink.FailOnce so the effectively-once red-path drives the real
// sink unchanged).
func (s *FileSink) FailOnce(dedupKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[dedupKey]++
}

// Deliver implements DeliverySink: it writes the intent to <Dir>/<key>.json under
// O_EXCL. An already-present file (a redelivery) is a successful no-op.
func (s *FileSink) Deliver(_ context.Context, in Intent) error {
	s.mu.Lock()
	if s.failNext[in.DedupKey] > 0 {
		s.failNext[in.DedupKey]--
		s.mu.Unlock()
		return fmt.Errorf("file sink: forced failure for %s", in.DedupKey)
	}
	s.mu.Unlock()

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.Dir, sanitizeKey(in.DedupKey)+".json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil // already delivered — idempotent no-op (effectively-once)
		}
		return err
	}
	defer f.Close()
	rec := map[string]any{
		"dedup_key":    in.DedupKey,
		"class":        in.Class,
		"payload":      in.Payload,
		"delivered_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(rec)
}

// Delivered returns how many spool files exist for dedupKey (0 or 1 by O_EXCL) —
// the real delivered-artifact count a red-path asserts.
func (s *FileSink) Delivered(dedupKey string) int {
	path := filepath.Join(s.Dir, sanitizeKey(dedupKey)+".json")
	if _, err := os.Stat(path); err == nil {
		return 1
	}
	return 0
}

// Total counts every spool file in Dir (the total delivered set).
func (s *FileSink) Total() int {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// sanitizeKey makes a dedup key ("cont:step:ordinal") a safe flat filename.
func sanitizeKey(k string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, k)
}

// HTTPSink performs the REAL outbound request for http.get / http.post intents
// (ADR-10 §3 std/http external). Any other effect class delegates to Fallback (a
// FileSink in the default wiring). Effectively-once is the honest limit: a crash
// between the request and the mark can redeliver — an HTTP effect is not naturally
// idempotent, so the dedup mark caps it at at-most-once-per-mark, redelivered on
// crash, exactly as ADR-05 §7 states.
type HTTPSink struct {
	Client   *http.Client
	Fallback DeliverySink
}

// NewHTTPSink builds an HTTPSink with a bounded-timeout client and a fallback for
// non-http classes.
func NewHTTPSink(fallback DeliverySink) *HTTPSink {
	return &HTTPSink{Client: &http.Client{Timeout: 30 * time.Second}, Fallback: fallback}
}

// Deliver implements DeliverySink.
func (s *HTTPSink) Deliver(ctx context.Context, in Intent) error {
	switch in.Class {
	case "http.get", "http.post":
		return s.doHTTP(ctx, in)
	default:
		if s.Fallback != nil {
			return s.Fallback.Deliver(ctx, in)
		}
		return nil
	}
}

func (s *HTTPSink) doHTTP(ctx context.Context, in Intent) error {
	url, _ := in.Payload["url"].(string)
	if url == "" {
		return fmt.Errorf("http sink: intent %s has no url", in.DedupKey)
	}
	method := http.MethodGet
	var body io.Reader
	if in.Class == "http.post" {
		method = http.MethodPost
		if b, ok := in.Payload["body"].(string); ok {
			body = strings.NewReader(b)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Idempotency-Key", in.DedupKey) // effectively-once hint to the peer
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err // transport failure → dispatcher retries (never lost)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 500 {
		return fmt.Errorf("http sink: %s returned %d (retryable)", url, resp.StatusCode)
	}
	return nil
}
