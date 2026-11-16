#!/usr/bin/env bash
# Deploys a demo-service release and reports it to DeployRadar the way GitHub
# would, with signed deployment_status webhooks (FR-9, docs/demo-script.md).
#
#   scripts/deploy-demo.sh <version> [error_rate]
#   scripts/deploy-demo.sh v1         # a healthy release
#   scripts/deploy-demo.sh v2 0.2     # the bad release: 20% of requests fail
#
# The deployment itself is recreating the demo-service container with that
# service.version and fault rate. Versions differ only in configuration, so
# the image `make up` built is reused: building here would load the machine
# during the before window and skew the very baseline being compared against.
# The version is reported as the ref, as when a release tag is deployed.
# Along the way the script checks the
# webhook's rules on the real stack: a repeated delivery is answered as a
# duplicate, and a late in_progress does not un-finish the deployment.
#
# Keep traffic flowing while the comparison windows pass (make load), or each
# window will be too quiet for a verdict and come back insufficient_data.
set -euo pipefail

cd "$(dirname "$0")/.."

ENV_FILE="${ENV_FILE:-.env}"
COMPOSE=(docker compose --env-file "$ENV_FILE" -f deployments/docker-compose.yml)

# env_value <name> <default>: the shell's value, else the env file's, else the
# default. The same precedence Compose uses, so the script and the stack
# always agree (and a throwaway project can override ports and the secret).
env_value() {
  local value=${!1:-}
  if [[ -z $value ]]; then
    value=$(grep -E "^$1=" "$ENV_FILE" 2>/dev/null | tail -n 1 | cut -d= -f2- || true)
  fi
  echo "${value:-$2}"
}
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok:   $*"; }

usage="usage: scripts/deploy-demo.sh <version> [error_rate]"
version=${1:?$usage}
rate=${2:-0}
[[ $version =~ ^[A-Za-z0-9._-]{1,64}$ ]] || fail "version must be 1 to 64 letters, digits, dots, dashes or underscores. $usage"
[[ $rate =~ ^(0(\.[0-9]+)?|1(\.0+)?)$ ]] || fail "error_rate must be a number from 0 to 1. $usage"
command -v openssl >/dev/null || fail "openssl is needed to sign the webhooks"
command -v curl >/dev/null || fail "curl is needed to send the webhooks"

DASHBOARD="http://localhost:$(env_value DASHBOARD_PORT 8080)"
WEBHOOK="$DASHBOARD/api/v1/webhooks/github"
ENVIRONMENT=$(env_value INFRASCOPE_ENVIRONMENT local)
WINDOW=$(env_value INFRASCOPE_DEPLOYRADAR_WINDOW 10m)
SETTLE=$(env_value INFRASCOPE_DEPLOYRADAR_SETTLE 2m)
# Read, never printed.
SECRET=$(env_value INFRASCOPE_GITHUB_WEBHOOK_SECRET "")
[[ ${#SECRET} -ge 16 ]] || fail "set INFRASCOPE_GITHUB_WEBHOOK_SECRET (16 or more characters) in $ENV_FILE, then run make up"

# What GitHub would assign: a new deployment ID per run, the checked-out
# commit as the SHA.
deployment_id=$(date +%s)
sha=$(git rev-parse HEAD 2>/dev/null || printf '%s' "$version" | openssl dgst -sha1 | awk '{print $NF}')
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
created_at=$(now)

# payload <state> <status time>: a GitHub-shaped deployment_status event.
payload() {
  printf '{"action":"created","deployment_status":{"id":%s,"state":"%s","environment":"%s","description":"demo-service %s","created_at":"%s"},' \
    "$RANDOM$RANDOM" "$1" "$ENVIRONMENT" "$version" "$2"
  printf '"deployment":{"id":%s,"sha":"%s","ref":"%s","environment":"%s","description":"demo-service %s (fault rate %s)","payload":{"service":"demo-service","version":"%s"},"created_at":"%s","creator":{"login":"deploy-demo"}},' \
    "$deployment_id" "$sha" "$version" "$ENVIRONMENT" "$version" "$rate" "$version" "$created_at"
  printf '"repository":{"name":"demo-service","full_name":"infrascope/demo-service"}}'
}

# send <delivery id> <body>: posts a signed delivery and prints the receipt.
send() {
  local signature
  signature=$(printf '%s' "$2" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $NF}')
  curl -sS --fail-with-body -X POST "$WEBHOOK" \
    -H 'Content-Type: application/json' \
    -H 'X-GitHub-Event: deployment_status' \
    -H "X-GitHub-Delivery: $1" \
    -H "X-Hub-Signature-256: sha256=$signature" \
    --data-binary "$2"
}

expect() { # <receipt> <fragment> <what>
  grep -q "$2" <<<"$1" || fail "$3: got $1"
  pass "$3"
}

started=$(now)
receipt=$(send "deploy-$deployment_id-1" "$(payload in_progress "$started")") || fail "webhook refused: $receipt"
expect "$receipt" '"outcome":"recorded"' "deployment $deployment_id reported in progress"
id=$(sed -n 's/.*"deployment_id":\([0-9]*\).*/\1/p' <<<"$receipt")

echo "      deploying demo-service $version (fault rate $rate)..."
DEMO_SERVICE_VERSION=$version DEMO_FAULT_ERROR_RATE=$rate \
  "${COMPOSE[@]}" up -d --no-build --no-deps --force-recreate --wait demo-service >/dev/null \
  || fail "demo-service did not come back healthy"
pass "demo-service $version is healthy"

succeeded=$(now)
success=$(payload success "$succeeded")
receipt=$(send "deploy-$deployment_id-2" "$success")
expect "$receipt" '"comparison_queued":true' "success reported; comparison queued"

receipt=$(send "deploy-$deployment_id-2" "$success")
expect "$receipt" '"outcome":"duplicate"' "a repeated delivery is answered as a duplicate"

receipt=$(send "deploy-$deployment_id-3" "$(payload in_progress "$started")")
expect "$receipt" '"applied":false' "a late in_progress does not un-finish the deployment"

cat <<EOF

Deployment $id succeeded at $succeeded.
The comparison is due $SETTLE + $WINDOW after that, plus about 30 s for the
last metrics to arrive. Keep traffic running (make load) until then.

  Dashboard: $DASHBOARD/deployments/$id
  API:       curl -b session.txt $DASHBOARD/api/v1/deployments/$id
EOF
