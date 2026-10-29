# Integration tests (Phase 2)

These test the API against **real** ClickHouse, PostgreSQL and Prometheus containers, started per test package with [testcontainers-go](https://golang.testcontainers.org/). They need only Docker, not the Compose stack.

Planned suites, each mapped to the requirements it verifies ([SPEC §11](../../docs/SPEC.md#11-testing-strategy)):

| Suite | What it proves | Requirement |
|---|---|---|
| Trace queries | Search filters, stable cursor pagination, trace-by-ID bounded by the lookup table | FR-3, AC-3.3, AC-3.4 |
| Log queries | Severity ranges, token search, trace and span filters, limits | FR-4 |
| Service metrics | API values equal direct PromQL results for fixture series | AC-2.1 |
| Alert evaluation | A rule goes to `firing` and back to `ok` against fixture series | AC-6.2 |
| **Schema drift** | The DDL the pinned collector creates matches the committed snapshot | ADR-0004, R1 |
| Degraded mode | With ClickHouse stopped, trace endpoints return `503 storage_unavailable` within the timeout while service endpoints keep working | NFR-1 |
| Retention | Created tables carry the configured TTL | NFR-4 |

Fixture telemetry is loaded the way production data arrives: through the pinned collector, using the Go OTLP exporter. That way the tests also exercise the exporter-managed schema. Tests live beside the code they cover (`apps/api/internal/...`) behind the `integration` build tag, so `go test ./...` stays fast, and they run with `go test -tags integration ./...` (`make test-integration`).
