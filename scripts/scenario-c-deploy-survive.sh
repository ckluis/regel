#!/usr/bin/env bash
# scenario-c-deploy-survive.sh — A MID-FLIGHT CRM WORKFLOW SURVIVING A DEPLOY.
#
# The real crm/followup workflow is parked mid-flight (a std/taak `sleep`
# checkpoint), then the world is DEPLOYED-AS-COMMIT twice — each deploy is a real
# `regel migrate N --commit` that bumps the epoch and flips the ADR-08 §2 fence
# row. The scenario proves, end to end, driving only the real CLI/HTTP doors:
#
#   1. deploy=commit: `migrate 2 --commit` flips the fence; the epoch-1 kernel
#      still serving trips the O5 fence (epoch.fence_tripped, ADR-08 §4a) on its
#      next work txn and FAILS CLOSED — no work commits under a mismatched pair;
#   2. `--wait-for-epoch` (B7): a staged epoch-3 binary refuses to SERVE, emits
#      the parked_waiting diagnostic, and begins serving the instant `migrate 3
#      --commit` lands;
#   3. the workflow parked before epoch 2 RESUMES CORRECTLY on the epoch-3 kernel
#      — two epoch boundaries later — with the IDENTICAL result an undisturbed run
#      gives and its mail.send effect delivered EXACTLY ONCE (outbox UNIQUE).
#
# Re-runnable against a fresh regel_scenario_c DB. Exits nonzero on the first
# mismatch; prints DEMO OK.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_scenario_c"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR_A=":8811"; BASE_A="http://localhost:8811"
ADDR_B=":8812"; BASE_B="http://localhost:8812"
BIN="$(mktemp -t regel.XXXXXX)"
LOG_A="$(mktemp -t regel-c-A.XXXXXX)"
LOG_B="$(mktemp -t regel-c-B.XXXXXX)"
SPOOL="$(mktemp -d -t regel-c-spool.XXXXXX)"

PID_A=""; PID_B=""
cleanup() {
  [ -n "$PID_A" ] && { kill -9 "$PID_A" 2>/dev/null; wait "$PID_A" 2>/dev/null; }
  [ -n "$PID_B" ] && { kill -9 "$PID_B" 2>/dev/null; wait "$PID_B" 2>/dev/null; }
  pkill -9 -f "$BIN serve" 2>/dev/null
  rm -rf "$BIN" "$LOG_A" "$LOG_B" "$SPOOL"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; [ -f "$LOG_A" ] && { echo "--- kernel A log ---"; tail -5 "$LOG_A"; }; [ -f "$LOG_B" ] && { echo "--- kernel B log ---"; tail -5 "$LOG_B"; }; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }

wait_listen() { # LOGFILE
  for i in $(seq 1 80); do grep -q "listening" "$1" && return 0; sleep 0.1; done
  cat "$1"; fail "kernel did not start ($1)"
}

echo "### building regel binary"; go build -o "$BIN" ./cmd/regel || fail "go build"
echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0: substrate + genesis (epoch 1) + grants + admit the real crm/followup"
"$BIN" migrate-db || fail "migrate-db"
"$BIN" genesis    || fail "genesis"
"$BIN" grant engineer:dev mail.send || fail "grant engineer:dev"
"$BIN" grant operator:op  mail.send || fail "grant operator:op"
V="$("$BIN" admit crm/followup.ts --name-prefix app/crm --actor engineer:dev --declare mail.send)" || fail "admit followup"
echo "$V" | grep -qF '"outcome": "admitted"' || { echo "$V"; fail "followup not admitted"; }
echo "admitted: app/crm/followup   (epoch $(sql "SELECT n FROM epoch_current"))"

step "1: kernel A (epoch 1) — start followup, catch it PARKED mid-flight, freeze the timer"
# poll 3s: the resume drain is NOTIFY-driven (immediate), but the timer scanner is
# slow, so once the workflow parks on sleep() we can freeze its wake deterministically.
"$BIN" serve -addr "$ADDR_A" -poll 3s -lease 5 -spool "$SPOOL" >"$LOG_A" 2>&1 &
PID_A=$!; wait_listen "$LOG_A"
echo "kernel A: $(head -1 "$LOG_A")"
CID="$(curl -s -X POST "$BASE_A/workflow/app/crm/followup" -H 'X-Regel-Actor: operator:op' -d '["Globex"]' | sed -n 's/.*"continuation_id": *"\([^"]*\)".*/\1/p')"
[ -n "$CID" ] || fail "followup did not start"
for i in $(seq 1 100); do
  ST="$(sql "SELECT status FROM continuation WHERE id='${CID}'")"
  [ "$ST" = "sleeping" ] && break
  [ "$ST" = "done" ] && fail "followup completed before we could park it (raise the sleep)"
  sleep 0.03
done
[ "$(sql "SELECT status FROM continuation WHERE id='${CID}'")" = "sleeping" ] || fail "followup never parked"
# Freeze the timer far in the future so no kernel resumes it until we choose.
sql "UPDATE continuation SET wake=jsonb_build_object('kind','timer','due','2999-01-01T00:00:00.000000Z') WHERE id='${CID}'" >/dev/null
EP0="$(sql "SELECT epoch FROM continuation WHERE id='${CID}'")"
[ "$EP0" = "1" ] || fail "parked epoch stamp = $EP0, want 1"
echo "PARKED mid-flight: $CID  (sleeping, provenance epoch=1, timer frozen)"

step "2: DEPLOY #1 — migrate 2 --commit (fence flips 1→2)"
"$BIN" migrate 2 --commit || fail "migrate 2 --commit"
[ "$(sql "SELECT n FROM epoch_current")" = "2" ] || fail "epoch_current != 2 after commit"
[ "$(sql "SELECT supersedes FROM epoch WHERE n=2")" = "1" ] || fail "epoch 2 does not supersede 1"
echo "epoch flipped to 2 (epoch 2 supersedes 1)"

step "3: O5 fence — the epoch-1 kernel A fails closed on its next work txn"
# Make the timer due now: kernel A (still epoch 1) will try to resume it and trip
# the O5 fence rather than run under a mismatched pair.
sql "UPDATE continuation SET wake=jsonb_build_object('kind','timer','due','2020-01-01T00:00:00.000000Z') WHERE id='${CID}'" >/dev/null
FENCED=""
for i in $(seq 1 120); do
  grep -q 'epoch.fence_tripped' "$LOG_A" && { FENCED=1; break; }
  sleep 0.1
done
[ -n "$FENCED" ] || fail "kernel A did not trip the O5 fence after the flip"
grep 'epoch.fence_tripped' "$LOG_A" | tail -1
echo "$(grep 'epoch.fence_tripped' "$LOG_A" | tail -1)" | grep -q '"observed_epoch":2' || fail "fence event observed_epoch != 2"
echo "$(grep 'epoch.fence_tripped' "$LOG_A" | tail -1)" | grep -q '"required_epoch":1' || fail "fence event required_epoch != 1"
# Fail-closed: no work committed under the mismatched pair — still not done.
[ "$(sql "SELECT status FROM continuation WHERE id='${CID}'")" != "done" ] || fail "workflow completed on the fenced epoch-1 kernel"
echo "O5 FENCE: kernel A tripped epoch.fence_tripped (observed 2, required 1) — no work committed"
kill -9 "$PID_A" 2>/dev/null; wait "$PID_A" 2>/dev/null; PID_A=""
echo "kernel A (epoch 1) retired"

step "4: --wait-for-epoch — stage an epoch-3 kernel B that refuses to serve yet (B7)"
"$BIN" serve -addr "$ADDR_B" -poll 200ms -lease 5 -spool "$SPOOL" --wait-for-epoch 3 >"$LOG_B" 2>&1 &
PID_B=$!
for i in $(seq 1 80); do grep -q 'parked_waiting' "$LOG_B" && break; sleep 0.1; done
grep -q 'parked_waiting' "$LOG_B" || { cat "$LOG_B"; fail "kernel B did not emit parked_waiting"; }
grep 'parked_waiting' "$LOG_B" | tail -1
grep -q 'listening' "$LOG_B" && fail "kernel B served before the epoch it waits for landed"
echo "kernel B staged: parked_waiting for epoch 3, NOT serving"

step "5: DEPLOY #2 — migrate 3 --commit; kernel B begins serving the instant it lands"
"$BIN" migrate 3 --commit || fail "migrate 3 --commit"
[ "$(sql "SELECT n FROM epoch_current")" = "3" ] || fail "epoch_current != 3"
wait_listen "$LOG_B"
head -1 "$LOG_B" >/dev/null; grep 'listening' "$LOG_B" | tail -1 | grep -q 'epoch 3' || fail "kernel B not serving epoch 3"
echo "kernel B serving epoch 3"

step "6: the parked workflow RESUMES on the epoch-3 kernel — two boundaries later"
for i in $(seq 1 200); do
  ST="$(sql "SELECT status FROM continuation WHERE id='${CID}'")"
  [ "$ST" = "done" ] && break
  [ "$ST" = "failed" ] && fail "followup failed on resume"
  sleep 0.1
done
[ "$(sql "SELECT status FROM continuation WHERE id='${CID}'")" = "done" ] || fail "followup not done after two deploys"
RESULT="$(curl -s "$BASE_B/continuation/$CID" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("result",""))' 2>/dev/null)"
[ "$RESULT" = "reminder-sent" ] || fail "result '$RESULT' != 'reminder-sent' (undisturbed-run parity)"
# Provenance stamp unchanged: resume never keyed off the epoch.
[ "$(sql "SELECT epoch FROM continuation WHERE id='${CID}'")" = "1" ] || fail "provenance epoch stamp moved"
echo "RESUMED: result='reminder-sent' (identical to an undisturbed run), provenance epoch still 1"

step "7: effect EXACTLY ONCE across two deploys (outbox UNIQUE + effectively-once delivery)"
OUTBOX="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${CID}'")"
DUPES="$(sql "SELECT count(*) FROM (SELECT continuation_id,step_seq,ordinal FROM outbox GROUP BY 1,2,3 HAVING count(*)>1) d")"
DELIVERED=0
for i in $(seq 1 100); do
  DELIVERED="$(sql "SELECT count(*) FROM outbox WHERE continuation_id='${CID}' AND delivered_at IS NOT NULL")"
  [ "${DELIVERED:-0}" -ge 1 ] && break; sleep 0.1
done
[ "$OUTBOX" = "1" ]    || fail "outbox=$OUTBOX != 1 (mail.send not exactly-once)"
[ "$DUPES" = "0" ]     || fail "$DUPES duplicate outbox dedup keys"
[ "$DELIVERED" = "1" ] || fail "delivered=$DELIVERED != 1 (dispatcher must deliver once)"
[ "$(find "$SPOOL" -type f | wc -l | tr -d ' ')" -ge 1 ] || fail "no spooled delivery file"
echo "EXACTLY-ONCE: outbox=1, dupes=0, delivered=1 to the FileSink spool"

echo
echo "=============================================================="
echo "DEMO OK — mid-flight crm/followup survived TWO deploys (epoch 1→2→3): O5 fence"
echo "          failed the old kernel closed, --wait-for-epoch staged the new one,"
echo "          the parked workflow resumed correctly, mail.send delivered exactly once"
