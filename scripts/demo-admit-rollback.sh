#!/usr/bin/env bash
# demo-admit-rollback.sh — the Stage-A walking skeleton end to end:
#   migrate-db → genesis → serve → admit v1 → eval → admit v2 (base) → eval →
#   as-of rollback → sandbox fuel park → grant-fuel restart.
#
# Runs against a fresh `regel_demo` database (dropped + recreated each run), so
# it is re-runnable. Every step echoes its command and response; the script exits
# nonzero on the first mismatch.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PGADMIN="${REGEL_DEMO_ADMIN_DSN:-postgres://clank@localhost:5432/postgres}"
DEMO_DB="regel_demo"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8791"
BASEURL="http://localhost:8791"
BIN="$(mktemp -t regel.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-serve.XXXXXX)"

SERVE_PID=""
cleanup() {
  if [ -n "$SERVE_PID" ]; then
    kill "$SERVE_PID" 2>/dev/null
    wait "$SERVE_PID" 2>/dev/null
  fi
  rm -f "$BIN" "$SERVE_LOG"
}
trap cleanup EXIT

fail() { echo "DEMO FAILED: $*" >&2; exit 1; }

step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }

# Assert that "$1" (haystack) contains "$2" (needle).
assert_contains() {
  echo "$1" | grep -qF "$2" || fail "expected to contain: $2 -- got: $1"
}

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

echo "### recreating database ${DEMO_DB}"
psql "$PGADMIN" -c "DROP DATABASE IF EXISTS ${DEMO_DB} WITH (FORCE)" >/dev/null 2>&1
psql "$PGADMIN" -c "CREATE DATABASE ${DEMO_DB}" >/dev/null 2>&1 || fail "create database"

step "0a: migrate-db"
OUT="$("$BIN" migrate-db)" || fail "migrate-db"
echo "$OUT"; assert_contains "$OUT" "substrate applied"

step "0b: genesis"
OUT="$("$BIN" genesis)" || fail "genesis"
echo "$OUT"; assert_contains "$OUT" "epoch 1 pinned"

step "0c: serve (background)"
"$BIN" serve --addr "$ADDR" >"$SERVE_LOG" 2>&1 &
SERVE_PID=$!
for i in $(seq 1 40); do
  curl -sf -o /dev/null -X POST "${BASEURL}/eval/__ping__" -d '[]' 2>/dev/null && break
  # 404 (name does not resolve) means the server is up; grep the log for readiness.
  grep -q "listening" "$SERVE_LOG" && break
  sleep 0.25
done
grep -q "listening" "$SERVE_LOG" || { cat "$SERVE_LOG"; fail "server did not start"; }
echo "server up: $(cat "$SERVE_LOG")"

step "1: admit examples/greet_v1.ts"
V1="$("$BIN" admit examples/greet_v1.ts --name-prefix app/demo --actor engineer:dev)" || fail "admit v1"
echo "$V1"; assert_contains "$V1" '"outcome": "admitted"'
V1HASH="$(echo "$V1" | sed -n 's/.*"app\/demo\/greet": "\(r1_[0-9a-z]*\)".*/\1/p')"
[ -n "$V1HASH" ] || fail "could not extract v1 hash"
echo "v1 head hash: $V1HASH"

step "2: eval app/demo/greet [\"world\"]  ⇒  \"hello, world\""
R="$(curl -s -X POST "${BASEURL}/eval/app/demo/greet" -d '["world"]')"
echo "response: $R"; [ "$R" = '"hello, world"' ] || fail "eval v1 mismatch: $R"

step "3: capture T0 = now (between versions)"
sleep 1
T0="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
sleep 1
echo "T0 = $T0"

step "4: admit examples/greet_v2.ts --base app/demo/greet=$V1HASH"
V2="$("$BIN" admit examples/greet_v2.ts --name-prefix app/demo --actor engineer:dev --base "app/demo/greet=${V1HASH}")" || fail "admit v2"
echo "$V2"; assert_contains "$V2" '"outcome": "admitted"'

step "5: eval app/demo/greet [\"world\"]  ⇒  \"HELLO, world!\" (new behavior)"
R="$(curl -s -X POST "${BASEURL}/eval/app/demo/greet" -d '["world"]')"
echo "response: $R"; [ "$R" = '"HELLO, world!"' ] || fail "eval v2 mismatch: $R"

step "6: eval ?as_of=$T0  ⇒  \"hello, world\" (rollback = as-of WHERE clause)"
R="$(curl -s -X POST "${BASEURL}/eval/app/demo/greet?as_of=${T0}" -d '["world"]')"
echo "response: $R"; [ "$R" = '"hello, world"' ] || fail "as-of rollback mismatch: $R"

step "7: admit examples/burn.ts, eval ?tier=sandbox&fuel=20000 ⇒ 202 fuel.exhausted"
BV="$("$BIN" admit examples/burn.ts --name-prefix app/demo --actor engineer:dev)" || fail "admit burn"
assert_contains "$BV" '"outcome": "admitted"'
PARK="$(curl -s -X POST "${BASEURL}/eval/app/demo/burn?tier=sandbox&fuel=20000" -d '[]')"
echo "park response: $PARK"
assert_contains "$PARK" '"class":"fuel.exhausted"'
assert_contains "$PARK" '"name":"grant-fuel"'
CID="$(echo "$PARK" | sed -n 's/.*"continuation_id":"\([^"]*\)".*/\1/p')"
[ -n "$CID" ] || fail "no continuation id"
echo "continuation: $CID"

step "8: restart grant-fuel {fuel:10000000}  ⇒  completed value 100000"
R="$(curl -s -X POST "${BASEURL}/continuation/${CID}/restart" -d '{"restart":"grant-fuel","args":{"fuel":10000000}}')"
echo "response: $R"; [ "$R" = "100000" ] || fail "restart completion mismatch: $R"

echo
echo "=============================================================="
echo "DEMO OK — all eight steps passed (admit → eval → rollback → park → restart)"
echo "=============================================================="
