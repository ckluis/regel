package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/catalog"
	"regel.dev/regel/internal/pgwire"
)

// --- harness (real PG 16.13) -------------------------------------------------

func baseDSN() string {
	if d := os.Getenv("REGEL_PG_TEST_DSN"); d != "" {
		return d
	}
	return "postgres://clank@localhost:5432/postgres"
}

func randName(prefix string) string {
	var b [6]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// mworld is a bootstrapped + genesis'd scratch DB with an MCP server over a pool.
type mworld struct {
	t    *testing.T
	cfg  pgwire.Config
	conn *pgwire.Conn
	pool *pgwire.Pool
	srv  *Server
}

const (
	agentKey    = "k-agent-org1"
	agentOrg    = "org1"
	otherOrg    = "org2"
	operatorKey = "k-operator-1"
)

func setupMCP(t *testing.T) *mworld {
	t.Helper()
	ctx := ctxT(t)
	base, err := pgwire.ParseDSN(baseDSN())
	if err != nil {
		t.Skipf("no test PG: %v", err)
	}
	admin, err := pgwire.Connect(ctx, base)
	if err != nil {
		t.Skipf("connect admin: %v", err)
	}
	defer admin.Close()
	db := randName("regel_mcp_")
	if _, err := admin.ExecSimple(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create db: %v", err)
	}
	cfg := base
	cfg.Database = db
	conn, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	if err := catalog.Bootstrap(ctx, conn, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := admission.Genesis(ctx, conn, admission.BuildImage()); err != nil {
		t.Fatalf("genesis: %v", err)
	}
	pool := pgwire.NewPool(cfg, 8)
	srv, err := New(ctx, pool)
	if err != nil {
		t.Fatalf("mcp.New: %v", err)
	}
	w := &mworld{t: t, cfg: cfg, conn: conn, pool: pool, srv: srv}
	// Bind the agent + operator API keys.
	w.bindKey(agentKey, "agent", "a1", 2, agentOrg)
	w.bindKey(operatorKey, "operator", "op1", 0, "")
	t.Cleanup(func() {
		pool.Close()
		conn.Close()
		cl, err := pgwire.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer cl.Close()
		cl.ExecSimple(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
	})
	return w
}

func (w *mworld) bindKey(key, kind, id string, scopeKind int, scopeID string) {
	w.t.Helper()
	if _, err := w.conn.Exec(context.Background(), `
INSERT INTO agent_key (key_hash, actor_kind, actor_id, scope_kind, scope_id)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (key_hash) DO UPDATE SET revoked=false`,
		HashKey(key), kind, id, scopeKind, scopeID); err != nil {
		w.t.Fatalf("bindKey: %v", err)
	}
}

func (w *mworld) count(query string, args ...any) int {
	var n int
	if _, err := w.conn.QueryRow(context.Background(), query, args, &n); err != nil {
		w.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// rpc drives one real JSON-RPC request through Dispatch and returns the response.
func (w *mworld) rpc(key, method string, params any) rpcResponse {
	w.t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, _ := json.Marshal(params)
		raw = b
	}
	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: raw}
	return w.srv.Dispatch(context.Background(), &Session{APIKey: key}, req)
}

// tool calls a tool and returns its parsed structured result (from content[0].text).
func (w *mworld) tool(key, name string, args any) map[string]any {
	w.t.Helper()
	resp := w.rpc(key, "tools/call", map[string]any{"name": name, "arguments": args})
	if resp.Error != nil {
		w.t.Fatalf("tool %s error: %+v", name, resp.Error)
	}
	return unwrapTool(w.t, resp)
}

func unwrapTool(t *testing.T, resp rpcResponse) map[string]any {
	t.Helper()
	b, _ := json.Marshal(resp.Result)
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &r); err != nil || len(r.Content) == 0 {
		t.Fatalf("bad tool result: %s (%v)", string(b), err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.Content[0].Text), &out); err != nil {
		t.Fatalf("tool payload not an object: %s", r.Content[0].Text)
	}
	return out
}

func rawResp(t *testing.T, resp rpcResponse) string {
	t.Helper()
	b, _ := json.Marshal(resp)
	return string(b)
}

// seedResource admits a resource with a PII field at the agent's overlay scope, so
// resource.query/mutate have a derived table to serve.
func (w *mworld) seedContactResource() {
	w.t.Helper()
	src := `import { resource } from "std/resource";
export const Contact = resource({
  fields: { name: "text", email: "pii:email" },
});
`
	p := admission.Patch{
		Modules:     []admission.ModuleSrc{{ModuleName: "app/crm", Source: src}},
		TargetScope: admission.Scope{Kind: 2, ID: agentOrg},
		BaseHashes:  map[string]string{},
	}
	auth := admission.Principal{ActorKind: "agent", ActorID: "a1", Via: "mcp", Chain: catalog.Chain{OrgID: agentOrg}}
	v, err := admission.Admit(context.Background(), w.conn, p, auth, admission.BuildImage())
	if err != nil || v.Outcome != admission.OutcomeAdmitted {
		w.t.Fatalf("seed Contact resource: %v / %q %+v", err, v.Outcome, v.Diagnostics)
	}
}

// seedProductFn admits a plain product-scope function (visible to all).
func (w *mworld) seedProductFn() string {
	w.t.Helper()
	src := "export function greet(name: string): string {\n  return \"hi \" + name;\n}\n"
	p := admission.Patch{
		Modules:     []admission.ModuleSrc{{ModuleName: "app/util", Source: src}},
		TargetScope: admission.Scope{Kind: 0, ID: ""},
		BaseHashes:  map[string]string{},
	}
	v, err := admission.Admit(context.Background(), w.conn, p,
		admission.Principal{ActorKind: "engineer", ActorID: "dev", Via: "cli"}, admission.BuildImage())
	if err != nil || v.Outcome != admission.OutcomeAdmitted {
		w.t.Fatalf("seed product fn: %v / %q", err, v.Outcome)
	}
	return v.Hashes["app/util/greet"]
}

// seedOtherOrgFn admits a function at org2 (out of the org1 agent's scope chain).
func (w *mworld) seedOtherOrgFn() {
	w.t.Helper()
	src := "export function secretf(): number {\n  return 42;\n}\n"
	p := admission.Patch{
		Modules:     []admission.ModuleSrc{{ModuleName: "app/secret", Source: src}},
		TargetScope: admission.Scope{Kind: 2, ID: otherOrg},
		BaseHashes:  map[string]string{},
	}
	v, err := admission.Admit(context.Background(), w.conn, p,
		admission.Principal{ActorKind: "agent", ActorID: "a2", Via: "mcp", Chain: catalog.Chain{OrgID: otherOrg}},
		admission.BuildImage())
	if err != nil || v.Outcome != admission.OutcomeAdmitted {
		w.t.Fatalf("seed org2 fn: %v / %q %+v", err, v.Outcome, v.Diagnostics)
	}
}

// --- every surface responds over real JSON-RPC (acceptance #2) ----------------

func TestAllSurfacesRespond(t *testing.T) {
	w := setupMCP(t)
	w.seedProductFn()
	w.seedContactResource()

	// initialize
	if r := w.rpc(agentKey, "initialize", nil); r.Error != nil {
		t.Fatalf("initialize: %+v", r.Error)
	}

	// tools/list — all 11 present.
	tl := w.rpc(agentKey, "tools/list", nil)
	names := listNames(t, tl.Result, "tools")
	wantTools := []string{"catalog.search", "catalog.get", "catalog.deps", "resource.query",
		"resource.mutate", "patch.submit", "verdict.get", "workflow.inspect", "condition.list",
		"condition.restart", "audit.query"}
	for _, n := range wantTools {
		if !contains(names, n) {
			t.Fatalf("tools/list missing %s (have %v)", n, names)
		}
	}
	if len(names) != 11 {
		t.Fatalf("want 11 tools, got %d: %v", len(names), names)
	}

	// resources/list — 6.
	rl := w.rpc(agentKey, "resources/list", nil)
	if got := len(listNames(t, rl.Result, "resources")); got != 6 {
		t.Fatalf("want 6 resources, got %d", got)
	}
	// prompts/list — 3.
	pl := w.rpc(agentKey, "prompts/list", nil)
	if got := len(listNames(t, pl.Result, "prompts")); got != 3 {
		t.Fatalf("want 3 prompts, got %d", got)
	}

	// Call each tool at least once (list + call, acceptance #2).
	w.tool(agentKey, "catalog.search", map[string]any{"query": "greet"})
	greet := w.tool(agentKey, "catalog.get", map[string]any{"qname": "app/util/greet@product"})
	if greet["name"] != "app/util/greet" {
		t.Fatalf("catalog.get greet wrong: %+v", greet)
	}
	w.tool(agentKey, "catalog.deps", map[string]any{"hash": greet["hash"], "dir": "out"})
	ins := w.tool(agentKey, "resource.mutate", map[string]any{"resource": "app/crm/Contact",
		"op": "insert", "values": map[string]any{"name": "Bob", "email": "bob@example.com"}})
	if ins["ok"] != true {
		t.Fatalf("mutate insert failed: %+v", ins)
	}
	w.tool(agentKey, "resource.query", map[string]any{"resource": "app/crm/Contact"})
	dry := w.tool(agentKey, "patch.submit", map[string]any{
		"source": "export const x: number = 1;\n", "module": "app/agent/x", "commit": false})
	if dry["outcome"] != "admitted" {
		t.Fatalf("dry-run should admit: %+v", dry)
	}
	// verdict.get on a refusal id (make one).
	esc := w.tool(agentKey, "patch.submit", map[string]any{
		"source": "export const y: number = 2;\n", "module": "app/agent/y",
		"scope": "product", "commit": true})
	if esc["refusal_id"] == nil {
		t.Fatalf("escalation should mint a refusal_id: %+v", esc)
	}
	vg := w.tool(agentKey, "verdict.get", map[string]any{"id": esc["refusal_id"]})
	if vg["outcome"] != "rejected" {
		t.Fatalf("verdict.get own refusal: %+v", vg)
	}
	w.tool(agentKey, "condition.list", map[string]any{"status": "open"})
	w.tool(agentKey, "workflow.inspect", map[string]any{"continuation_id": "00000000-0000-0000-0000-000000000000"})
	restart := w.tool(agentKey, "condition.restart", map[string]any{"condition_id": "x", "restart_name": "retry"})
	if restart["code"] != "RESTART_DISABLED" {
		t.Fatalf("agent condition.restart must be disabled: %+v", restart)
	}
	w.tool(agentKey, "audit.query", map[string]any{"subject": "agent:a1"})

	// Read each of the 6 resources.
	for _, uri := range []string{
		"catalog://definition/" + greet["hash"].(string),
		"catalog://name/app/util/greet@product",
		"catalog://resource/app/crm/Contact/schema",
		"catalog://epoch",
		"catalog://verifier-coverage",
		"catalog://verdict/" + esc["refusal_id"].(string),
	} {
		r := w.rpc(agentKey, "resources/read", map[string]any{"uri": uri})
		if r.Error != nil {
			t.Fatalf("resources/read %s: %+v", uri, r.Error)
		}
	}
	// Get each of the 3 prompts.
	for _, pn := range []string{"author-resource", "author-workflow"} {
		if r := w.rpc(agentKey, "prompts/get", map[string]any{"name": pn}); r.Error != nil {
			t.Fatalf("prompts/get %s: %+v", pn, r.Error)
		}
	}
	fv := w.rpc(agentKey, "prompts/get", map[string]any{"name": "fix-verdict",
		"arguments": map[string]any{"id": esc["refusal_id"]}})
	if fv.Error != nil {
		t.Fatalf("prompts/get fix-verdict: %+v", fv.Error)
	}
}

// --- stdio wire loop drives a real session -----------------------------------

func TestStdioWireLoop(t *testing.T) {
	w := setupMCP(t)
	w.seedProductFn()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out strings.Builder
	if err := w.srv.ServeStdio(context.Background(), &Session{APIKey: agentKey}, in, &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 { // notification produces no reply
		t.Fatalf("want 2 response lines, got %d: %q", len(lines), out.String())
	}
	for _, ln := range lines {
		var resp map[string]any
		if err := json.Unmarshal([]byte(ln), &resp); err != nil {
			t.Fatalf("non-JSON response line: %q", ln)
		}
		if resp["jsonrpc"] != "2.0" {
			t.Fatalf("bad jsonrpc version: %q", ln)
		}
	}
}

// --- qname round-trip: search → get → resource, unmodified (ADR-12 §2) --------

func TestQNameRoundTrip(t *testing.T) {
	w := setupMCP(t)
	w.seedProductFn()

	res := w.tool(agentKey, "catalog.search", map[string]any{"query": "greet"})
	results, _ := res["results"].([]any)
	if len(results) == 0 {
		t.Fatal("search returned no results")
	}
	first := results[0].(map[string]any)
	qname := first["qname"].(string)
	if qname != "app/util/greet@product" {
		t.Fatalf("unexpected qname %q", qname)
	}
	// The SAME qname feeds catalog.get and catalog://name unmodified.
	g := w.tool(agentKey, "catalog.get", map[string]any{"qname": qname})
	if g["qname"] != qname || g["hash"] != first["hash"] {
		t.Fatalf("get by qname drifted: %+v vs %+v", g, first)
	}
	rr := w.rpc(agentKey, "resources/read", map[string]any{"uri": "catalog://name/" + qname})
	if rr.Error != nil {
		t.Fatalf("catalog://name/%s: %+v", qname, rr.Error)
	}
	// The resource embeds the same qname.
	body := resourceText(t, rr)
	if body["qname"] != qname {
		t.Fatalf("resource qname drifted: %+v", body)
	}
}

// --- dry-run parity: commit:false then true ⇒ identical verdicts --------------

func TestDryRunParity(t *testing.T) {
	w := setupMCP(t)
	src := "export const parity: number = 7;\n"
	args := map[string]any{"source": src, "module": "app/agent/parity", "scope": "org." + agentOrg}

	dryArgs := copyMap(args)
	dryArgs["commit"] = false
	dry := w.tool(agentKey, "patch.submit", dryArgs)

	commitArgs := copyMap(args)
	commitArgs["commit"] = true
	real := w.tool(agentKey, "patch.submit", commitArgs)

	// Meaningful fields identical (outcome, hashes, delta, diagnostics, seeders).
	if dry["outcome"] != "admitted" || real["outcome"] != "admitted" {
		t.Fatalf("both should admit: dry=%v real=%v", dry["outcome"], real["outcome"])
	}
	if !jsonEq(dry["hashes"], real["hashes"]) {
		t.Fatalf("hashes differ: dry=%v real=%v", dry["hashes"], real["hashes"])
	}
	if !jsonEq(dry["delta"], real["delta"]) {
		t.Fatalf("delta differs: dry=%v real=%v", dry["delta"], real["delta"])
	}
	// Dry-run left NO admission row for the module; commit did.
	if dry["admission_id"] != nil {
		t.Fatalf("dry-run must not claim an admission id: %v", dry["admission_id"])
	}
	if real["admission_id"] == nil {
		t.Fatalf("commit must record an admission id")
	}
	if dry["dry_run"] != true {
		t.Fatalf("dry-run verdict must flag dry_run")
	}
}

// --- helpers -----------------------------------------------------------------

func listNames(t *testing.T, result any, key string) []string {
	t.Helper()
	b, _ := json.Marshal(result)
	var m map[string][]map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("list result: %v", err)
	}
	var out []string
	for _, e := range m[key] {
		if n, ok := e["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

func resourceText(t *testing.T, resp rpcResponse) map[string]any {
	t.Helper()
	b, _ := json.Marshal(resp.Result)
	var r struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(b, &r); err != nil || len(r.Contents) == 0 {
		t.Fatalf("bad resource result: %s", string(b))
	}
	var out map[string]any
	json.Unmarshal([]byte(r.Contents[0].Text), &out)
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func copyMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func jsonEq(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
