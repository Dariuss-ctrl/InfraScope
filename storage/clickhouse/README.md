# ClickHouse

ClickHouse stores traces and logs ([ADR-0002](../../docs/decisions/0002-clickhouse-for-traces-and-logs.md)).

## Schema ownership

The OpenTelemetry ClickHouse exporter creates the `otel` database and its tables when the collector starts (`create_schema: true` in [`telemetry/otel-collector.yaml`](../../telemetry/otel-collector.yaml)). See [ADR-0004](../../docs/decisions/0004-exporter-managed-clickhouse-schema.md) for why, and for the plan to move to owned migrations later.

| Table | Contents |
|---|---|
| `otel.otel_traces` | one row per span |
| `otel.otel_traces_trace_id_ts` | `TraceId → (Start, End)`, filled by a materialised view; bounds trace lookups |
| `otel.otel_logs` | one row per log record |

Column-level details and the query patterns they serve are in [SPEC §7.1](../../docs/SPEC.md#71-clickhouse-telemetry-store).

## Retention

Tables are partitioned by day and created with a TTL taken from `TELEMETRY_TTL` (default `72h`) and `ttl_only_drop_parts = 1`, so expiry drops whole parts instead of rewriting them. **The TTL is set only when a table is created.** To change it for existing tables, follow the [runbook](../../docs/runbook.md#disk-usage-keeps-growing).

## Server configuration

Files in `config.d/` are mounted into `/etc/clickhouse-server/config.d/`:

- `prometheus.xml` exposes the server's metrics on `:9363/metrics`, which Prometheus scrapes.

A read-only user for the API arrives in Phase 5 (`users.d/`).
