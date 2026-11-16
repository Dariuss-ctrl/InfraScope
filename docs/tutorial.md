# Tutorial: your first hour with InfraScope

This tutorial takes you from a fresh clone to two real investigations:

1. **Start InfraScope** and log in.
2. **Watch a service** under load on the overview.
3. **Find a slow request** and the database query that caused it.
4. **Catch a bad deployment** with DeployRadar and follow its evidence.
5. **Send your own service's telemetry**, and connect your real deployments.

Everything runs on your machine with Docker. Nothing is published beyond `localhost`. Each part ends with a checkpoint, so you know it worked before moving on.

## Before you start

You need:

- **Docker** with Compose v2 (Docker Desktop on macOS or Windows). InfraScope runs seven containers, ClickHouse among them, so give Docker a few gigabytes of memory to spare.
- **Free ports** `8080` (dashboard), `8082` (demo service), `4317` and `4318` (OTLP). Any of them can be changed in `.env`.
- **A shell with `git`, `curl` and `openssl`.** On Windows, Git Bash has all three.
- **`make`** is optional. Every step shows the command without it too.

> **No `make`?** Set this once in your shell, and use the commands marked *without make*:
>
> ```bash
> COMPOSE="docker compose --env-file .env -f deployments/docker-compose.yml"
> ```

## Part 1: Start InfraScope

```bash
git clone https://github.com/Dariuss-ctrl/InfraScope.git
cd InfraScope
cp .env.example .env
```

Open `.env` and set it up:

1. **Replace every `change-me-…` password.** Use letters, digits, `-`, `_` and `.` only, because some passwords end up inside connection URLs. `openssl rand -hex 16` makes a good one.
2. **Turn on DeployRadar** for Part 4 by setting a webhook secret: `INFRASCOPE_GITHUB_WEBHOOK_SECRET=` followed by the output of `openssl rand -hex 32`.
3. **Shorten DeployRadar's windows for this tutorial**, so you wait minutes rather than a quarter of an hour: `INFRASCOPE_DEPLOYRADAR_WINDOW=3m` and `INFRASCOPE_DEPLOYRADAR_SETTLE=1m`. Put the defaults (`10m`, `2m`) back for real use.

Start the stack:

```bash
make up          # without make: $COMPOSE up -d --build --wait
make smoke       # without make: bash scripts/smoke.sh
```

The first start builds the images and fills the demo shop's database with a million orders, so it takes several minutes. `make up` returns once every service is healthy. `make smoke` then checks the path a browser takes, from the dashboard to every dependency.

> **Faster first start:** set `DEMO_SEED_ORDERS=100000` in `.env` before `make up`. The trade-off is that Part 3's slow query is much less slow with a smaller table.

**Checkpoint:** open <http://localhost:8080> and log in with `INFRASCOPE_ADMIN_USER` and `INFRASCOPE_ADMIN_PASSWORD` from `.env`. The badge in the header says **All systems up**. If it names a dependency instead, click it: the status page says what is down and what still works.

The first time you open each page, a short tip in the corner explains it. **Got it** puts that one away, **Skip all tips** turns them all off, and the **?** in the header brings the current page's tip back.

## Part 2: Watch a service

The stack includes `demo-service`, a small shop API instrumented with OpenTelemetry, and a k6 script that sends it realistic traffic. Start the traffic in a **second terminal** and leave it running for the rest of the tutorial:

```bash
make load RATE=20 DURATION=45m
# without make: $COMPOSE --profile load run --rm --no-deps -e RATE=20 -e DURATION=45m k6
```

On the **Services** page, `demo-service` appears within a minute:

- **The figures across the top** are its request rate, error rate and p50, p95 and p99 latency for the selected time range.
- **The three charts** show request rate, error rate and latency over time. "No data" is never drawn as zero.
- **Clicking any point on a chart** opens the traces behind that moment. Clicking the latency chart sorts them slowest first, and clicking the error chart shows only failures.

**Checkpoint:** the request rate settles near 20 req/s and the error rate stays near zero.

## Part 3: Find a slow request

Scenario S1 ships a "bad deploy" in the database: the index behind the order-history query goes missing. Turn it on:

```bash
make demo-reset
# without make: $COMPOSE exec -T postgres psql -U postgres -d shop -c "DROP INDEX CONCURRENTLY IF EXISTS orders_customer_email_idx"
```

Then investigate it the way you would a real incident:

1. **Services:** within a couple of minutes, p95 latency for `demo-service` climbs while the request rate stays flat. Something got slower without any change in traffic.
2. **Click the latency spike.** The trace search opens on the slowest requests from that moment, mostly `GET /orders`.
3. **Open the slowest trace.** The waterfall shows every span of the request. Above it, a finding names the span where **most time was spent**: one `SELECT` against `orders`. Select it to see the statement.
4. **Scroll to Logs.** The demo service logged a slow-query warning in that span. It carries the same trace ID, so it is listed here without any search.

Now fix it, and watch p95 come back down on the overview:

```bash
make demo-fix
# without make: $COMPOSE exec -T postgres psql -U postgres -d shop -c "CREATE INDEX CONCURRENTLY IF NOT EXISTS orders_customer_email_idx ON orders (customer_email)"
```

**Checkpoint:** you went from "p95 went up" to the exact statement responsible, without typing an ID. The [demo walkthrough](demo-script.md) covers this scenario in more depth.

## Part 4: Catch a bad deployment

DeployRadar compares a service's error rate and p95 latency **before and after each deployment**, gives a verdict, and keeps the evidence. Deployments normally arrive from GitHub. Here, a script plays that role: it redeploys the demo service and reports the deployment with the same signed webhooks GitHub sends.

Keep the load from Part 2 running, then deploy a healthy release:

```bash
make demo-deploy V=v1
# without make: bash scripts/deploy-demo.sh v1
```

The script prints `ok:` for each step. Along the way it checks two rules on your stack: a repeated webhook is answered as a duplicate, and a late "in progress" status does not undo a finished deployment.

Open **Deployments**. `v1` is listed as *comparing…*. The comparison waits for the after window to close. With the tutorial's settings that is about five minutes after the deployment, and the page refreshes itself. Then `v1` reads **No change**.

Now ship the bad release, in which one storage call in five fails:

```bash
make demo-deploy V=v2 ERROR_RATE=0.2
# without make: bash scripts/deploy-demo.sh v2 0.2
```

About five minutes later, `v2` reads **Regression**, with a red edge on its row. Open it:

- **The verdict comes first:** the reason in one line (the error rate before and after, and how significant the change is), the confidence, and the route whose error rate rose most.
- **Before and after** compares the two windows, and **By route** gives the same counts for each endpoint.
- **How these numbers were measured** holds the exact Prometheus query behind every number. Paste one into Prometheus and you get the same value.
- **See failing traces** opens the failed requests from the after window. Each one carries the exception on its span and an ERROR log with the same trace ID.

Back on **Services**, the charts now show each deployment as a dashed line, red for `v2`, right where the error rate jumps.

Finally, roll forward with a healthy release:

```bash
make demo-deploy V=v3
# without make: bash scripts/deploy-demo.sh v3
```

`v3`'s before window measured the broken `v2`, so it comes back **Improvement**.

**Checkpoint:** three deployments with three different verdicts, each backed by numbers you can check.

> **When a verdict says *Not enough traffic*,** the windows had fewer than 200 requests each. Check that the load from Part 2 is still running. A verdict can also carry an amber caveat, for example when traffic changed sharply between the windows. The verdict stands, but its confidence is capped and the caveat says why. [ADR-0007](decisions/0007-deployment-health-comparison.md) explains the method and its limits.

## Part 5: Bring your own service

InfraScope accepts OpenTelemetry (OTLP) on `localhost:4318` (HTTP) and `localhost:4317` (gRPC). Most OpenTelemetry SDKs and agents read their settings from standard environment variables, so you can usually connect a service without changing its code:

```bash
export OTEL_SERVICE_NAME=checkout
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local
```

What appears where:

- **Traces and logs** from any instrumented service appear in **Traces** and **Logs** right away.
- **The Services overview** reads the standard HTTP server metric, `http.server.request.duration`. Go's `otelhttp` and the OpenTelemetry Java agent emit it. Some SDKs still emit the older `http.server.duration` by default, and need `OTEL_SEMCONV_STABILITY_OPT_IN=http` to switch.
- **A service running in Docker** cannot reach your `localhost`. Attach it to InfraScope's network and send to the collector by name:

  ```bash
  docker run --network infrascope_default \
    -e OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318 \
    -e OTEL_SERVICE_NAME=checkout your-image
  ```

**Real deployments.** To have DeployRadar judge your own deployments, GitHub's `deployment_status` webhook has to reach InfraScope, and each deployment must name its service the way your telemetry does. The [runbook's Deployments section](runbook.md#deployments-deployradar) walks through it. It covers exposing only the webhook path through a reverse proxy or tunnel, and setting `payload.service` and the environment in your GitHub Actions workflow.

> **Before exposing anything beyond your machine,** make sure every password in `.env` is your own, and read [Exposing OTLP beyond localhost](runbook.md#exposing-otlp-beyond-localhost).

## Clean up

Stop the load with Ctrl+C in its terminal, then:

```bash
make down     # stop, keep the data        (without make: $COMPOSE down)
make reset    # stop and delete all data   (without make: $COMPOSE down -v --remove-orphans)
```

## If something goes wrong

| Symptom | Where to look |
|---|---|
| `make up` waits a long time for `demo-service` on the first start | It is still seeding the database. See [demo-service is unhealthy right after the first start](runbook.md#demo-service-is-unhealthy-right-after-the-first-start). |
| A port is already in use | Change `DASHBOARD_PORT`, `DEMO_SERVICE_PORT`, `OTLP_GRPC_PORT` or `OTLP_HTTP_PORT` in `.env`, then `make up`. |
| Login fails | [Logging in fails](runbook.md#logging-in-fails) |
| `make demo-deploy` fails with `webhooks_disabled` or a secret message | `INFRASCOPE_GITHUB_WEBHOOK_SECRET` is missing from `.env`. Set it as in Part 1, then `make up`. |
| A deployment stays *comparing…* | [A deployment comparison stays pending](runbook.md#a-deployment-comparison-stays-pending) |
| Every deployment says *Not enough traffic* | [Every deployment comes back insufficient_data](runbook.md#every-deployment-comes-back-insufficient_data) |

## Where next

- The [demo walkthrough](demo-script.md): both investigations as a ten-minute demonstration.
- The [runbook](runbook.md): operations, incidents and setup for real use.
- The [architecture](architecture.md) and [specification](SPEC.md): how it is built and why.
