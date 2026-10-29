# demo-service

The toy shop that InfraScope observes: a Go HTTP API over its own `shop` PostgreSQL database, instrumented with OpenTelemetry so that every request leaves a trace, metrics and correlated logs. It is the system under test for the failure scenarios in [SPEC §9](../../docs/SPEC.md#9-demo-service-and-fault-scenarios).

## Endpoints

| Route | Notes |
|---|---|
| `GET /products?limit=&offset=` | page of products (limit 1–100, default 20) |
| `GET /products/{id}` | one product |
| `GET /orders?customer_email=&limit=` | a customer's recent orders. **This is the S1 query:** `orders.customer_email` ships without an index. |
| `POST /orders` | `{"customer_email", "product_id", "quantity"}`, priced from the product table |
| `GET /orders/{id}` | one order |
| `GET /healthz` | liveness; not traced or logged |

Every response carries `X-Trace-Id`, the ID of the request's trace.

## Telemetry it produces

| Signal | Source | What to look for |
|---|---|---|
| Traces | `otelhttp` server spans named after the route (`GET /orders/{id}`); `otelpgx` child spans per statement with `db.query.text` and `db.system.name` | the slow `SELECT … WHERE customer_email = $1` under S1 |
| Metrics | `http.server.request.duration` histogram labelled with `http.route`; Go runtime metrics; pgx pool and `db.client.operation.duration` metrics | p95 per route |
| Logs | `slog` through the `otelslog` bridge (and JSON on stdout): an access-log record per request, `order created`, and `slow query` at WARN | every record carries the trace and span IDs of the code that wrote it |

Exporters are configured with the standard OpenTelemetry variables. In Compose they send OTLP/HTTP to the collector. `OTEL_TRACES_EXPORTER=console` (or `none`) works for local experiments.

## Scenario S1: missing index

```bash
make demo-reset   # drop orders_customer_email_idx: the "bad deploy"
make demo-fix     # create it again (CREATE INDEX CONCURRENTLY, safe under load)
```

The full walkthrough is in [docs/demo-script.md](../../docs/demo-script.md).

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DEMO_HTTP_ADDR` | `:8082` | listen address |
| `DEMO_DATABASE_URL` | — (required) | `postgres://shop:…@postgres:5432/shop` |
| `DEMO_SLOW_QUERY_THRESHOLD` | `100ms` | statements at least this slow are logged at WARN |
| `DEMO_SEED_PRODUCTS` / `_CUSTOMERS` / `_ORDERS` | `10000` / `100000` / `1000000` | deterministic first-start data |
| `OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_METRIC_EXPORT_INTERVAL`, … | set in Compose | standard OpenTelemetry SDK settings |

On first start the service creates its schema and seeds the data inside one transaction before it starts listening. Expect a minute or so with the default sizes; the Compose healthcheck allows 300 s. Seeding is derived from row numbers rather than random values, so every environment gets identical data.

## Layout

```
cmd/demo-service/    wiring, graceful shutdown, healthcheck subcommand
internal/config/     environment configuration
internal/telemetry/  OpenTelemetry SDK setup (traces, metrics, logs, runtime)
internal/shop/       schema, seed, store, slow-query tracer, HTTP API
```

## Development

```bash
go test ./...                       # unit tests (handlers, tracer, config, SDK wiring)
make up-debug && make test-pipeline # end to end: request -> trace, logs, metrics
```

The store's SQL runs against real PostgreSQL in the pipeline test. Phase 2 adds testcontainers-based integration tests.
