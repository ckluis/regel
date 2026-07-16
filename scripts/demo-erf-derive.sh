#!/usr/bin/env bash
# demo-erf-derive.sh — THE Stage-D acceptance demo (ADR-10 §4/§5): admit a resource
# exercising the closed 13-type field roster (+ two pii wraps) through the gate,
# show the full eleven-pass derivation (schema/history/validator/policy/vault/
# horizon/components/openapi/mcptools/catalog/template — ADR-10 §4 + the ADR-11 §1
# 'template' pass), show the derived base table + its history shadow table + AFTER
# trigger, seal a pii value into the vault substrate (ADR-10 §4 item 5), prove the
# plaintext NEVER touches the base table or its history (grep), then crypto-shred
# the subject and prove the ciphertext is left permanently undecryptable.
#
# VaultPut (internal/admission/vault.go) now HAS a CLI door: `regel vault-put`
# (STAGE-E D12) calls the real internal/admission.VaultPut with the same per-subject
# AES-256-GCM AEAD the D1 test battery uses, reading the secret from STDIN (never
# argv). This script drives that door directly — no more openssl hand-roll. `regel
# shred` IS a real CLI door (internal/admission.CryptoShred) and is invoked unmodified.
#
# Re-runnable against a fresh `regel_erf_demo` DB. Exits nonzero on the first
# mismatch; prints DEMO OK.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_erf_demo"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
BIN="$(mktemp -t regel.XXXXXX)"
RESFILE="$(mktemp -t regel-contact.XXXXXX.ts)"

cleanup() {
  rm -f "$BIN" "$RESFILE"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }

step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }

sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0a: migrate-db"
OUT="$("$BIN" migrate-db)" || fail "migrate-db"
echo "$OUT"

step "0b: genesis"
OUT="$("$BIN" genesis)" || fail "genesis"
echo "$OUT"

step "1: admit a resource exercising the closed 13-type roster (+ 2 pii wraps)"
# The D1 acceptance fixture (internal/admission/d1_derive_test.go contactSrc): text,
# longtext, number, money, boolean, date, timestamp, pii:email, pii:phone, url,
# address, select, states, relation — every base type, two pii-wrapped.
cat >"$RESFILE" <<'TS'
import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Contact = resource({
  fields: {
    name: "text",
    notes: "longtext",
    score: "number",
    dealValue: "money",
    active: "boolean",
    closedOn: "date",
    lastSeen: "timestamp",
    email: "pii:email",
    phone: "pii:phone",
    site: "url",
    hq: "address",
    tier: "select:bronze|silver|gold",
    stage: "states:new|active|won|lost",
    company: "belongsTo:Company"
  },
  policy: orgScoped,
});
TS
V="$("$BIN" admit "$RESFILE" --name-prefix app/derive --actor engineer:dev)" || fail "admit contact"
echo "$V" | grep -qF '"outcome": "admitted"' || fail "Contact not admitted: $V"
echo "admitted: app/derive/Contact"

step "2: the eleven ADR-10 §4 derivation passes (+ ADR-11 §1 'template')"
PASSES="$(sql "SELECT pass FROM derived_artifact WHERE resource_name='app/derive/Contact' ORDER BY pass")"
echo "$PASSES"
EXPECTED="catalog
components
history
horizon
mcptools
openapi
policy
schema
template
validator
vault"
[ "$PASSES" = "$EXPECTED" ] || fail "derived_artifact pass set mismatch:\n$PASSES\n!=\n$EXPECTED"
N="$(sql "SELECT count(*) FROM derived_artifact WHERE resource_name='app/derive/Contact'")"
[ "$N" = "11" ] || fail "derived_artifact row count $N != 11"
echo "PASS: all eleven passes present, exactly once each"

step "3: the derived base table + history shadow table + AFTER trigger exist"
TABLE_EXISTS="$(sql "SELECT to_regclass('res_app_derive_contact') IS NOT NULL")"
HIST_EXISTS="$(sql "SELECT to_regclass('res_app_derive_contact_history') IS NOT NULL")"
[ "$TABLE_EXISTS" = "t" ] || fail "base table res_app_derive_contact missing"
[ "$HIST_EXISTS" = "t" ] || fail "history table res_app_derive_contact_history missing"
echo "base table res_app_derive_contact: present"
echo "history table res_app_derive_contact_history: present"
psql "$REGEL_PG_DSN" -c "\d res_app_derive_contact" | sed -n '1,25p'
TRG="$(sql "SELECT count(*) FROM pg_trigger WHERE tgname='res_app_derive_contact_hist_trg'")"
[ "$TRG" = "1" ] || fail "history AFTER trigger missing ($TRG)"
echo "PASS: res_app_derive_contact_hist_trg installed (1 row in pg_trigger)"
# pii fields (email, phone) derive NO base column — vault-routed only.
for col in email phone; do
  HAS="$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_derive_contact' AND column_name='${col}'")"
  [ "$HAS" = "0" ] || fail "pii field ${col} leaked a base column"
done
echo "PASS: email/phone (pii) have NO base table column — vault-routed only"

step "4: insert a row (non-pii columns only — pii never lands here)"
ID="$(sql "
INSERT INTO res_app_derive_contact
  (name, notes, score, \"dealValue\", \"dealValue_currency\", active, \"closedOn\", \"lastSeen\", site,
   hq_line1, hq_line2, hq_city, hq_region, hq_postal, hq_country, tier, stage, company_id)
VALUES
  ('Ada Lovelace', 'vip lead — Stage-D acceptance demo subject', 95, 500000, 'USD', true,
   '2026-01-15', '2026-07-01T12:00:00Z', 'https://acme.example',
   '1 Infinite Loop', '', 'Cupertino', 'CA', '95014', 'US', 'gold', 'active', NULL)
RETURNING id")"
[ -n "$ID" ] || fail "insert did not return an id"
echo "inserted subject id=$ID"

step "5: seal a pii value into the vault via the real 'regel vault-put' CLI door"
SECRET="ada.lovelace@acme.example"
# The secret is piped on STDIN (never argv); vault-put calls the real VaultPut AEAD.
printf '%s' "$SECRET" | "$BIN" vault-put --resource app/derive/Contact --subject "$ID" --field email --scope product \
  || fail "vault-put"
CIPHERTEXT="$(sql "SELECT ciphertext FROM vault WHERE resource='res_app_derive_contact' AND subject_id='${ID}' AND field='email'")"
[ -n "$CIPHERTEXT" ] || fail "vault-put wrote no ciphertext"
echo "sealed subject=$ID field=email ciphertext(preview)=${CIPHERTEXT:0:32}..."

step "6: an UPDATE fires the history trigger (a live edit, unrelated column)"
sql "UPDATE res_app_derive_contact SET score=97 WHERE id=${ID}" >/dev/null
HN="$(sql "SELECT count(*) FROM res_app_derive_contact_history WHERE id=${ID}")"
[ "$HN" = "1" ] || fail "history rows after UPDATE = $HN, want 1"
echo "PASS: 1 history row written for subject $ID"

step "7: the pii plaintext is ABSENT from the base table AND its history (grep)"
BASE_ROW="$(psql -qtA "$REGEL_PG_DSN" -c "SELECT * FROM res_app_derive_contact WHERE id=${ID}")"
echo "base row: $BASE_ROW"
if echo "$BASE_ROW" | grep -qF "$SECRET"; then
  fail "plaintext FOUND in base table res_app_derive_contact — vault isolation violated"
fi
echo "grep '$SECRET' in base table: ABSENT (pass)"
HIST_ROW="$(psql -qtA "$REGEL_PG_DSN" -c "SELECT * FROM res_app_derive_contact_history WHERE id=${ID}")"
echo "history row: $HIST_ROW"
if echo "$HIST_ROW" | grep -qF "$SECRET"; then
  fail "plaintext FOUND in history table res_app_derive_contact_history — vault isolation violated"
fi
echo "grep '$SECRET' in history table: ABSENT (pass)"

step "8: the vault ciphertext exists and is NOT the plaintext"
CT_ROW="$(sql "SELECT ciphertext FROM vault WHERE resource='res_app_derive_contact' AND subject_id='${ID}' AND field='email'")"
[ -n "$CT_ROW" ] || fail "no vault ciphertext row for subject $ID"
echo "vault.ciphertext = $CT_ROW"
if [ "$CT_ROW" = "$SECRET" ] || echo "$CT_ROW" | grep -qF "$SECRET"; then
  fail "vault ciphertext contains the plaintext — not actually sealed"
fi
echo "PASS: vault carries ciphertext only, distinct from the plaintext"

step "9: regel shred — crypto-shred the subject's vault key"
SHRED_OUT="$("$BIN" shred --resource app/derive/Contact --subject "$ID" --scope product --by operator:dpo)" || fail "shred"
echo "$SHRED_OUT"
echo "$SHRED_OUT" | grep -qF "key(s) destroyed" || fail "shred output missing 'key(s) destroyed': $SHRED_OUT"
echo "$SHRED_OUT" | grep -qF "attestation #" || fail "shred output missing attestation id: $SHRED_OUT"

step "10: the shred_attestation row"
ATT="$(psql "$REGEL_PG_DSN" -c "SELECT id, resource, subject_id, keys_shredded, shredded_by, shredded_at FROM shred_attestation WHERE resource='res_app_derive_contact' AND subject_id='${ID}'")"
echo "$ATT"
ATT_N="$(sql "SELECT count(*) FROM shred_attestation WHERE resource='res_app_derive_contact' AND subject_id='${ID}'")"
[ "$ATT_N" = "1" ] || fail "shred_attestation row count = $ATT_N, want 1"
echo "PASS: exactly 1 shred_attestation row"

step "11: post-shred — the vault key is gone; the ciphertext is now permanently undecryptable"
KEY_N="$(sql "SELECT count(*) FROM vault_key WHERE resource='res_app_derive_contact' AND subject_id='${ID}'")"
[ "$KEY_N" = "0" ] || fail "vault_key survived the shred ($KEY_N rows)"
echo "vault_key rows for subject $ID: $KEY_N (gone — the ONLY path to the AEAD key is destroyed)"
CT_N="$(sql "SELECT count(*) FROM vault WHERE resource='res_app_derive_contact' AND subject_id='${ID}'")"
[ "$CT_N" = "1" ] || fail "vault ciphertext blob unexpectedly gone/duplicated ($CT_N)"
echo "vault ciphertext blob rows for subject $ID: $CT_N (remains — crypto-shred destroys the KEY, not the blob)"
echo "Per internal/admission/vault.go VaultReveal: a missing vault_key row means the read path returns"
echo "the mask token (VaultMaskToken = \"‹masked›\"), never a decryption — the value is unreadable by"
echo "construction now that its only key row is deleted (asserted directly above: vault_key rows = 0)."

echo
echo "=============================================================="
echo "DEMO OK — erf Stage-D derivation: 11 passes, history live, pii vaulted + never leaked, crypto-shred verified"
echo "=============================================================="
