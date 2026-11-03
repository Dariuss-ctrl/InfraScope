import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { TraceSummary } from '../api/traces.ts'
import { type FakeApi, type Handler, apiError, fakeApi, loggedIn } from '../test/fakeApi.ts'
import { renderApp } from '../test/renderApp.tsx'

const noon = 'from=2026-10-30T12%3A00%3A00.000Z&to=2026-10-30T13%3A00%3A00.000Z'

function trace(n: number, overrides: Partial<TraceSummary> = {}): TraceSummary {
  return {
    trace_id: n.toString(16).padStart(32, '0'),
    root_service: 'demo-service',
    root_operation: 'GET /orders',
    start_time: '2026-10-30T12:30:00Z',
    duration_ns: 1_500_000 * n,
    span_count: 3,
    has_error: false,
    ...overrides,
  }
}

function withTraces(handler: Handler): FakeApi {
  return fakeApi({ ...loggedIn, 'GET /api/v1/traces': handler })
}

// The query strings of every trace search, in order.
function searches(api: FakeApi): URLSearchParams[] {
  return api.requests
    .filter((r) => r.path.startsWith('/api/v1/traces'))
    .map((r) => new URL(r.path, 'http://dashboard.test').searchParams)
}

const nothing = { body: { traces: [], next_cursor: null } }

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('trace explorer', () => {
  it('sends the filters in the URL to the API', async () => {
    const api = withTraces(nothing)
    renderApp(`/traces?service=demo-service&status=error&sort=duration&min_duration_ms=100&${noon}`)
    await screen.findByText('No traces match these filters in this time range.')
    expect(api.sent()).toContain(
      `GET /api/v1/traces?service=demo-service&status=error&sort=duration&min_duration_ms=100&${noon}`,
    )
  })

  it('lists each trace with a link to its waterfall', async () => {
    withTraces({
      body: {
        traces: [
          trace(1),
          trace(2, { has_error: true, root_operation: 'POST /orders', span_count: 7 }),
        ],
        next_cursor: null,
      },
    })
    renderApp(`/traces?${noon}`)
    const rows = within(await screen.findByRole('table', { name: 'Traces' })).getAllByRole('row')
    expect(rows).toHaveLength(3) // the header and two traces

    const failed = within(rows[2])
    expect(failed.getByRole('link', { name: 'POST /orders' }).getAttribute('href')).toBe(
      `/traces/${trace(2).trace_id}`,
    )
    expect(failed.getByText('error')).toBeTruthy()
    expect(failed.getByText('3 ms')).toBeTruthy()
    expect(failed.getByText('7')).toBeTruthy()
  })

  it('loads more results with the cursor and the same time bounds', async () => {
    const api = withTraces(({ url }) =>
      url.searchParams.get('cursor') === 'page-2'
        ? { body: { traces: [trace(3)], next_cursor: null } }
        : { body: { traces: [trace(1), trace(2)], next_cursor: 'page-2' } },
    )
    renderApp('/traces?range=15m')
    const table = await screen.findByRole('table', { name: 'Traces' })

    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(4))
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()

    const [first, second] = searches(api)
    expect(second.get('cursor')).toBe('page-2')
    expect(second.get('from')).toBe(first.get('from'))
    expect(second.get('to')).toBe(first.get('to'))
  })

  it('searches with the entered filters and keeps the time range', async () => {
    withTraces(nothing)
    const app = renderApp('/traces?range=6h')
    const form = await screen.findByRole('form', { name: 'Trace filters' })

    fireEvent.change(within(form).getByLabelText('Service'), { target: { value: ' demo-service ' } })
    fireEvent.change(within(form).getByLabelText('Min duration (ms)'), { target: { value: '250' } })
    fireEvent.change(within(form).getByLabelText('Status'), { target: { value: 'error' } })
    fireEvent.click(within(form).getByRole('button', { name: 'Search' }))

    expect(app.location()).toBe('/traces?service=demo-service&status=error&min_duration_ms=250&range=6h')
  })

  it('fills the form from the URL', async () => {
    withTraces(nothing)
    renderApp('/traces?service=demo-service&sort=duration')
    const form = await screen.findByRole('form', { name: 'Trace filters' })
    expect((within(form).getByLabelText('Service') as HTMLInputElement).value).toBe('demo-service')
    expect((within(form).getByLabelText('Order') as HTMLSelectElement).value).toBe('duration')
  })

  it('clears the filters but keeps the time range', async () => {
    withTraces(nothing)
    const app = renderApp('/traces?service=demo-service&status=error&range=6h')
    fireEvent.click(await screen.findByRole('button', { name: 'Clear' }))
    expect(app.location()).toBe('/traces?range=6h')
  })

  it('says when trace storage is down', async () => {
    withTraces(apiError(503, 'storage_unavailable'))
    renderApp('/traces')
    expect((await screen.findByRole('alert')).textContent).toBe(
      'Trace and log storage (ClickHouse) is unavailable.',
    )
  })

  it('shows why the API rejected a search', async () => {
    withTraces({
      status: 400,
      body: {
        error: {
          code: 'invalid_argument',
          message: 'Invalid query.',
          fields: [{ field: 'from', message: 'the range must not exceed 24h0m0s' }],
        },
      },
    })
    renderApp('/traces')
    expect((await screen.findByRole('alert')).textContent).toBe(
      'from: the range must not exceed 24h0m0s',
    )
  })
})
