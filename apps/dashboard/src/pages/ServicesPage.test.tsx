import { cleanup, fireEvent, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Point, ServiceMetrics, ServiceSummary } from '../api/services.ts'
import { type Handler, apiError, fakeApi, loggedIn } from '../test/fakeApi.ts'
import { renderApp } from '../test/renderApp.tsx'

// ECharts needs a real layout engine, so the browser tests cover the chart
// itself. Here it lists its series and offers a button that clicks a point.
vi.mock('../components/TimeSeriesChart.tsx', () => ({
  TimeSeriesChart: (props: {
    label: string
    series: { name: string; points: Point[] }[]
    onSelect?: (t: Date) => void
    markers?: { label: string; color: string }[]
  }) => (
    <div role="img" aria-label={props.label}>
      {props.series.map((s) => (
        <span key={s.name}>
          {s.name}: {s.points.length} points
        </span>
      ))}
      {props.markers?.map((m) => (
        <span key={m.label}>
          marker {m.label} in {m.color}
        </span>
      ))}
      <button type="button" onClick={() => props.onSelect?.(new Date(props.series[0].points[1].t))}>
        Pick the second point of {props.label}
      </button>
    </div>
  ),
}))

const busy: ServiceSummary = {
  name: 'demo-service',
  request_rate: 12.3456,
  error_rate: 0.025,
  latency_seconds: { p50: 0.0042, p95: 0.1234, p99: 0.9 },
}

const idle: ServiceSummary = {
  name: 'idle-service',
  request_rate: null,
  error_rate: null,
  latency_seconds: { p50: null, p95: null, p99: null },
}

// One point a minute from 12:00 UTC.
function series(...values: (number | null)[]): Point[] {
  return values.map((v, i) => ({ t: new Date(Date.UTC(2026, 9, 30, 12, i)).toISOString(), v }))
}

function metrics(service: string, quiet: boolean): ServiceMetrics {
  const nothing = series(null, null, null)
  return {
    service,
    step_seconds: 30,
    series: quiet
      ? {
          request_rate: nothing,
          error_rate: nothing,
          latency_p50_seconds: nothing,
          latency_p95_seconds: nothing,
          latency_p99_seconds: nothing,
        }
      : {
          request_rate: series(1, 2, 3),
          error_rate: series(0, 0.5, 0),
          latency_p50_seconds: series(0.01, 0.02, 0.01),
          latency_p95_seconds: series(0.05, 0.4, 0.05),
          latency_p99_seconds: series(0.1, 0.9, 0.1),
        },
  }
}

function withServices(...services: ServiceSummary[]) {
  const routes: Record<string, Handler> = {
    ...loggedIn,
    'GET /api/v1/services': { body: { services } },
  }
  for (const s of services) {
    routes[`GET /api/v1/services/${s.name}/metrics`] = {
      body: metrics(s.name, s.request_rate === null),
    }
  }
  return fakeApi(routes)
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('service overview', () => {
  it("shows each service's rate, errors and latency, and no data for a quiet one", async () => {
    withServices(busy, idle)
    renderApp('/services')

    const card = await screen.findByRole('article', { name: 'demo-service' })
    expect(within(card).getByText('12.3 req/s')).toBeTruthy()
    expect(within(card).getByText('2.5%').className).toBe('stat--alert')
    for (const latency of ['4.2 ms', '123 ms', '900 ms']) {
      expect(within(card).getByText(latency)).toBeTruthy()
    }
    const quiet = screen.getByRole('article', { name: 'idle-service' })
    expect(within(quiet).getAllByText('no data')).toHaveLength(5)
  })

  it('says there is no data instead of drawing empty charts (AC-2.2)', async () => {
    withServices(idle)
    renderApp('/services')
    const quiet = await screen.findByRole('article', { name: 'idle-service' })
    expect(await within(quiet).findAllByText('No data in this range')).toHaveLength(3)
    expect(within(quiet).queryByRole('img')).toBeNull()
  })

  it('charts request rate, error rate and the latency percentiles', async () => {
    withServices(busy)
    renderApp('/services')
    const card = await screen.findByRole('article', { name: 'demo-service' })
    const latency = await within(card).findByRole('img', { name: 'Latency for demo-service' })
    for (const percentile of ['p50', 'p95', 'p99']) {
      expect(within(latency).getByText(`${percentile}: 3 points`)).toBeTruthy()
    }
    expect(within(card).getByRole('img', { name: 'Request rate for demo-service' })).toBeTruthy()
    expect(within(card).getByRole('img', { name: 'Error rate for demo-service' })).toBeTruthy()
  })

  it('marks the charts with the deployments in the range, and links to their verdicts', async () => {
    const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
    const deployment = (id: number, ref: string, finished: string, verdict: string | null) => ({
      id,
      ref,
      finished_at: finished,
      created_at: finished,
      verdict,
      confidence: verdict ? 0.99 : null,
      comparison_status: verdict ? 'done' : 'pending',
      service: 'demo-service',
    })
    const live = fakeApi({
      ...loggedIn,
      'GET /api/v1/services': { body: { services: [busy] } },
      'GET /api/v1/services/demo-service/metrics': { body: metrics('demo-service', false) },
      'GET /api/v1/deployments': {
        body: {
          deployments: [
            deployment(3, 'v3', minutesAgo(5), null),
            deployment(2, 'v2', minutesAgo(20), 'regression'),
            deployment(1, 'v1', minutesAgo(180), 'no_change'), // finished before the hour shown
          ],
          next_cursor: null,
        },
      },
    })
    renderApp('/services')

    const card = await screen.findByRole('article', { name: 'demo-service' })
    const errors = await within(card).findByRole('img', { name: 'Error rate for demo-service' })
    await within(errors).findByText('marker v2 in --bad')
    expect(within(errors).getByText('marker v3 in --muted')).toBeTruthy()
    expect(within(errors).queryByText(/marker v1/)).toBeNull()

    const list = within(card).getByRole('heading', { name: 'Deployments in this range' }).parentElement!
    expect(within(list).getByRole('link', { name: 'v2' }).getAttribute('href')).toBe('/deployments/2')
    expect(within(list).getByText('Regression')).toBeTruthy()
    expect(within(list).getByText('comparing…')).toBeTruthy()

    const query = live.requests.find((r) => r.path.startsWith('/api/v1/deployments?'))
    expect(new URL(query?.path ?? '', 'http://dashboard.test').searchParams.get('service')).toBe('demo-service')
  })

  it('draws the charts without markers when deployments cannot be loaded', async () => {
    fakeApi({
      ...loggedIn,
      'GET /api/v1/services': { body: { services: [busy] } },
      'GET /api/v1/services/demo-service/metrics': { body: metrics('demo-service', false) },
      'GET /api/v1/deployments': apiError(503, 'database_unavailable'),
    })
    renderApp('/services')
    const card = await screen.findByRole('article', { name: 'demo-service' })
    const latency = await within(card).findByRole('img', { name: 'Latency for demo-service' })
    expect(within(latency).queryByText(/^marker/)).toBeNull()
    expect(within(card).queryByRole('heading', { name: 'Deployments in this range' })).toBeNull()
  })

  it('asks the API for the chosen range', async () => {
    const api = withServices(busy)
    renderApp('/services?range=6h')
    await screen.findByRole('article', { name: 'demo-service' })

    const list = api.requests.find((r) => r.path.startsWith('/api/v1/services?'))
    const query = new URL(list?.path ?? '', 'http://dashboard.test').searchParams
    expect(Date.parse(query.get('to') ?? '') - Date.parse(query.get('from') ?? '')).toBe(
      6 * 60 * 60_000,
    )
  })

  it('keeps the chosen range in the URL', async () => {
    withServices(busy)
    const app = renderApp('/services')
    const presets = await screen.findByRole('group', { name: 'Time range' })

    fireEvent.click(within(presets).getByRole('button', { name: '24h' }))
    expect(app.location()).toBe('/services?range=24h')
    expect(within(presets).getByRole('button', { name: '24h' }).getAttribute('aria-pressed')).toBe(
      'true',
    )
    fireEvent.click(within(presets).getByRole('button', { name: '1h' }))
    expect(app.location()).toBe('/services')
  })

  it('opens the failed requests behind a point on the error chart (AC-2.3)', async () => {
    withServices(busy)
    const app = renderApp('/services')
    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Pick the second point of Error rate for demo-service',
      }),
    )
    // The point at 12:01 summarises the minute before it.
    expect(app.location()).toBe(
      '/traces?service=demo-service&status=error' +
        '&from=2026-10-30T12%3A00%3A00.000Z&to=2026-10-30T12%3A01%3A00.000Z',
    )
  })

  it('opens the slowest requests behind a point on the latency chart', async () => {
    withServices(busy)
    const app = renderApp('/services')
    fireEvent.click(
      await screen.findByRole('button', { name: 'Pick the second point of Latency for demo-service' }),
    )
    expect(app.location()).toBe(
      '/traces?service=demo-service&sort=duration' +
        '&from=2026-10-30T12%3A00%3A00.000Z&to=2026-10-30T12%3A01%3A00.000Z',
    )
  })

  it('links to the slowest and failed traces in the same range', async () => {
    withServices(busy)
    renderApp('/services?range=6h')
    const card = await screen.findByRole('article', { name: 'demo-service' })
    expect(within(card).getByRole('link', { name: 'Slowest traces' }).getAttribute('href')).toBe(
      '/traces?service=demo-service&sort=duration&range=6h',
    )
    expect(within(card).getByRole('link', { name: 'Failed traces' }).getAttribute('href')).toBe(
      '/traces?service=demo-service&status=error&range=6h',
    )
  })

  it('applies a custom range', async () => {
    withServices(busy)
    const app = renderApp('/services')
    fireEvent.click(await screen.findByRole('button', { name: 'Custom' }))
    const form = screen.getByRole('form', { name: 'Custom time range' })
    fireEvent.change(within(form).getByLabelText('From'), { target: { value: '2026-10-30T09:00:00' } })
    fireEvent.change(within(form).getByLabelText('To'), { target: { value: '2026-10-30T10:30:00' } })
    fireEvent.click(within(form).getByRole('button', { name: 'Apply' }))

    const query = new URL(app.location() ?? '', 'http://dashboard.test').searchParams
    expect(Date.parse(query.get('to') ?? '') - Date.parse(query.get('from') ?? '')).toBe(90 * 60_000)
  })

  it('says when the metrics store is down', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/services': apiError(503, 'metrics_unavailable') })
    renderApp('/services')
    expect((await screen.findByRole('alert')).textContent).toBe(
      'The metrics store (Prometheus) is unavailable.',
    )
  })

  it('says when no service reported any requests', async () => {
    withServices()
    renderApp('/services')
    expect(await screen.findByText('No service reported any requests in this time range.')).toBeTruthy()
  })
})
