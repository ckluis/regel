package kernel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/pgwire"
)

const (
	greetV1 = "export function greet(name: string): string {\n  return \"hello, \" + name;\n}\n"
	greetV2 = "export function greet(name: string): string {\n  return \"HELLO, \" + name + \"!\";\n}\n"
	burnSrc = "export function burn(): number {\n  let x = 0;\n  while (x < 100000) {\n    x = x + 1;\n  }\n  return x;\n}\n"
)

func baseDSN() string {
	if d := os.Getenv("REGEL_PG_TEST_DSN"); d != "" {
		return d
	}
	return "postgres://clank@localhost:5432/postgres"
}

func randName(p string) string {
	var b [6]byte
	rand.Read(b[:])
	return p + hex.EncodeToString(b[:])
}

// testServer spins a scratch DB, migrates, runs genesis, and returns a live
// httptest server plus the pool for direct admission.
func testServer(t *testing.T) (*httptest.Server, *pgwire.Pool) {
	t.Helper()
	ctx := context.Background()
	base, err := pgwire.ParseDSN(baseDSN())
	if err != nil {
		t.Skipf("no test PG: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Skipf("connect admin: %v", err)
	}
	db := randName("regel_krn_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		admin.Close()
		t.Fatalf("create db: %v", err)
	}
	admin.Close()

	cfg := base
	cfg.Database = db
	boot, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	if err := catalog.Bootstrap(ctx, boot, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := admission.Genesis(ctx, boot, admission.BuildImage()); err != nil {
		t.Fatalf("genesis: %v", err)
	}
	boot.Close()

	pool := pgwire.NewPool(cfg, 8)
	srv, err := New(ctx, pool)
	if err != nil {
		t.Fatalf("kernel.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		pool.Close()
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	})
	return ts, pool
}

// admitSrc runs an admission over the pool and returns the verdict.
func admitSrc(t *testing.T, pool *pgwire.Pool, src, prefix string, base map[string]string) admission.Verdict {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release(conn)
	patch := admission.Patch{
		Modules:     []admission.ModuleSrc{{ModuleName: prefix, Source: src}},
		TargetScope: admission.Scope{Kind: 0, ID: ""},
		BaseHashes:  base,
	}
	v, err := admission.Admit(ctx, conn, patch, admission.Principal{ActorKind: "engineer", ActorID: "dev", Via: "cli"}, admission.BuildImage())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return v
}

func post(t *testing.T, url, body string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Post(url, "application/json", stringsReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func stringsReader(s string) io.Reader { return &sr{s: s} }

type sr struct {
	s string
	i int
}

func (r *sr) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

// TestWalkingSkeletonEndToEnd drives the same eight steps as the demo script in
// process: admit → eval → as-of rollback → sandbox park → restart.
func TestWalkingSkeletonEndToEnd(t *testing.T) {
	ts, pool := testServer(t)

	// (1) admit greet v1.
	v1 := admitSrc(t, pool, greetV1, "app/demo", nil)
	if v1.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit v1: %q", v1.Outcome)
	}
	v1hash := v1.Hashes["app/demo/greet"]

	// (2) eval greet ⇒ "hello, world".
	if code, body, _ := post(t, ts.URL+"/eval/app/demo/greet", `["world"]`); code != 200 || body != "\"hello, world\"\n" {
		t.Fatalf("eval v1: %d %q", code, body)
	}

	// (3) capture T0 between versions.
	time.Sleep(1100 * time.Millisecond)
	t0 := time.Now().UTC().Format(time.RFC3339)
	time.Sleep(1100 * time.Millisecond)

	// (4) admit greet v2 with base.
	v2 := admitSrc(t, pool, greetV2, "app/demo", map[string]string{"app/demo/greet": v1hash})
	if v2.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit v2: %q (%+v)", v2.Outcome, v2.Diagnostics)
	}

	// (5) eval greet ⇒ new behavior.
	if code, body, _ := post(t, ts.URL+"/eval/app/demo/greet", `["world"]`); code != 200 || body != "\"HELLO, world!\"\n" {
		t.Fatalf("eval v2: %d %q", code, body)
	}

	// (6) eval as-of T0 ⇒ OLD behavior (rollback = as-of WHERE clause).
	if code, body, _ := post(t, ts.URL+"/eval/app/demo/greet?as_of="+t0, `["world"]`); code != 200 || body != "\"hello, world\"\n" {
		t.Fatalf("eval as-of: %d %q", code, body)
	}

	// (7) admit burn, eval sandbox ⇒ 202 fuel.exhausted + restarts.
	if v := admitSrc(t, pool, burnSrc, "app/demo", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit burn: %q", v.Outcome)
	}
	code, body, _ := post(t, ts.URL+"/eval/app/demo/burn?tier=sandbox&fuel=20000", `[]`)
	if code != 202 {
		t.Fatalf("burn park: %d %q", code, body)
	}
	var park struct {
		ContinuationID string           `json:"continuation_id"`
		Class          string           `json:"class"`
		Restarts       []map[string]any `json:"restarts"`
	}
	if err := json.Unmarshal([]byte(body), &park); err != nil {
		t.Fatal(err)
	}
	if park.Class != "fuel.exhausted" || park.ContinuationID == "" {
		t.Fatalf("park payload: %q", body)
	}
	if !hasRestart(park.Restarts, "grant-fuel") {
		t.Fatalf("park restarts missing grant-fuel: %q", body)
	}

	// (8) restart grant-fuel ⇒ completed value.
	code, body, _ = post(t, ts.URL+"/continuation/"+park.ContinuationID+"/restart",
		`{"restart":"grant-fuel","args":{"fuel":10000000}}`)
	if code != 200 || body != "100000\n" {
		t.Fatalf("restart: %d %q", code, body)
	}
}

func hasRestart(rs []map[string]any, name string) bool {
	for _, r := range rs {
		if r["name"] == name {
			return true
		}
	}
	return false
}

// TestTransitionsPerRequestBudget measures transitions/request for greet over
// ~20 requests, asserts the ADR-04 §8 ≤50k ceiling, and writes the measured p95
// as a perf_budget row (the gate report reads it).
func TestTransitionsPerRequestBudget(t *testing.T) {
	ts, pool := testServer(t)
	if v := admitSrc(t, pool, greetV1, "app/perf", nil); v.Outcome != admission.OutcomeAdmitted {
		t.Fatalf("admit: %q", v.Outcome)
	}

	const n = 20
	samples := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		resp, err := http.Post(ts.URL+"/eval/app/perf/greet", "application/json", stringsReader(`["world"]`))
		if err != nil {
			t.Fatal(err)
		}
		tr, _ := strconv.ParseInt(resp.Header.Get("X-Regel-Transitions"), 10, 64)
		resp.Body.Close()
		samples = append(samples, tr)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95 := samples[(len(samples)*95)/100]
	if p95 <= 0 {
		t.Fatalf("no transition counts recorded: %v", samples)
	}
	if p95 > 50_000 {
		t.Fatalf("transitions/request p95 = %d exceeds the 50k ceiling", p95)
	}
	t.Logf("transitions/request p95 = %d over %d requests (ceiling 50000)", p95, n)

	// Record the measured budget for the gate report.
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release(conn)
	if _, err := conn.Exec(context.Background(), `
INSERT INTO perf_budget (epoch, metric, tier, budget, measured, milestone)
VALUES (1, 'transitions_per_request_p95', 'trusted', 50000, $1, 'M0')
ON CONFLICT (epoch, metric) DO UPDATE SET measured = EXCLUDED.measured`, p95); err != nil {
		t.Fatalf("write perf_budget: %v", err)
	}
}
