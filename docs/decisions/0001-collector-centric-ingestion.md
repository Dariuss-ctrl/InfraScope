# ADR-0001: Ingest telemetry through the OpenTelemetry Collector, not the API

- **Status:** Accepted
- **Date:** 2026-10-29

## Context

InfraScope has to accept traces, metrics and logs from instrumented services (FR-1) and also serve interactive queries (FR-2 to FR-5). The two workloads behave very differently. Ingest is continuous, bursty and append-only. Queries are occasional, latency-sensitive and read-heavy. If the Go API did both, a burst of telemetry could starve investigations at exactly the moment someone needs them, and we would have to re-implement OTLP decoding, batching, retry and back-pressure ourselves.

## Decision

All telemetry enters through the **OpenTelemetry Collector** (contrib distribution, version pinned). The collector receives OTLP, limits memory, enriches resource attributes, batches inside each exporter's sending queue, and writes to ClickHouse (traces, logs) and Prometheus (metrics).

The **Go API never receives telemetry.** It only reads storage and manages configuration.

## Consequences

**Positive**

- Ingest and query scale and fail independently. A query storm cannot drop telemetry, and an ingest burst cannot slow the UI.
- We get battle-tested OTLP handling, retry, queueing and memory limiting without writing it.
- Any OTLP-capable SDK or agent works unchanged, so InfraScope is not tied to our demo services.
- The collector exposes its own metrics, which gives us self-observability of the write path for free (NFR-7).

**Negative / accepted trade-offs**

- There is one more component to configure, pin and monitor.
- Collector config is version-sensitive (component names and options change between releases), so the version is pinned and CI validates the config with the pinned binary.
- Write-path behaviour (batching, retry) is configured in YAML rather than code, and is tested through pipeline tests rather than unit tests.

## Alternatives considered

| Option | Why not |
|---|---|
| API exposes OTLP endpoints and writes to storage | Couples the two workloads and re-implements mature functionality. Hardest to make reliable. |
| Applications write directly to ClickHouse and Prometheus | Every service needs storage credentials and storage-specific code, and there is no central place to batch, limit or redact. |
| A message queue (Kafka) between collector and storage | A real pattern at scale, but unjustified for a single-node, self-hosted target. The collector's persistent queue covers short outages (Phase 4). |

## Revisit when

Sustained ingest exceeds what one collector can handle, or durability requirements go beyond what a disk-backed exporter queue provides.
