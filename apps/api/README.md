# api

InfraScope's query and configuration API: Go standard library `net/http`, JSON over HTTP. The contract is **[openapi.yaml](openapi.yaml)**, and the integration tests validate every response against it. Requirements and phases are in [SPEC §8](../../docs/SPEC.md#8-api-surface).

## Endpoints

| Route | Auth | Reads |
|---|---|---|
| `GET /healthz`, `GET /readyz`, `GET /api/v1/status` | — | dependency probes |
| `POST /api/v1/auth/login` · `POST /api/v1/auth/logout` · `GET /api/v1/auth/me` | —, session, session | PostgreSQL |
| `GET /api/v1/services` · `GET /api/v1/services/{service}/metrics` | session | Prometheus |
| `GET /api/v1/traces` · `GET /api/v1/traces/{traceId}` | session | ClickHouse |
| `GET /api/v1/logs` | session | ClickHouse |
| `GET /api/v1/diagnostics/schema` | session | ClickHouse: which tables and skip indexes are missing, and their effect |
| `POST /api/v1/webhooks/github` | HMAC signature | PostgreSQL: GitHub `deployment_status` deliveries (DeployRadar) |
| `GET /api/v1/deployments` · `GET /api/v1/deployments/{deploymentId}` | session | PostgreSQL: deployments, status history and comparison evidence |
| `/api/v1/alert-rules`, `/api/v1/alerts` | session | `501` until Phase 5 |

## How it is built

- **Nothing user-supplied becomes query text.** ClickHouse queries bind every value as a typed server-side parameter (`{service:String}`), and SQL is assembled only from constants. PromQL label values are Go-quoted, which is exactly how PromQL parses strings. Each ClickHouse query also runs with `readonly=2` and a server-side `max_execution_time`.
- **Failures are typed.** Each client wraps errors as `backend.ErrUnavailable`, `ErrTimeout` or `ErrNotFound`, which the HTTP layer maps to `503 storage_unavailable` / `metrics_unavailable` / `database_unavailable`, `504 query_timeout` and `404`. Unexpected errors are logged and returned as a generic `500`.
- **Degraded, not down.** The API starts and serves with ClickHouse or Prometheus unreachable; only the features that need them fail. `/api/v1/status` also reports `clickhouse_schema`, which goes down when a table or skip index the queries rely on is missing. Being public, it names nothing; `/api/v1/diagnostics/schema` names what is missing and what it slows down or breaks, for a logged-in user.
- **Keyset pagination.** Cursors encode `(sort value, tie-breaker)`, so rows sharing a timestamp or duration never skip or repeat across pages. Pages hold at most 500 results, and a trace response at most 10,000 spans (`truncated: true` beyond that).
- **Sessions (ADR-0006).** bcrypt password hashes. Random 256-bit tokens stored only as SHA-256 hashes. An `HttpOnly`, `SameSite=Strict` cookie. A same-origin check on state-changing requests. Per-client login rate limiting (5 attempts, then one more every 12 s).
- **DeployRadar (ADR-0007).** The webhook is verified by `X-Hub-Signature-256` before anything is stored, every verified delivery is stored before it is processed, and statuses are applied by their own time, so redeliveries and late arrivals change nothing. A worker goroutine claims due comparisons with `FOR UPDATE SKIP LOCKED`, measures both windows in Prometheus, and stores the values with the exact PromQL behind them. The verdict rules are one pure function, `deployradar.Judge`.
- **Self-observability.** The API exports its own traces, metrics and logs as `infrascope-api`. Its outgoing ClickHouse and Prometheus calls appear as client spans.

## Layout

```
cmd/api/              wiring, graceful shutdown, healthcheck subcommand
internal/config/      environment configuration and validation
internal/backend/     the failure kinds every client returns
internal/postgres/    users, sessions, deployments, embedded migrations (advisory-locked)
internal/auth/        login, sessions, bootstrap of the admin account, rate limiter
internal/clickhouse/  trace and log search, trace detail, schema check, cursors
internal/prometheus/  service summaries and series, DeployRadar window stats (PromQL builders)
internal/deployradar/ deployment domain, status precedence, verdict rules, comparison worker
internal/github/      webhook signature check and deployment_status parsing
internal/httpapi/     routes, parameter validation, error envelope
internal/health/      dependency probes and readiness
internal/otelsetup/   OpenTelemetry SDK setup for the API itself
internal/testenv/     disposable containers for integration tests (build tag "integration")
```

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `INFRASCOPE_HTTP_ADDR` | `:8081` | listen address |
| `INFRASCOPE_DATABASE_URL` | — (required) | `postgres://infrascope:…@postgres:5432/infrascope` |
| `INFRASCOPE_CLICKHOUSE_URL` / `_USER` / `_PASSWORD` | `http://clickhouse:8123` / `default` / — | telemetry store |
| `INFRASCOPE_PROMETHEUS_URL` | `http://prometheus:9090` | metrics store |
| `INFRASCOPE_COLLECTOR_HEALTH_URL` | `http://otel-collector:13133` | collector `health_check` extension |
| `INFRASCOPE_ADMIN_USER` / `_PASSWORD` | `admin` / — (required, 12–72 characters) | the single account; changing the password revokes its sessions |
| `INFRASCOPE_SESSION_TTL` | `12h` | session lifetime |
| `INFRASCOPE_COOKIE_SECURE` | `false` | set `true` behind HTTPS |
| `INFRASCOPE_DEPENDENCY_TIMEOUT` | `2s` | health probes and connection attempts |
| `INFRASCOPE_QUERY_TIMEOUT` | `10s` | longest a single query may run |
| `INFRASCOPE_MAX_SEARCH_RANGE` | `24h` | widest trace or log search window |
| `INFRASCOPE_GITHUB_WEBHOOK_SECRET` | — (webhooks off) | shared with the GitHub webhook; at least 16 characters |
| `INFRASCOPE_DEPLOYRADAR_WINDOW` / `_SETTLE` | `10m` / `2m` | length of each comparison window, and the gap after a deployment before the after window |
| `INFRASCOPE_DEPLOYRADAR_MIN_REQUESTS` | `200` | requests each window needs before any verdict other than `insufficient_data` |

## Tests

```bash
go test ./...                                     # unit tests, no Docker needed
go test -tags integration -count=1 -p 1 ./...     # real PostgreSQL, ClickHouse and Prometheus containers
```

The integration suites are described in [tests/integration](../../tests/integration/README.md).
