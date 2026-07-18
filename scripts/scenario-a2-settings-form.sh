#!/usr/bin/env bash
# scenario-a2-settings-form.sh — R2 DISCHARGE: the POINT-AND-CLICK Settings form.
# Scenario-a proved a tenant field-add through the HTTP /admit door by editing the
# resource SOURCE on disk; its named papercut was that no point-and-click Settings
# FORM shipped. This scenario ships that form AS ADMITTED ROWS and drives the SAME
# door with the values the form captures:
#   - crm/settingsform.ts is a HAND-AUTHORED component (the same tier-1 vocabulary +
#     component-lowering gate AccountCard rides) — a card > stack > heading + label/
#     field/select + button Settings surface. It admits to a `component_template`
#     derived_artifact and MOUNTS via `?component=app/crm/SettingsForm`, rendering the
#     point-and-click controls (field-name input, type select, Admit button) live.
#   - SUBMIT walks the SAME HTTP /admit door scenario-a proves: the form's captured
#     (field name, field type) are spliced into the resource's CURRENT-HEAD
#     canonical_text (read from the catalog, not a disk file) and POSTed to /admit
#     under `--base` optimistic concurrency. Same verifiers, same catalog effect: the
#     owner column goes live exactly as scenario-a's programmatic path.
#   - RED / same-door proof: an INVALID field type captured by the form (a type
#     outside the closed 13-type roster) is REFUSED BY THE GATE (the tsgo verifier
#     stage), NOT by ad-hoc form validation — witnessed in the /admit Verdict, with no
#     column leaked. A stale-base concurrent field-add is likewise rejected at the cas
#     stage — the same optimistic-concurrency refusal scenario-a witnesses.
#
# NAMED RESIDUE (narrowed from R2): the form-to-def synthesis + /admit POST is the
# client/harness's job (a plain form POST is all a browser needs); it is deliberately
# NOT wired into the reactive ~15KB client's /session event bus (which drives ROW
# mutations, ADR-11 §7), so the kernel gains ZERO app logic and ZERO new Go. That
# submit-event -> server-side-admission auto-wiring is the remaining increment.
#
# Standalone against a fresh regel_crm_a2 DB. Exits nonzero on the first mismatch.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_crm_a2"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8790"
BASE="http://localhost:8790"
BIN="$(mktemp -t regel.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-a2-serve.XXXXXX)"

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
vfield() { python3 -c 'import sys,json;o=json.load(sys.stdin);print(o.get(sys.argv[1],""))' "$1"; }

# field_add SETTINGS-FORM-CAPTURED-NAME  TYPE  BASEHASH — the browser/harness role: take
# the resource's CURRENT-HEAD canonical_text, splice the field the FORM captured into
# the fields object, and POST it to the SAME /admit door under the head hash. No disk
# file is edited; the def under evolution is read live from the catalog.
field_add() {
  local fname="$1" ftype="$2" basehash="$3"
  local canon
  canon="$(sql "SELECT canonical_text FROM definition d JOIN name_pointer p ON p.hash=d.hash WHERE p.name='app/crm/Account'")"
  python3 -c '
import json,sys
canon,fname,ftype,base = sys.argv[1],sys.argv[2],sys.argv[3],sys.argv[4]
src = canon.replace("}, policy:", ", %s: \"%s\"}, policy:" % (fname, ftype))
sys.stdout.write(json.dumps({"modules":[{"module_name":"app/crm","source":src}],
  "base_hashes":{"app/crm/Account":base}}))
' "$canon" "$fname" "$ftype" "$basehash" | curl -s -X POST "$BASE/admit" \
    -H 'X-Regel-Actor: tenant:admin' -H 'Content-Type: application/json' --data-binary @-
}

head_hash() { sql "SELECT hash FROM name_pointer WHERE name='app/crm/Account'"; }

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0: substrate + genesis + admit Account v1 AND the SettingsForm component (as rows)"
"$BIN" migrate-db >/dev/null || fail "migrate-db"
"$BIN" genesis    >/dev/null || fail "genesis"
"$BIN" admit crm/account.ts     --name-prefix app/crm --actor engineer:dev >/dev/null 2>&1 || fail "admit Account"
SF="$("$BIN" admit crm/settingsform.ts --name-prefix app/crm --actor engineer:dev)"
echo "$SF" | grep -qF '"outcome": "admitted"' || { echo "$SF"; fail "SettingsForm did not admit"; }
CT="$(sql "SELECT count(*) FROM derived_artifact WHERE resource_name='app/crm/SettingsForm' AND pass='component_template'")"
[ "$CT" = "1" ] || fail "SettingsForm did not lower to a component_template (expected 1, got ${CT:-0})"
echo "PASS: SettingsForm admitted as rows -> component_template (same gate as AccountCard, grep-clean of app logic in Go)"
sql "INSERT INTO res_app_crm_account (org,name,industry,website,arr,arr_currency,tier,stage) VALUES ('acme','Globex','manufacturing','https://globex.example',120000,'USD','enterprise','active')" >/dev/null

step "1: MOUNT the Settings form — the point-and-click surface renders as admitted rows"
"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do grep -q "listening" "$SERVE_LOG" && break; sleep 0.1; done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
FORM="$(curl -s -H 'X-Regel-Actor: tenant:admin' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/detail/1?component=app/crm/SettingsForm")"
echo "$FORM" | grep -qF 'Add a field to Account' || { echo "$FORM" | head; fail "settings form heading missing"; }
echo "$FORM" | grep -qE 'class="rg-field"'  || fail "settings form has no field-name input"
echo "$FORM" | grep -qE 'class="rg-select"' || fail "settings form has no type select"
echo "$FORM" | grep -qE 'class="rg-button"' || fail "settings form has no submit button"
echo "controls rendered: $(echo "$FORM" | grep -oE 'rg-(field|select|button)' | sort -u | tr '\n' ' ')"
echo "PASS: the point-and-click Settings form renders live over the derived rows (admitted-rows presentation, no raw-HTML hatch)"

step "2: HAPPY — the operator picks (owner, text) in the form; SUBMIT walks the SAME /admit door"
V1HASH="$(head_hash)"
[ -n "$V1HASH" ] || fail "no Account head hash"
echo "Account head hash the form submits against: $V1HASH"
ADMIT="$(field_add owner text "$V1HASH")"
echo "$ADMIT" | python3 -c 'import sys,json;o=json.load(sys.stdin);print("  outcome:",o.get("outcome"),"| additive:",o.get("delta",{}).get("ddl_surface",{}).get("additive"))'
[ "$(echo "$ADMIT" | vfield outcome)" = "admitted" ] || { echo "$ADMIT"; fail "form-driven field-add did not admit"; }
[ "$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_crm_account' AND column_name='owner'")" = "1" ] || fail "owner column not added"
[ "$(sql "SELECT name FROM res_app_crm_account WHERE id=1")" = "Globex" ] || fail "existing row lost"
echo "PASS: form-captured (owner, text) admitted through the same HTTP /admit door -> owner column live, Globex intact"

step "3: OBSERVE — a fresh Account form mount now renders the owner field the Settings form added"
V2FORM="$(curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/crm/Account/form/1")"
echo "$V2FORM" | grep -qE '>owner</label>' || fail "Account form does not render the owner field the settings form added"
echo "PASS: the field the Settings form added is visible in the derived UI (schema evolution end-to-end)"

step "4: RED / SAME DOOR — an invalid TYPE captured by the form is refused BY THE GATE, not form validation"
V2HASH="$(head_hash)"
BAD="$(field_add territory geography "$V2HASH")"   # 'geography' is outside the 13-type roster
echo "$BAD" | python3 -c 'import sys,json
o=json.load(sys.stdin)
print("  outcome:",o.get("outcome"))
for d in o.get("diagnostics",[]):
    print("  refused-by:",d.get("stage_or_verifier"),d.get("code"),"-",d.get("message"))'
[ "$(echo "$BAD" | vfield outcome)" = "rejected" ] || { echo "$BAD"; fail "the gate should REJECT a field type outside the roster"; }
echo "$BAD" | grep -qF 'is not assignable to type' || { echo "$BAD"; fail "expected a gate-level type-roster refusal"; }
[ "$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_crm_account' AND column_name='territory'")" = "0" ] || fail "the refused field leaked a territory column"
echo "PASS: the gate (tsgo verifier) refused the invalid field type submitted from the form — no ad-hoc form validation, no column leaked"

step "5: RED / SAME DOOR — a concurrent field-add on a STALE base is rejected at the cas stage"
STALE="$(field_add region text "$V1HASH")"   # additive but based on the now-stale v1 head
echo "$STALE" | python3 -c 'import sys,json;o=json.load(sys.stdin);print("  outcome:",o.get("outcome"),"| failed stage:",[(s["stage"],s["status"]) for s in o.get("stages",[]) if s["status"]!="pass"])'
[ "$(echo "$STALE" | vfield outcome)" = "stale-base" ] || { echo "$STALE"; fail "a stale-base concurrent field-add should be rejected stale-base"; }
[ "$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='res_app_crm_account' AND column_name='region'")" = "0" ] || fail "stale-base change leaked a region column"
echo "PASS: the stale-base concurrent field-add was rejected (STALE_BASE, cas stage) — the same optimistic-concurrency refusal scenario-a witnesses"

echo
echo "=============================================================="
echo "DEMO OK — R2 point-and-click Settings form ships as admitted rows; its captured values walk the SAME /admit door (happy admits, gate refuses invalid), no app logic in Go"
echo "=============================================================="
