#!/usr/bin/env bash
# scenario-a-field-add.sh — TENANT FIELD-ADD FROM A SETTINGS FORM: runtime schema
# evolution via erf, with NO engineer in the loop. A tenant admin adds an `owner`
# field to Account by RE-ADMITTING the resource def through the real HTTP /admit
# door under `--base` OPTIMISTIC CONCURRENCY (the Account-v1 head hash). After the
# green admission:
#   - the derived base table HAS the new `owner` column (additive DDL);
#   - existing rows are INTACT (Globex still there, owner NULL);
#   - the history shadow table is PRESERVED (the pre-change edit survives);
#   - the derived form UI now RENDERS the owner field (observed via a session mount);
#   - a pre-existing session still renders (resync succeeds across the change);
#   - the append-only template artifacts record v1 (no owner) vs v2 (owner) — the
#     schema-behavior time record scenario (d) reads through the as-of UI.
# A CONCURRENT admin editing the STALE v1 base is rejected STALE_BASE (the cas
# stage) — optimistic concurrency is real, not decorative.
#
# NAMED PAPERCUT: a true point-and-click settings FORM (capture "field name / type"
# and synthesize the def) is not wired; the tenant admin submits the resource def
# SOURCE through the HTTP admit door. The schema evolution itself is fully real and
# observed via the UI — only the form-to-def authoring surface is the residue.
#
# Standalone against a fresh regel_crm_field DB. Exits nonzero on the first mismatch.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_crm_field"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8789"
BASE="http://localhost:8789"
BIN="$(mktemp -t regel.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-field-serve.XXXXXX)"
V2SRC="$(mktemp -t regel-account-v2.XXXXXX.ts)"
V4SRC="$(mktemp -t regel-account-v4.XXXXXX.ts)"

KERNEL_PID=""
cleanup() {
  [ -n "$KERNEL_PID" ] && { kill "$KERNEL_PID" 2>/dev/null; wait "$KERNEL_PID" 2>/dev/null; }
  pkill -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$SERVE_LOG" "$V2SRC" "$V4SRC"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }

# post_admit SRCFILE BASEHASH — POST an admission.Patch to /admit with a base hash,
# piping python straight into curl (no shell echo round-trip: zsh's echo would turn
# the JSON's \n escapes into raw newlines and corrupt the wire frame). Prints the
# raw Verdict JSON.
post_admit() {
  python3 -c '
import json,sys
src=open(sys.argv[1]).read()
sys.stdout.write(json.dumps({"modules":[{"module_name":"app/crm","source":src}],
  "base_hashes":{"app/crm/Account":sys.argv[2]}}))
' "$1" "$2" | curl -s -X POST "$BASE/admit" -H 'X-Regel-Actor: tenant:admin' \
    -H 'Content-Type: application/json' --data-binary @-
}
vfield() { python3 -c 'import sys,json;o=json.load(sys.stdin);print(o.get(sys.argv[1],""))' "$1"; }

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

# Synthesize the v2 (adds owner) and v4 (adds owner + region) defs from crm/account.ts.
python3 - "$V2SRC" "$V4SRC" <<'PY'
import sys
base = open("crm/account.ts").read()
anchor = '    stage: "states:prospect|active|churned",'
open(sys.argv[1], "w").write(base.replace(anchor, anchor + '\n    owner: "text",'))
open(sys.argv[2], "w").write(base.replace(anchor, anchor + '\n    owner: "text",\n    region: "text",'))
PY

step "0: substrate + genesis + admit Account v1; seed a row + an edit (history)"
"$BIN" migrate-db >/dev/null || fail "migrate-db"
"$BIN" genesis    >/dev/null || fail "genesis"
V1HASH="$("$BIN" admit crm/account.ts --name-prefix app/crm --actor engineer:dev | vfield hashes | python3 -c 'import sys;print(eval(sys.stdin.read())["app/crm/Account"])')"
[ -n "$V1HASH" ] || fail "no v1 Account hash"
echo "Account v1 head hash: $V1HASH"
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active')" >/dev/null
sql "UPDATE res_app_crm_account SET industry='mfg' WHERE id=1" >/dev/null   # fires the history trigger
HIST_BEFORE="$(sql "SELECT count(*) FROM res_app_crm_account_history WHERE id=1")"
[ "$HIST_BEFORE" -ge 1 ] || fail "expected a pre-change history row"
echo "seeded Globex (id=1) + 1 history row"

step "1: v1 UI has NO owner field; start a session that outlives the change"
"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do grep -q "listening" "$SERVE_LOG" && break; sleep 0.1; done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
V1FORM="$(curl -s -D /tmp/.rg_hdr_$$ -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/form/1")"
OLD_SID="$(grep -i '^X-Regel-Session:' /tmp/.rg_hdr_$$ | awk '{print $2}' | tr -d '\r')"; rm -f /tmp/.rg_hdr_$$
[ -n "$OLD_SID" ] || fail "no session id on v1 form mount"
echo "$V1FORM" | grep -qE '>owner</label>' && fail "v1 form already has an owner field (bad fixture)"
echo "v1 form has fields: $(echo "$V1FORM" | grep -oE '>[a-z]+</label>' | tr -d '></label' | tr '\n' ' ')"
echo "pre-change session (survives the field-add): $OLD_SID"
BOUNDARY="$(sql "SELECT now()")"   # as-of boundary: BEFORE the field-add

step "2: TENANT ADMIN re-admits Account v2 (adds owner) via HTTP /admit --base v1"
V2="$(post_admit "$V2SRC" "$V1HASH")"
echo "$V2" | python3 -c 'import sys,json;o=json.load(sys.stdin);print("  outcome:",o.get("outcome"),"additive:",o.get("delta",{}).get("ddl_surface",{}).get("additive"))'
[ "$(echo "$V2" | vfield outcome)" = "admitted" ] || fail "v2 field-add did not admit"
echo "PASS: tenant admin added the owner field with NO engineer — admitted under optimistic concurrency"

step "3: ASSERT — new column present, existing rows intact, history preserved"
[ "$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_crm_account' AND column_name='owner'")" = "1" ] || fail "owner column not added"
[ "$(sql "SELECT name FROM res_app_crm_account WHERE id=1")" = "Globex" ] || fail "existing row lost"
[ "$(sql "SELECT owner IS NULL FROM res_app_crm_account WHERE id=1")" = "t" ] || fail "existing row's new column not NULL"
[ "$(sql "SELECT count(*) FROM res_app_crm_account_history WHERE id=1")" -ge "$HIST_BEFORE" ] || fail "history was truncated by the migration"
echo "PASS: owner column added · Globex intact (owner NULL) · $(sql "SELECT count(*) FROM res_app_crm_account_history WHERE id=1") history row(s) preserved"

step "4: OBSERVE THROUGH THE UI — the derived form now renders the owner field"
V2FORM="$(curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/form/1")"
echo "$V2FORM" | grep -qE '>owner</label>' || fail "v2 form does not render the new owner field"
echo "PASS: a fresh form mount renders the owner field (schema evolution visible in the UI)"

step "5: the pre-change session STILL renders across the field-add (resync)"
RESYNC="$(curl -s -X POST "$BASE/session/${OLD_SID}/resync")"
echo "$RESYNC" | grep -qF '"eventSeq"' || fail "pre-change session failed to resync after the field-add"
echo "PASS: the session opened before the change resynced cleanly (old sessions still render)"

step "6: the append-only template artifacts record v1 (no owner) vs v2 (owner)"
# BEFORE the boundary: the template artifact had no owner slot; AFTER: it does. This
# is the schema-behavior time record scenario (d) reads through the as-of UI.
PRE_OWNER="$(sql "SELECT detail::text FROM derived_artifact WHERE resource_name='app/crm/Account' AND pass='template' AND created_at <= '${BOUNDARY}'::timestamptz ORDER BY id DESC LIMIT 1" | grep -c 'owner' || true)"
POST_OWNER="$(sql "SELECT detail::text FROM derived_artifact WHERE resource_name='app/crm/Account' AND pass='template' ORDER BY id DESC LIMIT 1" | grep -c 'owner' || true)"
[ "$PRE_OWNER" = "0" ] || fail "the pre-boundary template already mentioned owner"
[ "$POST_OWNER" -ge 1 ] || fail "the post-change template does not mention owner"
echo "PASS: pre-boundary template artifact has NO owner slot; the current one does (temporal schema record)"

step "7: OPTIMISTIC CONCURRENCY — a concurrent admin on the STALE v1 base is rejected"
V4="$(post_admit "$V4SRC" "$V1HASH")"   # additive (owner+region) but based on the now-stale v1 head
echo "$V4" | python3 -c 'import sys,json;o=json.load(sys.stdin);print("  outcome:",o.get("outcome"),"| failed stage:",[(s["stage"],s["status"]) for s in o.get("stages",[]) if s["status"]!="pass"])'
[ "$(echo "$V4" | vfield outcome)" = "stale-base" ] || fail "a stale-base concurrent change should be rejected stale-base"
[ "$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_crm_account' AND column_name='region'")" = "0" ] || fail "stale-base change leaked a region column"
echo "PASS: the concurrent stale-base edit was rejected (STALE_BASE, cas stage) — no region column leaked"

echo
echo "=============================================================="
echo "DEMO OK — tenant field-add: real re-admission via HTTP door + optimistic concurrency, new column live, rows/history intact, UI shows the field"
echo "=============================================================="
