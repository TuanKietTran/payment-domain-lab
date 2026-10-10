#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
LEDGER_PORT=${SMOKE_LEDGER_PORT:-18081}
PAYMENT_PORT=${SMOKE_PAYMENT_PORT:-18080}
TMPDIR_ROOT=${TMPDIR:-/tmp}
RUN_DIR=$(mktemp -d "$TMPDIR_ROOT/payment-sim-smoke.XXXXXX")
LEDGER_PID=
PAYMENT_PID=

cleanup() {
  if [ -n "$PAYMENT_PID" ]; then kill "$PAYMENT_PID" 2>/dev/null || true; fi
  if [ -n "$LEDGER_PID" ]; then kill "$LEDGER_PID" 2>/dev/null || true; fi
  if [ -n "$PAYMENT_PID" ]; then wait "$PAYMENT_PID" 2>/dev/null || true; fi
  if [ -n "$LEDGER_PID" ]; then wait "$LEDGER_PID" 2>/dev/null || true; fi
  rm -rf "$RUN_DIR"
}
trap cleanup EXIT HUP INT TERM

make -C "$ROOT" build >/dev/null
LEDGER_ADDR="127.0.0.1:$LEDGER_PORT" "$ROOT/bin/ledger-service" >"$RUN_DIR/ledger.log" 2>&1 &
LEDGER_PID=$!
PAYMENT_ADDR="127.0.0.1:$PAYMENT_PORT" LEDGER_URL="http://127.0.0.1:$LEDGER_PORT" "$ROOT/bin/payment-service" >"$RUN_DIR/payment.log" 2>&1 &
PAYMENT_PID=$!

wait_for() {
  url=$1
  log=$2
  count=0
  until curl -fsS "$url" >/dev/null 2>&1; do
    count=$((count + 1))
    if [ "$count" -ge 50 ]; then
      echo "timed out waiting for $url" >&2
      printf '%s\n' "--- service log ---" >&2
      tail -n 20 "$log" >&2
      exit 1
    fi
    sleep 0.1
  done
}

wait_for "http://127.0.0.1:$LEDGER_PORT/healthz" "$RUN_DIR/ledger.log"
wait_for "http://127.0.0.1:$PAYMENT_PORT/healthz" "$RUN_DIR/payment.log"

base="http://127.0.0.1:$PAYMENT_PORT"
created=$(curl -fsS "$base/v1/payments" -H 'Content-Type: application/json' -H 'Idempotency-Key: smoke-create' -d '{"amount":1000,"currency":"USD","description":"Smoke test"}')
payment_id=$(printf '%s' "$created" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
if [ -z "$payment_id" ]; then echo "could not parse payment id: $created" >&2; exit 1; fi

post_action() {
  action=$1
  key=$2
  body=$3
  curl -fsS -X POST "$base/v1/payments/$payment_id/actions/$action" -H 'Content-Type: application/json' -H "Idempotency-Key: $key" -d "$body"
}

post_action authorize smoke-authorize '{}' | grep -q '"status":"authorized"'
post_action capture smoke-capture '{}' | grep -q '"status":"captured"'
post_action refund smoke-refund '{"amount":250}' | grep -q '"status":"partially_refunded"'

status=$(curl -sS -o "$RUN_DIR/invalid.json" -w '%{http_code}' -X POST "$base/v1/payments/$payment_id/actions/capture" -H 'Content-Type: application/json' -H 'Idempotency-Key: smoke-invalid' -d '{}')
if [ "$status" != 409 ] || ! grep -q '"code":"invalid_transition"' "$RUN_DIR/invalid.json"; then
  echo "invalid transition did not return structured HTTP 409" >&2
  exit 1
fi

curl -fsS "http://127.0.0.1:$LEDGER_PORT/v1/postings" | grep -q '"postings"'
echo "smoke test passed: create -> authorize -> capture -> partial refund; invalid capture rejected"
