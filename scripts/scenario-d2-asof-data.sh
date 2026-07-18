#!/usr/bin/env bash
# scenario-d2-asof-data.sh — AS-OF ROW DATA (point-in-time reconstruction), the R3
# discharge. Stage-E scenario (d) proved as-of rolls SCHEMA/BEHAVIOR back through the
# ?as_of= mount but served the CURRENT row DATA (residue R3). This scenario proves the
# DATA leg: a ?as_of=T mount reconstructs the ROW VALUES that were live at T from the
# non-pii trigger history, while a live / ?as_of=now mount serves current values.
#
#   - admit Account + Contact; seed Globex (industry=manufacturing, arr=120000),
#     Initech, and Ada (Contact) with a vault-sealed pii email;
#   - capture boundary T0 (pre-change world instant);
#   - DATA changes after T0: UPDATE Globex (industry/arr), UPDATE Ada (role),
#     DELETE Initech, and CRYPTO-SHRED Ada's pii email;
#   - live detail mount → current data; ?as_of=T0 detail mount → HISTORICAL data;
#   - live table omits deleted Initech; ?as_of=T0 table reconstructs it;
#   - PII DISCIPLINE across as-of: ?as_of=T0 is BEFORE the shred, yet the pii email
#     stays MASKED and the plaintext is grep-ABSENT — history-excludes-PII means the
#     as-of path has no column to resurrect (a leak here would be a bug; it cannot).
#
# Standalone against a fresh regel_crm_asof_data DB. Exits nonzero on first mismatch.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_crm_asof_data"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8791"
BASE="http://localhost:8791"
BIN="$(mktemp -t regel.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-asofdata-serve.XXXXXX)"
SECRET="ada.lovelace@acme.example"

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
mount_live() { curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$1"; }
mount_asof() { curl -s -G -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' --data-urlencode "as_of=$1" "$2"; }

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0: substrate + genesis + admit Account/Contact; seed rows; seal Ada's pii email"
"$BIN" migrate-db >/dev/null || fail "migrate-db"
"$BIN" genesis    >/dev/null || fail "genesis"
"$BIN" admit crm/account.ts --name-prefix app/crm --actor engineer:dev >/dev/null || fail "admit Account"
"$BIN" admit crm/contact.ts --name-prefix app/crm --actor engineer:dev >/dev/null || fail "admit Contact"
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active')" >/dev/null
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES ('acme','Initech','software','https://initech.example',80000,'USD','pro','prospect')" >/dev/null
GID="$(sql "SELECT id FROM res_app_crm_account WHERE name='Globex'")"
IID="$(sql "SELECT id FROM res_app_crm_account WHERE name='Initech'")"
sql "INSERT INTO res_app_crm_contact (org,account_id,name,role,\"lastTouch\") VALUES ('acme',${GID},'Ada Lovelace','champion','2026-07-01T12:00:00Z')" >/dev/null
CID="$(sql "SELECT id FROM res_app_crm_contact WHERE name='Ada Lovelace'")"
printf '%s' "$SECRET" | "$BIN" vault-put --resource app/crm/Contact --subject "$CID" --field email --scope product >/dev/null || fail "vault-put"
echo "seeded Globex#$GID (manufacturing/120000), Initech#$IID, Contact Ada#$CID (role=champion, email sealed)"

step "1: capture boundary T0 (the pre-change world instant)"
T0="$(sql "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")"
[ -n "$T0" ] || fail "no boundary timestamp"
echo "boundary T0 = $T0"
sleep 0.1

step "2: DATA changes AFTER T0 — UPDATE Globex + Ada, DELETE Initech, SHRED Ada email"
sql "UPDATE res_app_crm_account SET industry='technology', arr=999000 WHERE id=${GID}" >/dev/null
sql "UPDATE res_app_crm_contact SET role='blocker' WHERE id=${CID}" >/dev/null
sql "DELETE FROM res_app_crm_account WHERE id=${IID}" >/dev/null
"$BIN" shred --resource app/crm/Contact --subject "$CID" --scope product --by operator:dpo >/dev/null || fail "shred"
echo "post-T0: Globex→technology/999000 · Ada.role→blocker · Initech DELETED · Ada.email crypto-shredded"
echo "history rows: account=$(sql "SELECT count(*) FROM res_app_crm_account_history") contact=$(sql "SELECT count(*) FROM res_app_crm_contact_history")"

step "3: serve; Account DETAIL — live shows current, ?as_of=T0 reconstructs historical"
"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do grep -q "listening" "$SERVE_LOG" && break; sleep 0.1; done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }

LIVE="$(mount_live "$BASE/ui/app/crm/Account/detail/${GID}")"
echo "$LIVE" | grep -qF 'technology' || fail "live detail must show current industry (technology)"
echo "$LIVE" | grep -qF '999000'     || fail "live detail must show current arr (999000)"
echo "PASS: live detail = technology / 999000 (current)"

PAST="$(mount_asof "$T0" "$BASE/ui/app/crm/Account/detail/${GID}")"
echo "$PAST" | grep -qF 'technology' && fail "as-of T0 detail LEAKED current industry (technology) — reconstruction failed"
echo "$PAST" | grep -qF '999000'     && fail "as-of T0 detail LEAKED current arr (999000)"
echo "$PAST" | grep -qF 'manufacturing' || fail "as-of T0 detail must reconstruct historical industry (manufacturing)"
echo "$PAST" | grep -qF '120000'        || fail "as-of T0 detail must reconstruct historical arr (120000)"
echo "PASS: as-of T0 detail = manufacturing / 120000 (HISTORICAL, point-in-time reconstructed)"

step "4: ?as_of=<now> returns CURRENT (no stale historical bleed)"
NOW="$(sql "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")"
NOWMOUNT="$(mount_asof "$NOW" "$BASE/ui/app/crm/Account/detail/${GID}")"
echo "$NOWMOUNT" | grep -qF 'technology'   || fail "as-of NOW must show current industry (technology)"
echo "$NOWMOUNT" | grep -qF 'manufacturing' && fail "as-of NOW leaked the historical value (manufacturing)"
echo "PASS: as-of NOW = current (technology) — the reconstruction collapses to head at the present instant"

step "5: Account TABLE — as-of T0 reconstructs the DELETED row; live omits it"
LIVE_TBL="$(mount_live "$BASE/ui/app/crm/Account/table")"
echo "$LIVE_TBL" | grep -qF 'Initech' && fail "live table must OMIT the deleted Initech"
echo "$LIVE_TBL" | grep -qF 'Globex'  || fail "live table must show Globex"
echo "PASS: live table shows Globex, omits deleted Initech"
PAST_TBL="$(mount_asof "$T0" "$BASE/ui/app/crm/Account/table")"
echo "$PAST_TBL" | grep -qF 'Initech' || fail "as-of T0 table must RECONSTRUCT Initech (deleted after T0)"
echo "$PAST_TBL" | grep -qF 'technology' && fail "as-of T0 table LEAKED current data (technology)"
echo "$PAST_TBL" | grep -qF 'manufacturing' || fail "as-of T0 table must reconstruct Globex's historical industry"
echo "PASS: as-of T0 table reconstructs the pre-change roster (Globex@manufacturing + deleted Initech)"

step "6: PII across as-of — ?as_of=T0 is BEFORE the shred, email STAYS MASKED, plaintext ABSENT"
C_LIVE="$(mount_live "$BASE/ui/app/crm/Contact/detail/${CID}")"
echo "$C_LIVE" | grep -qF "$SECRET" && fail "live contact leaked the plaintext email"
echo "$C_LIVE" | grep -qF 'blocker' || fail "live contact must show current role (blocker)"
C_PAST="$(mount_asof "$T0" "$BASE/ui/app/crm/Contact/detail/${CID}")"
echo "$C_PAST" | grep -qF "$SECRET" && fail "AS-OF (before shred) RESURRECTED the plaintext email — PII LEAK"
echo "$C_PAST" | grep -qF '••••'   || fail "as-of contact must render the pii mask glyph"
echo "$C_PAST" | grep -qF 'champion' || fail "as-of T0 contact must reconstruct historical role (champion)"
echo "PASS: as-of T0 contact = role 'champion' (historical) · email MASKED · plaintext ABSENT even before the shred"

step "7: the plaintext never lived in history either (structural PII exclusion)"
HIST_DUMP="$(psql -qtA "$REGEL_PG_DSN" -c "SELECT * FROM res_app_crm_contact_history")"
echo "$HIST_DUMP" | grep -qF "$SECRET" && fail "plaintext FOUND in contact history table"
HIST_HAS_EMAIL="$(sql "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='res_app_crm_contact_history' AND column_name='email')")"
[ "$HIST_HAS_EMAIL" = "f" ] || fail "pii column 'email' exists on the history table — as-of could resurrect it"
echo "PASS: history table has NO email column and NO plaintext — as-of reconstruction structurally cannot leak pii"

echo
echo "=============================================================="
echo "DEMO OK — as-of ROW DATA reconstructed point-in-time: historical values behind ?as_of=T0, current behind live/?as_of=now, deleted rows reconstructed, and PII stays masked across as-of (history-excludes-PII holds)"
echo "=============================================================="
