// Session endpoints (apps/api/openapi.yaml, tag "auth"). The session itself
// is an HttpOnly cookie, so the dashboard never holds a credential.
import { ApiError, apiGet, apiPost, describeError } from './client.ts'

export type CurrentUser = { username: string; expires_at: string }

// fetchSession returns null when nobody is logged in, so "logged out" is
// data rather than an error. Outages still throw: a session that cannot be
// checked is no reason to show the login page.
export async function fetchSession(signal?: AbortSignal): Promise<CurrentUser | null> {
  try {
    return await apiGet<CurrentUser>('/api/v1/auth/me', { signal })
  } catch (err) {
    if (err instanceof ApiError && err.code === 'unauthenticated') {
      return null
    }
    throw err
  }
}

export function login(username: string, password: string): Promise<void> {
  return apiPost('/api/v1/auth/login', { username, password })
}

export function logout(): Promise<void> {
  return apiPost('/api/v1/auth/logout')
}

// describeLoginError says what went wrong with a login and what to do next.
export function describeLoginError(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case 'unauthenticated':
        return 'Invalid username or password.'
      case 'rate_limited': {
        const wait = err.retryAfter === null ? 'a few seconds' : seconds(err.retryAfter)
        return `Too many login attempts. Try again in ${wait}.`
      }
      case 'forbidden':
        return 'The login came from a different address than the API, so it was refused. Open InfraScope at its own address.'
      case 'database_unavailable':
        return 'Logging in is unavailable because the configuration database (PostgreSQL) is down.'
    }
  }
  return describeError(err)
}

function seconds(n: number): string {
  return n === 1 ? '1 second' : `${n} seconds`
}
