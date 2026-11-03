import { cleanup, fireEvent, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Span, Trace } from '../api/traces.ts'
import { type Handler, apiError, fakeApi, loggedIn } from '../test/fakeApi.ts'
import { renderApp } from '../test/renderApp.tsx'

const traceId = '4bf92f3577b34da6a3ce929d0e0e4736'

function span(id: string, parent: string | null, name: string, ms: [number, number], extra: Partial<Span> = {}): Span {
  return {
    span_id: id.padStart(16, '0'),
    parent_span_id: parent?.padStart(16, '0') ?? null,
    service: 'demo-service',
    name,
    kind: 'internal',
    start_time: new Date(Date.UTC(2026, 9, 30, 12, 0, 0, ms[0])).toISOString(),
    duration_ns: (ms[1] - ms[0]) * 1_000_000,
    status: 'unset',
    attributes: {},
    resource_attributes: { 'service.name': 'demo-service' },
    events: [],
    ...extra,
  }
}

function trace(spans: Span[], extra: Partial<Trace> = {}): Trace {
  return {
    trace_id: traceId,
    start_time: '2026-10-30T12:00:00Z',
    end_time: '2026-10-30T12:00:00.120Z',
    spans,
    truncated: false,
    ...extra,
  }
}

// GET /orders, where one query takes nearly all of the 120 ms.
const slowQuery = 'SELECT id, total FROM orders WHERE customer_email = $1 ORDER BY created_at DESC'
const history = trace([
  span('1', null, 'GET /orders', [0, 120], { kind: 'server' }),
  span('2', '1', 'SELECT shop.orders', [5, 115], {
    kind: 'client',
    attributes: { 'db.system.name': 'postgresql', 'db.query.text': slowQuery },
  }),
  span('3', '1', 'encode response', [116, 118]),
  span('4', 'feedbeef', 'cache lookup', [1, 2]),
])

// POST /orders, failing in its insert.
const failed = trace([
  span('1', null, 'POST /orders', [0, 40], { kind: 'server', status: 'error' }),
  span('2', '1', 'INSERT shop.orders', [5, 30], {
    status: 'error',
    status_message: 'duplicate key',
    events: [
      {
        time: '2026-10-30T12:00:00.029Z',
        name: 'exception',
        attributes: {
          'exception.type': '*pgconn.PgError',
          'exception.message': 'duplicate key value violates unique constraint "orders_pkey"',
          'exception.stacktrace': 'goroutine 42 [running]:\nshop.(*Store).CreateOrder',
        },
      },
    ],
  }),
])

const noLogs = { body: { logs: [], next_cursor: null } }

function withTrace(handler: Handler, logs: Handler = noLogs) {
  return fakeApi({
    ...loggedIn,
    [`GET /api/v1/traces/${traceId}`]: handler,
    'GET /api/v1/logs': logs,
  })
}

// The slow-query warning the demo service writes in its database span.
const slowQueryLog = {
  timestamp: '2026-10-30T12:00:00.115Z',
  service: 'demo-service',
  severity_number: 13,
  severity_text: 'WARN',
  body: 'slow query',
  trace_id: traceId,
  span_id: '0000000000000002',
  attributes: { 'db.duration_ms': '110' },
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('trace detail', () => {
  it('draws every span, nested under its parent, and marks a missing parent (AC-3.1)', async () => {
    withTrace({ body: history })
    renderApp(`/traces/${traceId}`)
    const items = within(await screen.findByRole('list', { name: 'Spans' })).getAllByRole('listitem')

    expect(items.map((item) => item.querySelector('.waterfall__name')?.textContent)).toEqual([
      'GET /orders',
      'SELECT shop.orders',
      'encode response',
      'cache lookup',
    ])
    const indent = (i: number) => (items[i].querySelector('.waterfall__label') as HTMLElement).style.paddingLeft
    expect([indent(0), indent(1), indent(3)]).toEqual(['0rem', '0.875rem', '0rem'])
    expect(within(items[3]).getByText('missing parent')).toBeTruthy()
  })

  it('points at the span where the time went, and shows its statement', async () => {
    withTrace({ body: history })
    const app = renderApp(`/traces/${traceId}`)
    const findings = await screen.findByRole('list', { name: 'Findings' })
    expect(findings.textContent).toContain('Most time was spent in SELECT shop.orders (demo-service): 110 ms of its own, 92% of the trace.')

    fireEvent.click(within(findings).getByRole('button', { name: 'SELECT shop.orders' }))
    expect(app.location()).toBe(`/traces/${traceId}?span=0000000000000002`)
    const details = screen.getByRole('region', { name: 'Span details' })
    expect(within(details).getByText(slowQuery)).toBeTruthy()
    expect(within(details).getByText('postgresql')).toBeTruthy()
  })

  it('highlights a failure and shows its exception without leaving the page (AC-3.2)', async () => {
    withTrace({ body: failed })
    renderApp(`/traces/${traceId}`)
    const findings = await screen.findByRole('list', { name: 'Findings' })
    const failure = within(findings).getAllByRole('listitem')[1]
    expect(failure.textContent).toBe('2 spans failed, starting in INSERT shop.orders.')

    fireEvent.click(within(failure).getByRole('button', { name: 'INSERT shop.orders' }))
    const details = screen.getByRole('region', { name: 'Span details' })
    expect(within(details).getByText('error: duplicate key')).toBeTruthy()
    expect(within(details).getByText('*pgconn.PgError')).toBeTruthy()
    expect(within(details).getByText(/violates unique constraint/)).toBeTruthy()
    expect(within(details).getByText(/goroutine 42/)).toBeTruthy()
  })

  it('opens the span named in the URL', async () => {
    withTrace({ body: history })
    renderApp(`/traces/${traceId}?span=0000000000000003`)
    const details = await screen.findByRole('region', { name: 'Span details' })
    expect(within(details).getByRole('heading', { name: 'encode response' })).toBeTruthy()
  })

  it('links to the logs written during the trace and during one span', async () => {
    withTrace({ body: history })
    renderApp(`/traces/${traceId}?span=0000000000000002`)
    const bounds = 'from=2026-10-30T11%3A59%3A00.000Z&to=2026-10-30T12%3A01%3A00.120Z'
    expect((await screen.findByRole('link', { name: 'Logs for this trace' })).getAttribute('href')).toBe(
      `/logs?trace_id=${traceId}&${bounds}`,
    )
    expect(screen.getByRole('link', { name: 'Logs for this span' }).getAttribute('href')).toBe(
      `/logs?trace_id=${traceId}&span_id=0000000000000002&${bounds}`,
    )
  })

  it('lists the logs written during the trace, each naming the span that wrote it (FR-5)', async () => {
    const api = withTrace({ body: history }, { body: { logs: [slowQueryLog], next_cursor: null } })
    const app = renderApp(`/traces/${traceId}`)
    const logs = await screen.findByRole('list', { name: 'Trace log records' })
    expect(within(logs).getByText('slow query')).toBeTruthy()
    expect(api.sent()).toContain(
      `GET /api/v1/logs?trace_id=${traceId}&from=2026-10-30T11%3A59%3A00.000Z&to=2026-10-30T12%3A01%3A00.120Z`,
    )

    // The warning points at its span, and naming it selects that span.
    fireEvent.click(within(logs).getByRole('button', { name: 'SELECT shop.orders' }))
    expect(app.location()).toBe(`/traces/${traceId}?span=0000000000000002`)
    expect(screen.getByRole('region', { name: 'Span details' })).toBeTruthy()
  })

  it('narrows the logs to the selected span', async () => {
    const api = withTrace({ body: history })
    renderApp(`/traces/${traceId}?span=0000000000000003`)
    const panel = await screen.findByRole('region', { name: 'Trace logs' })
    expect(await within(panel).findByText('No logs were written during this trace.')).toBeTruthy()

    fireEvent.click(within(panel).getByRole('checkbox', { name: 'Only encode response' }))
    expect(await within(panel).findByText('This span wrote no logs.')).toBeTruthy()
    expect(api.sent().some((r) => r.includes('span_id=0000000000000003'))).toBe(true)
  })

  it('says plainly when a trace has expired', async () => {
    withTrace(apiError(404, 'not_found'))
    renderApp(`/traces/${traceId}`)
    expect((await screen.findByRole('alert')).textContent).toBe(
      'This trace is no longer available. It may have expired, or it was never exported.',
    )
  })

  it('warns when only part of a large trace was returned', async () => {
    withTrace({ body: { ...history, truncated: true } })
    renderApp(`/traces/${traceId}`)
    expect((await screen.findByRole('note')).textContent).toContain('only its earliest 4 spans are shown')
  })

  it('renders telemetry as text, never as markup', async () => {
    const hostile = '<img src=x onerror="alert(1)">'
    withTrace({
      body: trace([span('1', null, 'GET /', [0, 1], { attributes: { 'http.user_agent': hostile } })]),
    })
    renderApp(`/traces/${traceId}?span=0000000000000001`)
    const details = await screen.findByRole('region', { name: 'Span details' })
    expect(within(details).getByText(hostile)).toBeTruthy()
    expect(document.querySelector('img')).toBeNull()
  })
})
