# Diagnostic walkthrough

A ten-minute demonstration of the workflow InfraScope exists for: **from a latency symptom to a verified fix, using only telemetry.** It follows scenario **S1, *missing index*** ([SPEC §9](SPEC.md#9-demo-service-and-fault-scenarios)).

> **Status:** every step works as of **Phase 3**, except the benchmark report in step 7, which comes with Phase 5. Together, the [browser journeys](../tests/e2e/README.md) walk steps 3 to 5 on every CI run.

## Setup

```bash
cp .env.example .env          # then change every password in it
make up && make smoke         # stack is up and healthy
make demo-fix                 # start from the healthy state: index present
```

Open <http://localhost:8080> and sign in with `INFRASCOPE_ADMIN_USER` / `INFRASCOPE_ADMIN_PASSWORD` from `.env`.

## 1. Generate traffic

In a second terminal:

```bash
make load RATE=20 DURATION=10m
```

**Show:** the service overview. `demo-service` appears with a steady request rate close to the configured arrival rate, near-zero errors and low p95 latency.

**Point out:** these numbers come from the OpenTelemetry request-duration histogram in Prometheus. The k6 script knows nothing about InfraScope.

## 2. Introduce the slow query

While the load is running:

```bash
make demo-reset               # drops orders_customer_email_idx: the "bad deploy"
```

**Show:** within a few metric export intervals (15 s each), p95 latency for `demo-service` climbs while its request rate stays flat. Something got slower without failing.

## 3. Find an affected request

Click the spike on the **Latency** chart of `demo-service`. The trace explorer opens with that service and the minute behind the point, slowest first. Open the slowest `GET /orders` trace.

**Point out:** the filters are in the URL, so the investigation can be shared as a link, and the back button returns to the overview (AC-5.1).

## 4. Identify the bottleneck

The trace page says where the time went: *Most time was spent in SELECT*, with that span's share of the request. Click it. The span's details show its statement, with the predicate `WHERE customer_email = $1`.

Optionally, confirm outside InfraScope:

```bash
docker compose --env-file .env -f deployments/docker-compose.yml exec postgres   psql -U postgres -d shop -c "EXPLAIN ANALYZE SELECT * FROM orders WHERE customer_email = 'someone@example.com'"
# Seq Scan on orders ...
```

## 5. Read the logs written during that request

The trace page lists the logs written during the request. The slow-query warning names the `SELECT` span that wrote it (statements over 100 ms are logged at WARN, in their span's context), and clicking the span name selects it. **Logs for this trace** opens the same records in the log explorer, where each record's **Trace** link leads back to the same span.

**Point out:** no timestamps were matched by hand. Logs and spans share `TraceId`, and the lookup is bounded by the trace's own time range (SPEC [§7.1](SPEC.md#71-clickhouse-telemetry-store)).

## 6. Fix and re-run the same workload

```bash
make demo-fix                 # creates the index (CREATE INDEX CONCURRENTLY, safe under load)
make load RATE=20 DURATION=10m
```

Use identical rate and duration, so that the only variable is the index.

## 7. Compare before and after

**Show:** the overview's p95 for `demo-service` returns to its baseline. Then show the two k6 run summaries written up as a benchmark report *(Phase 5)*. [The benchmark method](benchmarks/README.md) defines what is recorded and how.

**Point out:** the improvement is stated as a measured before/after pair from the same workload on the same machine, with the report linked. It is not stated as a general claim.

## Reset

```bash
make demo-fix                 # leave the healthy state behind
make reset                    # or wipe everything
```
