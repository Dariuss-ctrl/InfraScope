import { cleanup, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { HealthReport } from './api/status.ts'
import { type Handler, fakeApi, healthy, loggedIn } from './test/fakeApi.ts'
import { renderApp } from './test/renderApp.tsx'

function withStatus(status: Handler) {
  fakeApi({ ...loggedIn, 'GET /api/v1/status': status })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('app shell', () => {
  it('redirects / to the services view', async () => {
    withStatus({ body: healthy })
    renderApp('/')
    expect(await screen.findByRole('heading', { level: 1, name: 'Services' })).toBeTruthy()
  })

  it('links every investigation view from the navigation', async () => {
    withStatus({ body: healthy })
    renderApp('/services')
    const nav = await screen.findByRole('navigation', { name: 'Main' })
    for (const name of ['Services', 'Traces', 'Logs', 'Alerts']) {
      expect(nav.querySelector(`a[href="/${name.toLowerCase()}"]`)?.textContent).toBe(name)
    }
  })

  it('shows a not-found page for unknown routes', async () => {
    withStatus({ body: healthy })
    renderApp('/nowhere')
    expect(await screen.findByRole('heading', { level: 1, name: 'Page not found' })).toBeTruthy()
  })
})

describe('status badge', () => {
  it('reports healthy dependencies', async () => {
    withStatus({ body: healthy })
    renderApp('/services')
    expect(await screen.findByText('All systems up')).toBeTruthy()
  })

  it('names the dependencies that are down', async () => {
    const degraded: HealthReport = {
      status: 'degraded',
      checked_at: '2026-10-29T00:00:00Z',
      dependencies: [
        { name: 'postgres', status: 'up', required: true, latency_ms: 1 },
        { name: 'clickhouse', status: 'down', required: false, latency_ms: 2000 },
      ],
    }
    withStatus({ body: degraded })
    renderApp('/services')
    expect(await screen.findByText('Degraded: clickhouse down')).toBeTruthy()
  })

  it('reports an unreachable API', async () => {
    withStatus(() => {
      throw new TypeError('Failed to fetch')
    })
    renderApp('/services')
    expect(await screen.findByText('API unreachable')).toBeTruthy()
  })
})
