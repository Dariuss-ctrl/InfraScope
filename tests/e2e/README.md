# End-to-end tests (Phase 3)

Browser tests with [Playwright](https://playwright.dev/) against the full Compose stack, using `demo-service` as the source of real telemetry.

Planned journeys ([SPEC §11](../../docs/SPEC.md#11-testing-strategy)):

| Journey | Requirement |
|---|---|
| Log in; an unauthenticated visit redirects to login | NFR-3 |
| Service overview → click a latency spike → filtered trace search → trace waterfall | FR-2, FR-3, AC-2.3 |
| Trace → "Logs for this trace" → log line → back to the same trace | FR-5, AC-5.2 |
| Reload and back/forward keep every filter (state in the URL) | AC-5.1 |
| Stop ClickHouse → banner and *storage unavailable* states, overview still renders | NFR-1 |

They run in CI on `main` and nightly. They are slower than the rest of the pyramid, so they cover journeys, not edge cases.
