#!/usr/bin/env bash
# Smoke test for a running stack (`make up` first). Checks the path a user
# takes: browser -> nginx -> API -> every dependency, plus the demo service.
# Exits non-zero on the first failure.
set -euo pipefail

cd "$(dirname "$0")/.."

ENV_FILE="${ENV_FILE:-.env}"
COMPOSE=(docker compose --env-file "$ENV_FILE" -f deployments/docker-compose.yml)

# Ports come from the env file when set there.
env_value() {
  local value
  value=$(grep -E "^$1=" "$ENV_FILE" 2>/dev/null | tail -n 1 | cut -d= -f2- || true)
  echo "${value:-$2}"
}
DASHBOARD="http://localhost:$(env_value DASHBOARD_PORT 8080)"
DEMO="http://localhost:$(env_value DEMO_SERVICE_PORT 8082)"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok:   $*"; }

# Every service with a healthcheck must report healthy. The collector has
# none (scratch image); its health is covered through the API below.
expected=(postgres clickhouse prometheus api demo-service dashboard)
health=$("${COMPOSE[@]}" ps --format '{{.Service}} {{.Health}}')
for service in "${expected[@]}"; do
  grep -qx "$service healthy" <<<"$health" || fail "$service is not healthy (docker compose ps: $(grep "^$service " <<<"$health" || echo 'not running'))"
  pass "$service healthy"
done

curl -fsS --retry 5 --retry-connrefused --retry-delay 2 -o /dev/null "$DASHBOARD/" \
  || fail "dashboard did not serve index.html at $DASHBOARD/"
pass "dashboard serves the app"

status=$(curl -fsS --retry 5 --retry-connrefused --retry-delay 2 "$DASHBOARD/api/v1/status") \
  || fail "API status endpoint not reachable through the dashboard proxy"
grep -q '"status":"ok"' <<<"$status" || fail "API reports dependencies down: $status"
pass "API reachable through nginx; all dependencies up"

curl -fsS -o /dev/null "$DEMO/healthz" || fail "demo-service /healthz failed at $DEMO"
pass "demo-service healthy"

echo "Smoke test passed."
