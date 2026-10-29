# Diagnostic walkthrough

A ten-minute demonstration of the workflow InfraScope exists for: **from a latency symptom to a verified fix, using only telemetry.** It follows scenario **S1, *missing index*** ([SPEC §9](SPEC.md#9-demo-service-and-fault-scenarios)).

> **Status:** this script describes the experience at the end of **Phase 3**. Steps marked *(Phase N)* depend on work that has not been built yet. Today, every command-line step works: traffic, the S1 index toggle, and confirming the slow query with `EXPLAIN`. The UI steps are the plan.

## Setup

```bash
cp .env.example .env          # then change every password in it
make up && make smoke         # stack is up and healthy
make demo-fix                 # start from the healthy state: index present
```

Open <http://localhost:8080> and sign in with `INFRASCOPE_ADMIN_USER` / `INFRASCOPE_ADMIN_PASSWORD` from `.env` *(Phase 3)*.

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

**Show:** within a few scrape intervals, p95 latency for `demo-service` climbs while its request rate stays flat. Something got slower without failing.

## 3. Find an affected request

Click the latency spike on the overview chart. The trace explorer opens filtered to `demo-service` and that time window *(Phase 3)*. Sort by **duration**, then open the slowest trace.

**Point out:** the filters are in the URL, so the investigation can be shared as a link (AC-5.1).

## 4. Read the logs written during that request

From the trace view, open **Logs for this trace**.

**Show:** the request's log lines, including the demo service's slow-query warning (statements over 100 ms are logged at WARN, with the trace context attached). Click a log line's trace link to return to the same trace.

**Point out:** no timestamps were matched by hand. Logs and spans share `TraceId`, and the lookup is bounded by the trace's own time range (SPEC [§7.1](SPEC.md#71-clickhouse-telemetry-store)).

## 5. Identify the bottleneck

In the waterfall, one `SELECT` span accounts for most of the request's duration. Its `db.query.text` attribute shows the predicate `WHERE customer_email = $1`.

Optionally, confirm outside InfraScope:

```bash
docker compose --env-file .env -f deployments/docker-compose.yml exec postgres \
  psql -U postgres -d shop -c "EXPLAIN ANALYZE SELECT * FROM orders WHERE customer_email = 'someone@example.com'"
# Seq Scan on orders ...
```

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
