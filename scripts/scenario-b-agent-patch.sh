#!/usr/bin/env bash
# scenario-b-agent-patch.sh — AGENT PATCH OVER MCP. An agent principal (bound to
# an API key, scoped to its own overlay org.org1) proposes a change to the CRM
# through the real `regel mcp` JSON-RPC door:
#
#   dry-run patch.submit{commit:false, scope:product}  → the Verdict shows the
#     product-scope escalation would be refused WITHOUT a human token, and returns
#     the patch's content hash (what would happen).
#   human: `regel approve --for agent:a1 --hash <hash>`  → a one-shot product-scope
#     approval token (only a product.write holder can mint it).
#   agent: patch.submit{commit:true, approvalToken}  → ADMITTED under the agent's
#     fuel budget.
#   PROVE LIVE: the new component renders through `?component=` — new behavior live.
#
#   PLUS a REFUSED path: a real commit that violates a verifier (a def naming a
#   capability it does not declare → V1 CAP_UNGRANTED) is REFUSED with ZERO code
#   trace (no name_pointer / definition rows) yet AUDITED in gate_refusal.
#
# Standalone against a fresh regel_crm_agent DB. Exits nonzero on the first
# mismatch. Follows demo-mcp-session.sh's JSON-RPC driving patterns.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_crm_agent"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8790"
BASE="http://localhost:8790"
BIN="$(mktemp -t regel.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-agent-serve.XXXXXX)"
AGENT_KEY="crm-agent-key"

KERNEL_PID=""
cleanup() {
  [ -n "$KERNEL_PID" ] && { kill "$KERNEL_PID" 2>/dev/null; wait "$KERNEL_PID" 2>/dev/null; }
  pkill -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$SERVE_LOG"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }

# The AccountBadge component the agent proposes (product-scope CRM code).
NEWCOMP='import { card, stack, heading, badge } from "std/ui";
export function AccountBadge(props: { name: string; tier: string }) {
  return card({}, [ stack({}, [ heading({ value: props.name }) ]), badge({ value: props.tier }) ]);
}
'
# A def that NAMES a capability without declaring it — a V1 verifier violation.
BADSRC='import { send } from "std/mail";
export function notify(): void {
  send("owner@acme.example", "hi");
}
'

# mkreq SOURCE MODULE SCOPE COMMIT [TOKEN] — emit one JSON-RPC patch.submit line
# (json.dumps escapes the TS source safely, as demo-mcp-session.sh does by hand).
mkreq() {
  python3 -c '
import json,sys
src,module,scope,commit=sys.argv[1],sys.argv[2],sys.argv[3],sys.argv[4]=="true"
args={"source":src,"module":module,"scope":scope,"commit":commit}
if len(sys.argv)>5 and sys.argv[5]: args["approvalToken"]=sys.argv[5]
print(json.dumps({"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"patch.submit","arguments":args}}))
' "$@"
}
# verdict FIELD — pull a field out of a single-response MCP tool-call result.
verdict() {
  python3 -c '
import json,sys
o=json.loads(sys.stdin.read().strip())
if "error" in o: print("__RPCERR__:"+o["error"]["message"]); raise SystemExit
i=json.loads(o["result"]["content"][0]["text"])
f=sys.argv[1]
if f=="codes": print(",".join(d.get("code","") for d in i.get("diagnostics",[])))
elif f=="hash": print(i.get("hashes",{}).get(sys.argv[2],""))
else: print(i.get(f,""))
' "$@"
}

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0: substrate + genesis + admit the CRM Account (the thing being patched)"
"$BIN" migrate-db >/dev/null || fail "migrate-db"
"$BIN" genesis    >/dev/null || fail "genesis"
"$BIN" admit crm/account.ts --name-prefix app/crm --actor engineer:dev >/dev/null || fail "admit Account"
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active')" >/dev/null
"$BIN" grant operator:human product.write >/dev/null || fail "grant product.write"
"$BIN" agent-key --key "$AGENT_KEY" --actor a1 --scope-id org1 || fail "agent-key"
echo "admitted app/crm/Account (seeded Globex); agent:a1 bound @org.org1"

step "1: agent DRY-RUN a product-scope component (commit:false) — verdict + hash"
DRY="$(mkreq "$NEWCOMP" app/crm product false | "$BIN" mcp --key "$AGENT_KEY" 2>&1)"
echo "$DRY"
DRY_OUT="$(echo "$DRY" | verdict outcome)"
DRY_CODES="$(echo "$DRY" | verdict codes)"
HASH="$(echo "$DRY" | verdict hash app/crm/AccountBadge)"
[ "$DRY_OUT" = "rejected" ]        || fail "dry-run should show product escalation refused w/o a token (got $DRY_OUT)"
echo "$DRY_CODES" | grep -q CAP_UNGRANTED || fail "dry-run diagnostics missing the escalation code ($DRY_CODES)"
[ -n "$HASH" ]                     || fail "dry-run did not return the patch content hash"
echo "PASS: dry-run shows a product-scope patch needs a human token; content hash = $HASH"

step "2: human mints a one-shot product-scope approval token (regel approve)"
TOKEN="$("$BIN" approve --for agent:a1 --hash "$HASH" --scope product --minter operator:human)" || fail "approve"
[ -n "$TOKEN" ] || fail "no approval token minted"
echo "operator:human approved hash $HASH → token $TOKEN"

step "3: agent COMMITS with the token — ADMITTED under its fuel budget"
COMMIT="$(mkreq "$NEWCOMP" app/crm product true "$TOKEN" | "$BIN" mcp --key "$AGENT_KEY" 2>&1)"
echo "$COMMIT"
[ "$(echo "$COMMIT" | verdict outcome)" = "admitted" ] || fail "approved commit did not admit"
AID="$(echo "$COMMIT" | verdict admission_id)"
[ -n "$AID" ] || fail "no admission_id on the committed patch"
[ "$(sql "SELECT count(*) FROM name_pointer WHERE name='app/crm/AccountBadge'")" -ge 1 ] || fail "AccountBadge not resolvable after commit"
echo "PASS: agent patch admitted (admission_id $AID), AccountBadge now a live definition"

step "4: PROVE LIVE — the new component renders through ?component="
"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do grep -q "listening" "$SERVE_LOG" && break; sleep 0.1; done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
CARD="$(curl -s -H 'X-Regel-Actor: human:a' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/detail/1?component=app/crm/AccountBadge")"
echo "$CARD" | grep -qF 'rg-card'    || fail "AccountBadge did not render a card"
echo "$CARD" | grep -qF 'rg-badge'   || fail "AccountBadge did not render its tier badge"
echo "$CARD" | grep -qF 'Globex'     || fail "AccountBadge did not bind Account#1 name"
echo "$CARD" | grep -qF 'enterprise' || fail "AccountBadge did not bind the tier value"
echo "PASS: the agent-authored component is LIVE — renders Globex + the enterprise tier badge"

step "5: REFUSED path — a verifier violation is refused with ZERO code trace, but audited"
REFUSAL_BEFORE="$(sql "SELECT count(*) FROM gate_refusal")"
BAD="$(mkreq "$BADSRC" app/agent/notify org.org1 true | "$BIN" mcp --key "$AGENT_KEY" 2>&1)"
echo "$BAD"
[ "$(echo "$BAD" | verdict outcome)" = "rejected" ]     || fail "capability violation should be rejected"
echo "$BAD" | verdict codes | grep -q CAP_UNGRANTED      || fail "expected the V1 CAP_UNGRANTED verifier code"
# ZERO code trace: no name_pointer / definition row for the refused def.
[ "$(sql "SELECT count(*) FROM name_pointer WHERE name LIKE 'app/agent/notify%'")" = "0" ] || fail "refused def left a name_pointer trace"
# ...but the refusal IS audited (gate_refusal grew by one).
REFUSAL_AFTER="$(sql "SELECT count(*) FROM gate_refusal")"
[ "$REFUSAL_AFTER" -gt "$REFUSAL_BEFORE" ] || fail "refusal was not audited in gate_refusal (${REFUSAL_BEFORE} -> ${REFUSAL_AFTER})"
echo "PASS: verifier violation REFUSED (V1 CAP_UNGRANTED), zero code trace, audited in gate_refusal (${REFUSAL_BEFORE} -> ${REFUSAL_AFTER})"

echo
echo "=============================================================="
echo "DEMO OK — agent patch over MCP: dry-run → human approve → committed → live; verifier violation refused with zero trace + audit"
echo "=============================================================="
