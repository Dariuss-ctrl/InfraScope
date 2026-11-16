# dashboard

InfraScope's investigation UI: React 19, TypeScript 7 and Vite 8, linted with oxlint and tested with Vitest and Testing Library. In production it is a static build served by unprivileged nginx, which also proxies `/api/` to the Go API ([`nginx.conf`](nginx.conf)).

## Views

| Route | What it shows | Requirement |
|---|---|---|
| `/login` | The login form, with the platform status, because a database outage explains a failed login. Afterwards the user returns to the page they asked for. | NFR-3 |
| `/services` | Each service's request rate, error rate and p50/p95/p99 latency, with charts. A click on a chart opens the traces behind that point. Deployments in the range are drawn on the charts, coloured by verdict, and listed below them with links. No data is never drawn as zero. | FR-2 |
| `/traces` | Trace search by service, operation, duration, status and time range, newest or slowest first. | FR-3 |
| `/traces/:traceId` | The waterfall, nested by parent span, naming the span that spent the most time and the first failure. Span details show the statement, events and attributes, and the logs written during the trace are listed below. `?span=` selects a span. | FR-3, FR-5 |
| `/logs` | Log search by service, minimum severity, text, trace and span. Each record links to the span that wrote it. | FR-4, FR-5 |
| `/status` | Every dependency and what breaks while it is down, and any missing ClickHouse table or index with its effect. | NFR-1 |
| `/deployments` | Deployments reported by GitHub, newest first, with their state and the verdict of their before/after comparison. Pending comparisons refresh themselves. | FR-9 |
| `/deployments/:deploymentId` | One deployment: the verdict and its reasons, the most affected route, the before/after table and the same counts per route, the PromQL behind every number, links to the failing traces and error logs of the after window, and the status history, including statuses that arrived too late to apply. | FR-9 |
| `/alerts` | Says that alerting comes in Phase 5. | FR-6 |

Every page except `/login` needs a session. A banner under the header names whatever is down and what still works. The first visit to each page shows a short tip explaining it ([`tips.ts`](src/tips.ts)). It can be dismissed per page or skipped altogether, the header's **?** brings it back, and the choice is kept in the browser.

## How it is built

- `src/api/` has one module per API area. `client.ts` turns every failure into an `ApiError` that carries the API's error code, so a page can say what is unavailable rather than just that something failed.
- `src/pages/` holds the views and `src/components/` the parts they share.
- Logic lives outside components and is unit-tested on its own: the waterfall layout and self time (`waterfall.ts`), time ranges and chart windows (`timeRange.ts`), URL filters (`links.ts`) and formatting (`format.ts`).
- ECharts loads with the first chart, not with the login page. The waterfall is a custom component.
- Styling is one stylesheet, [`src/index.css`](src/index.css). Colours, type and shape are tokens on `:root`, redefined for dark mode, which follows the system setting. Components never name a colour, and the charts read the same tokens. Red, amber and green only ever mean status; the accent marks what can be clicked or is selected.
- Tests render the whole app against a fake API at the `fetch` boundary (`src/test/fakeApi.ts`). The [browser journeys](../../tests/e2e/README.md) run against the real stack.

## Scripts

| Command | What it does |
|---|---|
| `npm run dev` | Dev server with `/api` proxied to `localhost:8081` (run the API with `make up-debug` or `go run`) |
| `npm run build` | Type-check, then a production build into `dist/` |
| `npm run typecheck` | Type-check only |
| `npm run lint` | oxlint ([`.oxlintrc.json`](.oxlintrc.json)) |
| `npm test` | Vitest, once |

## Conventions

- **Telemetry is untrusted input.** Span attributes and log bodies are rendered as text, never as HTML. `react/no-danger` is a lint error ([SPEC §10](../../docs/SPEC.md#10-security-model)).
- **State lives in the URL.** Filters and selections are query parameters, so every investigation can be shared and the back button works (AC-5.1).
- **Server state goes through TanStack Query.** No hand-rolled fetch-and-store in components.
