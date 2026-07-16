#!/usr/bin/env bash
# scenario-e-pii-shred.sh — PII CRYPTO-SHRED WITH ORACLE-STYLE ATTESTATION.
# Seals a Contact email through the REAL `regel vault-put` door, shows the derived
# detail render masks it (plaintext never in the HTML), then `regel shred`
# crypto-shreds the subject's vault key. The shred_attestation is verified
# ORACLE-STYLE: every field (resource, subject, keys_shredded, principal,
# timestamp) is independently recomputed and matched — not merely "a row exists".
# After the shred: the read path returns the mask token, the vault_key is gone,
# the ciphertext blob remains (undecryptable by construction), and the plaintext
# is grep-ABSENT from the base table, the history table, AND a live session render.
#
# Standalone against a fresh regel_crm_pii DB. Exits nonzero on the first mismatch.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_crm_pii"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8799"
BASE="http://localhost:8799"
BIN="$(mktemp -t regel.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-pii-serve.XXXXXX)"

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

SECRET="ada.lovelace@acme.example"

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0: migrate-db + genesis + admit Account/Contact from crm/"
"$BIN" migrate-db >/dev/null || fail "migrate-db"
"$BIN" genesis    >/dev/null || fail "genesis"
"$BIN" admit crm/account.ts --name-prefix app/crm --actor engineer:dev >/dev/null || fail "admit Account"
"$BIN" admit crm/contact.ts --name-prefix app/crm --actor engineer:dev >/dev/null || fail "admit Contact"
echo "admitted app/crm/Account + app/crm/Contact"

step "1: seed a Contact (pii columns NEVER seeded here — vault-routed)"
sql "INSERT INTO res_app_crm_contact (org,account_id,name,role,\"lastTouch\") VALUES ('acme',1,'Ada Lovelace','champion','2026-07-01T12:00:00Z')" >/dev/null || fail "seed contact"
ID="$(sql "SELECT id FROM res_app_crm_contact WHERE name='Ada Lovelace'")"
[ -n "$ID" ] || fail "no contact id"
echo "seeded Contact#$ID (Ada Lovelace)"

step "2: seal the pii email via the REAL 'regel vault-put' door (secret on STDIN)"
printf '%s' "$SECRET" | "$BIN" vault-put --resource app/crm/Contact --subject "$ID" --field email --scope product || fail "vault-put"
CT="$(sql "SELECT ciphertext FROM vault WHERE resource='res_app_crm_contact' AND subject_id='${ID}' AND field='email'")"
[ -n "$CT" ] || fail "no ciphertext sealed"
echo "$CT" | grep -qF "$SECRET" && fail "ciphertext CONTAINS the plaintext — not sealed"
echo "sealed: ciphertext(preview)=${CT:0:32}…  (distinct from plaintext)"

step "3: the derived detail render MASKS the email (plaintext absent from HTML)"
"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do grep -q "listening" "$SERVE_LOG" && break; sleep 0.1; done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
DETAIL="$(curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Contact/detail/${ID}")"
echo "$DETAIL" | grep -qF "$SECRET" && fail "PLAINTEXT email leaked into the detail render"
echo "$DETAIL" | grep -qF '••••' || fail "detail render does not carry the mask glyph (reveal-grant path)"
echo "PASS: detail render shows the mask glyph '••••·<tag>', plaintext ABSENT (reveal requires a grant)"

step "4: capture the shred boundary, then 'regel shred' the subject's vault key"
KEYS_BEFORE="$(sql "SELECT count(*) FROM vault_key WHERE resource='res_app_crm_contact' AND subject_id='${ID}'")"
[ "$KEYS_BEFORE" = "1" ] || fail "expected 1 vault_key before shred, got $KEYS_BEFORE"
T0="$(sql "SELECT now()")"
SHRED_OUT="$("$BIN" shred --resource app/crm/Contact --subject "$ID" --scope product --by operator:dpo)" || fail "shred"
T1="$(sql "SELECT now()")"
echo "$SHRED_OUT"
echo "$SHRED_OUT" | grep -qF "key(s) destroyed" || fail "shred output missing 'key(s) destroyed'"

step "5: ORACLE — recompute every attestation field and match the stored row"
# Independently recompute the expected attestation content:
EXP_TABLE="$(sql "SELECT table_name FROM derived_resource WHERE resource_name='app/crm/Contact' ORDER BY scope_kind,scope_id LIMIT 1")"
EXP_KEYS="$KEYS_BEFORE"           # keys_shredded must equal the pre-shred key count
EXP_BY="operator:dpo"            # principal passed to the door
read -r A_RES A_SUBJ A_KEYS A_BY A_AT <<<"$(sql "SELECT resource||' '||subject_id||' '||keys_shredded||' '||shredded_by||' '||to_char(shredded_at,'YYYYMMDDHH24MISS') FROM shred_attestation WHERE resource='res_app_crm_contact' AND subject_id='${ID}'")"
[ "$A_RES"  = "$EXP_TABLE" ] || fail "attestation resource '$A_RES' != oracle '$EXP_TABLE'"
[ "$A_SUBJ" = "$ID" ]        || fail "attestation subject '$A_SUBJ' != oracle '$ID'"
[ "$A_KEYS" = "$EXP_KEYS" ]  || fail "attestation keys_shredded '$A_KEYS' != oracle '$EXP_KEYS'"
[ "$A_BY"   = "$EXP_BY" ]    || fail "attestation principal '$A_BY' != oracle '$EXP_BY'"
# Timestamp must fall inside the [T0,T1] shred window we captured around the call.
IN_WINDOW="$(sql "SELECT (shredded_at BETWEEN '${T0}'::timestamptz AND '${T1}'::timestamptz) FROM shred_attestation WHERE resource='res_app_crm_contact' AND subject_id='${ID}'")"
[ "$IN_WINDOW" = "t" ] || fail "attestation timestamp not within the captured shred window [$T0,$T1]"
ATT_N="$(sql "SELECT count(*) FROM shred_attestation WHERE resource='res_app_crm_contact' AND subject_id='${ID}'")"
[ "$ATT_N" = "1" ] || fail "expected exactly 1 attestation, got $ATT_N"
echo "PASS: attestation recomputes EXACTLY — resource=$A_RES subject=$A_SUBJ keys=$A_KEYS by=$A_BY at∈[$T0,$T1]"

step "6: post-shred — key gone, blob remains, read returns the mask token"
KEYS_AFTER="$(sql "SELECT count(*) FROM vault_key WHERE resource='res_app_crm_contact' AND subject_id='${ID}'")"
[ "$KEYS_AFTER" = "0" ] || fail "vault_key survived the shred ($KEYS_AFTER)"
BLOB_AFTER="$(sql "SELECT count(*) FROM vault WHERE resource='res_app_crm_contact' AND subject_id='${ID}'")"
[ "$BLOB_AFTER" = "1" ] || fail "ciphertext blob unexpectedly gone/duplicated ($BLOB_AFTER)"
echo "vault_key=$KEYS_AFTER (destroyed) · ciphertext blob=$BLOB_AFTER (remains, undecryptable — the only key is gone)"
POST_DETAIL="$(curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Contact/detail/${ID}")"
echo "$POST_DETAIL" | grep -qF "$SECRET" && fail "plaintext appeared post-shred (impossible — key destroyed)"
echo "$POST_DETAIL" | grep -qF '••••' || fail "post-shred read does not return the mask token"
echo "PASS: post-shred read returns the mask token, never a decryption"

step "7: history stays clean — plaintext ABSENT from base + history + session snapshot"
BASE_DUMP="$(psql -qtA "$REGEL_PG_DSN" -c "SELECT * FROM res_app_crm_contact WHERE id=${ID}")"
echo "$BASE_DUMP" | grep -qF "$SECRET" && fail "plaintext FOUND in base table"
HIST_DUMP="$(psql -qtA "$REGEL_PG_DSN" -c "SELECT * FROM res_app_crm_contact_history WHERE id=${ID}")"
echo "$HIST_DUMP" | grep -qF "$SECRET" && fail "plaintext FOUND in history table"
# Bonus: the durable session-continuation frames (the CFR snapshot bytes) never
# carried the plaintext either.
SNAP_HITS="$(sql "SELECT count(*) FROM continuation WHERE kind='session' AND encode(frames,'escape') LIKE '%${SECRET}%'")"
[ "${SNAP_HITS:-0}" = "0" ] || fail "plaintext FOUND in a session snapshot ($SNAP_HITS)"
echo "PASS: '$SECRET' ABSENT from base table, history table, and every session snapshot"

echo
echo "=============================================================="
echo "DEMO OK — pii crypto-shred: sealed via vault-put, masked render, oracle-recomputed attestation, key destroyed, plaintext never anywhere"
echo "=============================================================="
