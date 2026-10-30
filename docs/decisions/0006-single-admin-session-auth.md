# ADR-0006: Single admin account with server-side sessions for the MVP

- **Status:** Accepted
- **Date:** 2026-10-29

## Context

NFR-3 requires that the dashboard and API are not open to anyone who can reach the port. Telemetry often contains internal URLs, SQL statements and error details. Multi-user management and RBAC are explicit non-goals, but the mechanism we pick should be secure by default and not need replacing just to add a second user later.

## Decision

- One administrator account. Its username and password come from `.env` and are written to the `users` table at startup (bcrypt hash; existing sessions are revoked if the password changes).
- **Server-side sessions:** login issues a random 256-bit token. Only its hash is stored in `sessions`, with an expiry. Logout deletes the row.
- The token travels in an `HttpOnly`, `SameSite=Strict` cookie, marked `Secure` when served over TLS. Nothing sensitive is kept in browser storage.
- Login is rate-limited per client IP. Mutating endpoints require JSON bodies and reject a request whose `Origin` header names a different origin (CSRF defence in depth). Requests without `Origin` come from non-browser clients and are allowed.
- Unauthenticated routes: `/healthz`, `/readyz`, `/api/v1/status` and `/api/v1/auth/login`.

## Consequences

**Positive**

- Sessions can really be revoked (logout, password change), unlike stateless tokens.
- The browser never holds a credential that works outside the cookie's site.
- The `users` and `sessions` model extends naturally to more users or OIDC later.

**Negative / accepted trade-offs**

- There is only one user, with no roles or audit trail per user.
- Every authenticated request needs a session lookup in PostgreSQL. That is trivial at this scale, and the API depends on PostgreSQL anyway.
- TLS termination is left to the deployer. Locally, everything is bound to `127.0.0.1`.

## Alternatives considered

| Option | Why not |
|---|---|
| No auth, rely on localhost binding | Fails NFR-3 as soon as the bind address changes. |
| HTTP basic auth at nginx | No logout, credentials sent on every request, poor UX. |
| Stateless JWTs | Revocation needs extra state anyway, and token theft lasts until expiry. Easy to get subtly wrong. |
| OIDC | Needs an external identity provider for a self-hosted single-user tool. Good future work. |

## Revisit when

A second user or an external identity provider is needed.
