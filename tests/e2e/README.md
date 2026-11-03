# Browser journeys

[Playwright](https://playwright.dev/) tests that use the dashboard the way a person does, against a running Compose stack, with `demo-service` producing real telemetry. Nothing is mocked: every page talks to the real API through nginx. They cover journeys, not edge cases; the dashboard's unit tests cover those.

| Journey | What it proves | Requirement |
|---|---|---|
| [`auth.spec.ts`](specs/auth.spec.ts) | A protected page sends a visitor to the login page and back. A wrong password is explained. The session survives a reload, and logging out ends it on the server. | NFR-3 |
| [`overview.spec.ts`](specs/overview.spec.ts) | The overview shows real request rates and charts, and a click on the latency chart opens that service's slowest traces for that moment. | FR-2, AC-2.3 |
| [`traces.spec.ts`](specs/traces.spec.ts) | A request found through the trace explorer opens as a waterfall, which points at the slow query and shows its statement. | FR-3 |
| [`workflow.spec.ts`](specs/workflow.spec.ts) | The whole investigation for scenario S1, without typing an ID: overview → slowest traces → the span that took the time and its statement → the slow-query warning it logged → the log explorer → back to the same span → back to the overview. | FR-5, AC-5.2 |
| [`degraded.disruptive.ts`](specs/degraded.disruptive.ts) | A dropped ClickHouse index is named on the status page with its effect, and the runbook's fix restores it. With ClickHouse stopped, every page says so and the overview keeps working. | NFR-1 |

A setup step logs in once and saves the session for the other journeys (in the git-ignored `.auth/`), so a run stays inside the login rate limit.

## Running them

They need a running stack (`make up`) and the admin credentials from `.env`:

```bash
make test-e2e
```

Without `make`:

```bash
cd tests/e2e && npm ci
npx playwright install chromium        # once; not needed with E2E_BROWSER_CHANNEL
set -a; . ../../.env; set +a
npx playwright test
```

`E2E_BROWSER_CHANNEL=msedge` (or `chrome`) drives a browser that is already installed instead of Playwright's own Chromium. `BASE_URL` and `DEMO_URL` point the journeys at a stack on other ports.

## The disruptive journeys

`degraded.disruptive.ts` drops an index and stops ClickHouse, so it is skipped unless the stack is disposable. To run it, set `E2E_DISRUPTIVE=1` and set `E2E_COMPOSE` to that stack's `docker compose` command, run from the repository root. CI does both, because its stack exists only for the run. To run it locally, start a separate copy of the stack under its own project name and ports, never the one you use:

```bash
export DASHBOARD_PORT=18080 DEMO_SERVICE_PORT=18082 OTLP_GRPC_PORT=14317 OTLP_HTTP_PORT=14318
docker compose -p infrascope-e2e --env-file .env -f deployments/docker-compose.yml up -d --build --wait
BASE_URL=http://localhost:18080 DEMO_URL=http://localhost:18082 E2E_DISRUPTIVE=1 \
  E2E_COMPOSE="docker compose -p infrascope-e2e --env-file .env -f deployments/docker-compose.yml" make test-e2e
docker compose -p infrascope-e2e --env-file .env -f deployments/docker-compose.yml down -v
```

Leave out `--rmi` when tearing it down: both projects share the `infrascope/*:dev` image tags.

## In CI

The smoke job runs every journey, the disruptive ones included, after the pipeline test, on the stack it has just started. It seeds 100,000 orders instead of a million, so the S1 query is fast there; the job lowers `DEMO_SLOW_QUERY_THRESHOLD` so the query still logs its slow-query warning. When a journey fails, the HTML report, with traces and screenshots, is kept as the `playwright-report` artifact.
