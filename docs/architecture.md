# InfraScope — Architecture

This document describes how InfraScope is put together and why. Requirements are defined in [SPEC.md](SPEC.md), and significant choices are recorded as [ADRs](decisions/README.md).

## 1. Design principles

1. **Separate ingest from query.** The OpenTelemetry Collector owns the write path, and the Go API owns the read path. Neither can overload the other ([ADR-0001](decisions/0001-collector-centric-ingestion.md)).
2. **Correlate on IDs, not timestamps.** Spans and logs share W3C trace context, so every cross-signal link is an exact ID lookup.
3. **Fail partially and visibly.** When a storage backend is down, the features it serves are marked unavailable and everything else keeps working.
4. **Bound everything.** Queues, query ranges, page sizes, timeouts and retention all have explicit limits.
5. **Measure before claiming.** Performance statements come from [benchmark reports](benchmarks/README.md), never from expectation.

## 2. Components

| Component | Technology (pinned) | Responsibility | Listens on | State |
|---|---|---|---|---|
| `demo-service` | Go 1.27, OpenTelemetry SDK | The system under observation: a toy shop API | `8082` | data in PostgreSQL `shop` |
| `otel-collector` | otelcol-contrib 0.164.0 | Receive OTLP; limit memory; enrich; batch; export | `4317` gRPC, `4318` HTTP, `13133` health, `8888` own metrics | exporter queues (in memory; on disk from Phase 4) |
| `clickhouse` | ClickHouse 26.8 (LTS) | Trace and log storage | `8123` HTTP, `9000` native, `9363` metrics | volume `clickhouse-data` |
| `prometheus` | Prometheus 3.15 | Metric storage (OTLP push + scrapes) | `9090` | volume `prometheus-data` |
| `postgres` | PostgreSQL 18 | `infrascope` (users, sessions, alert rules and state); `shop` (demo data) | `5432` | volume `postgres-data` |
| `api` | Go 1.27 (stdlib `net/http`) | Query and configuration API; alert evaluator | `8081` | stateless |
| `dashboard` | React 19 + Vite 8, served by nginx 1.30 | Investigation UI; reverse proxy for `/api/` | `8080` | stateless |
| `k6` *(profile `load`)* | k6 2.3 | Reproducible load | — | — |

## 3. Component view

```mermaid
flowchart LR
  k6["k6 load generator"] -->|HTTP| demo

  subgraph apps["Instrumented applications"]
    demo["demo-service<br/>Go + OTel SDK"]
    inv["inventory<br/>Phase 4"]
  end
  demo -->|"HTTP + traceparent"| inv
  demo -->|SQL| pgshop[("PostgreSQL<br/>db: shop")]

  demo -->|OTLP| col
  inv -->|OTLP| col
  api -->|"OTLP self-telemetry"| col

  subgraph pipeline["Telemetry pipeline"]
    col["OpenTelemetry Collector<br/>memory_limiter → resource → exporter queues"]
  end

  col -->|"traces + logs, native protocol, batched"| ch[("ClickHouse<br/>otel_traces · otel_logs")]
  col -->|"metrics, OTLP/HTTP"| prom[("Prometheus TSDB")]
  prom -.->|"scrape :8888"| col
  prom -.->|"scrape :9363"| ch

  subgraph product["InfraScope"]
    web["dashboard<br/>React SPA behind nginx"]
    api["Go query API<br/>queries · auth · alert evaluator"]
  end

  browser(["Browser"]) -->|HTTP| web
  web -->|"proxy /api/"| api
  api -->|"SQL, read-only"| ch
  api -->|PromQL| prom
  api -->|SQL| pgcfg[("PostgreSQL<br/>db: infrascope")]
```

`shop` and `infrascope` are two databases in one PostgreSQL instance. They have separate roles and are never mixed.

## 4. Key flows

### 4.1 Ingest path

```mermaid
sequenceDiagram
  autonumber
  participant D as demo-service (OTel SDK)
  participant C as OTel Collector
  participant CH as ClickHouse
  participant P as Prometheus
  D->>C: OTLP export of spans, logs and metrics, batched by the SDK
  C->>C: memory_limiter refuses data when over the memory limit
  C->>C: resource processor adds deployment.environment.name
  C->>C: exporter sending queue batches the data
  C->>CH: INSERT into otel_traces and otel_logs
  CH-->>CH: materialised view updates otel_traces_trace_id_ts
  C->>P: POST /api/v1/otlp/v1/metrics
  Note over C,CH: On failure the exporter retries with backoff and keeps data in its bounded queue. Data beyond capacity is dropped and counted.
```

- **Why exporter-side batching:** ClickHouse performs best with large inserts. The ClickHouse exporter documentation recommends the exporter's own `sending_queue.batch` over the standalone `batch` processor, because batches then sit in the same queue that retries and (from Phase 4) persists them.
- **Back-pressure:** when the collector is over its memory limit, `memory_limiter` makes the OTLP receiver return a retryable error. SDK exporters back off instead of the collector crashing.

### 4.2 Query path

```mermaid
sequenceDiagram
  participant B as Browser
  participant N as nginx
  participant A as Go API
  participant S as ClickHouse or Prometheus
  B->>N: GET /api/v1/traces?service=demo-service&min_duration_ms=500
  N->>A: proxy, with the API host re-resolved through Docker DNS
  A->>A: authenticate session, validate and clamp parameters
  A->>S: parameterised query with a context deadline
  alt backend answers in time
    S-->>A: rows
    A-->>B: 200 JSON with next_cursor
  else backend down or slow
    A-->>B: 503 storage_unavailable within the dependency timeout
  end
```

### 4.3 Correlation: trace → logs

```mermaid
sequenceDiagram
  participant UI as Dashboard
  participant A as Go API
  participant CH as ClickHouse
  UI->>A: GET /api/v1/traces/{traceId}
  A->>CH: SELECT Start, End FROM otel_traces_trace_id_ts WHERE TraceId = ?
  A->>CH: SELECT spans FROM otel_traces WHERE TraceId = ? AND Timestamp BETWEEN Start AND End
  A-->>UI: span tree with time bounds
  UI->>A: GET /api/v1/logs?trace_id={traceId}&from=Start&to=End
  A->>CH: SELECT FROM otel_logs WHERE TraceId = ? AND Timestamp BETWEEN ...
  A-->>UI: log records, each linking back to its span
```

The time bounds turn both lookups into partition-pruned reads instead of scans across the whole retention window.

### 4.4 Alert evaluation (Phase 4)

```mermaid
sequenceDiagram
  participant T as Evaluator (ticker, 30 s)
  participant PG as PostgreSQL
  participant P as Prometheus
  loop every 30 s
    T->>PG: load enabled rules and current state
    T->>P: instant PromQL query per rule
    T->>T: next state = f(state, value, threshold, for)
    T->>PG: update state and append event in one transaction
  end
```

The state transition is a pure function, which is why it can be tested exhaustively with table-driven tests ([ADR-0005](decisions/0005-alert-evaluation-in-the-api.md)).

## 5. Deployment topology

Everything runs as one Docker Compose project named `infrascope`, on its default user-defined network. Service names double as DNS names.

**Published ports.** All of them bind to `${INFRASCOPE_BIND}`, which defaults to `127.0.0.1`:

| Port | Service | Why it is published |
|---|---|---|
| `8080` | dashboard | the product UI and API entry point |
| `8082` | demo-service | drive the demo and toggle faults by hand |
| `4317`, `4318` | otel-collector | send OTLP from processes running on the host |

`deployments/docker-compose.debug.yml` additionally exposes Prometheus (`9090`), ClickHouse (`8123`, `9000`), PostgreSQL (`5432`), the API (`8081`) and the collector's health and metrics ports (`13133`, `8888`), on localhost only.

**Startup ordering and healthchecks**

| Service | Healthcheck | Waits for |
|---|---|---|
| postgres | `pg_isready -h 127.0.0.1` (TCP, so it only passes once init scripts have finished) | — |
| clickhouse | `wget --spider http://127.0.0.1:8123/ping` | — |
| prometheus | `wget --spider http://127.0.0.1:9090/-/ready` | — |
| otel-collector | none (scratch image). Readiness is reported by the API's status endpoint. | clickhouse healthy (the exporter creates the schema at startup, see [ADR-0004](decisions/0004-exporter-managed-clickhouse-schema.md)) |
| api | `api healthcheck` (the binary probes its own `/healthz`; the image is distroless) | postgres healthy, **but not** ClickHouse or Prometheus (degraded mode is by design) |
| demo-service | `demo-service healthcheck` | postgres healthy, collector started |
| dashboard | `wget --spider http://127.0.0.1:8080/healthz` | api started |

## 6. Failure modes

| Failure | Effect | Detection | Recovery |
|---|---|---|---|
| ClickHouse down | Trace and log endpoints return `503 storage_unavailable`; the overview still works. The collector queues and retries, and drops data after queue capacity or the 300 s retry window is used up. | `/api/v1/status`, dashboard banner, `otelcol_exporter_send_failed_*`, `otelcol_exporter_queue_size` | Restart ClickHouse; queued data flushes ([runbook](runbook.md#clickhouse-is-down-or-unhealthy)) |
| Prometheus down | The overview and alert evaluation stop (`503 metrics_unavailable`); traces and logs still work. The collector retries metric exports. | status endpoint, banner | Restart; alert state resumes from PostgreSQL |
| Collector down | No new telemetry arrives; stored data stays queryable. SDK exporters retry, then drop. | status endpoint; `up{job="otel-collector"} == 0` | Restart the collector |
| PostgreSQL down | Login, sessions and alerting fail; `/readyz` returns 503 because PostgreSQL is a required dependency. | `/readyz`, status endpoint | Restart; no telemetry is lost (it lives elsewhere) |
| API down | The dashboard's static assets still load; `/api/` returns 502. | status banner fails to load; container health | Restart. nginx re-resolves the API's address on each request, so no nginx restart is needed. |
| Collector memory pressure | `memory_limiter` refuses data and SDKs back off. | `otelcol_receiver_refused_*` | Raise the limit or lower the load ([runbook](runbook.md#collector-is-refusing-or-dropping-data)) |
| Disk full | ClickHouse inserts and Prometheus appends fail. | runbook disk checks | Lower the TTL or retention, or drop old partitions ([runbook](runbook.md#disk-usage-keeps-growing)) |

## 7. Configuration

| Where | What |
|---|---|
| `.env` (from `.env.example`, git-ignored) | Credentials, published ports, bind address, telemetry TTL |
| `telemetry/otel-collector.yaml` | Receivers, processors, exporters, queues, internal telemetry |
| `telemetry/prometheus.yaml` | Scrape targets, OTLP translation, retention, out-of-order window |
| `storage/clickhouse/config.d/` | ClickHouse server overrides (metrics endpoint) |
| `storage/postgres/init/` | First-start creation of the `infrascope` and `shop` databases and roles |
| `deployments/docker-compose.yml` | Images (pinned), wiring, healthchecks, volumes |

## 8. Self-observability

| Component | Signal source | Collected by |
|---|---|---|
| OTel Collector | Internal metrics at `:8888` (received, refused, sent and failed items; queue size and capacity) | Prometheus scrape |
| ClickHouse | Built-in endpoint at `:9363` (`ClickHouseMetrics_*`, `ClickHouseProfileEvents_*`) | Prometheus scrape |
| Prometheus | Its own `/metrics` (TSDB head series, samples appended) | Prometheus self-scrape |
| API | OTel traces, metrics and logs as `infrascope-api` (from Phase 2) | The pipeline it serves |
| All | `GET /api/v1/status` (per-dependency reachability and latency) | Dashboard banner, smoke test |

Monitoring the platform with itself has an obvious blind spot: if the pipeline is down, its own telemetry is too. The status endpoint probes dependencies directly, without going through the pipeline, and so it covers that gap.
