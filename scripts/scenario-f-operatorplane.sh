#!/usr/bin/env bash
# scenario-f-operatorplane.sh — STAGE-F R4: the operatorPlane promoted to v1.1 over
# a REAL `regel serve` process (not httptest). It proves the three v1.1 additions
# ride existing machinery, no new authority:
#
#   (1) SSE live updates   — GET /ui/operatorPlane now returns a REAL reactive
#       session (X-Regel-Session), and its SSE stream receives a live splice frame
#       when a condition resolves, over the ADR-11 §6 invalidation channel.
#   (2) approval-delta      — a third panel projecting pending→approved/refused
#       transitions from the durable_condition resolution rows.
#   (3) write actions       — the rendered restart button walks the EXISTING restart
#       door (POST /continuation/{id}/restart). RED first: a stale-hash write is
#       refused CONDITION_MOVED and an unknown restart NOT_FOUND — by the DOOR, not
#       UI logic — and the condition stays open. GREEN: the operator restart resolves.
#
# Re-runnable against a fresh regel_operator_r4 DB. Exits nonzero on the first
# mismatch; prints DEMO OK.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_operator_r4"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8799"
BASE="http://localhost:8799"
BIN="$(mktemp -t regel.XXXXXX)"
SIGFILE="$(mktemp -t regel-r4sig.XXXXXX.ts)"
SERVE_LOG="$(mktemp -t regel-r4-serve.XXXXXX)"
SSE_LOG="$(mktemp -t regel-r4-sse.XXXXXX)"

KERNEL_PID=""
SSE_PID=""
cleanup() {
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null
  if [ -n "$KERNEL_PID" ]; then kill "$KERNEL_PID" 2>/dev/null; wait "$KERNEL_PID" 2>/dev/null; fi
  pkill -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$SIGFILE" "$SERVE_LOG" "$SSE_LOG"
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

step "0a: migrate-db"; "$BIN" migrate-db >/dev/null || fail "migrate-db"
step "0b: genesis";    "$BIN" genesis    >/dev/null || fail "genesis"

step "0c: admit a taak.signal workflow (restarts: approve[cap:operator], abort)"
cat >"$SIGFILE" <<'TS'
import { signal } from "std/taak";
export function approve(): string {
  const r = signal("app.approval",
    [{ name: "approve", label: "Approve", capability: "operator" }, { name: "abort", label: "Abort" }]);
  return "resolved:" + r.restart;
}
TS
V="$("$BIN" admit "$SIGFILE" --name-prefix app/r4 --actor engineer:dev)" || fail "admit signal"
echo "$V" | grep -qF '"outcome": "admitted"' || fail "signal workflow not admitted: $V"
echo "admitted: app/r4/approve"

step "1: start regel serve"
"$BIN" serve -addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 60); do
  grep -q "listening" "$SERVE_LOG" && break
  kill -0 "$KERNEL_PID" 2>/dev/null || { cat "$SERVE_LOG"; fail "kernel exited during startup"; }
  sleep 0.1
done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "kernel did not start"; }
echo "kernel up (pid $KERNEL_PID)"

step "2: start the workflow — it parks on an open durable_condition"
OUT="$(curl -s -X POST "$BASE/workflow/app/r4/approve" -H 'X-Regel-Actor: operator:op' -d '[]')"
CID="$(echo "$OUT" | sed -n 's/.*"continuation_id": *"\([^"]*\)".*/\1/p')"
[ -n "$CID" ] || fail "workflow did not start: $OUT"
echo "continuation: $CID"
for i in $(seq 1 100); do
  ST="$(sql "SELECT status FROM continuation WHERE id='${CID}'")"
  [ "$ST" = "condition" ] && break
  sleep 0.05
done
[ "$ST" = "condition" ] || fail "workflow did not park on a condition (status=$ST)"
CONDID="$(sql "SELECT id FROM durable_condition WHERE continuation_id='${CID}' AND status='open'")"
[ -n "$CONDID" ] || fail "no open condition"
echo "open condition: $CONDID"

step "3: GET /ui/operatorPlane — a REAL reactive session (X-Regel-Session), 3 panels"
HDR="$(mktemp -t regel-r4-hdr.XXXXXX)"
BODY="$(curl -s -D "$HDR" -H 'X-Regel-Actor: human:op' "$BASE/ui/operatorPlane")"
SID="$(grep -i '^X-Regel-Session:' "$HDR" | awk '{print $2}' | tr -d '\r')"
rm -f "$HDR"
[ -n "$SID" ] || fail "operatorPlane v1.1 must be a reactive session (no X-Regel-Session header)"
echo "operator session: $SID"
for want in "condition inbox" "refusal ledger" "approval delta" "app.approval" "approve" "$CID"; do
  echo "$BODY" | grep -qF "$want" || fail "operatorPlane first paint missing '$want'"
done
echo "PASS: 3 panels render; the open condition + its restart button + continuation id are shown"

step "4: open the operator SSE stream (background curl -N)"
curl -s -N "$BASE/session/${SID}/events" >"$SSE_LOG" 2>&1 &
SSE_PID=$!
sleep 0.4
kill -0 "$SSE_PID" 2>/dev/null || fail "SSE curl exited immediately"
echo "SSE open on operator session $SID"

step "5: RED — the restart button walks the door; a STALE-hash write is refused (409 CONDITION_MOVED)"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/continuation/${CID}/restart" \
  -H 'Content-Type: application/json' \
  -d '{"restart":"approve","expected_hash":"0000000000000000000000000000000000000000000000000000000000000000"}')"
[ "$CODE" = "409" ] || fail "stale-hash restart: want 409, got $CODE"
BMOVED="$(curl -s -X POST "$BASE/continuation/${CID}/restart" -H 'Content-Type: application/json' \
  -d '{"restart":"approve","expected_hash":"deadbeef"}')"
echo "$BMOVED" | grep -qiF "moved" || fail "stale-hash restart body did not name 'moved': $BMOVED"
echo "refused (stale hash): $BMOVED"

step "5b: RED — an UNKNOWN restart is refused (404 NOT_FOUND) by the door"
CODE2="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/continuation/${CID}/restart" \
  -H 'Content-Type: application/json' -d '{"restart":"nope"}')"
[ "$CODE2" = "404" ] || fail "unknown restart: want 404, got $CODE2"
echo "refused (unknown restart): 404"
ST="$(sql "SELECT status FROM durable_condition WHERE id='${CONDID}'")"
[ "$ST" = "open" ] || fail "condition changed on a REFUSED write (status=$ST) — the door, not UI, must gate"
echo "PASS: both writes refused BY THE DOOR; condition still open (no state change)"

step "6: GREEN — the operator restart 'approve' resolves the condition through the door (200)"
CODE3="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/continuation/${CID}/restart" \
  -H 'Content-Type: application/json' -d '{"restart":"approve"}')"
[ "$CODE3" = "200" ] || fail "authorized restart: want 200, got $CODE3"
ST="$(sql "SELECT status FROM durable_condition WHERE id='${CONDID}'")"
[ "$ST" = "resolved" ] || fail "condition not resolved after approve (status=$ST)"
echo "PASS: condition resolved:approve through the restart door"

step "7: the LIVE SSE frame — the resolved condition splices out of the inbox / into the delta panel"
sleep 0.8
kill "$SSE_PID" 2>/dev/null; wait "$SSE_PID" 2>/dev/null; SSE_PID=""
echo "raw SSE transcript ($SSE_LOG):"; cat "$SSE_LOG"
grep -q '^id: ' "$SSE_LOG" || fail "no 'id:' lines captured on the operator SSE stream"
FOUND=0
while IFS= read -r line; do
  case "$line" in
    data:\ *)
      b64="${line#data: }"
      dec="$(echo "$b64" | base64 -d 2>/dev/null | strings)"
      if echo "$dec" | grep -qF "opdelta" && echo "$dec" | grep -qF "${CONDID%%-*}"; then FOUND=1; fi
      ;;
  esac
done <"$SSE_LOG"
[ "$FOUND" = "1" ] || fail "no SSE frame decoded to the live approval-delta splice for $CONDID"
echo "PASS: a post-resolution SSE frame decodes to the live operator splice (opdelta + condition id)"

step "8: a fresh mount shows the approve transition in the approval-delta panel"
BODY2="$(curl -s -H 'X-Regel-Actor: human:op' "$BASE/ui/operatorPlane")"
echo "$BODY2" | grep -qF "approval delta" || fail "approval-delta panel missing on re-mount"
echo "$BODY2" | grep -qF "approve" || fail "approval-delta panel does not render the approve transition"
echo "PASS: approval-delta panel renders the pending->approve transition"

echo
echo "=============================================================="
echo "DEMO OK — operatorPlane v1.1: reactive session, live SSE splice, approval delta, write-through-door (red then green)"
echo "=============================================================="
