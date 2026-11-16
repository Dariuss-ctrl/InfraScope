# ADR-0007: Judge each deployment by a before/after comparison in Prometheus

- **Status:** Accepted
- **Date:** 2026-11-15

## Context

FR-9 (DeployRadar) links deployments to changes in service health: after a deployment succeeds, did the service get worse? Four constraints were set before design:

- Keep the current stack. Applications keep sending OTLP to the Collector; there is no new ingestion format.
- Metrics stay in Prometheus. Metric aggregates are **not** copied into PostgreSQL.
- PostgreSQL stores only deployment facts and comparison results.
- ClickHouse (traces and logs) is unchanged; it may supply supporting evidence later.

Deployments arrive as GitHub `deployment_status` webhooks, which can be redelivered and arrive out of order. Traffic on a personal or small installation can be low, so any rule must be able to say "not enough data" instead of guessing.

## Decision

**Intake.** Webhooks are verified with HMAC-SHA256 (`X-Hub-Signature-256`, compared in constant time) before anything is stored. Every verified delivery is stored first, keyed by `X-GitHub-Delivery`, so a redelivery is answered as a duplicate. Statuses are applied by their own time, and a non-terminal status never replaces a terminal one.

**Windows.** The first `success` at time T queues one comparison: before `[T−W, T)` and after `[T+S, T+S+W)`. W (default 10 min) is long enough to collect traffic. S (default 2 min) skips the restart, where counters reset and caches are cold.

**Measurement.** Once the after window has closed, plus a 30 s grace for the last metric export, a worker in the API runs three instant queries at each window's end, all over the window's length:

```
sum(increase(http_server_request_duration_seconds_count{service_name="S",deployment_environment_name="E"}[W]))
sum(increase(http_server_request_duration_seconds_count{…,http_response_status_code=~"5.."}[W]))
histogram_quantile(0.95, sum by (le) (increase(http_server_request_duration_seconds_bucket{…,http_response_status_code!~"5.."}[W])))
```

p95 leaves out 5xx responses. Failures are often fast, an error returned before the real work, so counting them pulls p95 *down* exactly when a release breaks. The first live run showed it: injecting 20% failures moved p95 from 91 ms to 43 ms.

The values are stored with the exact query, evaluation time and range behind each, so anyone can rerun them in Prometheus. The error rate is derived from the two stored counts, so it always reconciles with them. Nothing else from Prometheus is copied.

**Judgement** (`deployradar.Judge`, a pure function):

| Signal | Regression when | Confidence |
|---|---|---|
| Either window | fewer than `MIN_REQUESTS` (200) requests, or no series → `insufficient_data` for everything | 0 |
| Error rate | two-proportion z-test p < 0.01 **and** the rate rose by at least 1 percentage point | 1 − p |
| p95 latency (non-5xx) | after ≥ 1.25 × before **and** at least 25 ms slower | n / (n + MIN_REQUESTS), n = the smaller window's requests |

**Caveats** keep the verdict but cap its confidence at 0.5, and add a reason that says why:

- **The load changed.** The request counts differ by more than ×2 either way, so the windows did not see the same load. The second live run hit this when the machine stalled: one after window saw 57% fewer requests. A sharp drop is also what a release looks like when it drops connections before they reach the service, which server-side metrics cannot see.
- **Another deployment succeeded inside a window.** The worker asks for other successful deployments of the same service and environment within the comparison's span. One in the before window means the baseline mixes two releases. One in the settle gap or the after window means the after window measures another release, or two.

Improvements are the mirror image. A high p-value is not evidence that nothing changed, so `no_change` also takes the sample-size confidence. The overall verdict takes a regression in either signal first, then an improvement in either, then a signal without data (`insufficient_data`), and is `no_change` only when both signals are unchanged. A fix that cuts errors and leaves latency alone is therefore an improvement. A regression or improvement takes the strongest confidence among the signals that found it, because either alone is enough. `no_change` takes the weakest, because both must hold. Each result stores `method_version` (now 2: version 1 measured p95 over all responses and had no caveats), so results judged by older rules remain identifiable.

**Execution.** The worker ticks every 15 s and claims due rows with `FOR UPDATE SKIP LOCKED` and a 2-minute lease, so several API instances share the work without computing a comparison twice. A crashed worker's rows free up when its lease passes. A Prometheus outage or timeout leaves the row pending for a retry, and the fifth failure marks it `failed`. Any other error (a query Prometheus rejects) fails it at once, since retrying would not change the answer.

## Consequences

**Positive**

- Every verdict is reproducible from what is stored: the counts, the p95, and the exact PromQL with its time and range.
- No metric data is duplicated, so there is nothing to keep in sync and no second retention policy.
- The rules are a pure function with table-driven tests, and the whole path is tested end to end against backfilled Prometheus history.
- Unlike the alert evaluator (ADR-0005), the worker is safe to run in several API instances.

**Negative / accepted trade-offs**

- **The z-test assumes independent requests.** Real traffic is bursty and retries correlate, so p-values are optimistic. The 1-percentage-point floor keeps a statistically significant but tiny change from being called a regression.
- **p95 comes from histogram buckets.** `histogram_quantile` interpolates within a bucket, so its resolution is bounded by the bucket boundaries (the OpenTelemetry defaults step 50 → 75 → 100 → 250 ms). A change inside one bucket can be hidden or exaggerated. The ratio and absolute floors, and a confidence that only claims sample size, reflect that it is not a significance test.
- **One window on each side is not a baseline.** A shift in traffic mix or time of day between the windows looks like an effect of the deployment. The caveats catch the large cases, a change in load or another deployment inside a window; a subtler shift in the mix of routes goes unnoticed.
- **The caveat thresholds are judgement, not statistics.** ×2 for load and a 0.5 cap on confidence are chosen to be plainly visible and easy to explain, not derived from data.
- **The comparison can only use what Prometheus still holds.** One not computed within retention (7 days) cannot be computed later.
- **Matching depends on naming.** A deployment finds its metrics only when `deployment.payload.service` (or the repository name) equals the `service_name` label, and the environment equals `deployment_environment_name`. The runbook says how to set them.

## Alternatives considered

| Option | Why not (now) |
|---|---|
| Copy windowed aggregates into PostgreSQL | Ruled out by the constraints: duplicated data with its own retention, and nothing gained over storing the query that reproduces it. |
| Prometheus recording or alerting rules | Rules cannot see deployment events, and editing rule files from the API means file writes and reloads (the reasoning of ADR-0005). |
| A rank test (Mann–Whitney) on per-request latency | Needs individual request durations, which live in ClickHouse traces, not in metrics. A candidate for supporting evidence later, not for the verdict. |
| A baseline from the same window on previous days | Handles time-of-day effects, but needs days of history that a fresh installation lacks. Future work. |
| Sequential testing during the after window | Gives earlier answers, but is more complex to explain and verify than one fixed comparison. |

## Revisit when

Real deployments produce verdicts that a person disagrees with, traffic shows strong daily seasonality, or ClickHouse evidence (failing trace and error log counts) is added to the result.
