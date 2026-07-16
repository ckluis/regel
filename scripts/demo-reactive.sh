#!/usr/bin/env bash
# demo-reactive.sh — THE ADR-11 acceptance demo: the reactive session layer live
# over a REAL `regel serve` process (not httptest). Admits a resource, mounts a
# derived table view (skeleton + data-slot markup + a session id), opens the raw
# SSE wire (GET /session/{id}/events) in a background curl piping into a transcript
# file, drives a real edit through POST /session/{id}/event (input draft, then
# submit — the server-authoritative, rowVersion-guarded mutation path, ADR-11 §5/
# §7), and shows the patch frame(s) the mutation pushes onto the SSE stream. It
# then POSTs /session/{id}/resync and shows the fresh full-repaint payload.
#
# Step 9 exercises cross-session invalidation fan-out: `regel serve` runs the
# reactive-layer loops (srv.StartSessions: invalidation LISTEN + idle sweeper) in
# the serving kernel, so a mutation committed through one session's step patches
# every other subscribed session's SSE stream. (This wiring gap was found by an
# earlier revision of this script and fixed in cmd/regel/main.go cmdServe.)
#
# Re-runnable against a fresh `regel_reactive_demo` DB. Exits nonzero on the first
# mismatch; prints DEMO OK.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_reactive_demo"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8798"
BASE="http://localhost:8798"
BIN="$(mktemp -t regel.XXXXXX)"
RESFILE="$(mktemp -t regel-widget.XXXXXX.ts)"
SERVE_LOG="$(mktemp -t regel-reactive-serve.XXXXXX)"
SSE_LOG="$(mktemp -t regel-reactive-sse.XXXXXX)"
SSE2_LOG="$(mktemp -t regel-reactive-sse2.XXXXXX)"

KERNEL_PID=""
SSE_PID=""
SSE2_PID=""
cleanup() {
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null
  [ -n "$SSE2_PID" ] && kill "$SSE2_PID" 2>/dev/null
  if [ -n "$KERNEL_PID" ]; then
    kill "$KERNEL_PID" 2>/dev/null
    wait "$KERNEL_PID" 2>/dev/null
  fi
  pkill -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$RESFILE" "$SERVE_LOG" "$SSE_LOG" "$SSE2_LOG"
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

step "0c: admit app/rx/Widget (org, name, score, pii:email — the D3 session-test fixture)"
cat >"$RESFILE" <<'TS'
import { resource } from "std/resource";
import { orgScoped } from "std/policy";
export const Widget = resource({
  fields: { org: "text", name: "text", score: "number", email: "pii:email" },
  policy: orgScoped,
});
TS
V="$("$BIN" admit "$RESFILE" --name-prefix app/rx --actor engineer:dev)" || fail "admit widget"
echo "$V" | grep -qF '"outcome": "admitted"' || fail "Widget not admitted: $V"
echo "admitted: app/rx/Widget"

step "0d: seed two rows"
sql "INSERT INTO res_app_rx_widget (org, name, score) VALUES ('acme','alpha',1)" >/dev/null
sql "INSERT INTO res_app_rx_widget (org, name, score) VALUES ('acme','beta',2)" >/dev/null
echo "seeded: alpha(org=acme,score=1), beta(org=acme,score=2)"

step "1: start regel serve as a script-local child"
"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do
  grep -q "listening" "$SERVE_LOG" && break
  kill -0 "$KERNEL_PID" 2>/dev/null || { cat "$SERVE_LOG"; fail "kernel exited during startup"; }
  sleep 0.1
done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
echo "kernel up (pid $KERNEL_PID): $(head -1 "$SERVE_LOG")"

step "2: GET /ui/app/rx/Widget/table (mount) — skeleton + data-slot + session id"
HDR_TABLE="$(mktemp -t regel-hdr-table.XXXXXX)"
BODY_TABLE="$(curl -s -D "$HDR_TABLE" -H 'X-Regel-Actor: human:a' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/rx/Widget/table")"
TABLE_SID="$(grep -i '^X-Regel-Session:' "$HDR_TABLE" | awk '{print $2}' | tr -d '\r')"
rm -f "$HDR_TABLE"
[ -n "$TABLE_SID" ] || fail "no X-Regel-Session header on table mount"
echo "table mount session id: $TABLE_SID"
echo "$BODY_TABLE" | grep -qF 'data-slot=' || fail "table mount missing data-slot markup"
echo "$BODY_TABLE" | grep -qF 'alpha' || fail "table mount missing seeded row 'alpha'"
echo "$BODY_TABLE" | grep -qF 'beta' || fail "table mount missing seeded row 'beta'"
echo "table first paint (skeleton + data-slot rows):"
echo "$BODY_TABLE"

step "3: GET /ui/app/rx/Widget/form/1 (mount the edit target) — a second session"
HDR_FORM="$(mktemp -t regel-hdr-form.XXXXXX)"
BODY_FORM="$(curl -s -D "$HDR_FORM" -H 'X-Regel-Actor: human:e' -H 'X-Regel-Horizon: acme' "$BASE/ui/app/rx/Widget/form/1")"
FORM_SID="$(grep -i '^X-Regel-Session:' "$HDR_FORM" | awk '{print $2}' | tr -d '\r')"
rm -f "$HDR_FORM"
[ -n "$FORM_SID" ] || fail "no X-Regel-Session header on form mount"
echo "form mount session id: $FORM_SID"
echo "$BODY_FORM" | grep -qF 'data-slot=' || fail "form mount missing data-slot markup"
echo "form first paint:"
echo "$BODY_FORM"
# The 'name' field's input slot id (form.<index>, fields sorted alphabetically:
# email, name, org, score ⇒ name is index 1) — read straight off the rendered HTML
# rather than hardcoded, so a field-order change would fail loudly here, not silently.
NAME_SLOT="$(echo "$BODY_FORM" | grep -oE '<label class="rg-label">name</label><input[^>]*data-slot="[^"]+"' | grep -oE 'data-slot="[^"]+"' | sed -E 's/data-slot="([^"]+)"/\1/')"
[ -n "$NAME_SLOT" ] || fail "could not locate the 'name' field's form slot id in the mount HTML"
echo "'name' field form slot id: $NAME_SLOT"

step "4: open the SSE stream on the form session (background curl -N, tee'd to a file)"
curl -s -N "$BASE/session/${FORM_SID}/events" >"$SSE_LOG" 2>&1 &
SSE_PID=$!
sleep 0.4
kill -0 "$SSE_PID" 2>/dev/null || fail "SSE curl exited immediately"
echo "SSE stream open on session $FORM_SID (pid $SSE_PID) -> $SSE_LOG"

step "5: POST /session/{id}/event — an input draft (event body: slotId/event/value/eventSeq)"
R1="$(curl -s -X POST "$BASE/session/${FORM_SID}/event" -H 'Content-Type: application/json' \
  -d "{\"slotId\":\"${NAME_SLOT}\",\"event\":\"input\",\"value\":\"ALPHA-LIVE\",\"eventSeq\":0}")"
echo "input response: $R1"
echo "$R1" | grep -qF '"applied":true' || fail "input event was not applied: $R1"

step "6: POST /session/{id}/event — submit (the rowVersion-guarded write + NOTIFY regel_invalidate)"
R2="$(curl -s -X POST "$BASE/session/${FORM_SID}/event" -H 'Content-Type: application/json' \
  -d '{"slotId":"","event":"submit","value":"","eventSeq":1}')"
echo "submit response: $R2"
echo "$R2" | grep -qF '"applied":true' || fail "submit event was not applied: $R2"
DBVAL="$(sql "SELECT name FROM res_app_rx_widget WHERE id=1")"
[ "$DBVAL" = "ALPHA-LIVE" ] || fail "row 1 name = '$DBVAL', want 'ALPHA-LIVE' (write did not commit)"
echo "PASS: res_app_rx_widget row 1 name committed to 'ALPHA-LIVE'"

step "7: the captured SSE frame lines (id: + data:) — decode + show the value arrived live"
sleep 0.5
kill "$SSE_PID" 2>/dev/null; wait "$SSE_PID" 2>/dev/null; SSE_PID=""
echo "raw SSE transcript ($SSE_LOG):"
cat "$SSE_LOG"
grep -q '^id: ' "$SSE_LOG" || fail "no 'id:' lines captured on the SSE stream"
grep -q '^data: ' "$SSE_LOG" || fail "no 'data:' lines captured on the SSE stream"
FOUND_LIVE=0
while IFS= read -r line; do
  case "$line" in
    data:\ *)
      b64="${line#data: }"
      if echo "$b64" | base64 -d 2>/dev/null | strings | grep -qF "ALPHA-LIVE"; then
        FOUND_LIVE=1
      fi
      ;;
  esac
done <"$SSE_LOG"
[ "$FOUND_LIVE" = "1" ] || fail "no SSE frame decoded to contain the mutated value 'ALPHA-LIVE'"
echo "PASS: a post-mutation SSE frame decodes (base64 -> strings) to contain 'ALPHA-LIVE'"

step "8: POST /session/{id}/resync — a fresh full-repaint frame"
RESYNC="$(curl -s -X POST "$BASE/session/${FORM_SID}/resync")"
echo "resync response: $RESYNC"
echo "$RESYNC" | grep -qF '"eventSeq"' || fail "resync response missing eventSeq"
echo "$RESYNC" | grep -qF 'ALPHA-LIVE' || fail "resync snapshot does not reflect the committed value"
echo "PASS: resync returned a fresh snapshot carrying the committed value"

step "9: cross-session invalidation fan-out to the table viewer"
# The serving kernel runs the invalidation LISTEN loop (StartSessions in cmdServe),
# so the SEPARATE table-viewer session (mounted in step 2) must receive a live
# patch when this form session commits a mutation.
curl -s -N "$BASE/session/${TABLE_SID}/events" >"$SSE2_LOG" 2>&1 &
SSE2_PID=$!
sleep 0.4
curl -s -X POST "$BASE/session/${FORM_SID}/event" -H 'Content-Type: application/json' \
  -d "{\"slotId\":\"${NAME_SLOT}\",\"event\":\"input\",\"value\":\"BETA-CROSS\",\"eventSeq\":2}" >/dev/null
curl -s -X POST "$BASE/session/${FORM_SID}/event" -H 'Content-Type: application/json' \
  -d '{"slotId":"","event":"submit","value":"","eventSeq":3}' >/dev/null
sleep 2
kill "$SSE2_PID" 2>/dev/null; wait "$SSE2_PID" 2>/dev/null; SSE2_PID=""
if [ -s "$SSE2_LOG" ] && grep -q '^id: ' "$SSE2_LOG"; then
  echo "PASS: table session received a live cross-session patch:"
  cat "$SSE2_LOG"
else
  fail "table viewer session received no cross-session patch (invalidation LISTEN loop not running?)"
fi

step "10: clean shutdown"
kill "$KERNEL_PID" 2>/dev/null
wait "$KERNEL_PID" 2>/dev/null
KERNEL_PID=""
echo "PASS: kernel process terminated cleanly"

echo
echo "=============================================================="
echo "DEMO OK — reactive layer live: mount, SSE, event-driven mutation, live patch frame, resync"
echo "=============================================================="
