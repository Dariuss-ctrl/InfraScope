# InfraScope — Product & Engineering Specification

| | |
|---|---|
| **Status** | Draft v0.5 · Phase 4 (DeployRadar) |
| **Last updated** | 2026-11-15 |
| **Related** | [Architecture](architecture.md) · [Decision records](decisions/README.md) · [Runbook](runbook.md) · [Demo walkthrough](demo-script.md) · [Benchmark method](benchmarks/README.md) · [API contract](../apps/api/openapi.yaml) |

---

## 1. Overview

### 1.1 Problem

When a request is slow or fails, the evidence is split across three places. Metrics show *that* something is wrong, traces show *where* the time went, and logs explain *why*. Developers usually connect the three by hand, matching timestamps across tools, and that is the slowest part of most investigations.

### 1.2 Product

InfraScope is a self-hostable observability platform. It collects traces, metrics and logs through OpenTelemetry and stores them in purpose-fit databases: ClickHouse for traces and logs, Prometheus for metrics. On top of that data it provides **one investigation workflow**: service health → a slow or failed request → the span that consumed the time → the logs written during that span.

The differentiator is **correlation, not charts**. Every log record and span carries the W3C trace context, and InfraScope uses that shared ID so that moving between signals is a single click instead of a timestamp search.

### 1.3 Scope summary

- **In scope:** OTLP ingestion via the OpenTelemetry Collector, service overview (rate, errors, duration), trace search and waterfall, log search, trace↔log navigation, threshold alerting, a demo workload with injectable faults, one-command self-hosting, and self-monitoring.
- **Out of scope:** see [§2.2](#22-non-goals).

---

## 2. Goals and non-goals

### 2.1 Goals

| ID | Goal | How we know |
|---|---|---|
| G1 | A developer can self-host the full stack with one command on a laptop. | `make up` and `make smoke` pass on a clean clone (CI runs the same). |
| G2 | The path from symptom to root cause is short and needs no copy-pasting of IDs. | The e2e test walks service overview → trace → logs → trace using links alone. |
| G3 | Every performance statement is backed by a reproducible measurement. | Each number in the README links to a report produced by the [benchmark method](benchmarks/README.md). |
| G4 | The platform observes itself. | Collector, ClickHouse, Prometheus and the API all expose health and metrics that can be inspected (NFR-7). |
| G5 | The codebase shows production engineering practice. | Tests at every layer, ADRs for significant decisions, CI gates, documented operations. |

### 2.2 Non-goals

- **Multi-tenancy and RBAC.** There is a single administrator account.
- **High availability, horizontal scaling, Kubernetes.** The target is one node running Docker Compose.
- **Replacing Grafana, Jaeger or a commercial APM.** Feature parity is explicitly not a goal.
- **Long-term storage, downsampling or archival.** Data expires by TTL.
- **Auto-instrumentation for arbitrary languages.** Any OTLP source can send data, but only the Go demo services are tested.
- **Notification integrations** beyond an optional generic webhook (stretch).
- **A custom query language.** Users filter through structured parameters.

---

## 3. Users and scenarios

**Personas**

- **Application developer:** runs services locally or on a single VM and needs to understand slow or failing requests.
- **Platform operator:** deploys InfraScope, keeps it healthy and controls its resource use. On a personal setup this is usually the same person.

**User stories**

| ID | Story |
|---|---|
| US-1 | As a developer, I want to see which services are slow or erroring right now, so I know where to look first. |
| US-2 | As a developer, I want to find example requests that were slow or failed, filtered by service, operation, duration and status. |
| US-3 | As a developer, I want to see a request as a span waterfall, so I can tell which operation consumed the time. |
| US-4 | As a developer, I want to jump from a trace to the logs written during it, and from an error log back to its trace. |
| US-5 | As a developer, I want to be told when error rate or latency crosses a threshold I set. |
| US-6 | As an operator, I want to deploy with one command, bound disk usage, and check the health of the platform itself. |

---

## 4. Functional requirements

Each requirement lists its acceptance criteria (AC) and the roadmap phase that delivers it ([§12](#12-roadmap)). The ACs are written to be automatable, and [§11](#11-testing-strategy) maps each one to a test layer.

### FR-1 Telemetry ingestion · Phase 1

The OpenTelemetry Collector receives OTLP over gRPC (`4317`) and HTTP (`4318`) for traces, metrics and logs. Traces and logs are written to ClickHouse and metrics are written to Prometheus through its native OTLP endpoint. The InfraScope API never receives telemetry ([ADR-0001](decisions/0001-collector-centric-ingestion.md)).

- **AC-1.1** One request to the demo service produces a trace with an HTTP server span and at least one database client span, linked by parent/child IDs.
- **AC-1.2** Log records written while handling that request carry the trace's `TraceId` and the active `SpanId`.
- **AC-1.3** The request is reflected in `http_server_request_duration_seconds_*` series in Prometheus, labelled with `service_name`.
- **AC-1.4** The pipeline test ([§11](#11-testing-strategy), [`tests/pipeline`](../tests/pipeline/README.md)) finds the trace, the logs and the metric, each within a polling timeout of 60 s. That number is a test timeout, not a latency claim; actual ingest-to-query lag is measured under NFR-2. In Phase 1 the test queries ClickHouse and Prometheus directly. From Phase 2 it goes through the InfraScope API.
- **AC-1.5** The collector's own metrics (accepted, refused, sent and failed items, queue size) are scraped by Prometheus.

### FR-2 Service overview · Phase 2 (API), Phase 3 (UI)

The overview lists every service that reported data in the selected time range with **request rate**, **error rate** (share of requests with HTTP status 5xx) and **latency p50/p95/p99**, derived from the request-duration histogram in Prometheus. Time ranges are the last 15 min, 1 h, 6 h or 24 h, or a custom range; the chart step is chosen automatically.

- **AC-2.1** API values match the equivalent PromQL run directly against Prometheus, to within floating-point rounding. This is checked by an integration test.
- **AC-2.2** A service with no traffic in the range shows *no data*, not zero.
- **AC-2.3** Clicking a point on the latency or error chart opens the trace explorer filtered to that service and time window (see FR-5).

### FR-3 Trace explorer · Phase 2 (API), Phase 3 (UI)

**Search:** filters for service, operation (span name), minimum and maximum duration, status (`error` | `ok` | any) and time range. The filters apply to each trace's root span, which represents the request as a whole. Results can be sorted by start time or by duration and are paginated with an opaque cursor. Each result shows the trace ID, root operation, root service, start time, duration, span count and an error flag.

**Detail:** the full span tree drawn as a waterfall. For each span it shows service, name, kind, start offset, duration, status, attributes and events (including recorded exceptions). Database spans show the statement from `db.query.text`. Values are bound as parameters, so they are not part of the statement text.

- **AC-3.1** A trace with *N* spans renders all *N*, nested by `ParentSpanId`. Spans whose parent is missing (not yet ingested, or expired) appear at the root level with a visible "missing parent" marker.
- **AC-3.2** Spans with status `Error` are highlighted, and their exception events can be read without leaving the page.
- **AC-3.3** Paging through search results has no duplicates or gaps. Ordering is stable because the cursor is a tie-broken key: `(sort value, TraceId)`.
- **AC-3.4** A trace lookup by ID reads the trace's time bounds from the lookup table first ([§7.1](#71-clickhouse-telemetry-store)), so it never scans the whole retention window.

### FR-4 Log explorer · Phase 2 (API), Phase 3 (UI)

Logs can be filtered by time range, service, minimum severity, free text in the body, trace ID and span ID, and are shown newest first with cursor pagination. Each row shows the timestamp, service, severity and body, with the trace and span IDs as links and the attributes expandable.

- **AC-4.1** The severity filter uses OpenTelemetry `SeverityNumber` ranges (TRACE 1–4, DEBUG 5–8, INFO 9–12, WARN 13–16, ERROR 17–20, FATAL 21–24), so text variants such as `warn` and `WARNING` behave the same.
- **AC-4.2** Text search is a case-insensitive token match on the body.
- **AC-4.3** The server enforces limits on the queried time range and page size (see NFR-2, *guard rails*) and rejects violations with a `400 invalid_argument` that names the field.

### FR-5 Correlation · Phase 3

- **Trace → logs:** the trace detail page lists the log records with that `TraceId`, optionally narrowed to the selected span. The query reuses the trace's time bounds.
- **Log → trace:** any log record with a `TraceId` links to the trace detail page. If the trace is no longer available (expired or never exported), the page says so explicitly instead of showing an error.
- **Overview → traces:** see AC-2.3. Clicking the error chart also pre-selects `status=error`.
- **AC-5.1** Every view keeps its filters in the URL, so an investigation can be bookmarked or shared and the browser's back and forward buttons work.
- **AC-5.2** The e2e test completes the round trip overview → trace → logs → trace without typing an ID.

### FR-6 Alerting · Phase 5

Threshold rules are stored in PostgreSQL and evaluated by the API ([ADR-0005](decisions/0005-alert-evaluation-in-the-api.md)).

- **Rule fields:** name, service, signal (`error_rate` | `latency_p95` | `request_rate`), comparator (`>` | `<`), threshold, evaluation window (1–60 min), `for` duration (how long the condition must hold), enabled.
- **Evaluation:** every 30 s. Each rule becomes a PromQL query. Each rule has a state machine `ok → pending → firing → ok`, and every transition is recorded as an event. Rule state survives an API restart.
- **UI:** a list of active and recent alerts. Each alert links to the service overview at the time it fired.
- **AC-6.1** Table-driven unit tests cover every state transition, including flapping around the threshold and a rule edited or disabled while firing.
- **AC-6.2** An integration test loads fixture series into Prometheus and observes a rule go to `firing` and then back to `ok`.
- **AC-6.3** Invalid rules are rejected with `400 invalid_argument` and a list of field errors.
- **Stretch:** webhook notification with retry and backoff.

### FR-7 Self-hosting · Phase 0, maintained every phase

- The whole stack starts with `make up`, or the equivalent single `docker compose` command for machines without Make.
- All configuration lives in version-controlled files plus a single `.env` file for secrets and ports.
- Every long-running container has a healthcheck, and startup ordering uses health conditions.
- `make reset` removes all data volumes.
- **AC-7.1** On a clean clone with Docker installed, `cp .env.example .env && make up && make smoke` succeeds. CI runs the same steps.
- **AC-7.2** Only the dashboard, the demo service and the OTLP ports are published, and they bind to `127.0.0.1` by default ([§10](#10-security-model)).

### FR-8 Demonstration workload · Phase 1 (S1), Phase 4 (S4), Phase 5 (S2, S3)

A small instrumented Go service (`demo-service`, a toy shop) and a k6 load script generate realistic traffic and reproducible failures. The scenarios are described in [§9](#9-demo-service-and-fault-scenarios).

- **AC-8.1** `make load` runs the documented request mix ([benchmarks](benchmarks/README.md#reference-workload)) at a configurable rate and duration.
- **AC-8.2** Each scenario (S1–S4) can be turned on and reset with a documented command, and is diagnosable through the InfraScope UI alone.

### FR-9 DeployRadar: deployments and their effect on health · Phase 4

GitHub `deployment_status` webhooks become deployment records. After each successful deployment, the API compares the service's error rate and p95 latency before and after it, in Prometheus, and stores a verdict with the evidence behind it ([ADR-0007](decisions/0007-deployment-health-comparison.md)). Ingestion is unchanged: a deployment is matched to its metrics by the `service_name` and `deployment_environment_name` labels, and no metric data is copied into PostgreSQL.

- **Intake:** `POST /api/v1/webhooks/github`, authenticated by `X-Hub-Signature-256` (HMAC-SHA256 of the body with `INFRASCOPE_GITHUB_WEBHOOK_SECRET`). Every verified delivery is stored before it is processed. A repeated delivery ID is answered as a duplicate unless its first attempt failed, so GitHub's redeliver button still works. The service is `deployment.payload.service`, falling back to the repository name; the environment is the status's, falling back to the deployment's.
- **Precedence:** statuses are applied by their own time, not their arrival order. A non-terminal status never replaces a terminal one (`success`, `failure`, `error`), and `inactive` is recorded but keeps the outcome. The history keeps every delivered status, including the ones not applied.
- **Comparison:** the first `success` at T queues one comparison: before `[T−W, T)`, after `[T+S, T+S+W)` (`INFRASCOPE_DEPLOYRADAR_WINDOW`, default 10 min; `INFRASCOPE_DEPLOYRADAR_SETTLE`, default 2 min). Once the after window has closed, a worker in the API measures requests, 5xx responses and p95 latency with `increase()` over each window, and stores the values with the exact PromQL, evaluation time and range behind each.
- **Verdict:** `insufficient_data` when either window has fewer than `INFRASCOPE_DEPLOYRADAR_MIN_REQUESTS` (default 200) requests, or no series. Otherwise, per signal: the error rate by a two-proportion z-test (p < 0.01 and a change of at least one percentage point), and p95 of non-5xx responses (failures are often fast, so counting them would hide a slowdown) by a ratio of 1.25 and at least 25 ms. The overall verdict is a regression if either signal regressed, otherwise an improvement if either improved, and comes with a confidence. Two caveats keep the verdict but cap its confidence at 0.5 and say why: request counts that differ by more than ×2 between the windows, and another deployment of the same service inside a window.
- **UI:** a Deployments view lists deployments with their state and verdict. A deployment's page leads with the verdict, its reasons and the most affected route, then the before/after table, the same counts per `http_route`, the PromQL behind every number, the status history, and links to the failing traces and error logs of the after window. The service overview marks each deployment on its charts, coloured by verdict, and links to it.
- **AC-9.1** Signature verification rejects a wrong secret, a tampered body, a missing header and a prefix other than `sha256=`, and nothing is stored for a rejected request.
- **AC-9.2** A repeated delivery is not applied twice. Concurrent and out-of-order statuses for one deployment converge on the same final state, and a late `in_progress` never un-finishes a deployment (integration tests against PostgreSQL).
- **AC-9.3** Against backfilled Prometheus history with a known error-rate jump at the deployment, the engine stores `regression` with the counts and the queries, and a low-traffic service gets `insufficient_data` (integration test).
- **AC-9.4** On the running stack, `make demo-deploy V=v2 ERROR_RATE=0.2` under `make load` produces a `regression` verdict, shown on the deployment's page and through the API.

---

## 5. Non-functional requirements

| ID | Requirement | Target / approach | Verified by |
|---|---|---|---|
| **NFR-1** | **Reliability: degraded mode.** Losing one storage backend degrades the matching features instead of breaking the product. | If ClickHouse is down, the service overview (Prometheus) still works and the trace and log views show a *storage unavailable* state. If Prometheus is down, the reverse happens. The API answers within the per-dependency timeout (default 2 s) with `503 storage_unavailable` or `503 metrics_unavailable`; it never hangs. `GET /api/v1/status` reports each dependency, and the dashboard shows a banner. During short outages the collector queues telemetry and retries, up to the queue capacity. From Phase 5 the queue is persisted to disk so it survives a collector restart. | Integration test that stops the ClickHouse container; e2e test for the banner; Phase 5 outage checklist ([runbook](runbook.md)). |
| **NFR-2** | **Performance: measured, not promised.** | No latency or throughput figure is claimed until it has been measured. The [benchmark method](benchmarks/README.md) records API p50/p95/p99 per endpoint, ingest-to-query lag, collector refused or dropped items, and container CPU and memory at a documented workload and on documented hardware. **Guard rails** are design limits, not performance claims: page size ≤ 500 (default 50), search range ≤ 24 h by default (configurable), and a ClickHouse `max_execution_time` of 10 s for every API query, so no single request can monopolise the database. | k6 runs per release; reports kept in `docs/benchmarks/reports/`. |
| **NFR-3** | **Security.** | Login is required for every API route except health, status, login and the GitHub webhook, which is authenticated by its signature instead. Sessions are server-side, using random 256-bit tokens stored hashed, in an `HttpOnly`, `SameSite=Strict` cookie that is `Secure` when served over TLS. Passwords are stored as bcrypt hashes. Login attempts are rate-limited. Databases and Prometheus are never published. Secrets exist only in `.env` (git-ignored) and in server-side environment variables; the frontend never receives a credential. All ClickHouse queries use bound parameters, and the API uses a read-only ClickHouse user from Phase 5. Telemetry content is treated as untrusted input ([§10](#10-security-model)). | Unit tests for query building (no string concatenation of user input); integration tests for authentication; `govulncheck` and `npm audit` in CI. |
| **NFR-4** | **Data retention.** Telemetry cannot grow without bound. | Traces and logs: table TTL from `TELEMETRY_TTL` (default `72h`). Partitions are daily and `ttl_only_drop_parts` is set, so expired data is removed by dropping whole parts. Metrics: Prometheus keeps 7 days or 2 GB, whichever is reached first. The collector's queue is bounded. | `SHOW CREATE TABLE` shows the TTL (integration test); the runbook includes a disk-usage check. |
| **NFR-5** | **Testing.** | Unit, integration, pipeline, end-to-end and load tests ([§11](#11-testing-strategy)). Coverage is reported, not targeted. | CI on every pull request. |
| **NFR-6** | **Reproducibility.** | Image tags and dependency versions are pinned and lockfiles are committed. Demo seed data is deterministic (fixed RNG seed). Every operation runs through a documented command, and benchmark reports record the hardware and versions used. | CI starts the stack from a clean checkout. |
| **NFR-7** | **Self-observability.** | Prometheus scrapes the collector's internal metrics, ClickHouse's built-in metrics endpoint and itself. The API is instrumented with OpenTelemetry as `infrascope-api` and appears in the dashboard under a *Platform* group. The runbook lists the signals to check first. | Platform view in the UI (Phase 5); the runbook's checks reference real metric names. |

---

## 6. Architecture summary

The full design is in **[architecture.md](architecture.md)**. In short:

1. Instrumented services export OTLP to the **OpenTelemetry Collector**. The collector limits memory, adds resource attributes and batches inside each exporter's sending queue.
2. Traces and logs go to **ClickHouse** ([ADR-0002](decisions/0002-clickhouse-for-traces-and-logs.md)). Metrics go to **Prometheus** through its OTLP endpoint ([ADR-0003](decisions/0003-prometheus-for-metrics.md)).
3. The **Go query API** reads from ClickHouse, Prometheus and PostgreSQL (configuration and alert state) and serves JSON to the **React dashboard**. nginx serves the dashboard and proxies `/api/`, so the browser only ever talks to one origin.
4. The API evaluates alert rules on a timer and records state in PostgreSQL.

---

## 7. Data model

### 7.1 ClickHouse telemetry store

The OpenTelemetry ClickHouse exporter creates and owns the tables, using the pinned collector version ([ADR-0004](decisions/0004-exporter-managed-clickhouse-schema.md)). The database is `otel`. The columns InfraScope relies on:

**`otel_traces`**: one row per span.

| Column | Type | Use |
|---|---|---|
| `Timestamp` | `DateTime64(9)` | span start |
| `TraceId`, `SpanId`, `ParentSpanId` | `String` | tree construction (root span: `ParentSpanId = ''`) |
| `ServiceName` | `LowCardinality(String)` | filtering, grouping |
| `SpanName`, `SpanKind` | `LowCardinality(String)` | operation filter |
| `Duration` | `UInt64` (ns) | duration filter and sort |
| `StatusCode`, `StatusMessage` | `String` | error flag |
| `SpanAttributes`, `ResourceAttributes` | `Map(LowCardinality(String), String)` | detail view (`http.route`, `db.query.text`, …) |
| `Events.*` | `Nested` | exceptions and annotations |

Engine layout: `PARTITION BY toDate(Timestamp)`, `ORDER BY (ServiceName, SpanName, toDateTime(Timestamp))`, a bloom-filter skip index on `TraceId` and a min-max index on `Duration`.

**`otel_traces_trace_id_ts`**, which a materialised view keeps up to date with `(TraceId, Start, End)` for every trace and is ordered by `TraceId`. A trace lookup reads this small table first and then queries `otel_traces` with `TraceId = ? AND Timestamp BETWEEN Start AND End`. That prunes partitions instead of scanning the whole retention window (AC-3.4).

**`otel_logs`**: one row per log record. It has `Timestamp`, `TraceId`, `SpanId`, `SeverityText`, `SeverityNumber`, `ServiceName`, `Body`, `LogAttributes` and `ResourceAttributes`. It is ordered by `(toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)`, with skip indexes on `TraceId` and on `lower(Body)`. The exporter builds these as full-text `text` indexes on ClickHouse versions that support them (26.8 does) and as bloom filters otherwise. The exact DDL for the pinned versions is committed in `apps/api/internal/clickhouse/testdata/schema/`, and the schema-drift test compares against it.

**Query patterns → physical design**

| Query | Served by |
|---|---|
| Trace search by service, operation and time | primary-key prefix `(ServiceName, SpanName)` plus daily partition pruning |
| Trace by ID | lookup table (ordered by `TraceId`), then a time-bounded read with the `TraceId` bloom filter |
| Logs by time and service | primary key `(toStartOfFiveMinutes(Timestamp), ServiceName, …)` |
| Logs for a trace | `TraceId` bloom filter, time-bounded by the trace's `[Start, End]` |
| Log text search | token index on `lower(Body)` (`hasToken`) |

### 7.2 Prometheus metrics

Metrics arrive via OTLP and are translated with Prometheus's default `UnderscoreEscapingWithSuffixes` strategy. The resource attribute `service.name` is promoted to the label `service_name`. The overview uses the OpenTelemetry semantic-convention histogram `http.server.request.duration`, which becomes:

```promql
# request rate
sum by (service_name) (rate(http_server_request_duration_seconds_count[$window]))

# error rate (5xx share)
  sum by (service_name) (rate(http_server_request_duration_seconds_count{http_response_status_code=~"5.."}[$window]))
/ sum by (service_name) (rate(http_server_request_duration_seconds_count[$window]))

# latency p95
histogram_quantile(0.95, sum by (service_name, le) (rate(http_server_request_duration_seconds_bucket[$window])))
```

The exact series and label names are confirmed against real data in Phase 1, and an integration test pins them (risk R4).

### 7.3 PostgreSQL configuration store

The `infrascope` database is managed by versioned migrations in `apps/api/internal/postgres/migrations/` (Phase 2). They are embedded in the API binary and applied at startup under a PostgreSQL advisory lock, so instances starting together cannot race. The `shop` database belongs to the demo service and is never read by InfraScope.

| Table | Key columns | Notes |
|---|---|---|
| `users` *(Phase 2)* | `id`, `username` (unique), `password_hash`, `created_at`, `updated_at` | The admin is created or updated from `.env` at startup. |
| `sessions` *(Phase 2)* | `token_hash` (PK, SHA-256 of the token), `user_id`, `created_at`, `expires_at` | Server-side sessions make logout and revocation real. |
| `webhook_deliveries` *(Phase 4)* | `delivery_id` (PK), `event`, `payload`, `received_at`, `outcome`, `error`, `processed_at` | Every verified webhook, stored before it is processed. The key makes redeliveries idempotent. |
| `deployments` *(Phase 4)* | `id`, `provider` + `provider_deployment_id` (unique), `service`, `environment`, `ref`, `sha`, `state`, `state_at`, `finished_at` | `service` and `environment` hold Prometheus label values, so no service registry is needed. |
| `deployment_changes` *(Phase 4)* | `id`, `deployment_id`, `delivery_id`, `state`, `state_at`, `applied` | Append-only status history, including the statuses the precedence rule did not apply. |
| `deployment_comparisons` *(Phase 4)* | `deployment_id` (unique), the two windows, request and error counts and p95 per window, verdicts, `confidence`, `reasons`, `queries`, `routes` (per-route counts, migration 0003), `status`, `attempts`, `claimed_at` | The measured values and the exact PromQL behind them; metric series stay in Prometheus. |
| `alert_rules` | `id`, `name` (unique), `service`, `signal`, `comparator`, `threshold`, `window_seconds`, `for_seconds`, `enabled`, `created_at`, `updated_at` | `CHECK` constraints mirror API validation. |
| `alert_rule_state` | `rule_id` (PK), `state`, `since`, `last_value`, `last_evaluated_at` | Lets the evaluator resume after a restart. |
| `alert_events` | `id`, `rule_id`, `state`, `value`, `observed_at` | History; index on `(rule_id, observed_at DESC)`. |

---

## 8. API surface

The **[OpenAPI 3.1 contract](../apps/api/openapi.yaml) is the source of truth**. This section summarises it.

**Conventions**

- JSON in and out. Timestamps are RFC 3339 strings. Span and trace durations are integer nanoseconds (`duration_ns`), as stored. Latencies aggregated from metrics are float seconds (`*_seconds`), as Prometheus reports them.
- Time-range parameters are `from` and `to`, both RFC 3339. If omitted, the range is the last hour.
- Pagination: `limit` (default 50, maximum 500) plus an opaque `cursor`. Responses include `next_cursor` when more results exist.
- Error envelope: `{"error": {"code": "...", "message": "...", "fields": [...]}}`.

| Code | HTTP | Meaning |
|---|---|---|
| `invalid_argument` | 400 | validation failed (`fields` lists each problem; unknown or repeated parameters are rejected) |
| `unauthenticated` | 401 | no, invalid or expired session |
| `forbidden` | 403 | a state-changing request from another origin |
| `not_found` | 404 | unknown trace, service, … |
| `invalid_signature` | 401 | a webhook's `X-Hub-Signature-256` does not match its body |
| `payload_too_large` | 413 | a webhook body over 1 MiB |
| `rate_limited` | 429 | too many login attempts |
| `not_implemented` | 501 | endpoint defined in the contract but not built yet |
| `storage_unavailable` | 503 | ClickHouse unreachable |
| `metrics_unavailable` | 503 | Prometheus unreachable |
| `database_unavailable` | 503 | PostgreSQL unreachable. A session that cannot be checked is a 503, never a 401, so an outage logs nobody out. |
| `webhooks_disabled` | 503 | no `INFRASCOPE_GITHUB_WEBHOOK_SECRET` is set |
| `query_timeout` | 504 | the query exceeded its time limit (`INFRASCOPE_QUERY_TIMEOUT`, default 10 s) |
| `internal` | 500 | unexpected failure (details are logged, never returned) |

**Endpoints**

| Method & path | Auth | Backend | Phase |
|---|---|---|---|
| `GET /healthz` | — | none (liveness) | 0 ✅ |
| `GET /readyz` | — | all dependencies (readiness) | 0 ✅ |
| `GET /api/v1/status` | — | all dependencies (dashboard banner) | 0 ✅ |
| `POST /api/v1/auth/login`, `POST /api/v1/auth/logout` | —, session | PostgreSQL | 2 ✅ |
| `GET /api/v1/auth/me` | session | PostgreSQL | 2 ✅ |
| `GET /api/v1/services` | session | Prometheus | 2 ✅ |
| `GET /api/v1/services/{service}/metrics` | session | Prometheus | 2 ✅ |
| `GET /api/v1/traces` | session | ClickHouse | 2 ✅ |
| `GET /api/v1/traces/{traceId}` | session | ClickHouse | 2 ✅ |
| `GET /api/v1/logs` | session | ClickHouse | 2 ✅ |
| `GET /api/v1/diagnostics/schema` | session | ClickHouse (missing tables and skip indexes, with their effect) | 3 ✅ |
| `POST /api/v1/webhooks/github` | HMAC signature | PostgreSQL | 4 ✅ |
| `GET /api/v1/deployments` · `GET /api/v1/deployments/{id}` | session | PostgreSQL (with each comparison's verdict and evidence) | 4 ✅ |
| `GET, POST /api/v1/alert-rules` · `GET, PUT, DELETE /api/v1/alert-rules/{id}` | session | PostgreSQL | 5 |
| `GET /api/v1/alerts` | session | PostgreSQL | 5 |

---

## 9. Demo service and fault scenarios

`demo-service` is a small Go shop API backed by its own `shop` database. It is instrumented with OpenTelemetry: `otelhttp` for HTTP (server spans named after the matched route, such as `GET /orders/{id}`), `otelpgx` for PostgreSQL, the `otelslog` bridge for logs, and Go runtime metrics. Seed data is generated deterministically on first start: 10k products, 100k customers and 1M orders by default, all configurable.

Three details make the signals link up:

- Every response carries an `X-Trace-Id` header, so a caller (or a test) can find its own trace.
- Every request writes an access-log record in its trace context.
- Statements slower than `DEMO_SLOW_QUERY_THRESHOLD` (100 ms) are logged at WARN in the context of their database span, so a slow span and its log line point at each other.

| Endpoint | Purpose |
|---|---|
| `GET /products`, `GET /products/{id}` | the common, fast reads |
| `GET /orders?customer_email=` | customer order history, which is the S1 query |
| `POST /orders` | the write path. It calls `inventory` from Phase 5. |
| `GET /orders/{id}` | point lookup |
| `POST /admin/faults` *(Phase 5)* | toggles the runtime faults for S2 and S3 |

| ID | Scenario | Mechanism | What the investigation should reveal | Phase |
|---|---|---|---|---|
| **S1** | **Missing index** | `orders.customer_email` ships without an index, so the history query does a sequential scan over about 1M rows. The fix, `make demo-fix`, adds the index with `CREATE INDEX CONCURRENTLY`, which is safe under load. `make demo-reset` drops it again. | p95 rises for the `/orders` route → slow traces are dominated by one `SELECT` span → that span's `db.query.text` shows the predicate → after the fix, the same workload is re-measured. | 1 |
| **S2** | **Error burst** | `POST /admin/faults {"error_rate": 0.2}` makes 20% of `POST /orders` fail with a logged ERROR that includes the exception. | The error-rate alert fires → error traces → the exception event and the matching ERROR log share a trace ID. | 5 |
| **S3** | **Slow dependency** | A second service, `inventory`, gets injected latency beyond `demo-service`'s client timeout. | The trace crosses two services through W3C `traceparent` propagation, and the timeout appears as an error span on the client side. | 5 |
| **S4** | **Bad release** | `make demo-deploy V=v2 ERROR_RATE=0.2` recreates `demo-service` with `DEMO_FAULT_ERROR_RATE=0.2`, so 20% of its storage calls fail, and reports the deployment by signed webhook. `make demo-deploy V=v3` is the healthy fix. | DeployRadar marks v2 a `regression` with the before/after counts and their PromQL → the failing traces of the after window → each one's exception event and ERROR log. | 4 |

The load generator is `tests/load/workload.js` (k6). The request mix and arrival rate are defined in the [benchmark method](benchmarks/README.md#reference-workload).

---

## 10. Security model

| Threat | Mitigation |
|---|---|
| Exposed infrastructure ports | Only the dashboard, the demo service and OTLP are published, and they bind to `127.0.0.1` by default (`INFRASCOPE_BIND`). PostgreSQL, ClickHouse, Prometheus and the API stay on the internal Compose network. A debug override exposes them to localhost only, on demand. |
| Credential leakage | Secrets live only in `.env` (git-ignored; `.env.example` holds placeholders) and in server-side environment variables. Collector config reads credentials through `${env:…}`. The browser receives a session cookie and never a database credential. |
| Injection through search filters | Every ClickHouse and PostgreSQL query uses bound parameters. Sort fields and filter operators are taken from allow-lists, never from interpolated user strings. PromQL label values are escaped by a single helper that has unit tests. |
| **Untrusted telemetry content** (XSS, misleading output) | Span attributes and log bodies are attacker-controllable input. React escapes rendered text, and the UI never renders telemetry as HTML (`dangerouslySetInnerHTML` is banned by lint). Long values are truncated in tables. nginx sends a restrictive Content-Security-Policy. |
| Cross-site request forgery | `SameSite=Strict` session cookie. A state-changing request whose `Origin` names a different origin is rejected with `403 forbidden`; requests without `Origin` come from non-browser clients, which hold no victim's cookie. Request bodies must be `application/json`, which a cross-site HTML form cannot send. |
| Password guessing | bcrypt hashing and per-IP rate limiting on login. |
| Forged or replayed deployment webhooks | The webhook route is public but authenticated: `X-Hub-Signature-256` must be the HMAC-SHA256 of the exact body under `INFRASCOPE_GITHUB_WEBHOOK_SECRET` (at least 16 characters), compared in constant time. Nothing is stored before the signature checks out, bodies are capped at 1 MiB, and a replayed delivery ID is answered as a duplicate without being applied. With no secret set, the route refuses everything. |
| Untrusted telemetry senders | The OTLP ports bind to localhost by default. Exposing them on a network requires turning on the collector's `bearertokenauth` extension (documented in the runbook). |
| Resource exhaustion through queries | The guard rails in NFR-2: maximum range, maximum page size, query timeouts, and per-dependency client timeouts. |
| Secrets in telemetry | The demo service uses parameterised SQL, so values never appear in `db.query.text`, and it never logs request bodies. Collector-side redaction is listed as future work. |

---

## 11. Testing strategy

| Layer | Tooling | Scope | Runs |
|---|---|---|---|
| Static | `gofmt`, `go vet`, `golangci-lint` (from Phase 1), `govulncheck`, `oxlint`, `tsc` | style, common bugs, known-vulnerable dependencies | every PR |
| Unit (Go) | `testing`, table-driven, `httptest` | readiness aggregation, query builders (SQL and PromQL), validation, cursor encoding, alert state machine | every PR |
| Unit (UI) | Vitest + Testing Library | span-tree → waterfall layout, URL state, formatting, empty and error states | every PR |
| Integration | `testcontainers-go` (ClickHouse, PostgreSQL, Prometheus) | repositories and HTTP handlers against real databases; **schema-drift test** comparing the exporter-created DDL with a committed snapshot ([ADR-0004](decisions/0004-exporter-managed-clickhouse-schema.md)); degraded mode with a stopped container | every PR |
| Pipeline | Docker Compose + Go test | request → collector → storage → API, polling until visible (AC-1.4) | every PR |
| End-to-end | Playwright against the Compose stack | login; overview → trace → logs → trace; missing-index diagnosis; outage banners | every PR |
| Load | k6 | reference workload and benchmark reports (NFR-2) | before each release |

**Requirement coverage**

| Layer | Covers |
|---|---|
| Unit | FR-3 (AC-3.1, AC-3.3), FR-4 (AC-4.1), FR-6 (AC-6.1, AC-6.3), FR-9 (AC-9.1), NFR-3 |
| Integration | FR-2 (AC-2.1), FR-3 (AC-3.4), FR-6 (AC-6.2), FR-9 (AC-9.2, AC-9.3), NFR-1, NFR-4 |
| Pipeline | FR-1 |
| End-to-end | FR-2 (AC-2.3), FR-3, FR-5 (AC-5.1, AC-5.2), NFR-1, NFR-3 |
| Smoke | FR-7 |
| Load | FR-8, NFR-2 |

---

## 12. Roadmap

Phases are ordered by dependency, not by date. A phase is done when its **exit criteria** pass, which are demonstrable and, where possible, automated.

| Phase | Theme | Deliverables | Exit criteria |
|---|---|---|---|
| **0** | Specification & skeleton | This spec, architecture, ADRs, runbook; Compose stack with pinned images and healthchecks; API with health, readiness and status; dashboard shell; CI | `make up` + `make smoke` pass; CI green |
| **1** | Instrumented demo & pipeline | `demo-service` with OTel traces, metrics and logs; pgx instrumentation; seed data; S1; k6 request mix | A pipeline test sees one request as a trace (HTTP and DB spans), correlated logs and RED metrics |
| **2** | Query API | Migrations; auth; services and metrics, trace search and detail, log search; error envelope; integration tests; schema-drift test | All Phase 2 endpoints pass integration tests against real containers; the OpenAPI contract and handlers agree |
| **3** | Investigation UI | Login; service overview (ECharts); trace search and waterfall; log explorer; correlation links; URL state; degraded banners | The e2e round trip passes; S1 can be diagnosed from the UI alone |
| **4** | DeployRadar | GitHub `deployment_status` webhook with signature check and idempotent storage; status precedence; before/after comparison engine and worker; deployments API and views; S4 and `make demo-deploy` ([ADR-0007](decisions/0007-deployment-health-comparison.md)) | AC-9.1 to AC-9.3 pass in CI; on the running stack, a deliberate error regression is reported as `regression` with its evidence (AC-9.4) |
| **5** | Failure scenarios, alerting & reliability | Alert rules, evaluator and UI; S2 and S3; `inventory` service; persistent collector queue; read-only ClickHouse user; platform view | The alert fires and resolves (integration test); the ClickHouse outage checklist passes, with the measured data loss within queue capacity documented |
| **6** | Benchmarked release | Benchmark reports (S1 before and after), screenshots, short demo video, ADR review, limitations, `v1.0.0` | A newcomer can go from clean clone to the S1 diagnosis using the README alone; every number in the README links to a report |

**Milestones:** the *walking skeleton* is the end of Phase 3 (FR-1 to FR-5, FR-7, FR-8 with S1). *v1.0* is the end of Phase 6, when every FR and NFR has been verified.

---

## 13. Risks and mitigations

| ID | Risk | Mitigation |
|---|---|---|
| R1 | Exporter schema changes when the collector is upgraded. | The collector version is pinned. A schema-drift test fails CI when the created DDL changes. ADR-0004 records the exit plan of owning the DDL as migrations. |
| R2 | Two query backends (Prometheus and ClickHouse) increase API complexity. | Each backend sits behind its own repository interface. Degraded mode is handled per backend. Time ranges are parsed and validated by a single shared function. |
| R3 | ClickHouse resource use on a laptop. | Memory use is measured in Phase 6 and recommended resources are published from those measurements. The runbook documents server memory settings. |
| R4 | OTel → Prometheus name translation differs from expectations. | The translation strategy is set explicitly. An integration test asserts the expected series and label names. |
| R5 | Scope creep (dashboards, analytics, notifications). | Every FR is tied to a phase. Stretch items are listed separately and wait until v1.0. |
| R6 | High-cardinality metric labels. | Only allow-listed resource attributes are promoted to labels. High-cardinality data (user IDs, order IDs) belongs on spans and logs in ClickHouse. |
| R7 | Malicious or malformed telemetry content. | Treated as untrusted input ([§10](#10-security-model)). |

---

## 14. Future work (post-v1.0)

- **Exemplars:** jump from a latency spike straight to an example trace.
- **Span-metrics connector:** derive RED metrics from traces for services that export no metrics.
- **Service dependency map** built from cross-service parent/child spans.
- **Owned ClickHouse schema** with pre-aggregated materialised views for faster overviews.
- Tail-based sampling; collector-side redaction of sensitive attributes.
- OIDC login; Alertmanager integration; saved investigations.
- **DeployRadar:** list the commits a deployment shipped (GitHub's compare API, which needs a token); add failing-trace and error-log counts from ClickHouse as evidence; baselines from the same time on earlier days, for services with daily seasonality ([ADR-0007](decisions/0007-deployment-health-comparison.md)).

---

## 15. Glossary

| Term | Meaning |
|---|---|
| **Span / trace** | A span is one timed operation. A trace is the tree of spans for one request, linked by parent IDs. |
| **OTLP** | The OpenTelemetry protocol for sending telemetry over gRPC or HTTP. |
| **RED** | Rate, Errors, Duration: the three service-level signals on the overview. |
| **p95** | 95th-percentile latency: 95% of requests were at least this fast. |
| **Semantic conventions** | Standard OpenTelemetry attribute names (`http.route`, `db.query.text`, …) that make signals comparable across services. |
| **Cardinality** | The number of distinct label combinations. High cardinality makes Prometheus expensive. |
| **TTL** | Time-to-live; rows older than this are deleted automatically. |
| **Degraded mode** | Running with one dependency down, with the affected features clearly marked unavailable. |
| **Sending queue** | The buffer inside each collector exporter that absorbs bursts and outages. |
