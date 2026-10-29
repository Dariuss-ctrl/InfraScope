# dashboard

InfraScope's investigation UI: React 19, TypeScript 7 and Vite 8, linted with oxlint and tested with Vitest and Testing Library. In production it is a static build served by unprivileged nginx, which also proxies `/api/` to the Go API ([`nginx.conf`](nginx.conf)).

## What exists today (Phase 0)

- The app shell: navigation for Services, Traces, Logs and Alerts. Each view states which phase builds it and which requirement specifies it.
- A **status badge** fed by `GET /api/v1/status`, refreshed every 15 s. It names any dependency that is down, which is the start of the degraded-mode banner required by NFR-1.

Views are built in Phase 3 (FR-2 to FR-5) and Phase 4 (FR-6). ECharts is used for time series; the trace waterfall is a custom component.

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
