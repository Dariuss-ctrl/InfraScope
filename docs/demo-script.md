# Diagnostic walkthrough

A ten-minute demonstration of the workflow InfraScope exists for: **from a latency symptom to a verified fix, using only telemetry.** It follows scenario **S1, *missing index*** ([SPEC §9](SPEC.md#9-demo-service-and-fault-scenarios)).

> **Status:** every step works as of **Phase 4**, except the benchmark report in step 7, which comes with Phase 6. [Part 2](#part-2-did-that-deployment-do-it) shows DeployRadar catching a bad release. Together, the [browser journeys](../tests/e2e/README.md) walk steps 3 to 5 on every CI run.

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

**Show:** the overview's p95 for `demo-service` returns to its baseline. Then show the two k6 run summaries written up as a benchmark report *(Phase 6)*. [The benchmark method](benchmarks/README.md) defines what is recorded and how.

**Point out:** the improvement is stated as a measured before/after pair from the same workload on the same machine, with the report linked. It is not stated as a general claim.

## Part 2: did that deployment do it?

Scenario **S4, *bad release*** ([SPEC §9](SPEC.md#9-demo-service-and-fault-scenarios)): a release in which one storage call in five fails, reported to DeployRadar the way GitHub would report it. About fifteen minutes, most of it waiting for windows to fill.

**Setup:** set `INFRASCOPE_GITHUB_WEBHOOK_SECRET` in `.env` (`openssl rand -hex 32`). For a demo, shorten the windows too: `INFRASCOPE_DEPLOYRADAR_WINDOW=3m` and `INFRASCOPE_DEPLOYRADAR_SETTLE=1m`. Then `make up`. Keep traffic running throughout, in a second terminal:

```bash
make load RATE=20 DURATION=20m
```

### 8. Deploy a healthy release

After three minutes of traffic, so the first before window is full:

```bash
make demo-deploy V=v1
```

The script reports the deployment in progress, recreates `demo-service` as `v1`, and reports success. On the way it checks that a repeated delivery is answered as a duplicate and that a late `in_progress` does not un-finish the deployment.

**Show:** *Deployments* lists it with *comparing…*. About five minutes later (settle, window, then 30 s for the last metrics) the row says **No change**.

### 9. Ship the bad release

```bash
make demo-deploy V=v2 ERROR_RATE=0.2
```

**Show:** five minutes later, the v2 row says **Regression**. Its page leads with the reason in one line, with the before/after table and the confidence below it. In the [recorded demo run](results/2026-11-16-demo-run.md) it read *error rate 0.0% → 19.5% (+19.5 pp, p<0.001)*; the exact figures vary from run to run, since one request in five fails at random.

**Point out:** every number can be checked. *PromQL behind these numbers* holds the exact query, evaluation time and range for each value, ready to paste into Prometheus. Nothing was copied out of Prometheus except the results.

### 10. Follow the evidence

Click **Failing traces after the deployment**. The trace search opens on `demo-service`, `status=error`, over the after window. Open one: the server span carries the exception, and *Logs for this trace* shows the ERROR record with the same trace ID.

**Point out:** the investigation from part 1 starts from the deployment, with the time window already chosen.

### 11. Roll forward

```bash
make demo-deploy V=v3
```

The v3 comparison measures v2 as its before window, so it comes back **Improvement**.

## Reset

```bash
make demo-fix                 # leave the healthy state behind
make demo-deploy V=v3         # healthy demo-service (or: make up)
make reset                    # or wipe everything
```
