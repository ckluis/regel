#!/usr/bin/env bash
# demo-kill9-resume.sh — THE Stage-B acceptance demo (ADR-05 Red-Path Test 1):
#   leg 1: run a 4-step aggregating workflow to completion on kernel A (reference
#          result + ordered outbox trace);
#   leg 2: start a fresh workflow, kill -9 the kernel mid-flight, restart a NEW
#          kernel process on the same DB, and assert the workflow completes with
#          the IDENTICAL result and an exactly-once outbox (no double effect, no
#          missing effect — the UNIQUE dedup key + txn atomicity under test).
#
# Runs against a fresh `regel_kill9_demo` database (dropped + recreated), so it
# is re-runnable. Kernel lease is 2s so the reaper re-offers the dead kernel's
# work within seconds. Exits nonzero on the first mismatch; prints DEMO OK.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_kill9_demo"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR_A=":8793"
ADDR_B=":8794"
BASE_A="http://localhost:8793"
BASE_B="http://localhost:8794"
BIN="$(mktemp -t regel.XXXXXX)"
LOG_A="$(mktemp -t regel-kernelA.XXXXXX)"
LOG_B="$(mktemp -t regel-kernelB.XXXXXX)"

KERNEL_PID=""
cleanup() {
  if [ -n "$KERNEL_PID" ]; then
    kill -9 "$KERNEL_PID" 2>/dev/null
    wait "$KERNEL_PID" 2>/dev/null
  fi
  # Belt and braces: reap any serve process still running from THIS binary path.
  pkill -9 -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$LOG_A" "$LOG_B"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }

step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }

sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }

# start_kernel ADDR LOGFILE — spawns `regel serve` (lease 2s, poll 100ms) and
# waits for readiness; sets KERNEL_PID.
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

# start_workflow BASEURL — POSTs the workflow, echoes the continuation id.
start_workflow() {
  local out cid
  out="$(curl -s -X POST "$1/workflow/app/kill9/w" -H 'X-Regel-Actor: operator:op' -d '[]')"
  cid="$(echo "$out" | sed -n 's/.*"continuation_id": *"\([^"]*\)".*/\1/p')"
  [ -n "$cid" ] || fail "workflow did not start: $out"
  echo "$cid"
}

# wait_done CID SECONDS — polls the DB until the continuation is done.
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

# result_of CID — the decoded terminal result via the kernel HTTP door.
result_of() {
  curl -s "$1/continuation/$2" | sed -n 's/.*"result": *\([0-9.]*\).*/\1/p'
}

# trace_of CID — the ordered (class,step_seq,ordinal) outbox fingerprint.
trace_of() {
  sql "SELECT string_agg(class||'@'||step_seq||'.'||ordinal, ';' ORDER BY step_seq, ordinal) FROM outbox WHERE continuation_id='$1'"
}

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

step "0c: admit examples/kill9.ts (4 steps: compute + effect + sleep each)"
V="$("$BIN" admit examples/kill9.ts --name-prefix app/kill9 --actor engineer:dev)" || fail "admit kill9"
echo "$V" | grep -qF '"outcome": "admitted"' || fail "kill9 not admitted: $V"
echo "admitted: app/kill9/w"

step "1: LEG 1 (reference) — kernel A runs the workflow to completion, uninterrupted"
start_kernel "$ADDR_A" "$LOG_A"
echo "kernel A up (pid $KERNEL_PID, lease 2s): $(head -1 "$LOG_A")"
REF_CID="$(start_workflow "$BASE_A")"
echo "reference workflow: $REF_CID"
wait_done "$REF_CID" 60
REF_RESULT="$(result_of "$BASE_A" "$REF_CID")"
REF_TRACE="$(trace_of "$REF_CID")"
REF_OUTBOX="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${REF_CID}'")"
echo "reference result: $REF_RESULT"
echo "reference outbox trace ($REF_OUTBOX rows): $REF_TRACE"
[ "$REF_RESULT" = "10000" ] || fail "reference result $REF_RESULT != 10000"
[ "$REF_OUTBOX" = "4" ] || fail "reference outbox $REF_OUTBOX != 4"

step "2: LEG 2 — fresh workflow, then kill -9 the kernel MID-FLIGHT"
KILL_CID="$(start_workflow "$BASE_A")"
echo "kill-leg workflow: $KILL_CID"
# Poll until >=1 effect committed AND a step is in flight (a running task).
KILL_MOMENT=""
for i in $(seq 1 300); do
  OUTN="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${KILL_CID}'")"
  RUNN="$(sql "SELECT count(*) FROM task WHERE status='running'")"
  ST="$(sql "SELECT status FROM continuation WHERE id='${KILL_CID}'")"
  SEQ="$(sql "SELECT step_seq FROM continuation WHERE id='${KILL_CID}'")"
  if [ "${OUTN:-0}" -ge 1 ] && [ "${RUNN:-0}" -ge 1 ]; then
    KILL_MOMENT="outbox=$OUTN running_tasks=$RUNN status=$ST step_seq=$SEQ"
    break
  fi
  [ "$ST" = "done" ] && fail "workflow finished before a kill window opened"
  sleep 0.02
done
[ -n "$KILL_MOMENT" ] || fail "no kill window observed"
echo "kill moment: [$KILL_MOMENT]"
echo ">>> kill -9 $KERNEL_PID (kernel A — no graceful shutdown, no lease release)"
kill -9 "$KERNEL_PID" || fail "kill -9"
wait "$KERNEL_PID" 2>/dev/null
KERNEL_PID=""
ST_AFTER="$(sql "SELECT status FROM continuation WHERE id='${KILL_CID}'")"
SEQ_AFTER="$(sql "SELECT step_seq FROM continuation WHERE id='${KILL_CID}'")"
echo "stranded: continuation status=$ST_AFTER step_seq=$SEQ_AFTER (running task lease expires in <=2s)"
[ "$ST_AFTER" != "done" ] || fail "workflow was already done at the kill"

step "3: restart — kernel B (NEW process, same DB) reaps the lease and resumes"
start_kernel "$ADDR_B" "$LOG_B"
echo "kernel B up (pid $KERNEL_PID): $(head -1 "$LOG_B")"
wait_done "$KILL_CID" 60
KILL_RESULT="$(result_of "$BASE_B" "$KILL_CID")"
KILL_TRACE="$(trace_of "$KILL_CID")"
KILL_OUTBOX="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${KILL_CID}'")"
REOFFERS="$(curl -s "$BASE_B/healthz" | sed -n 's/.*"reoffers": *\([0-9]*\).*/\1/p')"
echo "kill-leg result: $KILL_RESULT"
echo "kill-leg outbox trace ($KILL_OUTBOX rows): $KILL_TRACE"
echo "kernel B healthz reoffers: $REOFFERS"

step "4: assertions — identical result, exactly-once outbox"
if [ "$KILL_RESULT" = "$REF_RESULT" ]; then
  echo "PASS: result identical across the kill ($KILL_RESULT == $REF_RESULT)"
else
  fail "result mismatch: kill-leg $KILL_RESULT != reference $REF_RESULT"
fi
if [ "$KILL_OUTBOX" = "4" ]; then
  echo "PASS: outbox exactly 4 rows (no double effect, no missing effect)"
else
  fail "outbox rows $KILL_OUTBOX != 4 (exactly-once violated)"
fi
if [ "$KILL_TRACE" = "$REF_TRACE" ]; then
  echo "PASS: ordered effect trace identical ($KILL_TRACE)"
else
  fail "trace mismatch: $KILL_TRACE != $REF_TRACE"
fi
DUPES="$(sql "SELECT count(*) FROM (SELECT continuation_id, step_seq, ordinal FROM outbox GROUP BY 1,2,3 HAVING count(*)>1) d")"
if [ "$DUPES" = "0" ]; then
  echo "PASS: zero duplicate dedup keys in the outbox"
else
  fail "$DUPES duplicate outbox keys"
fi
if [ -n "$REOFFERS" ] && [ "$REOFFERS" -ge 1 ]; then
  echo "PASS: kernel B re-offered the dead kernel's work (reoffers=$REOFFERS)"
else
  fail "kernel B reoffers=$REOFFERS, expected >=1"
fi

echo
echo "=============================================================="
echo "DEMO OK — kill -9 mid-step, cross-kernel resume, effect exactly once"
echo "=============================================================="
