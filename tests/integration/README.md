# Integration tests

The API's integration tests run against **real** PostgreSQL, ClickHouse and Prometheus servers in disposable containers ([testcontainers-go](https://golang.testcontainers.org/)). Nothing is mocked. They need Docker, but not the Compose stack. They live beside the code they cover, behind the `integration` build tag:

```bash
make test-integration
# or: cd apps/api && go test -tags integration -count=1 -p 1 ./...
```

## How the environment is built

- **ClickHouse** gets its schema from the pinned OpenTelemetry Collector, running the repository's own `telemetry/otel-collector.yaml`. The tests therefore see exactly the tables production uses. Fixtures are then inserted in the exporter's column layout: traces with errors and exception events, a trace without logs, a 30-span trace, and 1,200 traces and 1,050 logs that share timestamps.
- **Prometheus** starts on history backfilled with `promtool tsdb create-blocks-from openmetrics`. That gives deterministic request rates, error ratios and latency buckets instead of waiting for live samples.
- **Image versions** are pinned in `internal/testenv`, and `TestImagesMatchCompose` fails if they drift from `deployments/docker-compose.yml`.

## What is covered

| Concern | Where |
|---|---|
| Invalid authentication (wrong password, malformed body, no session, forged token) | `httpapi` · `TestAuthenticationFailures` |
| Expired and revoked sessions | `httpapi` · `TestAuthenticationFailures`; `postgres` · `TestUsersAndSessions`, `TestAuthServiceAgainstPostgres` |
| Login rate limiting | `httpapi` · `TestAuthenticationFailures` |
| Invalid date ranges | `httpapi` · `TestInvalidDateRanges` |
| Empty search results | `httpapi` · `TestEmptyResultsAndTracesWithoutLogs`; `clickhouse` · `TestEmptyWindowFindsNothing` |
| Large result sets and stable pagination with ties | `clickhouse` · `TestSearchTracesPaginatesLargeResultSets`, `TestSearchLogsPaginatesWithTies`; `httpapi` · `TestLargeResultSetsPaginate` |
| Huge traces are truncated, not unbounded | `clickhouse` · `TestGetTraceTruncatesLargeTraces` |
| Missing ClickHouse indexes: detected, and results stay correct | `clickhouse` · `TestMissingIndexDegradesButStaysCorrect` |
| Trace lookups actually use the TraceId index and partition pruning | `clickhouse` · `TestTraceLookupUsesItsIndexes` |
| Schema drift after a collector upgrade (ADR-0004) | `clickhouse` · `TestSchemaMatchesSnapshot` |
| Database connection failures (closed port, stopped server) | `postgres` · `TestUnreachableDatabaseIsUnavailable`; `clickhouse` · `TestDeadlinesAndConnectionFailures`; `prometheus` · `TestDeadlinesAndOutages`; `httpapi` · `TestDegradedModeAndOutages` |
| Query timeouts (deadline exceeded, server that never answers) | `clickhouse` · `TestDeadlinesAndConnectionFailures`; `prometheus` · `TestDeadlinesAndOutages`; `httpapi` · `TestDegradedModeAndOutages` |
| Degraded mode: one backend down, the rest still serve (NFR-1) | `httpapi` · `TestDegradedModeAndOutages` |
| A PostgreSQL outage is `503`, never a `401` | `httpapi` · `TestDegradedModeAndOutages` |
| Trace IDs with no matching logs | `httpapi` · `TestEmptyResultsAndTracesWithoutLogs`; `clickhouse` · `TestSearchLogs` |
| SQL and PromQL injection | `httpapi` · `TestInjectionAttemptsAreInert`; `clickhouse` · `TestSearchTracesFilters`, `TestSearchLogs`; `prometheus` · `TestPromQLInjectionIsInert` |
| Read-only mode cannot be escaped from a query | `clickhouse` · `TestQueriesCannotLeaveReadOnlyMode` |
| Authorization boundaries (every protected route) | `httpapi` · `TestAuthenticationFailures` (and the unit-level `TestEveryProtectedRouteNeedsASession`) |
| API values equal the equivalent PromQL (AC-2.1); no traffic is "no data", not zero (AC-2.2) | `prometheus` · `TestServicesMatchDirectPromQL`, `TestNoTrafficIsNoDataNotZero` |
| Migrations are idempotent and safe when run concurrently | `postgres` · `TestMigrationsAreIdempotentAndSafeToRunConcurrently` |
| Every response matches the OpenAPI contract | `httpapi` · every call goes through a contract validator |
