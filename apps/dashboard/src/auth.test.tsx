import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CurrentUser } from './api/auth.ts'
import { apiGet } from './api/client.ts'
import { admin, apiError, fakeApi, healthy, loggedIn } from './test/fakeApi.ts'
import { renderApp } from './test/renderApp.tsx'

const status = { 'GET /api/v1/status': { body: healthy } }
const loggedOut = { ...status, 'GET /api/v1/auth/me': apiError(401, 'unauthenticated') }

function logIn(username: string, password: string) {
  fireEvent.change(screen.getByLabelText('Username'), { target: { value: username } })
  fireEvent.change(screen.getByLabelText('Password'), { target: { value: password } })
  fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('login', () => {
  it('sends a visitor without a session to the login page, then back to what they asked for', async () => {
    let session: CurrentUser | null = null
    const api = fakeApi({
      ...status,
      'GET /api/v1/auth/me': () => (session ? { body: session } : apiError(401, 'unauthenticated')),
      'POST /api/v1/auth/login': () => {
        session = admin
        return { status: 204 }
      },
    })
    const app = renderApp('/traces?service=demo-service')

    await screen.findByRole('form', { name: 'Log in' })
    expect(app.location()).toBe('/login?next=%2Ftraces%3Fservice%3Ddemo-service')

    logIn('admin', 'correct horse battery')
    expect(await screen.findByRole('heading', { level: 1, name: 'Traces' })).toBeTruthy()
    expect(app.location()).toBe('/traces?service=demo-service')
    expect(api.requests).toContainEqual({
      method: 'POST',
      path: '/api/v1/auth/login',
      body: { username: 'admin', password: 'correct horse battery' },
      contentType: 'application/json',
    })
  })

  it('explains a wrong password', async () => {
    fakeApi({ ...loggedOut, 'POST /api/v1/auth/login': apiError(401, 'unauthenticated') })
    renderApp('/login')
    logIn('admin', 'not the password')
    expect((await screen.findByRole('alert')).textContent).toBe('Invalid username or password.')
  })

  it('says when to try again after too many attempts', async () => {
    fakeApi({
      ...loggedOut,
      'POST /api/v1/auth/login': apiError(429, 'rate_limited', { 'Retry-After': '7' }),
    })
    renderApp('/login')
    logIn('admin', 'a guess')
    expect((await screen.findByRole('alert')).textContent).toBe(
      'Too many login attempts. Try again in 7 seconds.',
    )
  })

  it('keeps a logged-in user away from the login page', async () => {
    fakeApi(loggedIn)
    const app = renderApp('/login?next=%2Flogs')
    expect(await screen.findByRole('heading', { level: 1, name: 'Logs' })).toBeTruthy()
    expect(app.location()).toBe('/logs')
  })

  it('never redirects to another site after logging in', async () => {
    fakeApi(loggedIn)
    const app = renderApp('/login?next=%2F%2Fevil.example%2Fphish')
    expect(await screen.findByRole('heading', { level: 1, name: 'Services' })).toBeTruthy()
    expect(app.location()).toBe('/services')
  })
})

describe('session', () => {
  it('reports a database outage instead of asking the user to log in again', async () => {
    fakeApi({ ...status, 'GET /api/v1/auth/me': apiError(503, 'database_unavailable') })
    const app = renderApp('/services')
    expect(await screen.findByRole('heading', { name: "Can't check your session" })).toBeTruthy()
    expect(screen.getByText(/PostgreSQL/)).toBeTruthy()
    expect(app.location()).toBe('/services')
  })

  it('logs out and forgets everything fetched during the session', async () => {
    let session: CurrentUser | null = admin
    const api = fakeApi({
      ...status,
      'GET /api/v1/auth/me': () => (session ? { body: session } : apiError(401, 'unauthenticated')),
      'POST /api/v1/auth/logout': () => {
        session = null
        return { status: 204 }
      },
    })
    const app = renderApp('/services')
    app.client.setQueryData(['traces', 'search'], { traces: [] })

    fireEvent.click(await screen.findByRole('button', { name: 'Log out' }))
    expect(await screen.findByRole('form', { name: 'Log in' })).toBeTruthy()
    expect(app.location()).toBe('/login')
    expect(api.sent()).toContain('POST /api/v1/auth/logout')
    expect(app.client.getQueryData(['traces', 'search'])).toBeUndefined()
  })

  it('returns to the login page when the session expires mid-investigation', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/probe': apiError(401, 'unauthenticated') })
    const app = renderApp('/services?range=1h')
    await screen.findByRole('heading', { level: 1, name: 'Services' })

    await app.client
      .fetchQuery({ queryKey: ['probe'], queryFn: () => apiGet('/api/v1/probe') })
      .catch(() => undefined)
    await waitFor(() => expect(app.location()).toBe('/login?next=%2Fservices%3Frange%3D1h'))
    expect(screen.getByRole('form', { name: 'Log in' })).toBeTruthy()
  })
})
