#!/usr/bin/env bash
# demo-taak.sh — THE D4 acceptance demo (ADR-10 §6 std/taak + ADR-05 §5/§7):
#   a std/taak workflow (sleep + an external log.write effect per step + a final
#   receive) is admitted through the gate, started on kernel A, kill -9'd
#   mid-flight, and resumed on a FRESH kernel B on the same DB. The workflow
#   completes with the IDENTICAL result, an exactly-once outbox (UNIQUE dedup
#   key), and every external intent DELIVERED effectively-once by the outbox
#   dispatcher (delivered_at stamped exactly once). A post-restart channel send
#   proves the receive wake survives the kill.
#
# Re-runnable against a fresh `regel_taak_demo` DB. Exits nonzero on the first
# mismatch; prints DEMO OK.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_taak_demo"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR_A=":8795"
ADDR_B=":8796"
BASE_A="http://localhost:8795"
BASE_B="http://localhost:8796"
BIN="$(mktemp -t regel.XXXXXX)"
LOG_A="$(mktemp -t regel-taakA.XXXXXX)"
LOG_B="$(mktemp -t regel-taakB.XXXXXX)"

KERNEL_PID=""
cleanup() {
  if [ -n "$KERNEL_PID" ]; then
    kill -9 "$KERNEL_PID" 2>/dev/null
    wait "$KERNEL_PID" 2>/dev/null
  fi
  pkill -9 -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$LOG_A" "$LOG_B"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }

start_kernel() {
  local addr="$1" log="$2"
  "$BIN" serve -addr "$addr" -lease 2 -poll 100ms >"$log" 2>&1 &
  KERNEL_PID=$!
  for i in $(seq 1 60); do
    grep -q "listening" "$log" && return 0
    kill -0 "$KERNEL_PID" 2>/dev/null || { cat "$log"; fail "kernel exited during startup"; }
    sleep 0.1
  done
  cat "$log"; fail "kernel did not start"
}

start_workflow() {
  local out cid
  out="$(curl -s -X POST "$1/workflow/app/taak/w" -H 'X-Regel-Actor: operator:op' -d '[]')"
  cid="$(echo "$out" | sed -n 's/.*"continuation_id": *"\([^"]*\)".*/\1/p')"
  [ -n "$cid" ] || fail "workflow did not start: $out"
  echo "$cid"
}

wait_done() {
  local cid="$1" secs="$2" st
  for i in $(seq 1 $((secs * 10))); do
    st="$(sql "SELECT status FROM continuation WHERE id='${cid}'")"
    [ "$st" = "done" ] && return 0
    [ "$st" = "failed" ] && fail "continuation $cid failed"
    sleep 0.1
  done
  fail "continuation $cid not done after ${secs}s (status=$(sql "SELECT status FROM continuation WHERE id='${cid}'"))"
}

result_of() { curl -s "$1/continuation/$2" | sed -n 's/.*"result": *\([0-9.]*\).*/\1/p'; }

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0a: migrate-db"; "$BIN" migrate-db || fail "migrate-db"
step "0b: genesis";    "$BIN" genesis    || fail "genesis"

step "0c: admit examples/taak-kill.ts (std/taak sleep+receive + std/log.write)"
V="$("$BIN" admit examples/taak-kill.ts --name-prefix app/taak --actor engineer:dev)" || fail "admit taak"
echo "$V" | grep -qF '"outcome": "admitted"' || fail "taak workflow not admitted: $V"
echo "admitted: app/taak/w"

step "1: start on kernel A, then kill -9 MID-FLIGHT"
start_kernel "$ADDR_A" "$LOG_A"
echo "kernel A up (pid $KERNEL_PID, lease 2s): $(head -1 "$LOG_A")"
CID="$(start_workflow "$BASE_A")"
echo "taak workflow: $CID"
KILL_MOMENT=""
for i in $(seq 1 300); do
  OUTN="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${CID}'")"
  RUNN="$(sql "SELECT count(*) FROM task WHERE status='running'")"
  ST="$(sql "SELECT status FROM continuation WHERE id='${CID}'")"
  if [ "${OUTN:-0}" -ge 1 ] && [ "${RUNN:-0}" -ge 1 ]; then
    KILL_MOMENT="outbox=$OUTN running_tasks=$RUNN status=$ST"
    break
  fi
  [ "$ST" = "done" ] && fail "workflow finished before a kill window opened"
  sleep 0.02
done
[ -n "$KILL_MOMENT" ] || fail "no kill window observed"
echo "kill moment: [$KILL_MOMENT]"
echo ">>> kill -9 $KERNEL_PID (kernel A — no graceful shutdown)"
kill -9 "$KERNEL_PID"; wait "$KERNEL_PID" 2>/dev/null; KERNEL_PID=""

step "2: kernel B (fresh process, same DB) resumes; send the receive bonus"
start_kernel "$ADDR_B" "$LOG_B"
echo "kernel B up (pid $KERNEL_PID): $(head -1 "$LOG_B")"
# Wait until it drains the loop and parks on receive("taakdone"), then send 42.
for i in $(seq 1 300); do
  KIND="$(sql "SELECT wake->>'kind' FROM continuation WHERE id='${CID}'")"
  ST="$(sql "SELECT status FROM continuation WHERE id='${CID}'")"
  { [ "$ST" = "sleeping" ] && [ "$KIND" = "message" ]; } && break
  [ "$ST" = "done" ] && break
  sleep 0.05
done
echo ">>> POST /channel/taakdone/send value=42 (resolves the receive wake)"
curl -s -X POST "$BASE_B/channel/taakdone/send" -H 'X-Regel-Actor: operator:op' -d '{"value":42}' >/dev/null
wait_done "$CID" 60

step "3: assertions — identical result, exactly-once outbox, effectively-once delivery"
RESULT="$(result_of "$BASE_B" "$CID")"
OUTBOX="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${CID}'")"
DELIVERED="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${CID}' AND delivered_at IS NOT NULL")"
DUPES="$(sql "SELECT count(*) FROM (SELECT continuation_id, step_seq, ordinal FROM outbox GROUP BY 1,2,3 HAVING count(*)>1) d")"
REOFFERS="$(curl -s "$BASE_B/healthz" | sed -n 's/.*"reoffers": *\([0-9]*\).*/\1/p')"
DEADDELIVER="$(sql "SELECT count(*) FROM task WHERE kind='deliver' AND status='dead'")"
echo "result=$RESULT outbox=$OUTBOX delivered=$DELIVERED dupes=$DUPES reoffers=$REOFFERS"

[ "$RESULT" = "10042" ] || fail "result $RESULT != 10042 (sleep+receive did not survive the kill)"
[ "$OUTBOX" = "4" ]     || fail "outbox $OUTBOX != 4 (exactly-once violated)"
[ "$DELIVERED" = "4" ]  || fail "delivered $DELIVERED != 4 (dispatcher must deliver every intent once)"
[ "$DUPES" = "0" ]      || fail "$DUPES duplicate outbox dedup keys"
[ "$DEADDELIVER" = "0" ] || fail "$DEADDELIVER dead deliver tasks"
{ [ -n "$REOFFERS" ] && [ "$REOFFERS" -ge 1 ]; } || fail "reoffers=$REOFFERS, expected >=1"

echo "PASS: result identical across kill (10042), receive wake survived"
echo "PASS: outbox exactly 4, zero duplicate dedup keys (effect exactly-once)"
echo "PASS: all 4 external intents delivered effectively-once by the dispatcher"
echo "PASS: kernel B re-offered the dead kernel's work (reoffers=$REOFFERS)"

echo
echo "=============================================================="
echo "DEMO OK — std/taak kill -9 mid-step: sleep+receive resume, effect once, delivered once"
echo "=============================================================="
