# Pipeline test

The Phase 1 exit criterion as an automated test ([SPEC FR-1](../../docs/SPEC.md#fr-1-telemetry-ingestion--phase-1)). It sends one real order to `demo-service` and then waits for three pieces of evidence:

1. **A trace in ClickHouse** with a root span `POST /orders` and a child database span (`db.system.name` set). The trace ID comes from the response's `X-Trace-Id` header.
2. **Log records in ClickHouse** with the same `TraceId`: the access log, the "order created" record, and any slow-query warning.
3. **Request metrics in Prometheus:** `http_server_request_duration_seconds_count` labelled `service_name="demo-service"`, `http_route="/orders"`, `http_request_method="POST"`. That pins the OTLP → Prometheus name translation (risk R4).

Each signal gets 60 s to appear. That is a test timeout, not a latency claim.

## Running it

It needs the stack running with internal ports published, so it can query ClickHouse and Prometheus directly. The InfraScope API takes over that role from Phase 2.

```bash
make up-debug
make test-pipeline
```

Without `make`:

```bash
set -a; . ./.env; set +a
cd tests/pipeline && INFRASCOPE_PIPELINE=1 go test -count=1 -v ./...
```

`go test ./...` without `INFRASCOPE_PIPELINE=1` skips the test, so ordinary unit-test runs stay offline. CI runs it after starting the stack.
