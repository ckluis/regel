#!/usr/bin/env bash
# scenario-g-files-i18n.sh — the R13 discharge: the reference CRM CONSUMES the
# newly-promoted `std/files` and `std/i18n` batteries (STAGE-F R13, ADR-10 §3
# BUILD-F). Both shipped DEFER → SHIP as genesis rows; this scenario is the RED
# PATH that earned the promotion — before it, `crm/attach.ts` is REFUSED at
# admission (IMPORT_UNRESOLVED for std/files.put + std/i18n.t; see
# evidence-f/r13/before.txt); after it, the workflow admits, runs, and spools a
# real attachment whose file NAME is localized and whose id is content-addressed.
#
#   - admit Account + crm/attach.ts (the batteries now resolve → admitted);
#   - seed one Account (Globex);
#   - serve with a FileSink spool + the outbox dispatcher live;
#   - run attach(Globex, locale, content) through the HTTP /workflow door TWICE:
#       locale=es → file name "Contrato.txt"  (std/i18n.t translation lookup),
#       locale=en → file name "Contract.txt";
#   - ASSERT the spooled files.put artifact carries: the localized name, the exact
#     content (download = read payload.content), and id == SHA-256(content) computed
#     independently (content-addressing, std/files);
#   - ASSERT a missing-key locale falls back deterministically (i18n fallback chain).
#
# Standalone against a fresh regel_crm_r13 DB. Exits nonzero on first mismatch.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_crm_r13"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8796"
BASE="http://localhost:8796"
BIN="$(mktemp -t regel.XXXXXX)"
SPOOL="$(mktemp -d -t regel-r13-spool.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-r13-serve.XXXXXX)"

KERNEL_PID=""
cleanup() {
  [ -n "$KERNEL_PID" ] && { kill "$KERNEL_PID" 2>/dev/null; wait "$KERNEL_PID" 2>/dev/null; }
  pkill -f "$BIN serve" 2>/dev/null
  rm -rf "$BIN" "$SPOOL" "$SERVE_LOG"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }
sha256() { printf '%s' "$1" | shasum -a 256 | awk '{print $1}'; }

# run_attach starts attach(account, locale, content), waits for the continuation to
# finish + the intent to be delivered, and echoes the single spool file's JSON.
run_attach() {
  local account="$1" locale="$2" content="$3"
  local before after
  before="$(find "$SPOOL" -type f | wc -l | tr -d ' ')"
  local cid
  cid="$(curl -s -X POST "$BASE/workflow/app/crm/attach" -H 'X-Regel-Actor: operator:op' \
    -d "[\"${account}\",\"${locale}\",\"${content}\"]" \
    | sed -n 's/.*"continuation_id": *"\([^"]*\)".*/\1/p')"
  [ -n "$cid" ] || fail "attach workflow did not start (locale=$locale)"
  local i st
  for i in $(seq 1 100); do
    st="$(sql "SELECT status FROM continuation WHERE id='${cid}'")"
    [ "$st" = "done" ] && break
    [ "$st" = "failed" ] && fail "attach workflow failed (locale=$locale)"
    sleep 0.1
  done
  [ "$(sql "SELECT status FROM continuation WHERE id='${cid}'")" = "done" ] || fail "attach not done (locale=$locale)"
  for i in $(seq 1 100); do
    after="$(find "$SPOOL" -type f | wc -l | tr -d ' ')"
    [ "${after:-0}" -gt "${before:-0}" ] && break
    sleep 0.1
  done
  [ "${after:-0}" -gt "${before:-0}" ] || fail "no new spool artifact for locale=$locale"
  # the newest spool file
  find "$SPOOL" -type f -exec ls -t {} + | head -1
}

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0: substrate + genesis (16-battery roster: std/files + std/i18n now SHIP)"
"$BIN" migrate-db >/dev/null || fail "migrate-db"
"$BIN" genesis    >/dev/null || fail "genesis"
FILES_PTRS="$(sql "SELECT count(*) FROM name_pointer WHERE name LIKE 'std/files/%'")"
I18N_PTRS="$(sql  "SELECT count(*) FROM name_pointer WHERE name LIKE 'std/i18n/%'")"
[ "${FILES_PTRS:-0}" -ge 2 ] || fail "std/files did not ship as genesis rows (got $FILES_PTRS)"
[ "${I18N_PTRS:-0}"  -ge 2 ] || fail "std/i18n did not ship as genesis rows (got $I18N_PTRS)"
echo "PASS: std/files ($FILES_PTRS rows: File+put) + std/i18n ($I18N_PTRS rows: Bundle+t) are genesis name_pointers"

step "1: admit Account + crm/attach.ts — the batteries now RESOLVE (was RED, see before.txt)"
"$BIN" admit crm/account.ts --name-prefix app/crm --actor engineer:dev >/dev/null || fail "admit Account"
OUT="$("$BIN" admit crm/attach.ts --name-prefix app/crm --actor engineer:dev)" || { echo "$OUT"; fail "admit attach"; }
echo "$OUT" | grep -qF '"outcome": "admitted"' || { echo "$OUT"; fail "attach not admitted"; }
echo "PASS: app/crm/attach admitted (imports std/files.put + std/i18n.t resolve)"

step "2: seed one Account (Globex)"
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES
  ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active')" >/dev/null || fail "seed"
echo "seeded: Globex"

step "3: serve with a FileSink spool + the outbox dispatcher"
"$BIN" serve -addr "$ADDR" -spool "$SPOOL" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do grep -q "listening" "$SERVE_LOG" && break; sleep 0.1; done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
echo "kernel up: $(head -1 "$SERVE_LOG")"

step "4: attach a document in locale=es → std/i18n localizes the NAME, std/files content-addresses it"
CONTENT_ES="Globex master services agreement — signed 2026."
ART_ES="$(run_attach "Globex" "es" "$CONTENT_ES")"
echo "spooled artifact (es): $ART_ES"
cat "$ART_ES"
echo
grep -qF '"class":"files.put"' "$ART_ES" || fail "es artifact is not a files.put intent"
grep -qF '"name":"Contrato.txt"' "$ART_ES" || fail "std/i18n did NOT localize the name to Contrato.txt (es)"
grep -qF "$CONTENT_ES" "$ART_ES" || fail "std/files did not spool the content (download leg)"
WANT_ID_ES="$(sha256 "$CONTENT_ES")"
grep -qF "\"id\":\"${WANT_ID_ES}\"" "$ART_ES" || fail "std/files id is not content-addressed (want SHA-256=$WANT_ID_ES)"
echo "PASS(es): name=Contrato.txt (i18n) · content spooled (download) · id=SHA-256(content)=${WANT_ID_ES:0:16}… (content-addressed)"

step "5: attach in locale=en → the SAME lookup yields Contract.txt (locale the scenario sets drives it)"
CONTENT_EN="Globex master services agreement — signed 2026."
ART_EN="$(run_attach "Globex" "en" "$CONTENT_EN")"
grep -qF '"name":"Contract.txt"' "$ART_EN" || fail "std/i18n did NOT localize the name to Contract.txt (en)"
# Same content ⇒ SAME content-addressed id as the es run (idempotent by construction).
WANT_ID_EN="$(sha256 "$CONTENT_EN")"
[ "$WANT_ID_EN" = "$WANT_ID_ES" ] || fail "identical content must yield identical id"
grep -qF "\"id\":\"${WANT_ID_EN}\"" "$ART_EN" || fail "en id not content-addressed"
echo "PASS(en): name=Contract.txt (i18n) · same content ⇒ same id (content-addressing is a function of bytes)"

step "6: a missing-locale key falls back deterministically (i18n fixed fallback chain)"
# locale 'fr' is absent from the app bundle → t() falls back to en → "Contract.txt".
CONTENT_FR="Fallback probe."
ART_FR="$(run_attach "Globex" "fr" "$CONTENT_FR")"
grep -qF '"name":"Contract.txt"' "$ART_FR" || fail "i18n fallback chain broken (fr → en expected Contract.txt)"
echo "PASS(fr): unknown locale fell back through the fixed chain (fr → en) → Contract.txt — never empty, never a crash"

echo
echo "=============================================================="
echo "DEMO OK — R13: the CRM consumes std/files + std/i18n (promoted DEFER→SHIP as genesis rows):"
echo "  · std/i18n.t localized the attachment name per the locale the caller set (es/en), with a deterministic fallback"
echo "  · std/files.put spooled a durable, readable, CONTENT-ADDRESSED artifact through the same outbox/FileSink door mail rides"
echo "=============================================================="
