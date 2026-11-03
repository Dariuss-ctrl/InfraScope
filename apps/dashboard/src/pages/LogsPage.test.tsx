import { cleanup, fireEvent, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { LogRecord } from '../api/logs.ts'
import { type Handler, apiError, fakeApi, loggedIn } from '../test/fakeApi.ts'
import { renderApp } from '../test/renderApp.tsx'

const traceId = '4bf92f3577b34da6a3ce929d0e0e4736'
const spanId = '00f067aa0ba902b7'
const noon = 'from=2026-10-30T12%3A00%3A00.000Z&to=2026-10-30T13%3A00%3A00.000Z'

function record(body: string, extra: Partial<LogRecord> = {}): LogRecord {
  return {
    timestamp: '2026-10-30T12:30:00.123456Z',
    service: 'demo-service',
    severity_number: 9,
    severity_text: 'INFO',
    body,
    trace_id: traceId,
    span_id: spanId,
    attributes: { 'http.route': '/orders' },
    ...extra,
  }
}

function withLogs(handler: Handler) {
  return fakeApi({ ...loggedIn, 'GET /api/v1/logs': handler })
}

const nothing = { body: { logs: [], next_cursor: null } }

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('log explorer', () => {
  it('sends the filters in the URL to the API', async () => {
    const api = withLogs(nothing)
    renderApp(`/logs?service=demo-service&severity=warn&q=slow+query&trace_id=${traceId.toUpperCase()}&${noon}`)
    await screen.findByText('No logs were written during this trace in this time range.')
    expect(api.sent()).toContain(
      `GET /api/v1/logs?service=demo-service&severity=warn&q=slow+query&trace_id=${traceId}&${noon}`,
    )
  })

  it('shows each record with its severity, and links it to the span that wrote it (FR-5)', async () => {
    withLogs({
      body: {
        logs: [
          record('slow query', { severity_number: 13, severity_text: 'WARN' }),
          record('no trace here', { trace_id: null, span_id: null }),
        ],
        next_cursor: null,
      },
    })
    renderApp(`/logs?${noon}`)
    const [warning, untraced] = within(await screen.findByRole('list', { name: 'Log records' })).getAllByRole(
      'listitem',
    )

    expect(warning.className).toBe('log log--warn')
    expect(within(warning).getByText('WARN')).toBeTruthy()
    expect(within(warning).getByRole('link', { name: `Trace ${traceId}` }).getAttribute('href')).toBe(
      `/traces/${traceId}?span=${spanId}`,
    )
    expect(within(untraced).queryByRole('link')).toBeNull()
  })

  it('expands a record to its full body and attributes', async () => {
    withLogs({ body: { logs: [record('order created')], next_cursor: null } })
    renderApp(`/logs?${noon}`)
    const entry = within(await screen.findByRole('list', { name: 'Log records' })).getByRole('listitem')

    fireEvent.click(within(entry).getByRole('button', { name: 'Details' }))
    const attributes = within(entry).getByRole('table', { name: 'Log attributes' })
    expect(within(attributes).getByText('/orders')).toBeTruthy()
    expect(within(entry).getByText('INFO (9)')).toBeTruthy()
    expect(within(entry).getByRole('button', { name: 'Hide' }).getAttribute('aria-expanded')).toBe('true')
  })

  it('names the trace it is showing logs for, with a way back to it', async () => {
    withLogs({ body: { logs: [record('order created')], next_cursor: null } })
    renderApp(`/logs?trace_id=${traceId}&span_id=${spanId}&${noon}`)
    const note = await screen.findByRole('note')
    expect(note.textContent).toBe(`Logs written during trace ${traceId} in span ${spanId}.`)
    expect(within(note).getByRole('link').getAttribute('href')).toBe(`/traces/${traceId}?span=${spanId}`)
  })

  it('searches with the entered filters and keeps the time range', async () => {
    withLogs(nothing)
    const app = renderApp('/logs?range=6h')
    const form = await screen.findByRole('form', { name: 'Log filters' })
    fireEvent.change(within(form).getByLabelText('Minimum severity'), { target: { value: 'error' } })
    fireEvent.change(within(form).getByLabelText('Text'), { target: { value: 'timeout' } })
    fireEvent.click(within(form).getByRole('button', { name: 'Search' }))
    expect(app.location()).toBe('/logs?severity=error&q=timeout&range=6h')
  })

  it('renders log bodies as text, never as markup', async () => {
    const hostile = '<script>alert(1)</script><img src=x onerror=alert(2)>'
    withLogs({ body: { logs: [record(hostile)], next_cursor: null } })
    renderApp(`/logs?${noon}`)
    expect(await screen.findByText(hostile)).toBeTruthy()
    expect(document.querySelector('img, script')).toBeNull()
  })

  it('says when log storage is down', async () => {
    withLogs(apiError(503, 'storage_unavailable'))
    renderApp('/logs')
    expect((await screen.findByRole('alert')).textContent).toBe(
      'Trace and log storage (ClickHouse) is unavailable.',
    )
  })

  it('passes a malformed trace ID on, so the API can say what is wrong with it', async () => {
    const api = withLogs({
      status: 400,
      body: {
        error: {
          code: 'invalid_argument',
          message: 'Invalid query.',
          fields: [{ field: 'trace_id', message: 'must be 32 hexadecimal characters' }],
        },
      },
    })
    renderApp('/logs?trace_id=nope')
    expect((await screen.findByRole('alert')).textContent).toBe('trace_id: must be 32 hexadecimal characters')
    expect(api.sent().some((r) => r.includes('trace_id=nope'))).toBe(true)
  })
})
