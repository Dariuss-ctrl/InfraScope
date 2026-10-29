# ADR-0004: Let the ClickHouse exporter own the telemetry schema for the MVP

- **Status:** Accepted
- **Date:** 2026-10-29

## Context

The OpenTelemetry ClickHouse exporter can create its database and tables on startup (`create_schema: true`). The alternative is to own the DDL as versioned migrations and set `create_schema: false`. Owning the schema allows custom projections and pre-aggregated views, but it duplicates the exporter's column contract, and that contract would then have to be kept in sync with every collector upgrade by hand.

## Decision

For the MVP, **the exporter creates and owns the schema** (`otel_traces`, `otel_traces_trace_id_ts` with its materialised view, and `otel_logs`), with:

- the collector version **pinned**,
- TTL set through the exporter's `ttl` option (from `TELEMETRY_TTL`),
- a **schema-drift test** (Phase 2): an integration test creates the schema with the pinned collector and compares `SHOW CREATE TABLE` output with a committed snapshot, so any upgrade that changes the schema fails CI and is reviewed deliberately.

## Consequences

**Positive**

- This is the fastest path to a working pipeline, and the upstream schema is already tuned (sort keys, bloom filters, trace-ID lookup table).
- The exporter's insert statements always match the tables, because the same component created them.

**Negative / accepted trade-offs**

- **Collector startup depends on ClickHouse availability**, because the DDL runs when the exporter starts. Compose waits for ClickHouse to be healthy before starting the collector. This coupling is the main reason to revisit the decision.
- **TTL is applied only when a table is created.** Changing `TELEMETRY_TTL` later does not alter existing tables. The runbook documents the `ALTER TABLE … MODIFY TTL` procedure.
- We cannot add our own projections or materialised views without first moving to owned DDL.

## Alternatives considered

| Option | Why not (now) |
|---|---|
| Owned DDL as migrations, `create_schema: false` | The right long-term shape, but it front-loads schema maintenance before the product proves its query patterns. |
| A hand-written schema of our own | Would require a custom exporter or insert path. That violates ADR-0001's "don't rebuild the write path". |

## Revisit when

We need pre-aggregated views (for example RED metrics from spans), we want the collector to start independently of ClickHouse, or the drift test flags an upstream change we don't want to take.
