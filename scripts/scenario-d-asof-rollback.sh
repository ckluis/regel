#!/usr/bin/env bash
# scenario-d-asof-rollback.sh — AS-OF ROLLBACK OBSERVED THROUGH THE UI. The whole
# point of regel's "rollback = as-of": a governed change is never destructive, and
# the pre-change world is queryable at a timestamp — through the SAME reactive UI
# users see live. This scenario:
#   - admits Account v1 + a value function metric()=100; seeds an Account row;
#   - captures a boundary timestamp T0 (before any change);
#   - admits a def change: Account v2 (adds an `owner` field) + metric()=200;
#   - LIVE UI mount → renders the owner field (post-change schema/behavior);
#   - AS-OF UI mount (?as_of=T0) → renders the pre-change schema (NO owner field) —
#     the rollback observed THROUGH THE UI surface, not a CLI side channel;
#   - secondary check: CLI `eval --as-of T0` resolves the OLD code (metric=100)
#     while a live eval resolves the new code (metric=200).
#
# Standalone against a fresh regel_crm_asof DB. Exits nonzero on the first mismatch.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_crm_asof"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8788"
BASE="http://localhost:8788"
BIN="$(mktemp -t regel.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-asof-serve.XXXXXX)"
V2SRC="$(mktemp -t regel-account-v2.XXXXXX.ts)"
M1="$(mktemp -t regel-metric1.XXXXXX.ts)"
M2="$(mktemp -t regel-metric2.XXXXXX.ts)"

KERNEL_PID=""
cleanup() {
  [ -n "$KERNEL_PID" ] && { kill "$KERNEL_PID" 2>/dev/null; wait "$KERNEL_PID" 2>/dev/null; }
  pkill -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$SERVE_LOG" "$V2SRC" "$M1" "$M2"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }
mount_html() { curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$1"; }

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

# v2 Account def = crm/account.ts + an owner field.
python3 - "$V2SRC" <<'PY'
import sys
base = open("crm/account.ts").read()
anchor = '    stage: "states:prospect|active|churned",'
open(sys.argv[1], "w").write(base.replace(anchor, anchor + '\n    owner: "text",'))
PY
printf 'export function metric(): number { return 100; }\n' >"$M1"
printf 'export function metric(): number { return 200; }\n' >"$M2"

step "0: substrate + genesis + admit Account v1 and metric v1 (=100); seed a row"
"$BIN" migrate-db >/dev/null || fail "migrate-db"
"$BIN" genesis    >/dev/null || fail "genesis"
ACC_V1="$("$BIN" admit crm/account.ts --name-prefix app/crm --actor engineer:dev | python3 -c 'import sys,json;print(json.load(sys.stdin)["hashes"]["app/crm/Account"])')"
MET_V1="$("$BIN" admit "$M1" --name-prefix app/crm --actor engineer:dev | python3 -c 'import sys,json;print(json.load(sys.stdin)["hashes"]["app/crm/metric"])')"
[ -n "$ACC_V1" ] && [ -n "$MET_V1" ] || fail "v1 admissions failed"
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active')" >/dev/null
echo "admitted Account v1 + metric v1; seeded Globex"

step "1: capture the as-of BOUNDARY (T0) — the pre-change world instant"
# Microsecond precision + explicit Z offset: the CLI --as-of and the HTTP ?as_of=
# both parse RFC3339 (a whole-second boundary can miss a sub-second admission).
T0="$(sql "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")"
[ -n "$T0" ] || fail "no boundary timestamp"
echo "boundary T0 = $T0"
sleep 0.1

step "2: admit the DEF CHANGE — Account v2 (adds owner) + metric v2 (=200)"
"$BIN" admit "$V2SRC" --name-prefix app/crm --actor engineer:dev --base "app/crm/Account=$ACC_V1" \
  | python3 -c 'import sys,json;o=json.load(sys.stdin);print("  Account v2:",o.get("outcome"))' || fail "Account v2"
[ "$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_crm_account' AND column_name='owner'")" = "1" ] || fail "owner column not added"
"$BIN" admit "$M2" --name-prefix app/crm --actor engineer:dev --base "app/crm/metric=$MET_V1" \
  | python3 -c 'import sys,json;o=json.load(sys.stdin);print("  metric v2:",o.get("outcome"))' || fail "metric v2"
echo "def change admitted (schema + code)"

step "3: start serve; LIVE UI mount renders the POST-change schema (owner present)"
"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do grep -q "listening" "$SERVE_LOG" && break; sleep 0.1; done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
LIVE="$(mount_html "$BASE/ui/app/crm/Account/form/1")"
echo "$LIVE" | grep -qE '>owner</label>' || fail "live UI mount does not render the owner field"
echo "PASS: live form mount renders the owner field (post-change behavior)"

step "4: AS-OF UI mount (?as_of=T0) renders the PRE-change schema (NO owner) — ROLLBACK VIA UI"
# URL-encode the '+'/':' safely — T0 uses 'Z', so only ':' matters; curl --data-urlencode via -G.
PAST="$(curl -s -G -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' \
  --data-urlencode "as_of=$T0" "$BASE/ui/app/crm/Account/form/1")"
echo "$PAST" | grep -qE '>owner</label>' && fail "as-of UI mount BEFORE the change LEAKED the owner field (rollback not observed)"
echo "$PAST" | grep -qE '>name</label>'  || fail "as-of UI mount did not render the v1 fields (name)"
echo "PASS: as-of form mount at T0 renders the v1 schema — owner ABSENT, name present (rollback observed through the UI)"

step "5: secondary — CLI eval --as-of resolves the OLD code, live resolves the new"
LIVE_METRIC="$("$BIN" eval app/crm/metric)"
ASOF_METRIC="$("$BIN" eval app/crm/metric --as-of "$T0")"
echo "live metric = $LIVE_METRIC · as-of(T0) metric = $ASOF_METRIC"
[ "$LIVE_METRIC" = "200" ] || fail "live eval should resolve metric v2 (=200), got $LIVE_METRIC"
[ "$ASOF_METRIC" = "100" ] || fail "as-of eval should resolve metric v1 (=100), got $ASOF_METRIC"
echo "PASS: rollback = as-of holds for code too — eval --as-of T0 → 100, live → 200"

echo
echo "=============================================================="
echo "DEMO OK — as-of rollback observed through the UI: as-of mount shows the pre-change schema, live shows post-change; eval --as-of confirms code rollback"
echo "=============================================================="
