# InfraScope — Runbook

Operational procedures for a single-node InfraScope deployment. Run every command from the repository root, with a `.env` file in place (`cp .env.example .env`).

> **No `make`?** (e.g. Windows without WSL) Every target is a thin wrapper. Define the Compose prefix once and use the equivalents in the table:
>
> ```bash
> COMPOSE="docker compose --env-file .env -f deployments/docker-compose.yml"
> ```

## Quick reference

| Task | Make | Docker Compose equivalent |
|---|---|---|
| Start (build, wait until healthy) | `make up` | `$COMPOSE up -d --build --wait` |
| Start with internal ports exposed on localhost | `make up-debug` | `$COMPOSE -f deployments/docker-compose.debug.yml up -d --build --wait` |
| Container status and health | `make ps` | `$COMPOSE ps` |
| Follow logs (all, or one service) | `make logs` / `make logs S=otel-collector` | `$COMPOSE logs -f --tail=200 otel-collector` |
| Smoke test | `make smoke` | `bash scripts/smoke.sh` |
| Generate load against the running stack | `make load RATE=20 DURATION=2m` | `$COMPOSE --profile load run --rm --no-deps -e RATE=20 -e DURATION=2m k6` |
| End-to-end telemetry test (needs `up-debug`) | `make test-pipeline` | see [tests/pipeline](../tests/pipeline/README.md) |
| Scenario S1: drop / restore the index | `make demo-reset` / `make demo-fix` | `$COMPOSE exec -T postgres psql -U postgres -d shop -c "DROP INDEX CONCURRENTLY IF EXISTS orders_customer_email_idx"` |
| Stop, keep data | `make down` | `$COMPOSE down` |
| Stop and **delete all data** | `make reset` | `$COMPOSE down -v --remove-orphans` |

## Health at a glance

1. **Containers:** `make ps`. Every service except `otel-collector` (scratch image, no shell) should show `healthy`.
2. **Dependencies as the API sees them:**

   ```bash
   curl -s http://localhost:8080/api/v1/status
   ```

   `status` is `ok`, `degraded` (an optional dependency such as ClickHouse is down) or `unavailable` (PostgreSQL is down). Each dependency is listed with its probe latency. Error details are in the API logs (`make logs S=api`), never in the response.
3. **Deeper inspection:** start with `make up-debug`. Then Prometheus is at <http://localhost:9090> (see *Status → Targets*), the collector's health is at <http://localhost:13133/>, and its metrics are at <http://localhost:8888/metrics>.

## Signals to check first

Query these in Prometheus (debug mode). A `*` stands for the signal type (`spans`, `log_records` or `metric_points`); the exact names can carry a `_total` suffix.

| Signal | Healthy | If not |
|---|---|---|
| `otelcol_receiver_refused_*` | flat | the collector is shedding load → [collector refusing data](#collector-is-refusing-or-dropping-data) |
| `otelcol_exporter_send_failed_*` | flat | a backend is rejecting or unreachable → check ClickHouse and Prometheus |
| `otelcol_exporter_queue_size / otelcol_exporter_queue_capacity` | well below 1 | an exporter is falling behind. Once the queue is full, new data is dropped. |
| `up{job=~"otel-collector\|clickhouse"}` | `1` | the scrape target is down |
| `ClickHouseProfileEvents_FailedInsertQuery` | not increasing | inserts are failing → check ClickHouse logs and disk |
| `prometheus_tsdb_head_series` | stable | cardinality is growing → look for a new high-cardinality label |
| `count by (service_name) (target_info)` | one series per reporting service | a service is not pushing metrics → check its OTLP endpoint |

## Incidents

### demo-service is unhealthy right after the first start

On its first start, demo-service creates and seeds the `shop` database (1M orders by default) *before* it starts listening, so its healthcheck fails until seeding finishes. The healthcheck allows a 300 s start period for this.

1. `make logs S=demo-service`. A `seeded demo data` record followed by `listening` means it recovered by itself. `make ps` then shows it healthy.
2. If `make up` gave up first, run it again. It is idempotent.
3. On Docker Desktop the VM's clock can jump forward (for example after the host sleeps), which uses up the start period early. If this keeps happening, lower `DEMO_SEED_ORDERS` in `.env` and run `make reset && make up`. A smaller table makes scenario S1's slowdown less pronounced.

### ClickHouse is down or unhealthy

**Symptoms:** trace and log views show *storage unavailable*; `/api/v1/status` reports `clickhouse` as down; the collector logs export errors.

1. `make ps`, then `make logs S=clickhouse`.
2. Check for an out-of-memory kill: `docker inspect --format '{{.State.OOMKilled}}' infrascope-clickhouse-1`.
3. Check disk space (see [disk usage](#disk-usage-keeps-growing)).
4. Restart: `$COMPOSE restart clickhouse`. The collector retries and flushes its queue.

**Data-loss bounds:** while ClickHouse is down, the collector keeps telemetry in each exporter's bounded sending queue and retries for up to 300 s per batch (`retry_on_failure.max_elapsed_time`). Data beyond the queue capacity or past the retry window is dropped and counted in `otelcol_exporter_send_failed_*`. Until Phase 4 the queue is in memory, so restarting the collector during an outage loses whatever was queued.

### Collector is refusing or dropping data

- **Refused** (`otelcol_receiver_refused_*` rising): `memory_limiter` is over its limit (`limit_mib` in `telemetry/otel-collector.yaml`). Raise the limit together with the container's memory, or reduce load. SDKs receive a retryable error and back off, so short bursts are absorbed.
- **Failed** (`otelcol_exporter_send_failed_*` rising): the backend rejected the data or was unreachable. `make logs S=otel-collector` shows the reason. A schema mismatch after a collector upgrade appears here (see [ADR-0004](decisions/0004-exporter-managed-clickhouse-schema.md)).
- **Config errors:** the collector exits at startup. `make logs S=otel-collector` names the offending key. Component names change between releases; for example `otlphttp` is now `otlp_http`.

### Prometheus is not receiving application metrics

1. `count by (service_name) (target_info)` should list each service.
2. Confirm that Prometheus started with `--web.enable-otlp-receiver` (`$COMPOSE config | grep otlp`).
3. Collector logs showing HTTP 4xx from Prometheus usually mean out-of-order samples older than `storage.tsdb.out_of_order_time_window` (30 m), or an invalid metric or label name.

### Disk usage keeps growing

1. Volume sizes: `docker system df -v | grep infrascope`.
2. ClickHouse table sizes:

   ```bash
   $COMPOSE exec clickhouse sh -c 'clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" -q "
     SELECT table, formatReadableSize(sum(bytes_on_disk)) AS size, sum(rows) AS rows
     FROM system.parts WHERE active AND database = '\''otel'\''
     GROUP BY table ORDER BY sum(bytes_on_disk) DESC"'
   ```

3. **Shorten telemetry retention.** `TELEMETRY_TTL` only applies when the tables are *created* (ADR-0004), so existing tables have to be altered as well. Check the current clause with `SHOW CREATE TABLE otel.otel_traces` first. For example, to change from 3 days to 1:

   ```sql
   ALTER TABLE otel.otel_traces             MODIFY TTL toDateTime(Timestamp) + toIntervalDay(1);
   ALTER TABLE otel.otel_traces_trace_id_ts MODIFY TTL toDateTime(Start)     + toIntervalDay(1);
   ALTER TABLE otel.otel_logs               MODIFY TTL toDateTime(Timestamp) + toIntervalDay(1);
   ```

   Then set `TELEMETRY_TTL=24h` in `.env`, so that a later `make reset` recreates the tables consistently.
4. **Emergency:** drop whole days at once (partitions are daily): `ALTER TABLE otel.otel_logs DROP PARTITION '2026-10-27';`
5. **Prometheus:** lower `storage.tsdb.retention.time` or `.size` in `telemetry/prometheus.yaml`, then `$COMPOSE restart prometheus`.

### The dashboard loads but `/api` returns 502

The API is down or restarting. Check `make logs S=api` and `make ps`. nginx re-resolves the API's address through Docker DNS on every request, so it recovers on its own once the API is back. No nginx restart is needed.

### PostgreSQL will not start after an upgrade

PostgreSQL 18 images keep data under `/var/lib/postgresql/18/docker`, and the volume is mounted at `/var/lib/postgresql`. Major-version upgrades need `pg_upgrade` or a dump and restore. For a disposable local setup, `make reset` is the fast path, at the cost of losing all data.

## Exposing OTLP beyond localhost

The OTLP ports bind to `127.0.0.1` by default. Before binding them to a reachable interface:

1. Require a token. In `telemetry/otel-collector.yaml`, add the extension and reference it from each OTLP protocol:

   ```yaml
   extensions:
     bearertokenauth:
       token: ${env:OTLP_BEARER_TOKEN}
   receivers:
     otlp:
       protocols:
         grpc:
           endpoint: 0.0.0.0:4317
           auth:
             authenticator: bearertokenauth
   service:
     extensions: [health_check, bearertokenauth]
   ```

2. Put TLS in front of the collector (a reverse proxy or the receiver's `tls` settings). Bearer tokens over plain HTTP are readable by anyone on the path.
3. Only then change `INFRASCOPE_BIND`. It controls all published ports, so the dashboard should also be behind TLS.

## Backup and restore

Telemetry is short-lived by design (TTL). The data worth keeping is the `infrascope` database: alert rules and their history.

```bash
$COMPOSE exec -T postgres pg_dump -U postgres -d infrascope > infrascope-$(date +%F).sql
$COMPOSE exec -T postgres psql -U postgres -d infrascope < infrascope-YYYY-MM-DD.sql
```
