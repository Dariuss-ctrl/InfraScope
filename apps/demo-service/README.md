# demo-service

The toy shop that InfraScope observes. In Phase 0 it serves only `GET /healthz` and `GET /`. Phase 1 turns it into the instrumented system under test described in [SPEC §9](../../docs/SPEC.md#9-demo-service-and-fault-scenarios):

- shop endpoints (`/products`, `/orders`) backed by its own `shop` PostgreSQL database, with deterministic seed data;
- OpenTelemetry traces (`otelhttp`, `otelpgx`), metrics and logs (`slog` through the `otelslog` bridge), exported to the collector over OTLP;
- fault scenarios S1 (missing index), S2 (error burst) and, with the `inventory` service, S3 (slow dependency).

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DEMO_HTTP_ADDR` | `:8082` | listen address |
| `DEMO_DATABASE_URL` | — | `postgres://shop:…@postgres:5432/shop` (Phase 1) |
| `OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT` | set in Compose | standard OpenTelemetry SDK settings (Phase 1) |

## Development

```bash
go test ./...
go run ./cmd/demo-service
```
