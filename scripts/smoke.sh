#!/usr/bin/env bash
# End-to-end smoke test: boots throwaway Mongo + Valkey containers, runs the
# integration tests against them, starts the real binary, probes the HTTP
# surface, then SIGTERMs it and checks the graceful-shutdown sequence.
#
# Uses its own container names and host ports so it never collides with
# `make compose-up` or other local stacks. Override ports via SMOKE_*_PORT.
set -euo pipefail

MONGO_PORT=${SMOKE_MONGO_PORT:-37018}
VALKEY_PORT=${SMOKE_VALKEY_PORT:-36382}
APP_PORT=${SMOKE_APP_PORT:-17991}
MONGO_CTR=gst-smoke-mongo
VALKEY_CTR=gst-smoke-valkey
BIN=bin/smoke-server
LOG=$(mktemp -t gst-smoke-log)
BASE="http://localhost:${APP_PORT}"
APP_PID=""
FAILED=0

cleanup() {
  [ -n "$APP_PID" ] && kill "$APP_PID" 2>/dev/null || true
  docker rm -f "$MONGO_CTR" "$VALKEY_CTR" >/dev/null 2>&1 || true
}
trap cleanup EXIT

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILED=1; }

# check <name> <expected-status> <method> <path> [jq-filter expected-value]
check() {
  local name=$1 want=$2 method=$3 path=$4 filter=${5:-} want_val=${6:-}
  local body status
  body=$(curl -s -o /dev/stdout -w '\n%{http_code}' -X "$method" "${BASE}${path}")
  status=${body##*$'\n'}
  body=${body%$'\n'*}
  if [ "$status" != "$want" ]; then
    fail "$name — status $status, want $want (body: $body)"
    return
  fi
  if [ -n "$filter" ]; then
    local got
    got=$(printf '%s' "$body" | jq -r "$filter")
    if [ "$got" != "$want_val" ]; then
      fail "$name — $filter = $got, want $want_val"
      return
    fi
  fi
  pass "$name"
}

echo "==> starting mongo (:$MONGO_PORT) + valkey (:$VALKEY_PORT)"
docker rm -f "$MONGO_CTR" "$VALKEY_CTR" >/dev/null 2>&1 || true
docker run -d --name "$MONGO_CTR" -p "${MONGO_PORT}:27017" mongo:7 --replSet rs0 --bind_ip_all >/dev/null
docker run -d --name "$VALKEY_CTR" -p "${VALKEY_PORT}:6379" valkey/valkey:8.1-alpine >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$MONGO_CTR" mongosh --quiet --eval \
    'try { rs.status().ok } catch (e) { rs.initiate({_id:"rs0",members:[{_id:0,host:"localhost:27017"}]}).ok }' 2>/dev/null | grep -q 1; then
    break
  fi
  sleep 1
done
docker exec "$MONGO_CTR" mongosh --quiet --eval 'rs.status().ok' | grep -q 1 || { echo "mongo replica set never became ready"; exit 1; }

export SVC_MONGO_URI="mongodb://localhost:${MONGO_PORT}/?replicaSet=rs0&directConnection=true"
export SVC_VALKEY_ADDR="localhost:${VALKEY_PORT}"
export SVC_HTTP_PORT="$APP_PORT"
export SVC_MONGO_DATABASE="smoke"
export SVC_SHUTDOWN_TIMEOUT="5s"
# Dev-only JWT secrets — boot fails without them (config.validate).
export SVC_AUTH_JWT_ACCESS_SECRET="smoke-access-secret-dev-only"
export SVC_AUTH_JWT_REFRESH_SECRET="smoke-refresh-secret-dev-only"
unset OTEL_EXPORTER_OTLP_ENDPOINT SVC_KAFKA_BROKERS

echo "==> integration tests + per-file gate against real containers"
if make -s coverage-integration-gate; then
  pass "make coverage-integration-gate"
else
  fail "make coverage-integration-gate"
fi

echo "==> building + starting the server (:$APP_PORT)"
mkdir -p bin
go build -o "$BIN" ./cmd/server
APP_NAME="go-service-template-smoke" APP_ENV="smoke" "$BIN" >"$LOG" 2>&1 &
APP_PID=$!

for _ in $(seq 1 30); do
  curl -sf "${BASE}/readiness" >/dev/null 2>&1 && break
  sleep 0.5
done

echo "==> probing HTTP surface"
check "GET /healthcheck → 200 + app_name" 200 GET /healthcheck .app_name go-service-template-smoke
check "GET /healthcheck → every check healthy" 200 GET /healthcheck '[.checks[].is_healthy] | all' true
check "GET /liveness → 200" 200 GET /liveness
check "GET /readiness → 200 (mongo + valkey pinged)" 200 GET /readiness
check "GET /nope → 404 envelope" 404 GET /nope .code ROUTE_NOT_FOUND
check "GET /api/v1/nope → 404 through throttle + idempotency" 404 GET /api/v1/nope .code ROUTE_NOT_FOUND
check "GET /api/v1/notifications without token → 401" 401 GET /api/v1/notifications .code AUTH_TOKEN_MISSING
check "PUT /api/v1/devices/x without token → 401" 401 PUT /api/v1/devices/x .code AUTH_TOKEN_MISSING
check "POST /healthcheck → 405 envelope" 405 POST /healthcheck .code METHOD_NOT_ALLOWED

if curl -sI "${BASE}/liveness" | grep -qi '^request-id:'; then
  pass "Request-Id response header present"
else
  fail "Request-Id response header missing"
fi

echo "==> graceful shutdown (SIGTERM)"
kill -TERM "$APP_PID"
for _ in $(seq 1 20); do
  kill -0 "$APP_PID" 2>/dev/null || break
  sleep 0.5
done
if kill -0 "$APP_PID" 2>/dev/null; then
  fail "process still alive 10s after SIGTERM"
else
  APP_PID=""
  if grep -q '"shutdown complete"' "$LOG"; then
    pass "shutdown sequence completed"
  else
    fail "no 'shutdown complete' log line"
  fi
fi

if grep -q '"level":"error"' "$LOG"; then
  fail "server logged errors:"
  grep '"level":"error"' "$LOG" | head -5
fi

echo
if [ "$FAILED" -ne 0 ]; then
  echo "SMOKE FAILED — server log: $LOG"
  exit 1
fi
echo "SMOKE OK"
rm -f "$LOG"
