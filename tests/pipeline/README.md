# Pipeline test

The end-to-end check of FR-1 ([SPEC](../../docs/SPEC.md#fr-1-telemetry-ingestion--phase-1)), made through the InfraScope API exactly as the dashboard would make it. The test logs in through nginx, sends one real order to `demo-service`, and waits for three pieces of evidence:

1. **The trace** (`GET /api/v1/traces/{id}`), with a root span `POST /orders` and a child database span (`db.system.name` set). The trace ID comes from the response's `X-Trace-Id` header.
2. **Its logs** (`GET /api/v1/logs?trace_id=...`), every record linked back to the same trace.
3. **Request metrics** (`GET /api/v1/services`), with `demo-service` reporting a request rate. That proves the OTLP → Prometheus path and its name translation (risk R4). A rate needs a counter that rises between two samples. On a fresh stack the order is the service's first request, so its series starts at 1 and stays flat. The test therefore keeps sending read-only requests while it waits.

Each signal gets 60 s to appear. That is a test timeout, not a latency claim.

## Running it

It needs only the running stack. Everything goes through the published dashboard port.

```bash
make up
make test-pipeline
```

Without `make`:

```bash
set -a; . ./.env; set +a     # the admin credentials come from .env
cd tests/pipeline && INFRASCOPE_PIPELINE=1 go test -count=1 -v ./...
```

`go test ./...` without `INFRASCOPE_PIPELINE=1` skips the test, so ordinary unit-test runs stay offline. CI runs it after starting the stack.
