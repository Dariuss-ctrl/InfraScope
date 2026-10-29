# Benchmark method

InfraScope makes **no performance claims without a report.** This page defines how reports are produced, so that any number in the README can be traced back to a run, and any run can be repeated.

## Principles

1. **Same workload, one variable.** Comparisons (before/after a fix, version A against B) change exactly one thing.
2. **Record the environment.** Hardware, OS, Docker resources and image versions go in every report.
3. **Warm up, then measure.** The first minutes, while caches, connection pools and ClickHouse parts are still settling, are excluded.
4. **Report distributions, not averages.** Use p50, p95 and p99, plus the sample count.
5. **Keep raw outputs.** k6 summaries are attached to the report, not just quoted.

## Reference workload

Driven by `tests/load/workload.js` (k6, `constant-arrival-rate` executor). In Phase 0 the script exercises `/healthz` only, so the harness itself can be verified. The shop request mix arrives with the demo service in Phase 1:

| Request | Share | Notes |
|---|---|---|
| `GET /products` | 50% | paginated list |
| `GET /products/{id}` | 20% | random product |
| `GET /orders?customer_email=` | 15% | the S1 query |
| `POST /orders` | 10% | write path |
| `GET /orders/{id}` | 5% | point lookup |

| Parameter | Default | Override |
|---|---|---|
| Arrival rate | 20 req/s | `RATE` |
| Duration | 2 min (benchmarks use 10 min) | `DURATION` |
| Warm-up excluded | first 2 min | report field |
| Seed data | 10k products, 1M orders, fixed seed | demo-service config |

## What is measured

| Metric | How | Why |
|---|---|---|
| Demo-service latency p50/p95/p99, error rate | k6 summary | the user-visible effect of a change (S1 before/after) |
| InfraScope API latency per endpoint | a separate k6 script against `/api/v1/*` with realistic filters (Phase 2+) | NFR-2: query performance |
| Ingest-to-query lag | a probe sends a span with a unique attribute and polls the API until it is visible, repeated 50× | how fresh investigations are |
| Collector health | `otelcol_receiver_refused_*`, `otelcol_exporter_send_failed_*`, peak queue size | was data lost during the run? |
| Resource usage | `docker stats` sampled every 5 s → peak and mean CPU and memory per container | sizing guidance (risk R3) |

## Procedure

1. `make reset && make up`, then let seed data finish loading (Phase 1+).
2. Record the environment (template below).
3. Run the warm-up, then the measured run, with fixed `RATE` and `DURATION`.
4. Export the k6 summary and the Prometheus snapshots listed above.
5. Write a report in `reports/YYYY-MM-DD-<topic>.md` from the template and link it from wherever the numbers are quoted.

## Report template

```markdown
# <Topic> — <YYYY-MM-DD>

## Question
What this run is meant to show (e.g. "Effect of orders_customer_email_idx on S1 latency").

## Environment
| | |
|---|---|
| CPU / RAM | |
| OS / Docker version / Docker memory limit | |
| InfraScope commit | |
| Image versions | from `docker compose images` |

## Workload
Rate, duration, warm-up, seed size, request mix (link to workload.js at the commit).

## Results
| Metric | Run A | Run B | Change |
|---|---|---|---|
| p50 / p95 / p99 (ms) | | | |
| Error rate | | | |
| Requests completed | | | |
| Collector refused / failed | | | |
| Peak memory (ClickHouse, collector) | | | |

## Interpretation
What changed, why we believe it is causal, and what this run does **not** show.

## Raw data
Links to k6 summaries and query outputs committed alongside this report.
```
