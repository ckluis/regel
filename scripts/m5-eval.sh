#!/usr/bin/env bash
# m5-eval.sh — run the ADR-12 OPEN M5 gates against a REAL LLM (`claude -p`)
# driving the REAL MCP agent plane. Resumable: the eval DB persists every scored
# (task,attempt), so re-running fills only gaps. Never fakes a number — if the LLM
# is unreachable mid-run, affected gates stay OPEN as partial and this stays
# re-runnable.
#
#   scripts/m5-eval.sh [--fresh] [--authoring-n N] [--restart-m M] [--k K]
#
# --fresh drops + recreates the eval DB (loses resume state). Default reuses it.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
EVAL_DB="${REGEL_M5_DB:-regel_m5_eval}"
export REGEL_PG_DSN="${REGEL_M5_DSN:-postgres://clank@localhost:5432/${EVAL_DB}}"
BIN="${REGEL_M5_BIN:-$(mktemp -t regel-m5.XXXXXX)}"
# Absolute — `go test` runs with cwd = the package dir, so a relative path would
# resolve under gate/m5eval/.
EVID="${REGEL_M5_EVIDENCE:-$ROOT/spec/gates/evidence-e/m5}"
case "$EVID" in /*) ;; *) EVID="$ROOT/$EVID";; esac
FRESH=0

while [ $# -gt 0 ]; do
  case "$1" in
    --fresh) FRESH=1;;
    --authoring-n) export REGEL_M5_AUTHORING_N="$2"; shift;;
    --restart-m) export REGEL_M5_RESTART_M="$2"; shift;;
    --k) export REGEL_M5_K="$2"; shift;;
    --timeout) export REGEL_M5_TIMEOUT_S="$2"; shift;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
  shift
done

fail() { echo "M5-EVAL FAILED: $*" >&2; exit 1; }

echo "### building regel binary → $BIN"
go build -o "$BIN" ./cmd/regel || fail "go build"
export REGEL_M5_BIN="$BIN"

db_exists() { psql "$PGADMIN" -tAc "SELECT 1 FROM pg_database WHERE datname='${EVAL_DB}'" 2>/dev/null | grep -q 1; }

if [ "$FRESH" = "1" ]; then
  echo "### --fresh: dropping ${EVAL_DB}"
  psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${EVAL_DB} WITH (FORCE)" >/dev/null 2>&1
fi

if ! db_exists; then
  echo "### creating + bootstrapping ${EVAL_DB}"
  psql "$PGADMIN" -c "CREATE DATABASE ${EVAL_DB}" >/dev/null 2>&1 || fail "create database"
  "$BIN" migrate-db >/dev/null || fail "migrate-db"
  "$BIN" genesis   >/dev/null || fail "genesis"
  "$BIN" agent-key --key "${REGEL_M5_KEY:-m5-agent}" --actor a1 --scope-id org1 || fail "agent-key"
else
  echo "### reusing existing ${EVAL_DB} (resumable) — re-applying schema (idempotent)"
  "$BIN" migrate-db >/dev/null || fail "migrate-db"
fi

mkdir -p "$EVID"
export REGEL_M5_LLM=1
export REGEL_M5_EVIDENCE="$EVID"

echo "### running the real-LLM M5 eval (serial, resumable) — this can take a while"
go test ./gate/m5eval/ -run TestM5EvalRealLLM -count=1 -v -timeout "${REGEL_M5_GOTIMEOUT:-4h}"
rc=$?

echo
echo "### evidence written under ${EVID}:"
ls -la "$EVID" 2>/dev/null
echo
echo "### m5_gate rows:"
psql "$REGEL_PG_DSN" -c "SELECT gate, corpus_size, floor_size, measured, floor, green, partial FROM m5_gate ORDER BY gate" 2>/dev/null
echo "### admission_capacity (agent, §5 derived):"
psql "$REGEL_PG_DSN" -c "SELECT agent_kind, capacity, derived_from FROM admission_capacity WHERE agent_kind='agent'" 2>/dev/null

exit $rc
