# ADR-0002: Store traces and logs in ClickHouse

- **Status:** Accepted
- **Date:** 2026-10-29

## Context

Traces and logs are append-only, high-volume, and queried by time range plus a few filters (service, operation, severity, trace ID), with occasional free-text search (FR-3, FR-4). The store has to support cheap expiry (NFR-4), cope with a laptop-sized footprint, and offer a query interface that the API can build safely and test (NFR-3). We want **one** store for both signals, because correlation (FR-5) joins them on `TraceId` within a time window.

## Decision

Use **ClickHouse** (26.8 LTS, pinned) as the single store for traces and logs, written by the OpenTelemetry ClickHouse exporter.

## Consequences

**Positive**

- Columnar storage with per-column codecs compresses repetitive telemetry well, and range scans and aggregations over time are its core strength.
- Partition-by-day with TTL lets retention drop whole parts rather than delete rows (NFR-4).
- The exporter ships a well-tuned schema, including a bloom-filter index on `TraceId` and a trace-ID → time-range lookup table, which makes trace-by-ID cheap (AC-3.4).
- Plain SQL with bound parameters is easy to generate, review and unit-test from Go.
- Traces and logs live side by side, so trace → logs is one time-bounded query.

**Negative / accepted trade-offs**

- Text search is token-based (`hasToken` on a token bloom filter), not relevance-ranked full-text search. That is adequate for "find logs containing this word", not for ranked search.
- ClickHouse is a stateful service with real memory needs. Its footprint is measured in Phase 5 rather than assumed (risk R3).
- Good performance depends on querying along the table's sort key. Our query builders have to respect the physical design documented in [SPEC §7.1](../SPEC.md#71-clickhouse-telemetry-store).

## Alternatives considered

| Option | Why not |
|---|---|
| OpenSearch / Elasticsearch | Excellent text search, but a heavier JVM footprint and more operational tuning (shards, mappings), and weaker at the aggregations the overview and benchmarks need. |
| Grafana Loki + Tempo | Two more systems with two query languages (LogQL, TraceQL). Most of the project would become configuring someone else's stack instead of building the query layer. |
| PostgreSQL (+ partitioning or TimescaleDB) | One fewer service, but a row store is a poor fit for high-volume append and scan, and we would have to design the whole schema ourselves. |
| Jaeger with its own storage | Covers traces only, so logs would still need a home and correlation would cross systems. |

## Revisit when

Requirements shift to relevance-ranked log search, or to ingest volumes that need a ClickHouse cluster.
