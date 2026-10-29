# ADR-0003: Store metrics in Prometheus via native OTLP ingestion

- **Status:** Accepted
- **Date:** 2026-10-29

## Context

The service overview (FR-2) and alerting (FR-6) need request rate, error rate and latency percentiles over time windows. These come from the OpenTelemetry HTTP request-duration histogram. We also want to scrape the platform's own components (NFR-7). The ClickHouse exporter can store metrics too, but its metrics support is less mature than its trace and log support, and we would have to implement histogram quantile maths in SQL.

## Decision

Use **Prometheus 3** (pinned) for metrics.

- The collector pushes OTLP metrics to Prometheus's **native OTLP endpoint** (`--web.enable-otlp-receiver`). There is no remote-write exporter and no scraping of a collector-side Prometheus exporter.
- Prometheus also scrapes the collector's and ClickHouse's own metrics endpoints.
- `service.name` is promoted to the `service_name` label. Only allow-listed resource attributes become labels.
- A 30-minute out-of-order window accepts slightly late pushed samples.
- Retention is set in `prometheus.yaml` (`storage.tsdb.retention`), which replaced the deprecated retention flags.

## Consequences

**Positive**

- PromQL (`rate`, `histogram_quantile`) expresses every overview and alert signal directly, and it is a widely transferable skill.
- Native OTLP ingestion removes a translation hop and an extra component.
- The same Prometheus monitors the platform itself.

**Negative / accepted trade-offs**

- **There are two query backends**, which adds API complexity and two degraded modes (risk R2). We contain this behind per-backend repository interfaces.
- OTLP → Prometheus name translation (dots to underscores, unit suffixes) has to be pinned and tested (risk R4).
- Cardinality has to be governed deliberately. High-cardinality context belongs on spans and logs, not on metric labels (risk R6).
- Exemplars (metric → trace links) are deferred to future work.

## Alternatives considered

| Option | Why not |
|---|---|
| Metrics in ClickHouse as well (single backend) | Less mature metrics path in the exporter, no PromQL, and percentile maths over histogram buckets would have to be re-implemented in SQL. |
| VictoriaMetrics or Mimir | PromQL-compatible and efficient, but less standard and more to learn and operate, for no benefit at single-node scale. |
| Collector `prometheus` exporter scraped by Prometheus | An extra hop that loses OTLP semantics. Native OTLP ingestion is now built in. |

## Revisit when

We want metrics → trace navigation via exemplars, or a single backend becomes worth the cost of custom percentile queries.
