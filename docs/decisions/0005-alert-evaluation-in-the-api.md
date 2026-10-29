# ADR-0005: Evaluate alert rules inside the API

- **Status:** Accepted
- **Date:** 2026-10-29

## Context

FR-6 requires threshold rules that users create and edit from the dashboard, a visible alert history, and correct `for`-duration semantics. Prometheus already has a rule engine, and Alertmanager handles notification. Prometheus rules, however, live in files: editing them from a UI means writing rule files and triggering reloads, and alert history needs Alertmanager plus a webhook receiver that stores events anyway.

## Decision

The Go API runs an **evaluator goroutine** on a 30-second ticker:

1. Load enabled rules and their current state from PostgreSQL.
2. Run one instant PromQL query per rule (`error_rate`, `latency_p95` or `request_rate` for a service and window).
3. Compute the next state with a **pure function** `next(state, value, rule, now)` over `ok → pending → firing → ok`.
4. Persist the new state and append an event in **one transaction**.

## Consequences

**Positive**

- Rule CRUD is ordinary API and database work, with validation shared by the API and `CHECK` constraints.
- The state machine is a pure function, so table-driven tests can cover every transition, including flapping and rules edited mid-incident (AC-6.1).
- State and history live in PostgreSQL, so they survive restarts and can be queried for the UI.

**Negative / accepted trade-offs**

- It works with a single instance only: running two API replicas would double-evaluate. That is acceptable under the single-node non-goal. If the API ever scales out, a PostgreSQL advisory lock gives simple leader election.
- We re-implement `for` semantics that Prometheus already provides. That code is small and fully tested.
- If the API is down, evaluation stops. The status endpoint shows the last evaluation time, so this is never silent.
- Notification is out of the MVP (an optional webhook is a stretch goal).

## Alternatives considered

| Option | Why not |
|---|---|
| Prometheus alerting rules + Alertmanager | Mature, but UI-driven editing becomes file generation and reloads, and history still needs a custom receiver and store. |
| Grafana alerting | Adds Grafana, and moves a core feature outside the product. |
| Evaluate in the collector | The collector is a pipeline, not a rule engine. There is no suitable component. |

## Revisit when

We need rich notification routing (silences, grouping, inhibition). At that point, generating Prometheus rules and handing off to Alertmanager becomes worth its complexity.
