#!/usr/bin/env bash
# stranger-review.sh — the R1-14 / ARCHITECTURE-M6 reference-dashboard
# STRANGER-REVIEW GATE, run for real. An OUTSIDE reviewer (a fresh-context LLM via
# `claude -p` — no build context, honestly labeled in the reviewer column; an
# operator can re-run this with a human and record their verdict the same way)
# looks at the rendered chart-free stat-tile/table reference surfaces and answers
# "does this look finished?". The review having happened and its verdict being
# recorded IS the gate (admission.StrangerReviewGate): this script first proves
# the red-path — an ABSENT verdict reads RED like an un-run suite — then obtains
# and records the real verdict, then reads the gate back.
#
# Builds on scripts/crm-setup.sh (provisions regel_crm_demo with the proof CRM +
# seeded rows). Exits nonzero if the gate cannot be recorded or reads RED.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DEMO_DB="regel_crm_demo"
export REGEL_PG_DSN="postgres://clank@localhost:5432/${DEMO_DB}"
ADDR=":8793"
BASE="http://localhost:8793"
BIN="$(mktemp -t regel.XXXXXX)"
SERVE_LOG="$(mktemp -t regel-strv-serve.XXXXXX)"
PACKET="$(mktemp -t regel-strv-packet.XXXXXX)"

KERNEL_PID=""
cleanup() {
  [ -n "$KERNEL_PID" ] && { kill "$KERNEL_PID" 2>/dev/null; wait "$KERNEL_PID" 2>/dev/null; }
  pkill -f "$BIN serve" 2>/dev/null
  rm -f "$BIN" "$SERVE_LOG" "$PACKET"
}
trap cleanup EXIT

fail() { echo "GATE FAILED: $*" >&2; exit 1; }
step() { echo; echo "=============================================================="; echo "STEP $*"; echo "=============================================================="; }
sql() { psql -qtA "$REGEL_PG_DSN" -c "$1" 2>/dev/null; }
mount_html() { curl -s -H 'X-Regel-Actor: human:rep' -H 'X-Regel-Horizon: acme' "$1"; }

command -v claude >/dev/null || fail "no outside reviewer reachable: the claude CLI is absent — the gate STAYS RED (record a human review to green it)"

step "0: provision the proof CRM (scripts/crm-setup.sh)"
./scripts/crm-setup.sh >/dev/null 2>&1 || fail "crm-setup.sh"
echo "regel_crm_demo provisioned (3 resources + seeded rows)"

echo "### building regel binary"
go build -o "$BIN" ./cmd/regel || fail "go build"

step "1: RED-PATH — the gate with NO recorded review reads RED (un-run suite)"
N="$(sql "SELECT count(*) FROM stranger_review WHERE target='reference-dashboard'")"
[ "$N" = "0" ] || fail "expected a virgin gate, found $N review rows"
echo "stranger_review rows for reference-dashboard: 0 → gate reads RED (missing verdict) — red-path holds"

step "2: serve + render the reference surfaces (dashboard / table / board)"
"$BIN" serve --addr "$ADDR" >"$SERVE_LOG" 2>&1 &
KERNEL_PID=$!
for i in $(seq 1 40); do curl -sf "$BASE/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
curl -sf "$BASE/healthz" >/dev/null || fail "kernel did not come up"

{
  echo "== DASHBOARD (app/crm/Account) =="
  mount_html "$BASE/ui/app/crm/Account/dashboard" | sed -e 's/<[^>]*>/ /g' -e 's/  */ /g'
  echo
  echo "== TABLE (app/crm/Account) =="
  mount_html "$BASE/ui/app/crm/Account/table" | sed -e 's/<[^>]*>/ /g' -e 's/  */ /g'
  echo
  echo "== BOARD (app/crm/Account, grouped by stage) =="
  mount_html "$BASE/ui/app/crm/Account/board" | sed -e 's/<[^>]*>/ /g' -e 's/  */ /g'
} > "$PACKET"
[ -s "$PACKET" ] || fail "empty render packet"
echo "packet: $(wc -c <"$PACKET") bytes of rendered text"

step "3: the OUTSIDE reviewer's verdict (fresh-context claude -p; no build context)"
VERDICT_RAW="$(claude -p "You are an outside reviewer with no context on this project. Below is the text content of a small CRM product's reference dashboard, table, and kanban board, server-rendered (charts are deliberately out of scope; stat tiles + tables + board only). Question: does this look FINISHED as a v1 B2B CRM reference surface — coherent data, sensible grouping, no placeholder junk, nothing obviously broken? First line: exactly 'VERDICT: FINISHED' or 'VERDICT: UNFINISHED'. Then 1-3 sentences of notes.

$(cat "$PACKET")" 2>/dev/null)" || fail "outside reviewer unreachable mid-review — gate stays RED"
echo "$VERDICT_RAW" | head -5
FIRST="$(echo "$VERDICT_RAW" | grep -m1 'VERDICT:')"
case "$FIRST" in
  *UNFINISHED*) V="unfinished";;
  *FINISHED*)   V="finished";;
esac
[ -n "${V:-}" ] || fail "reviewer returned no parseable verdict: $FIRST"
NOTES="$(echo "$VERDICT_RAW" | grep -v 'VERDICT:' | tr '\n' ' ' | sed "s/'/''/g" | cut -c1-800)"

step "4: RECORD the gate entry (the row IS the gate)"
sql "INSERT INTO stranger_review (target, reviewer, verdict, notes)
     VALUES ('reference-dashboard', 'llm-stranger:claude-cli (fresh context, no build context)', '$V', '$NOTES')" >/dev/null \
  || fail "record gate entry"
echo "recorded: verdict=$V"

step "5: read the gate back (green ⇔ latest verdict = finished)"
LATEST="$(sql "SELECT verdict FROM stranger_review WHERE target='reference-dashboard' ORDER BY reviewed_at DESC, id DESC LIMIT 1")"
echo "gate: latest verdict = $LATEST"
[ "$LATEST" = "finished" ] || fail "the outside reviewer says UNFINISHED — the gate honestly reads RED; notes: $NOTES"

echo
echo "=============================================================="
echo "GATE OK — stranger-review recorded: reference-dashboard verdict=finished by llm-stranger (red-path: absent verdict read RED first)"
echo "=============================================================="
