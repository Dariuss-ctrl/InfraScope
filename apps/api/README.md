# api

InfraScope's query and configuration API: Go standard library `net/http`, JSON over HTTP. The contract is **[openapi.yaml](openapi.yaml)**. Requirements and phases are in [SPEC §8](../../docs/SPEC.md#8-api-surface).

## What exists today (Phase 0)

- `GET /healthz`: liveness. Checks no dependencies, so a database outage never restarts the API.
- `GET /readyz`: readiness. Returns `503` only when a **required** dependency (PostgreSQL) is down.
- `GET /api/v1/status`: the same report, always `200`, for the dashboard banner.
- Every other `/api/` route answers `501 not_implemented` in the standard error envelope.
- `api healthcheck`: probes `/healthz` from inside the distroless container.

Dependencies are probed concurrently, each bounded by `INFRASCOPE_DEPENDENCY_TIMEOUT`. The bound holds even for a probe that ignores its context. Reports never include error details, because they are served unauthenticated; those go to the structured log.

## Layout

```
cmd/api/            entry point: config, wiring, graceful shutdown, healthcheck subcommand
internal/config/    environment configuration and validation
internal/health/    dependency probes and readiness aggregation
internal/httpapi/   routes and the JSON/error-envelope conventions
```

Planned packages:

| Package | Phase | Responsibility |
|---|---|---|
| `internal/auth` | 2 | login, sessions, rate limiting, CSRF checks |
| `internal/store` | 2 | PostgreSQL access (`users`, `sessions`, alert rules and state) |
| `internal/telemetry` | 2 | ClickHouse trace and log queries (parameterised builders, cursors) |
| `internal/metrics` | 2 | Prometheus queries (PromQL builders, label escaping) |
| `internal/alerting` | 4 | evaluator loop and the pure state-transition function |
| `migrations/` | 2 | versioned PostgreSQL schema for the `infrascope` database |

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `INFRASCOPE_HTTP_ADDR` | `:8081` | listen address |
| `INFRASCOPE_DATABASE_URL` | — (required) | `postgres://infrascope:…@postgres:5432/infrascope` |
| `INFRASCOPE_CLICKHOUSE_URL` | `http://clickhouse:8123` | ClickHouse HTTP interface |
| `INFRASCOPE_PROMETHEUS_URL` | `http://prometheus:9090` | Prometheus HTTP API |
| `INFRASCOPE_COLLECTOR_HEALTH_URL` | `http://otel-collector:13133` | collector `health_check` extension |
| `INFRASCOPE_DEPENDENCY_TIMEOUT` | `2s` | upper bound for any dependency call |
| `INFRASCOPE_ADMIN_USER`, `INFRASCOPE_ADMIN_PASSWORD` | — | admin bootstrap (Phase 2) |

## Development

```bash
go test ./...
INFRASCOPE_DATABASE_URL=postgres://u:p@localhost:5432/infrascope go run ./cmd/api
```
