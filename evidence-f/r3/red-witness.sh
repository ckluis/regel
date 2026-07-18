#!/usr/bin/env bash
# R3 RED witness: with the pre-R3 binary, a ?as_of=T0 mount serves CURRENT row data
# (and current PII mask). We change a row's data AFTER T0 and show the as-of mount
# leaks the new value instead of the historical one.
set -uo pipefail
BIN="${1:?bin}"
PGADMIN="postgres://clank@localhost:5432/postgres"
DEMO_DB="regel_r3_red"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8790"; BASE="http://localhost:8790"
SERVE_LOG="$(mktemp)"; KPID=""
cleanup(){ [ -n "$KPID" ] && { kill "$KPID" 2>/dev/null; wait "$KPID" 2>/dev/null; }; pkill -f "$BIN serve" 2>/dev/null; rm -f "$SERVE_LOG"; }
trap cleanup EXIT
sql(){ psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }
mount_asof(){ curl -s -G -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' --data-urlencode "as_of=$1" "$2"; }
mount_live(){ curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$1"; }

psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1
"$BIN" migrate-db >/dev/null; "$BIN" genesis >/dev/null
"$BIN" admit crm/account.ts --name-prefix app/crm --actor engineer:dev >/dev/null
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active')" >/dev/null
ID="$(sql "SELECT id FROM res_app_crm_account WHERE name='Globex'")"
echo "seeded Globex id=$ID industry=manufacturing arr=120000"

T0="$(sql "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")"
echo "boundary T0 = $T0"
sleep 0.1

# DATA CHANGE after T0: industry manufacturing -> technology, arr 120000 -> 999000
sql "UPDATE res_app_crm_account SET industry='technology', arr=999000 WHERE id=$ID" >/dev/null
echo "post-T0 UPDATE: industry=technology arr=999000 (history row written by trigger)"
echo "history rows: $(sql "SELECT count(*) FROM res_app_crm_account_history WHERE id=$ID")"

"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 & KPID=$!
for i in $(seq 1 60); do grep -q listening "$SERVE_LOG" && break; sleep 0.1; done

LIVE="$(mount_live "$BASE/ui/app/crm/Account/detail/$ID")"
PAST="$(mount_asof "$T0" "$BASE/ui/app/crm/Account/detail/$ID")"
echo
echo "=== LIVE detail mount (expect technology) ==="
echo "$LIVE" | grep -oE 'manufacturing|technology' | sort -u
echo "=== AS-OF T0 detail mount (SHOULD be manufacturing; pre-R3 shows...) ==="
echo "$PAST" | grep -oE 'manufacturing|technology' | sort -u
echo
if echo "$PAST" | grep -q technology; then
  echo "RED CONFIRMED: as-of T0 mount served the CURRENT value 'technology' — historical row DATA is NOT reconstructed (residue R3)."
else
  echo "no red (as-of served historical) — unexpected on pre-R3 binary"
fi
