#!/usr/bin/env bash
# crm-setup.sh — DELIVERABLE 1 of the Stage-E proof CRM. It DOGFOODS the real
# regel surfaces: a fresh substrate, genesis, capability grants, then the whole
# CRM ADMITTED as rows through the gate — three erf resources (Account/Contact/
# Activity), a std/taak follow-up workflow (mail.send over the outbox → FileSink
# spool), a hand-authored component (AccountCard, D3 lowering), and a typed
# std/sql pipeline read (D1). There is NO hand-written app DDL: every table,
# history shadow, policy, board, dashboard, and component template is DERIVED.
#
# Seeding prefers the REAL doors: a form-submit edit through the reactive session
# layer and a vault-put of a pii email through the CLI door; bulk rows go in via
# psql (the one allowed side door, kept minimal). It then ASSERTS the derivations
# and prints DEMO OK. Re-runnable against a fresh regel_crm_demo DB; exits nonzero
# on the first mismatch.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_crm_demo"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8794"
BASE="http://localhost:8794"
BIN="$(mktemp -t regel.XXXXXX)"
SPOOL="$(mktemp -d -t regel-crm-spool.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-crm-serve.XXXXXX)"

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

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0a: migrate-db + genesis"
"$BIN" migrate-db || fail "migrate-db"
"$BIN" genesis    || fail "genesis"

step "0b: grant the capabilities the CRM's code declares (mail.send, sql.query)"
# A capability-bearing native (mail.send/sql.query) is admitted ONLY when the def
# declares it AND the actor holds a matching grant — no ambient authority.
"$BIN" grant engineer:dev mail.send || fail "grant mail.send"
"$BIN" grant engineer:dev sql.query || fail "grant sql.query"
echo "granted: engineer:dev → mail.send, sql.query"

step "1: admit the CRM as rows — 3 resources + workflow + component + pipeline"
admit_ok() {
  local file="$1" name="$2"; shift 2
  local out
  out="$("$BIN" admit "$file" --name-prefix app/crm --actor engineer:dev "$@")" || { echo "$out"; fail "admit $name"; }
  echo "$out" | grep -qF '"outcome": "admitted"' || { echo "$out"; fail "$name not admitted"; }
  echo "admitted: $name"
}
admit_ok crm/account.ts   app/crm/Account
admit_ok crm/contact.ts   app/crm/Contact
admit_ok crm/activity.ts  app/crm/Activity
admit_ok crm/followup.ts  app/crm/followup    --declare mail.send
admit_ok crm/accountcard.ts app/crm/AccountCard
admit_ok crm/pipeline.ts  app/crm/activePipeline --declare sql.query

step "2: ASSERT — 3 resources derived (table + history + policy each)"
for r in Account Contact Activity; do
  tbl="res_app_crm_$(echo "$r" | tr 'A-Z' 'a-z')"
  [ "$(sql "SELECT to_regclass('${tbl}') IS NOT NULL")" = "t" ]         || fail "base table ${tbl} missing"
  [ "$(sql "SELECT to_regclass('${tbl}_history') IS NOT NULL")" = "t" ] || fail "history table ${tbl}_history missing"
  POL="$(sql "SELECT coalesce(policy_name,'') FROM derived_resource WHERE resource_name='app/crm/${r}'")"
  [ -n "$POL" ] || fail "resource app/crm/${r} has no derived policy"
  echo "  app/crm/${r}: table ${tbl} + ${tbl}_history + policy '${POL}'  OK"
done
NRES="$(sql "SELECT count(DISTINCT resource_name) FROM derived_resource WHERE resource_name LIKE 'app/crm/%'")"
[ "$NRES" = "3" ] || fail "expected 3 derived resources, got $NRES"
echo "PASS: exactly 3 derived resources"

step "3: ASSERT — pii fields (Contact.email/phone) derive NO base column (vault-routed)"
for col in email phone; do
  HAS="$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_crm_contact' AND column_name='${col}'")"
  [ "$HAS" = "0" ] || fail "pii field ${col} leaked a base column"
done
echo "PASS: Contact.email/phone are vault-routed (no base column)"

step "4: ASSERT — board(Account) + dashboards derived; component template lowered"
BOARD="$(sql "SELECT detail::text FROM derived_artifact WHERE resource_name='app/crm/Account' AND pass='template'" | grep -c '"board"')"
[ "$BOARD" = "1" ] || fail "Account (states field) did not derive a board template"
echo "  board(app/crm/Account): present  OK"
for r in Account Contact Activity; do
  DASH="$(sql "SELECT detail::text FROM derived_artifact WHERE resource_name='app/crm/${r}' AND pass='template'" | grep -c '"dashboard"')"
  [ "$DASH" = "1" ] || fail "app/crm/${r} did not derive a dashboard template"
done
echo "  dashboard(Account/Contact/Activity): present  OK"
CT="$(sql "SELECT count(*) FROM derived_artifact WHERE resource_name='app/crm/AccountCard' AND pass='component_template'")"
[ "$CT" = "1" ] || fail "AccountCard did not lower to a component_template ($CT)"
echo "  component_template(app/crm/AccountCard): present  OK"

step "5: ASSERT — workflow + pipeline admitted as catalog definitions"
for d in followup activePipeline; do
  N="$(sql "SELECT count(*) FROM name_pointer WHERE name='app/crm/${d}'")"
  [ "${N:-0}" -ge 1 ] || fail "definition app/crm/${d} not resolvable"
  echo "  app/crm/${d}: admitted  OK"
done

step "6: bulk-seed rows (psql side door — minimal) then edit via the REAL form door"
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES
  ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active'),
  ('acme','Initech','software','https://initech.example',36000,'USD','pro','prospect'),
  ('acme','Umbrella','biotech','https://umbrella.example',480000,'USD','enterprise','active')" >/dev/null || fail "seed accounts"
sql "INSERT INTO res_app_crm_contact (org,account_id,name,role,\"lastTouch\") VALUES
  ('acme',1,'Ada Lovelace','champion','2026-07-01T12:00:00Z'),
  ('acme',2,'Alan Turing','economic buyer','2026-06-15T09:00:00Z')" >/dev/null || fail "seed contacts"
sql "INSERT INTO res_app_crm_activity (org,account_id,contact_id,kind,note,\"on\",done) VALUES
  ('acme',1,1,'call','intro call — strong fit','2026-07-02T15:00:00Z',true),
  ('acme',1,1,'email','sent pricing','2026-07-05T10:00:00Z',false)" >/dev/null || fail "seed activities"
echo "seeded: 3 accounts, 2 contacts, 2 activities"

"$BIN" serve -addr "$ADDR" -spool "$SPOOL" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do grep -q "listening" "$SERVE_LOG" && break; sleep 0.1; done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
echo "kernel up: $(head -1 "$SERVE_LOG")"

# REAL DOOR #1 — a form-submit edit through the reactive session layer.
HDR="$(mktemp -t regel-hdr.XXXXXX)"
BODY="$(curl -s -D "$HDR" -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/form/2")"
SID="$(grep -i '^X-Regel-Session:' "$HDR" | awk '{print $2}' | tr -d '\r')"; rm -f "$HDR"
[ -n "$SID" ] || fail "no session id on form mount"
IND_SLOT="$(echo "$BODY" | grep -oE '<label class="rg-label">industry</label><input[^>]*data-slot="[^"]+"' | grep -oE 'data-slot="[^"]+"' | sed -E 's/data-slot="([^"]+)"/\1/')"
[ -n "$IND_SLOT" ] || fail "could not find the industry form slot"
curl -s -X POST "$BASE/session/${SID}/event" -H 'Content-Type: application/json' \
  -d "{\"slotId\":\"${IND_SLOT}\",\"event\":\"input\",\"value\":\"fintech\",\"eventSeq\":0}" >/dev/null
curl -s -X POST "$BASE/session/${SID}/event" -H 'Content-Type: application/json' \
  -d '{"slotId":"","event":"submit","value":"","eventSeq":1}' >/dev/null
NEWIND="$(sql "SELECT industry FROM res_app_crm_account WHERE id=2")"
[ "$NEWIND" = "fintech" ] || fail "form-door edit did not commit (industry='$NEWIND')"
echo "REAL DOOR (form submit): Initech.industry committed to 'fintech'"

# REAL DOOR #2 — seal a pii email into the vault via the CLI vault-put door.
printf '%s' 'ada.lovelace@acme.example' | "$BIN" vault-put --resource app/crm/Contact --subject 1 --field email --scope product || fail "vault-put"
CT_N="$(sql "SELECT count(*) FROM vault WHERE resource='res_app_crm_contact' AND subject_id='1' AND field='email'")"
[ "$CT_N" = "1" ] || fail "vault-put wrote no ciphertext"
echo "REAL DOOR (vault-put): Contact#1 email sealed (ciphertext-only)"

step "7: run the follow-up workflow through the HTTP door → outbox → FileSink spool"
CID="$(curl -s -X POST "$BASE/workflow/app/crm/followup" -H 'X-Regel-Actor: operator:op' -d '["Globex"]' | sed -n 's/.*"continuation_id": *"\([^"]*\)".*/\1/p')"
[ -n "$CID" ] || fail "followup workflow did not start"
for i in $(seq 1 100); do
  ST="$(sql "SELECT status FROM continuation WHERE id='${CID}'")"
  [ "$ST" = "done" ] && break
  [ "$ST" = "failed" ] && fail "followup workflow failed"
  sleep 0.1
done
[ "$(sql "SELECT status FROM continuation WHERE id='${CID}'")" = "done" ] || fail "followup not done"
# Delivery (delivered_at stamp + spool write) is the outbox dispatcher's async
# job — poll for it rather than racing the step that merely enqueued the intent.
DELIVERED=0
for i in $(seq 1 100); do
  DELIVERED="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${CID}' AND delivered_at IS NOT NULL")"
  [ "${DELIVERED:-0}" -ge 1 ] && break
  sleep 0.1
done
[ "$DELIVERED" = "1" ] || fail "expected 1 delivered mail intent, got $DELIVERED"
SPOOLED="$(find "$SPOOL" -type f | wc -l | tr -d ' ')"
[ "${SPOOLED:-0}" -ge 1 ] || fail "no spooled delivery file"
echo "PASS: followup done, 1 mail.send intent delivered effectively-once to the FileSink spool"
find "$SPOOL" -type f -exec cat {} \; | head -1

step "8: ASSERT — table/board/component render live over the derived rows"
# The horizon-scoped table lists the account names.
TABLE_HTML="$(curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/table")"
for n in Globex Initech Umbrella; do
  echo "$TABLE_HTML" | grep -qF "$n" || fail "table did not render account '$n'"
done
echo "  table lists Globex/Initech/Umbrella  OK"
# The board is grouped by the states field: one column per member, cards partitioned.
BOARD_HTML="$(curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/board")"
for col in prospect active churned; do
  echo "$BOARD_HTML" | grep -qF ">${col}</h2>" || fail "board missing the '$col' column"
done
# Two active accounts (Globex, Umbrella) live under the 'active' column.
ACTIVE_CARDS="$(echo "$BOARD_HTML" | grep -oE 'data-slot="board.badge.1#[0-9]+"' | wc -l | tr -d ' ')"
[ "$ACTIVE_CARDS" = "2" ] || fail "expected 2 cards under 'active', got $ACTIVE_CARDS"
echo "  board grouped by stage: prospect|active|churned, 2 cards under active  OK"
# The hand-authored component binds Account#1's name into its heading leaf.
CARD_HTML="$(curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/detail/1?component=app/crm/AccountCard")"
echo "$CARD_HTML" | grep -qF 'rg-card'  || fail "AccountCard component did not render a card"
echo "$CARD_HTML" | grep -qF 'Globex'   || fail "AccountCard did not bind Account#1 name"
echo "  AccountCard component renders a card binding Account#1 (Globex)  OK"
echo "PASS: table + board + AccountCard component render over the live derived rows"

echo
echo "=============================================================="
echo "DEMO OK — proof CRM admitted as rows: 3 resources + workflow + component + pipeline, real-door seed, live derivations"
echo "=============================================================="
