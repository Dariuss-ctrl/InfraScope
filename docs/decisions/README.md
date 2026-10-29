# Architecture decision records

Each significant decision is recorded as a short ADR: the context, the decision, its consequences, and the alternatives that were rejected. ADRs are immutable once accepted. A changed decision gets a new ADR that supersedes the old one, so the history of the reasoning is preserved.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-collector-centric-ingestion.md) | Ingest telemetry through the OpenTelemetry Collector, not the API | Accepted |
| [0002](0002-clickhouse-for-traces-and-logs.md) | Store traces and logs in ClickHouse | Accepted |
| [0003](0003-prometheus-for-metrics.md) | Store metrics in Prometheus via native OTLP ingestion | Accepted |
| [0004](0004-exporter-managed-clickhouse-schema.md) | Let the ClickHouse exporter own the telemetry schema for the MVP | Accepted |
| [0005](0005-alert-evaluation-in-the-api.md) | Evaluate alert rules inside the API | Accepted |
| [0006](0006-single-admin-session-auth.md) | Single admin account with server-side sessions for the MVP | Accepted |

To add a decision, copy [0000-template.md](0000-template.md), take the next number, and link it here.
