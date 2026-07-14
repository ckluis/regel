#!/usr/bin/env bash
# demo-mcp-session.sh — a REAL MCP/agent-plane session end to end (ADR-12), Stage-C
# gate evidence. Drives the `regel mcp` stdio door with owned JSON-RPC 2.0:
#
#   initialize → tools/list → catalog.search → catalog.get →
#   patch.submit{commit:false} (dry-run, rejected) → fix →
#   patch.submit{commit:false} (dry-run, admitted) → patch.submit{commit:true} →
#   verdict.get
#   PLUS a REFUSED abuse case (product-scope escalation, no token)
#   PLUS a fuel-budget exhaustion (budget-exhausted refusal, retrievable by id).
#
# Deterministic + re-runnable against a fresh regel_mcp_demo DB. Exits 0 only when
# every expected outcome is observed. Prints the full JSON-RPC exchange.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_mcp_demo"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
BIN="$(mktemp -t regel.XXXXXX)"
AGENT_KEY="demo-agent-key"

cleanup() { rm -f "$BIN"; }
trap cleanup EXIT
fail() { echo "DEMO FAILED: $*" >&2; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
assert_contains() { echo "$1" | grep -qF "$2" || fail "expected to contain: $2 -- got: $1"; }

# mcp_session KEY  — feeds NDJSON on stdin to `regel mcp`, echoes request+response.
mcp_session() {
  local key="$1"
  "$BIN" mcp --key "$key"
}

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0a: migrate-db + genesis"
"$BIN" migrate-db >/dev/null || fail "migrate-db"
"$BIN" genesis >/dev/null || fail "genesis"
echo "substrate + genesis OK"

step "0b: seed a product-scope definition + bind the agent's API key"
"$BIN" admit examples/greet_v1.ts --name-prefix app/util --actor engineer:dev >/dev/null || fail "seed greet"
"$BIN" agent-key --key "$AGENT_KEY" --actor a1 --scope-id org1 || fail "agent-key"

step "1: the authoring loop — initialize → search → get → dry-run(bad) → fix → dry-run(ok) → commit"
REQS_A=$(cat <<'JSON'
{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"catalog.search","arguments":{"query":"greet"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"catalog.get","arguments":{"qname":"app/util/greet@product"}}}
{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"patch.submit","arguments":{"source":"export const broken = ;\n","module":"app/agent/feature","scope":"org.org1","commit":false}}}
{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"patch.submit","arguments":{"source":"export const feature: number = 42;\n","module":"app/agent/feature","scope":"org.org1","commit":false}}}
{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"patch.submit","arguments":{"source":"export const feature: number = 42;\n","module":"app/agent/feature","scope":"org.org1","commit":true}}}
JSON
)
echo "--- requests ---"; echo "$REQS_A"
OUT_A="$(echo "$REQS_A" | mcp_session "$AGENT_KEY")" || fail "session A"
echo "--- responses ---"; echo "$OUT_A"
assert_contains "$OUT_A" 'app/util/greet@product'
assert_contains "$OUT_A" '\"outcome\":\"rejected\"'   # the bad dry-run
assert_contains "$OUT_A" '\"outcome\":\"admitted\"'   # the fixed dry-run + commit
assert_contains "$OUT_A" '\"dry_run\":true'

# Extract the committed admission_id (response id 7) for verdict.get.
PATCH_ID="$(echo "$OUT_A" | python3 -c '
import sys, json
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    o=json.loads(line)
    if o.get("id")==7:
        inner=json.loads(o["result"]["content"][0]["text"])
        print(inner.get("admission_id",""))
')"
[ -n "$PATCH_ID" ] || fail "could not extract committed admission_id"
echo "committed admission_id: $PATCH_ID"

step "2: verdict.get {patch_id} — the committed Verdict is retrievable by id"
REQS_V=$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"verdict.get","arguments":{"id":"%s"}}}\n' "$PATCH_ID")
echo "--- request ---"; echo "$REQS_V"
OUT_V="$(echo "$REQS_V" | mcp_session "$AGENT_KEY")" || fail "verdict.get"
echo "--- response ---"; echo "$OUT_V"
assert_contains "$OUT_V" '\"outcome\":\"admitted\"'

step "3: REFUSED abuse case — product-scope escalation with no approval token"
REQS_ESC='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"patch.submit","arguments":{"source":"export const sneak: number = 1;\n","module":"app/agent/sneak","scope":"product","commit":true}}}'
echo "--- request ---"; echo "$REQS_ESC"
OUT_ESC="$(echo "$REQS_ESC" | mcp_session "$AGENT_KEY")" || fail "escalation session"
echo "--- response ---"; echo "$OUT_ESC"
assert_contains "$OUT_ESC" '\"outcome\":\"rejected\"'
assert_contains "$OUT_ESC" 'CAP_UNGRANTED'
echo "product escalation refused + audited (CAP_UNGRANTED)"

step "4: fuel-budget exhaustion — flooding is priced, refusal retrievable by id"
# Shrink the agent's admission-fuel bucket (no refill) so a short flood exhausts it.
psql "$REGEL_PG_DSN" -c \
  "INSERT INTO admission_fuel (principal, capacity, tokens, refill_per_sec) VALUES ('agent:a1', 8, 4, 0)
   ON CONFLICT (principal) DO UPDATE SET tokens=4, refill_per_sec=0;" >/dev/null || fail "shrink bucket"
REQS_FLOOD=$(cat <<'JSON'
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"patch.submit","arguments":{"source":"export const j: number = 1;\n","module":"app/agent/f1","scope":"org.org1","commit":true}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"patch.submit","arguments":{"source":"export const j: number = 2;\n","module":"app/agent/f2","scope":"org.org1","commit":true}}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"patch.submit","arguments":{"source":"export const j: number = 3;\n","module":"app/agent/f3","scope":"org.org1","commit":true}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"patch.submit","arguments":{"source":"export const j: number = 4;\n","module":"app/agent/f4","scope":"org.org1","commit":true}}}
JSON
)
echo "--- requests ---"; echo "$REQS_FLOOD"
OUT_FLOOD="$(echo "$REQS_FLOOD" | mcp_session "$AGENT_KEY")" || fail "flood session"
echo "--- responses ---"; echo "$OUT_FLOOD"
assert_contains "$OUT_FLOOD" '\"outcome\":\"budget-exhausted\"'

# The pre-BEGIN budget refusal is retrievable by its durable refusal_id.
REFUSAL_ID="$(echo "$OUT_FLOOD" | python3 -c '
import sys, json
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    o=json.loads(line)
    inner=json.loads(o["result"]["content"][0]["text"])
    if inner.get("outcome")=="budget-exhausted":
        print(inner.get("refusal_id","")); break
')"
[ -n "$REFUSAL_ID" ] || fail "no budget refusal_id"
echo "budget refusal_id: $REFUSAL_ID"
REQS_RF=$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"verdict.get","arguments":{"id":"%s"}}}\n' "$REFUSAL_ID")
OUT_RF="$(echo "$REQS_RF" | mcp_session "$AGENT_KEY")" || fail "verdict.get refusal"
echo "--- verdict.get {refusal_id} ---"; echo "$OUT_RF"
assert_contains "$OUT_RF" '\"outcome\":\"budget-exhausted\"'

echo
echo "=============================================================="
echo "DEMO OK — real MCP session: authoring loop + verdict.get + REFUSED escalation + fuel exhaustion"
echo "=============================================================="
