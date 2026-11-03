import { cleanup, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SchemaReport } from '../api/diagnostics.ts'
import type { HealthReport } from '../api/status.ts'
import { type Handler, apiError, fakeApi, loggedIn } from '../test/fakeApi.ts'
import { renderApp } from '../test/renderApp.tsx'

const schemaDown: HealthReport = {
  status: 'degraded',
  checked_at: '2026-10-30T12:00:00Z',
  dependencies: [
    { name: 'postgres', status: 'up', required: true, latency_ms: 2 },
    { name: 'clickhouse', status: 'up', required: false, latency_ms: 3 },
    { name: 'clickhouse_schema', status: 'down', required: false, latency_ms: 4 },
  ],
}

const missingIndex: SchemaReport = {
  checked_at: '2026-10-30T12:00:00Z',
  complete: false,
  missing: [
    {
      kind: 'index',
      table: 'otel.otel_logs',
      name: 'idx_lower_body',
      effect: 'Text search in logs is slower: every message in the time range is read.',
    },
  ],
}

function withPlatform(status: HealthReport, schema: Handler) {
  return fakeApi({
    ...loggedIn,
    'GET /api/v1/status': { body: status },
    'GET /api/v1/diagnostics/schema': schema,
  })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('platform status page', () => {
  it('lists every dependency with what breaks while it is down', async () => {
    withPlatform(schemaDown, { body: { ...missingIndex, complete: true, missing: [] } })
    renderApp('/status')
    const table = await screen.findByRole('table', { name: 'Dependencies' })
    const row = within(table).getByRole('row', { name: /Configuration database/ })
    expect(within(row).getByText('(required)')).toBeTruthy()
    expect(within(row).getByText('Logging in and checking sessions fail.')).toBeTruthy()
    expect(await screen.findByText('Every table and index the searches rely on is present.')).toBeTruthy()
  })

  it('names a missing index and what it slows down, without server logs', async () => {
    withPlatform(schemaDown, { body: missingIndex })
    renderApp('/status')
    const missing = await screen.findByRole('table', { name: 'Missing from the schema' })
    expect(within(missing).getByText('index idx_lower_body on otel.otel_logs')).toBeTruthy()
    expect(within(missing).getByText(/every message in the time range is read/)).toBeTruthy()
    expect(screen.getByText(/ClickHouse schema incomplete/)).toBeTruthy()
  })

  it('says why the schema cannot be checked while ClickHouse is down', async () => {
    withPlatform(schemaDown, apiError(503, 'storage_unavailable'))
    renderApp('/status')
    const page = await screen.findByRole('heading', { level: 2, name: 'Trace and log storage schema' })
    expect(page).toBeTruthy()
    expect(
      (await screen.findAllByRole('alert')).some((a) =>
        a.textContent?.includes('Trace and log storage (ClickHouse) is unavailable.'),
      ),
    ).toBe(true)
  })
})

describe('outage banner', () => {
  it('appears on every page, saying what is down and what still works', async () => {
    fakeApi({
      ...loggedIn,
      'GET /api/v1/status': {
        body: {
          ...schemaDown,
          status: 'degraded',
          dependencies: [
            { name: 'clickhouse', status: 'down', required: false, latency_ms: 2000 },
            { name: 'clickhouse_schema', status: 'down', required: false, latency_ms: 2000 },
          ],
        },
      },
    })
    renderApp('/services')
    const banner = await screen.findByRole('alert')
    expect(banner.textContent).toContain(
      'Trace and log storage (ClickHouse) is down. Trace and log search are unavailable. Service metrics still work.',
    )
    expect(banner.textContent).not.toContain('missing tables or indexes')
    expect(within(banner).getByRole('link', { name: 'Platform status' }).getAttribute('href')).toBe('/status')
  })

  it('stays away while everything is up', async () => {
    fakeApi(loggedIn)
    renderApp('/services')
    await screen.findByText('All systems up')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('links the header badge to the status page', async () => {
    fakeApi(loggedIn)
    renderApp('/services')
    expect((await screen.findByRole('link', { name: 'All systems up' })).getAttribute('href')).toBe('/status')
  })
})
