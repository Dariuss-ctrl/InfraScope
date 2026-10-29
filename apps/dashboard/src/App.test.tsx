import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AppRoutes } from './App.tsx'
import type { HealthReport } from './api/status.ts'

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <AppRoutes />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function stubStatus(report: HealthReport) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({ ok: true, status: 200, json: async () => report })),
  )
}

const healthy: HealthReport = {
  status: 'ok',
  checked_at: '2026-10-29T00:00:00Z',
  dependencies: [{ name: 'postgres', status: 'up', required: true, latency_ms: 1 }],
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('app shell', () => {
  it('redirects / to the services view', async () => {
    stubStatus(healthy)
    renderAt('/')
    expect(await screen.findByRole('heading', { level: 1, name: 'Services' })).toBeTruthy()
  })

  it('links every investigation view from the navigation', () => {
    stubStatus(healthy)
    renderAt('/services')
    const nav = screen.getByRole('navigation', { name: 'Main' })
    for (const name of ['Services', 'Traces', 'Logs', 'Alerts']) {
      expect(nav.querySelector(`a[href="/${name.toLowerCase()}"]`)?.textContent).toBe(name)
    }
  })

  it('shows a not-found page for unknown routes', () => {
    stubStatus(healthy)
    renderAt('/nowhere')
    expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toBeTruthy()
  })
})

describe('status badge', () => {
  it('reports healthy dependencies', async () => {
    stubStatus(healthy)
    renderAt('/services')
    expect(await screen.findByText('All systems up')).toBeTruthy()
  })

  it('names the dependencies that are down', async () => {
    stubStatus({
      status: 'degraded',
      checked_at: '2026-10-29T00:00:00Z',
      dependencies: [
        { name: 'postgres', status: 'up', required: true, latency_ms: 1 },
        { name: 'clickhouse', status: 'down', required: false, latency_ms: 2000 },
      ],
    })
    renderAt('/services')
    expect(await screen.findByText('Degraded: clickhouse down')).toBeTruthy()
  })

  it('reports an unreachable API', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )
    renderAt('/services')
    expect(await screen.findByText('API unreachable')).toBeTruthy()
  })
})
