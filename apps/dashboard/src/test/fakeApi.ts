import { vi } from 'vitest'
import type { CurrentUser } from '../api/auth.ts'
import type { HealthReport } from '../api/status.ts'

// A stand-in for the API at the fetch boundary. Each test lists exactly the
// responses it depends on, keyed by "METHOD /path"; handlers also see the
// query string. Anything unlisted answers 599, so a missing stub fails
// loudly instead of looking like an outage. A handler that throws behaves
// like a network failure.
export type Reply = { status?: number; body?: unknown; headers?: Record<string, string> }
export type Handler = Reply | ((request: { url: URL; body: unknown }) => Reply)

export type SentRequest = {
  method: string
  path: string // with the query string
  body: unknown
  contentType: string | undefined
}

export type FakeApi = {
  requests: SentRequest[]
  sent: () => string[] // "METHOD /path?query" for each request, in order
}

export function fakeApi(routes: Record<string, Handler>): FakeApi {
  const requests: SentRequest[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://dashboard.test')
      const method = init?.method ?? 'GET'
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
      const contentType = new Headers(init?.headers).get('Content-Type') ?? undefined
      requests.push({ method, path: url.pathname + url.search, body, contentType })

      const handler = routes[`${method} ${url.pathname}`]
      if (handler === undefined) {
        return new Response(`no fake for ${method} ${url.pathname}`, { status: 599 })
      }
      const reply = typeof handler === 'function' ? handler({ url, body }) : handler
      const status = reply.status ?? 200
      return new Response(status === 204 ? null : JSON.stringify(reply.body ?? null), {
        status,
        headers: { 'Content-Type': 'application/json', ...reply.headers },
      })
    }),
  )
  return { requests, sent: () => requests.map((r) => `${r.method} ${r.path}`) }
}

export function apiError(status: number, code: string, headers?: Record<string, string>): Reply {
  return { status, body: { error: { code, message: `fake ${code}` } }, headers }
}

export const admin: CurrentUser = { username: 'admin', expires_at: '2026-10-30T12:00:00Z' }

export const healthy: HealthReport = {
  status: 'ok',
  checked_at: '2026-10-30T00:00:00Z',
  dependencies: [{ name: 'postgres', status: 'up', required: true, latency_ms: 1 }],
}

// The responses every logged-in page needs. The overview, where a login
// lands, shows no services unless a test adds some.
export const loggedIn: Record<string, Handler> = {
  'GET /api/v1/auth/me': { body: admin },
  'GET /api/v1/status': { body: healthy },
  'GET /api/v1/services': { body: { services: [] } },
  // The overview asks each service for its deployments, to mark its charts.
  'GET /api/v1/deployments': { body: { deployments: [], next_cursor: null } },
}
