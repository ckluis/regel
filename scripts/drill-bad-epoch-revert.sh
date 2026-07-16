#!/usr/bin/env bash
# drill-bad-epoch-revert.sh — the ADR-08 §6a BAD-EPOCH REVERT DRILL, run as a
# release gate under a measured clock (A7 + L1).
#
# A "bad" epoch is deployed (`migrate 2 --commit`); a workflow is parked UNDER it,
# so it is a dependent BOUND to the bad epoch (its provenance stamp is epoch 2).
# The operator classifies the epoch bad and REVERTS — a NEW epoch 3 carrying the
# prior-good epoch-1 pair (rolling back is rolling forward to the previous binary,
# ADR-08 §6a) — via the real `migrate 3 --revert-to 1` door. The drill then asserts
# the load-bearing L1 property that Gate-4 never checked before: the dependent
# bound to the bad epoch is HELD FAIL-CLOSED — DDL-backed epoch_hold state + a
# 'condition' status the reactor never resumes — rather than silently running
# against the reverted world. Time-to-recovered is recorded.
#
# Re-runnable against a fresh regel_drill_revert DB. Exits nonzero on the first
# mismatch; prints DEMO OK.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_drill_revert"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR_B=":8813"; BASE_B="http://localhost:8813"
ADDR_C=":8814"
BIN="$(mktemp -t regel.XXXXXX)"
LOG_B="$(mktemp -t regel-drill-B.XXXXXX)"
LOG_C="$(mktemp -t regel-drill-C.XXXXXX)"
WF="$(mktemp -t regel-drill-wf.XXXXXX.ts)"

PID_B=""; PID_C=""
cleanup() {
  [ -n "$PID_B" ] && { kill -9 "$PID_B" 2>/dev/null; wait "$PID_B" 2>/dev/null; }
  [ -n "$PID_C" ] && { kill -9 "$PID_C" 2>/dev/null; wait "$PID_C" 2>/dev/null; }
  pkill -9 -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$LOG_B" "$LOG_C" "$WF"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }
wait_listen() { for i in $(seq 1 80); do grep -q "listening" "$1" && return 0; sleep 0.1; done; cat "$1"; fail "kernel did not start ($1)"; }
now_ms() { python3 -c 'import time;print(int(time.time()*1000))'; }

echo "### building regel binary"; go build -o "$BIN" ./cmd/regel || fail "go build"
echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

printf 'import { sleep } from "std/wf";\nexport function w(): number { sleep(3600000); return 7; }\n' > "$WF"

step "0: substrate + genesis (epoch 1) + admit a long-running workflow"
"$BIN" migrate-db || fail "migrate-db"
"$BIN" genesis    || fail "genesis"
V="$("$BIN" admit "$WF" --name-prefix app/drill --actor engineer:dev)" || fail "admit"
echo "$V" | grep -qF '"outcome": "admitted"' || { echo "$V"; fail "workflow not admitted"; }
R1ROOT="$(sql "SELECT std_manifest_root FROM epoch WHERE n=1")"
echo "admitted: app/drill/w   (epoch 1, root ${R1ROOT:0:16})"

step "1: DEPLOY the BAD epoch 2, then park a dependent UNDER it"
"$BIN" migrate 2 --commit || fail "migrate 2 --commit"
[ "$(sql "SELECT n FROM epoch_current")" = "2" ] || fail "epoch_current != 2"
"$BIN" serve -addr "$ADDR_B" -poll 200ms -lease 5 >"$LOG_B" 2>&1 &
PID_B=$!; wait_listen "$LOG_B"
grep 'listening' "$LOG_B" | tail -1 | grep -q 'epoch 2' || fail "kernel B not on epoch 2"
CID="$(curl -s -X POST "$BASE_B/workflow/app/drill/w" -H 'X-Regel-Actor: operator:op' -d '[]' | sed -n 's/.*"continuation_id": *"\([^"]*\)".*/\1/p')"
[ -n "$CID" ] || fail "workflow did not start"
for i in $(seq 1 100); do [ "$(sql "SELECT status FROM continuation WHERE id='${CID}'")" = "sleeping" ] && break; sleep 0.05; done
[ "$(sql "SELECT status FROM continuation WHERE id='${CID}'")" = "sleeping" ] || fail "workflow never parked"
[ "$(sql "SELECT epoch FROM continuation WHERE id='${CID}'")" = "2" ] || fail "dependent not stamped epoch 2 (not bound to the bad epoch)"
echo "dependent parked: $CID  (bound to bad epoch 2)"
kill -9 "$PID_B" 2>/dev/null; wait "$PID_B" 2>/dev/null; PID_B=""
echo "bad-epoch fleet (kernel B) retired"

step "2: CLASSIFY bad → REVERT to the prior-good pair (migrate 3 --revert-to 1)"
# Classification inputs per §6a: the structured fence diagnostics, a canary re-run,
# and the O1-O5 gates. Here the canary is green (the defect is an evaluator bug the
# corpus is blinded to — simulated), so the operator chooses REVERT over roll-forward.
"$BIN" canary >/dev/null || fail "pre-revert canary should be green"
T0="$(now_ms)"
"$BIN" migrate 3 --revert-to 1 || fail "migrate 3 --revert-to 1"
[ "$(sql "SELECT n FROM epoch_current")" = "3" ] || fail "epoch_current != 3 after revert"
[ "$(sql "SELECT supersedes FROM epoch WHERE n=3")" = "2" ] || fail "revert epoch 3 must supersede the bad epoch 2"
R3ROOT="$(sql "SELECT std_manifest_root FROM epoch WHERE n=3")"
[ "$R3ROOT" = "$R1ROOT" ] || fail "revert epoch 3 root ($R3ROOT) does not carry the prior-good epoch-1 root ($R1ROOT)"
echo "REVERTED: epoch 3 carries epoch-1's pair (root ${R3ROOT:0:16}), supersedes 2"

step "3: ASSERT the bad-epoch dependent is HELD FAIL-CLOSED (L1)"
HOLDS="$(sql "SELECT count(*) FROM epoch_hold WHERE continuation_id='${CID}' AND released_at IS NULL")"
[ "$HOLDS" = "1" ] || fail "expected 1 active epoch_hold row for the dependent, got $HOLDS"
BADEP="$(sql "SELECT bad_epoch FROM epoch_hold WHERE continuation_id='${CID}'")"
[ "$BADEP" = "2" ] || fail "epoch_hold.bad_epoch = $BADEP, want 2"
[ "$(sql "SELECT status FROM continuation WHERE id='${CID}'")" = "condition" ] || fail "held dependent status != condition (not fenced from the reactor)"
echo "HELD: epoch_hold row present (bad_epoch=2), status=condition — DDL-backed, fail-closed"

step "4: PROVE fail-closed — the epoch-3 kernel NEVER resumes the held dependent"
"$BIN" serve -addr "$ADDR_C" -poll 100ms -lease 5 >"$LOG_C" 2>&1 &
PID_C=$!; wait_listen "$LOG_C"
grep 'listening' "$LOG_C" | tail -1 | grep -q 'epoch 3' || fail "kernel C not on epoch 3"
sleep 1.0   # give the reactor ample time to (wrongly) pick it up
[ "$(sql "SELECT status FROM continuation WHERE id='${CID}'")" = "condition" ] || fail "held dependent was RESUMED against the reverted world — NOT fail-closed"
[ "$(sql "SELECT count(*) FROM continuation WHERE id='${CID}' AND result IS NOT NULL")" = "0" ] || fail "held dependent produced a result — it ran against the reverted world"
T1="$(now_ms)"
echo "FAIL-CLOSED confirmed: the held dependent stayed 'condition', produced no result under the epoch-3 fleet"

step "5: VERIFY recovery — one epoch serving, canary green, clock recorded"
[ "$(sql "SELECT n FROM epoch_current")" = "3" ] || fail "not a single serving epoch"
"$BIN" canary >/dev/null || fail "post-revert world-rehash canary not green"
echo "one epoch serving (3), world-rehash canary GREEN"
echo "TIME-TO-RECOVERED (revert → reverted fleet verified fail-closed): $((T1 - T0)) ms"

echo
echo "=============================================================="
echo "DEMO OK — bad-epoch revert drill: the dependent bound to the bad epoch was"
echo "          HELD FAIL-CLOSED (epoch_hold + condition), the revert carried the"
echo "          prior-good pair, and the reverted fleet never resumed held work"
