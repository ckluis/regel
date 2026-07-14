package mcp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/pgwire"
)

// redpath_test.go is the ADR-12 structural red-path suite (constraint #9): every
// abuse mode names its control and its test, driven through the REAL MCP door.

const seededPII = "topsecret@vault.example"

// --- PII exfiltration sweep (§4 kill-test) ------------------------------------

func TestPIIExfilSweep(t *testing.T) {
	w := setupMCP(t)
	w.seedProductFn()
	w.seedContactResource()

	// Seed PII plaintext through resource.mutate (lands in the derived table).
	ins := w.tool(agentKey, "resource.mutate", map[string]any{"resource": "app/crm/Contact",
		"op": "insert", "values": map[string]any{"name": "Alice", "email": seededPII}})
	if ins["ok"] != true {
		t.Fatalf("mutate insert: %+v", ins)
	}

	// Drive every tool + resource + error path; collect every response byte.
	var all strings.Builder
	record := func(resp rpcResponse) { all.WriteString(rawResp(t, resp)) }

	greet := w.tool(agentKey, "catalog.get", map[string]any{"qname": "app/util/greet@product"})
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "catalog.search", "arguments": map[string]any{"query": "Contact"}}))
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "catalog.get", "arguments": map[string]any{"qname": "app/crm/Contact@org." + agentOrg}}))
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "catalog.deps", "arguments": map[string]any{"hash": greet["hash"]}}))
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "resource.query", "arguments": map[string]any{"resource": "app/crm/Contact"}}))
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "resource.query", "arguments": map[string]any{"resource": "app/crm/Contact", "filter": map[string]any{"email": seededPII}}}))
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "audit.query", "arguments": map[string]any{"subject": "agent:a1"}}))
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "workflow.inspect", "arguments": map[string]any{"continuation_id": "x"}}))
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "condition.list", "arguments": map[string]any{}}))
	// error paths
	record(w.rpc(agentKey, "tools/call", map[string]any{"name": "resource.query", "arguments": map[string]any{"resource": "app/crm/DoesNotExist"}}))
	record(w.rpc(agentKey, "resources/read", map[string]any{"uri": "catalog://resource/app/crm/Contact/schema"}))
	record(w.rpc(agentKey, "resources/read", map[string]any{"uri": "catalog://name/app/crm/Contact@org." + agentOrg}))

	if strings.Contains(all.String(), seededPII) {
		t.Fatalf("PII plaintext %q leaked in an MCP response", seededPII)
	}
	// The query must have masked the field (proof it served the row at all).
	q := w.tool(agentKey, "resource.query", map[string]any{"resource": "app/crm/Contact"})
	rows, _ := q["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal("resource.query returned no rows (masking not actually exercised)")
	}
	if rows[0].(map[string]any)["email"] != maskToken {
		t.Fatalf("email not masked: %+v", rows[0])
	}
}

// A reveal grant for an agent principal is a CHECK violation (§4 layer 1).
func TestRevealGrantAgentRejected(t *testing.T) {
	w := setupMCP(t)
	_, err := w.conn.Exec(context.Background(),
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('agent:a1','pii.reveal','','test')`)
	if err == nil {
		t.Fatal("reveal grant for an agent must be refused by the CHECK")
	}
	if !pgwire.IsCode(err, pgwire.CodeCheckViolation) {
		t.Fatalf("want CHECK violation, got %v", err)
	}
	// A human may hold it.
	if _, err := w.conn.Exec(context.Background(),
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('operator:op1','pii.reveal','','test')`); err != nil {
		t.Fatalf("human reveal grant must be allowed: %v", err)
	}
}

// --- spam flood: budget exhaustion, no admission rows, refill restores --------

func TestSpamFloodBudget(t *testing.T) {
	w := setupMCP(t)
	// Force the agent's admission-fuel bucket small with no refill.
	if _, err := w.conn.Exec(context.Background(),
		`INSERT INTO admission_fuel (principal, capacity, tokens, refill_per_sec) VALUES ('agent:a1', 10, 3, 0)`); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	admsBefore := w.count(`SELECT count(*) FROM admission`)

	var lastRefusal string
	sawBudget := false
	for i := 0; i < 20; i++ {
		v := w.tool(agentKey, "patch.submit", map[string]any{
			"source": "export const g x = ;\n", "module": "app/agent/spam", "scope": "org." + agentOrg, "commit": true})
		if v["outcome"] == "budget-exhausted" {
			sawBudget = true
			lastRefusal, _ = v["refusal_id"].(string)
			break
		}
	}
	if !sawBudget {
		t.Fatal("flood never hit budget-exhausted")
	}
	// No admission rows were opened by the flood.
	if got := w.count(`SELECT count(*) FROM admission`); got != admsBefore {
		t.Fatalf("flood opened admission rows: %d -> %d", admsBefore, got)
	}
	// The pre-BEGIN refusal is retrievable by id through verdict.get (own principal).
	vg := w.tool(agentKey, "verdict.get", map[string]any{"id": lastRefusal})
	if vg["outcome"] != "budget-exhausted" {
		t.Fatalf("budget refusal not retrievable by id: %+v", vg)
	}
	// Refill restores service.
	if _, err := w.conn.Exec(context.Background(),
		`UPDATE admission_fuel SET tokens=10 WHERE principal='agent:a1'`); err != nil {
		t.Fatal(err)
	}
	ok := w.tool(agentKey, "patch.submit", map[string]any{
		"source": "export const good: number = 1;\n", "module": "app/agent/good", "scope": "org." + agentOrg, "commit": true})
	if ok["outcome"] != "admitted" {
		t.Fatalf("refill did not restore service: %+v", ok)
	}
}

// --- scope escalation ± token (both principals recorded) ----------------------

func TestScopeEscalationWithoutToken(t *testing.T) {
	w := setupMCP(t)
	v := w.tool(agentKey, "patch.submit", map[string]any{
		"source": "export const e: number = 1;\n", "module": "app/agent/esc", "scope": "product", "commit": true})
	if v["outcome"] != "rejected" {
		t.Fatalf("product escalation must be rejected: %+v", v)
	}
	rid, _ := v["refusal_id"].(string)
	if rid == "" {
		t.Fatal("escalation must mint a refusal_id")
	}
	// The refusal-ledger row names principal, scope, and hashes.
	var principal, scope string
	var hashes []string
	if ok, err := w.conn.QueryRow(context.Background(),
		`SELECT principal, scope_attempted, submitted_hashes FROM gate_refusal WHERE refusal_id=$1`,
		[]any{rid}, &principal, &scope, &hashes); err != nil || !ok {
		t.Fatalf("refusal row: ok=%v err=%v", ok, err)
	}
	if principal != "agent:a1" || scope != "0:" || len(hashes) == 0 {
		t.Fatalf("refusal row wrong: principal=%q scope=%q hashes=%v", principal, scope, hashes)
	}
	// The diagnostic is CAP_UNGRANTED.
	if !hasDiagCode(v, "CAP_UNGRANTED") {
		t.Fatalf("want CAP_UNGRANTED: %+v", v["diagnostics"])
	}
}

func TestScopeEscalationWithToken(t *testing.T) {
	w := setupMCP(t)
	src := "export const approved: number = 99;\n"
	// Dry-run to learn the exact content hash the token must bind to.
	dry := w.tool(agentKey, "patch.submit", map[string]any{
		"source": src, "module": "app/agent/prod", "scope": "product", "commit": false})
	// Dry-run itself is refused pre-hash? No — scope policy runs after lowering, so a
	// dry-run without a token is a rejection carrying the hashes.
	hashes := hashList(dry["hashes"])
	if len(hashes) == 0 {
		t.Fatalf("dry-run produced no hashes: %+v", dry)
	}
	// Grant the operator product.write, then mint a one-shot token bound to the hash.
	if _, err := w.conn.Exec(context.Background(),
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('operator:human','product.write','','test')`); err != nil {
		t.Fatal(err)
	}
	token, err := admission.MintApprovalToken(context.Background(), w.conn, "operator:human", "agent:a1",
		admission.Scope{Kind: 0, ID: ""}, hashes, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	v := w.tool(agentKey, "patch.submit", map[string]any{
		"source": src, "module": "app/agent/prod", "scope": "product", "commit": true, "approvalToken": token})
	if v["outcome"] != "admitted" {
		t.Fatalf("token-approved product patch must admit: %+v", v)
	}
	// The admission records BOTH principals: agent (actor) + approving human.
	if v["approved_by"] != "operator:human" {
		t.Fatalf("verdict must record the approving human: %+v", v["approved_by"])
	}
	admID := int64(v["admission_id"].(float64))
	var actor string
	var consumedBy int64
	w.conn.QueryRow(context.Background(),
		`SELECT actor_kind||':'||actor_id FROM admission WHERE id=$1`, []any{admID}, &actor)
	w.conn.QueryRow(context.Background(),
		`SELECT COALESCE(consumed_by,0) FROM approval_token WHERE token=$1`, []any{token}, &consumedBy)
	if actor != "agent:a1" || consumedBy != admID {
		t.Fatalf("both-principals binding wrong: actor=%q consumed_by=%d admID=%d", actor, consumedBy, admID)
	}
}

// --- token replay + drift -----------------------------------------------------

func TestTokenReplayAndDrift(t *testing.T) {
	w := setupMCP(t)
	if _, err := w.conn.Exec(context.Background(),
		`INSERT INTO grant_row (subject, capability, scope, granted_by) VALUES ('operator:human','product.write','','test')`); err != nil {
		t.Fatal(err)
	}
	srcA := "export const a: number = 1;\n"
	dry := w.tool(agentKey, "patch.submit", map[string]any{"source": srcA, "module": "app/agent/tok", "scope": "product", "commit": false})
	hashes := hashList(dry["hashes"])
	token, _ := admission.MintApprovalToken(context.Background(), w.conn, "operator:human", "agent:a1",
		admission.Scope{Kind: 0, ID: ""}, hashes, time.Hour)

	// First use admits + consumes.
	if v := w.tool(agentKey, "patch.submit", map[string]any{"source": srcA, "module": "app/agent/tok", "scope": "product", "commit": true, "approvalToken": token}); v["outcome"] != "admitted" {
		t.Fatalf("first use must admit: %+v", v)
	}
	// Replay: the same (consumed) token is refused.
	replay := w.tool(agentKey, "patch.submit", map[string]any{"source": srcA, "module": "app/agent/tok", "scope": "product", "commit": true, "approvalToken": token})
	if replay["outcome"] != "rejected" || !hasDiagCode(replay, "APPROVAL_INVALID") {
		t.Fatalf("replay must be refused: %+v", replay)
	}

	// Drift: a fresh token, but one byte of the patch changed after approval.
	srcB := "export const a: number = 2;\n"
	token2, _ := admission.MintApprovalToken(context.Background(), w.conn, "operator:human", "agent:a1",
		admission.Scope{Kind: 0, ID: ""}, hashes, time.Hour) // bound to A's hashes
	drift := w.tool(agentKey, "patch.submit", map[string]any{"source": srcB, "module": "app/agent/tok2", "scope": "product", "commit": true, "approvalToken": token2})
	if drift["outcome"] != "rejected" || !hasDiagCode(drift, "APPROVAL_INVALID") {
		t.Fatalf("drift must be refused: %+v", drift)
	}
}

// --- rotation: revoke mid-session ⇒ next request refused ----------------------

func TestRotation(t *testing.T) {
	w := setupMCP(t)
	w.seedProductFn()
	// A working request first (and an admission attributed to the agent).
	v := w.tool(agentKey, "patch.submit", map[string]any{"source": "export const r: number = 1;\n", "module": "app/agent/rot", "scope": "org." + agentOrg, "commit": true})
	if v["outcome"] != "admitted" {
		t.Fatalf("pre-rotation admit: %+v", v)
	}
	admsWithAgent := w.count(`SELECT count(*) FROM admission WHERE actor_kind='agent' AND actor_id='a1'`)
	if admsWithAgent == 0 {
		t.Fatal("no admission attributed to the agent")
	}
	// Revoke the key bundle.
	if _, err := w.conn.Exec(context.Background(), `UPDATE agent_key SET revoked=true WHERE key_hash=$1`, HashKey(agentKey)); err != nil {
		t.Fatal(err)
	}
	// The NEXT request is refused.
	resp := w.rpc(agentKey, "tools/call", map[string]any{"name": "catalog.search", "arguments": map[string]any{}})
	if resp.Error == nil || resp.Error.Code != codeUnauthorized {
		t.Fatalf("post-rotation request must be unauthorized, got %+v", resp)
	}
	// Past admissions remain attributed.
	if got := w.count(`SELECT count(*) FROM admission WHERE actor_kind='agent' AND actor_id='a1'`); got != admsWithAgent {
		t.Fatalf("rotation changed past attribution: %d -> %d", admsWithAgent, got)
	}
}

// --- unnameable reads are byte-identical (§3 leak discipline) -----------------

func TestUnnameableReadsByteIdentical(t *testing.T) {
	w := setupMCP(t)
	w.seedOtherOrgFn() // a REAL name at org2, invisible to the org1 agent.

	realOOS := w.rpc(agentKey, "tools/call", map[string]any{"name": "catalog.get",
		"arguments": map[string]any{"qname": "app/secret/secretf@org." + otherOrg}})
	hallucinated := w.rpc(agentKey, "tools/call", map[string]any{"name": "catalog.get",
		"arguments": map[string]any{"qname": "app/nope/ghost@org." + otherOrg}})
	if rawResp(t, realOOS) != rawResp(t, hallucinated) {
		t.Fatalf("out-of-scope real name distinguishable from a hallucinated name:\n%s\n%s",
			rawResp(t, realOOS), rawResp(t, hallucinated))
	}
	// catalog://name honors the same filter: the NOT_FOUND payload is identical (the
	// echoed request URI differs by construction — it is the caller's own input).
	realRes := resourceText(t, w.rpc(agentKey, "resources/read", map[string]any{"uri": "catalog://name/app/secret/secretf@org." + otherOrg}))
	hallRes := resourceText(t, w.rpc(agentKey, "resources/read", map[string]any{"uri": "catalog://name/app/nope/ghost@org." + otherOrg}))
	if !jsonEq(realRes, hallRes) {
		t.Fatalf("catalog://name leaks existence: %+v vs %+v", realRes, hallRes)
	}
	if realRes["error"] != "NOT_FOUND" {
		t.Fatalf("out-of-scope resource read should be NOT_FOUND: %+v", realRes)
	}
}

// --- verdict.get is caller-scoped (foreign id ⇒ identical NOT_FOUND) ----------

func TestVerdictGetCallerScoped(t *testing.T) {
	w := setupMCP(t)
	// agent a1 mints a refusal (its own).
	esc := w.tool(agentKey, "patch.submit", map[string]any{"source": "export const z: number = 1;\n", "module": "app/agent/vg", "scope": "product", "commit": true})
	rid := esc["refusal_id"].(string)
	// a1 can fetch it.
	if own := w.tool(agentKey, "verdict.get", map[string]any{"id": rid}); own["outcome"] != "rejected" {
		t.Fatalf("owner must retrieve own refusal: %+v", own)
	}
	// The operator (a DIFFERENT principal) gets NOT_FOUND — byte-identical to an
	// id that never existed.
	foreign := w.rpc(operatorKey, "tools/call", map[string]any{"name": "verdict.get", "arguments": map[string]any{"id": rid}})
	unknown := w.rpc(operatorKey, "tools/call", map[string]any{"name": "verdict.get", "arguments": map[string]any{"id": "00000000-0000-0000-0000-000000000000"}})
	if rawResp(t, foreign) != rawResp(t, unknown) {
		t.Fatalf("foreign id distinguishable from unknown id:\n%s\n%s", rawResp(t, foreign), rawResp(t, unknown))
	}
	var nf map[string]any
	json.Unmarshal([]byte(unwrapToolText(t, unknown)), &nf)
	if nf["error"] != "NOT_FOUND" {
		t.Fatalf("want NOT_FOUND, got %+v", nf)
	}
}

// --- fenced restart: CONDITION_MOVED + already-resolved idempotent reject ------

func TestConditionRestartFence(t *testing.T) {
	w := setupMCP(t)
	condOpen, _, frameHash := w.seedCondition("open")

	// Agent authority is DISABLED (ships disabled at Stage C, §7).
	dis := w.tool(agentKey, "condition.restart", map[string]any{
		"condition_id": condOpen, "restart_name": "retry", "expectedHash": frameHash})
	if dis["code"] != "RESTART_DISABLED" {
		t.Fatalf("agent restart must be disabled: %+v", dis)
	}

	// Operator with the WRONG expectedHash ⇒ CONDITION_MOVED (nothing resumed).
	moved := w.tool(operatorKey, "condition.restart", map[string]any{
		"condition_id": condOpen, "restart_name": "retry", "expectedHash": "deadbeef"})
	if moved["code"] != "CONDITION_MOVED" {
		t.Fatalf("wrong expectedHash must be CONDITION_MOVED: %+v", moved)
	}

	// An already-resolved condition ⇒ idempotent reject (no double resume).
	condResolved, _, rhash := w.seedCondition("resolved")
	again := w.tool(operatorKey, "condition.restart", map[string]any{
		"condition_id": condResolved, "restart_name": "retry", "expectedHash": rhash})
	if again["code"] != "ALREADY_RESOLVED" {
		t.Fatalf("resolved condition must idempotent-reject: %+v", again)
	}
}

// --- helpers -----------------------------------------------------------------

func hasDiagCode(v map[string]any, code string) bool {
	b, _ := json.Marshal(v["diagnostics"])
	var ds []map[string]any
	json.Unmarshal(b, &ds)
	for _, d := range ds {
		if d["code"] == code {
			return true
		}
	}
	return false
}

func hashList(h any) []string {
	b, _ := json.Marshal(h)
	var m map[string]string
	json.Unmarshal(b, &m)
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func unwrapToolText(t *testing.T, resp rpcResponse) string {
	t.Helper()
	b, _ := json.Marshal(resp.Result)
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(b, &r)
	if len(r.Content) == 0 {
		t.Fatalf("no content: %s", string(b))
	}
	return r.Content[0].Text
}

// seedCondition inserts a continuation + durable_condition (+ a restart) directly,
// returning (conditionID, continuationID, frameHash). status is 'open' or 'resolved'.
func (w *mworld) seedCondition(status string) (string, string, string) {
	w.t.Helper()
	ctx := context.Background()
	var rootHash string
	if _, err := w.conn.QueryRow(ctx, `SELECT hash FROM definition LIMIT 1`, nil, &rootHash); err != nil {
		w.t.Fatalf("root def: %v", err)
	}
	frames := []byte("frames-" + status)
	var contID string
	if ok, err := w.conn.QueryRow(ctx, `
INSERT INTO continuation (id, kind, root_def_hash, epoch, format_ver, frames, wake, status, principal)
VALUES (gen_random_uuid(),'workflow',$1,1,1,$2::bytea,'{"kind":"manual"}'::jsonb,'condition','{"subject":"operator:op1"}'::jsonb)
RETURNING id`, []any{rootHash, byteaHex(frames)}, &contID); err != nil || !ok {
		w.t.Fatalf("seed continuation: ok=%v err=%v", ok, err)
	}
	var frameHash string
	w.conn.QueryRow(ctx, `SELECT encode(sha256(frames),'hex') FROM continuation WHERE id=$1`, []any{contID}, &frameHash)

	var condID string
	if status == "resolved" {
		// A resolved condition needs a restart row (FK) + the resolved_* fields.
		if ok, err := w.conn.QueryRow(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload)
VALUES (gen_random_uuid(),$1,'cond.test','{}'::jsonb) RETURNING id`, []any{contID}, &condID); err != nil || !ok {
			w.t.Fatalf("seed condition: %v", err)
		}
		var restartID string
		w.conn.QueryRow(ctx, `INSERT INTO restart (id, condition_id, name, label) VALUES (gen_random_uuid(),$1,'retry','Retry') RETURNING id`, []any{condID}, &restartID)
		if _, err := w.conn.Exec(ctx, `
UPDATE durable_condition SET status='resolved', resolved_restart=$2, resolved_by='op', resolved_at=now() WHERE id=$1`,
			condID, restartID); err != nil {
			w.t.Fatalf("resolve condition: %v", err)
		}
	} else {
		if ok, err := w.conn.QueryRow(ctx, `
INSERT INTO durable_condition (id, continuation_id, class, payload)
VALUES (gen_random_uuid(),$1,'cond.test','{}'::jsonb) RETURNING id`, []any{contID}, &condID); err != nil || !ok {
			w.t.Fatalf("seed condition: %v", err)
		}
		w.conn.Exec(ctx, `INSERT INTO restart (id, condition_id, name, label) VALUES (gen_random_uuid(),$1,'retry','Retry')`, condID)
	}
	return condID, contID, frameHash
}

func byteaHex(b []byte) string { return `\x` + hex.EncodeToString(b) }
