import { cleanup, fireEvent, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { DeploymentDetail, DeploymentSummary } from '../api/deployments.ts'
import { apiError, fakeApi, loggedIn } from '../test/fakeApi.ts'
import { renderApp } from '../test/renderApp.tsx'

function summary(id: number, overrides: Partial<DeploymentSummary> = {}): DeploymentSummary {
  return {
    id,
    provider: 'github',
    repository: 'infrascope/demo-service',
    provider_deployment_id: String(1000 + id),
    service: 'demo-service',
    environment: 'local',
    ref: 'main',
    sha: '0123456789abcdef0123456789abcdef01234567',
    creator: 'deploy-demo',
    description: `demo-service v${id}`,
    created_at: '2026-11-15T10:00:00Z',
    state: 'success',
    state_at: '2026-11-15T10:01:00Z',
    finished_at: '2026-11-15T10:01:00Z',
    updated_at: '2026-11-15T10:01:00Z',
    comparison_status: null,
    verdict: null,
    confidence: null,
    ...overrides,
  }
}

const regression: DeploymentDetail = {
  ...summary(2),
  changes: [
    { delivery_id: 'deploy-1-1', state: 'in_progress', state_at: '2026-11-15T10:00:00Z', applied: true, recorded_at: '2026-11-15T10:00:00Z' },
    { delivery_id: 'deploy-1-2', state: 'success', state_at: '2026-11-15T10:01:00Z', applied: true, recorded_at: '2026-11-15T10:01:00Z' },
    { delivery_id: 'deploy-1-3', state: 'in_progress', state_at: '2026-11-15T10:00:00Z', applied: false, recorded_at: '2026-11-15T10:01:02Z' },
  ],
  comparison: {
    status: 'done',
    attempts: 1,
    before: { from: '2026-11-15T09:58:00Z', to: '2026-11-15T10:01:00Z' },
    after: { from: '2026-11-15T10:02:00Z', to: '2026-11-15T10:05:00Z' },
    error: null,
    result: {
      verdict: 'regression',
      error_rate_verdict: 'regression',
      p95_verdict: 'no_change',
      confidence: 0.99995,
      reasons: [
        'error rate 0.4% → 19.8% (+19.4 pp, p<0.001)',
        'p95 (non-5xx) 48 ms → 51 ms (+6%)',
        'traffic fell 57% between the windows (3607 → 1554 requests), so they did not see the same load.',
      ],
      before: { requests: 1799.6, errors: 7.2, error_rate: 0.004, p95_seconds: 0.048 },
      after: { requests: 1801.2, errors: 356.6, error_rate: 0.198, p95_seconds: 0.051 },
      queries: {
        after_errors: {
          promql:
            'sum(increase(http_server_request_duration_seconds_count{service_name="demo-service",deployment_environment_name="local",http_response_status_code=~"5.."}[180s]))',
          time: '2026-11-15T10:05:00Z',
          range: '180s',
        },
        before_requests: {
          promql: 'sum(increase(http_server_request_duration_seconds_count{service_name="demo-service"}[180s]))',
          time: '2026-11-15T10:01:00Z',
          range: '180s',
        },
      },
      routes: [
        {
          route: '/orders',
          before: { requests: 200, errors: 0, error_rate: 0 },
          after: { requests: 190, errors: 80, error_rate: 0.421 },
        },
        {
          route: '/products',
          before: { requests: 1599.6, errors: 7.2, error_rate: 0.0045 },
          after: { requests: 1611.2, errors: 9, error_rate: 0.0056 },
        },
      ],
      method_version: 1,
      computed_at: '2026-11-15T10:05:40Z',
    },
  },
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('deployments list', () => {
  it('shows each deployment with its state and health verdict', async () => {
    fakeApi({
      ...loggedIn,
      'GET /api/v1/deployments': {
        body: {
          deployments: [
            summary(4, { state: 'in_progress', finished_at: null }),
            summary(3, { comparison_status: 'pending' }),
            summary(2, { comparison_status: 'done', verdict: 'regression', confidence: 0.9999 }),
            summary(1, { comparison_status: 'done', verdict: 'insufficient_data', confidence: 0 }),
          ],
          next_cursor: null,
        },
      },
    })
    renderApp('/deployments')
    const table = await screen.findByRole('table', { name: 'Deployments' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(4)
    expect(within(rows[0]).getByText('in progress')).toBeTruthy()
    expect(within(rows[0]).getByText('not compared')).toBeTruthy()
    expect(within(rows[1]).getByText('comparing…')).toBeTruthy()
    expect(within(rows[2]).getByText('Regression').className).toContain('badge--error')
    expect(within(rows[3]).getByText('Not enough traffic').className).toContain('badge--warn')
    expect(within(rows[2]).getByRole('link', { name: 'main' }).getAttribute('href')).toBe('/deployments/2')
    // Releases from one commit are told apart by their description.
    expect(within(rows[2]).getByText(/demo-service v2 ·/)).toBeTruthy()
    // A regression marks its whole row; the others stay plain.
    expect(rows.map((r) => r.className)).toEqual(['', '', 'results__row--bad', ''])
  })

  it('sends the filters and pages with the cursor', async () => {
    const api = fakeApi({
      ...loggedIn,
      'GET /api/v1/deployments': ({ url }) =>
        url.searchParams.get('cursor') === '2'
          ? { body: { deployments: [summary(1)], next_cursor: null } }
          : { body: { deployments: [summary(3), summary(2)], next_cursor: '2' } },
    })
    renderApp('/deployments?service=demo-service&environment=local')
    await screen.findByRole('table', { name: 'Deployments' })
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await vi.waitFor(() => expect(screen.getAllByRole('row')).toHaveLength(4))
    expect(api.sent().filter((r) => r.startsWith('GET /api/v1/deployments'))).toEqual([
      'GET /api/v1/deployments?service=demo-service&environment=local',
      'GET /api/v1/deployments?service=demo-service&environment=local&cursor=2',
    ])
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })

  it('says how deployments arrive when there are none', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments': { body: { deployments: [], next_cursor: null } } })
    renderApp('/deployments')
    expect(await screen.findByText(/No deployments yet/)).toBeTruthy()
  })

  it('reports a database outage instead of an empty list', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments': apiError(503, 'database_unavailable') })
    renderApp('/deployments')
    expect(await screen.findByRole('alert')).toBeTruthy()
    expect(screen.queryByText(/No deployments yet/)).toBeNull()
  })
})

describe('deployment detail', () => {
  it('shows the verdict with its evidence', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments/2': { body: regression } })
    renderApp('/deployments/2')

    expect(await screen.findByRole('heading', { level: 1, name: 'demo-service main' })).toBeTruthy()

    // The verdict comes first, led by the finding that decided it.
    const verdict = screen.getByRole('region', { name: /Regression/ })
    expect(within(verdict).getByRole('heading', { level: 2 }).textContent).toBe('Regression99.9% confidence')
    expect(within(verdict).getByText('This deployment made demo-service worse.')).toBeTruthy()
    expect(within(verdict).getByText('error rate 0.4% → 19.8% (+19.4 pp, p<0.001)').className).toBe('verdict__headline')
    // The other findings follow, each in its own signal's tone; a caveat is amber.
    const others = within(verdict).getByRole('list', { name: 'Other findings' })
    expect(within(others).getByText('p95 (non-5xx) 48 ms → 51 ms (+6%)').closest('li')?.className).not.toContain(
      'finding--bad',
    )
    expect(within(others).getByText(/^traffic fell 57%/).closest('li')?.className).toContain('finding--warn')

    const table = screen.getByRole('table', { name: 'Before and after' })
    const row = (name: string) => within(table).getByRole('row', { name: new RegExp(`^${name}`) })
    const cells = (name: string) => within(row(name)).getAllByRole('cell').map((c) => c.textContent)
    expect(cells('Requests')).toEqual(['1,800', '1,801', '0%', ''])
    expect(cells('5xx responses')).toEqual(['7', '357', '+350', ''])
    expect(cells('Error rate')).toEqual(['0.4%', '19.8%', '+19.4 pp', 'Regression'])
    expect(cells('p95 latency')).toEqual(['48 ms', '51 ms', '+6%', 'No change'])
    // The change that made the regression is marked as such.
    expect(within(row('Error rate')).getByText('+19.4 pp').className).toContain('change--bad')
  })

  it('makes the failing traces the next step after a regression', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments/2': { body: regression } })
    renderApp('/deployments/2')
    const after = 'from=2026-11-15T10%3A02%3A00.000Z&to=2026-11-15T10%3A05%3A00.000Z'
    const traces = await screen.findByRole('link', { name: 'See failing traces' })
    expect(traces.getAttribute('href')).toBe(`/traces?service=demo-service&status=error&${after}`)
    expect(traces.className).toContain('button-link--primary')
    expect(screen.getByRole('link', { name: 'Error logs' }).getAttribute('href')).toBe(
      `/logs?service=demo-service&severity=error&${after}`,
    )
    expect(screen.getByRole('link', { name: 'Overview' }).getAttribute('href')).toBe(
      '/services?from=2026-11-15T09%3A58%3A00.000Z&to=2026-11-15T10%3A05%3A00.000Z',
    )
  })

  it('names the route that broke, and breaks the evidence down by route', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments/2': { body: regression } })
    renderApp('/deployments/2')
    const verdict = await screen.findByRole('region', { name: /Regression/ })
    expect(within(verdict).getByText(/^Most affected route:/).textContent).toBe(
      'Most affected route: /orders, 5xx 0% → 42.1%',
    )

    const table = screen.getByRole('table', { name: 'By route' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows.map((r) => within(r).getByRole('rowheader').textContent)).toEqual(['/orders', '/products'])
    expect(within(rows[0]).getAllByRole('cell').map((c) => c.textContent)).toEqual([
      '200 → 190',
      '0%',
      '42.1%',
      '+42.1 pp',
    ])
    // Only a notable change is coloured: /orders broke, /products barely moved.
    expect(within(rows[0]).getByText('+42.1 pp').className).toContain('change--bad')
    expect(within(rows[1]).getByText('+0.1 pp').className).not.toContain('change--')
  })

  it('names no route when none stands out', async () => {
    const calm = structuredClone(regression)
    calm.comparison!.result!.routes = [
      { route: '', before: { requests: 5, errors: 0, error_rate: 0 }, after: { requests: 4, errors: 0, error_rate: 0 } },
    ]
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments/2': { body: calm } })
    renderApp('/deployments/2')
    await screen.findByRole('region', { name: /Regression/ })
    expect(screen.queryByText(/^Most affected route:/)).toBeNull()
    // A request no route matched is named in words.
    expect(within(screen.getByRole('table', { name: 'By route' })).getByText('(no route)')).toBeTruthy()
  })

  it('keeps the trace link secondary when nothing regressed', async () => {
    const fine = structuredClone(regression)
    fine.comparison!.result!.verdict = 'no_change'
    fine.comparison!.result!.error_rate_verdict = 'no_change'
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments/2': { body: fine } })
    renderApp('/deployments/2')
    expect((await screen.findByRole('link', { name: 'See failing traces' })).className).not.toContain(
      'button-link--primary',
    )
    expect(screen.getByText('Error rate and p95 latency held steady.')).toBeTruthy()
  })

  it('shows the exact PromQL behind the numbers, before then after', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments/2': { body: regression } })
    renderApp('/deployments/2')
    await screen.findByText('How these numbers were measured')
    const headings = screen.getAllByRole('heading', { level: 4 }).map((h) => h.textContent)
    expect(headings).toEqual(['Requests, before', '5xx responses, after'])
    expect(screen.getByText(regression.comparison!.result!.queries.after_errors.promql)).toBeTruthy()
  })

  it('shows which statuses the precedence rule only recorded', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments/2': { body: regression } })
    renderApp('/deployments/2')
    const history = await screen.findByRole('table', { name: 'Status history' })
    const rows = within(history).getAllByRole('row').slice(1)
    expect(rows.map((r) => within(r).getAllByRole('cell')[2].textContent)).toEqual([
      'applied',
      'applied',
      'recorded only',
    ])
  })

  it('says when the verdict is due while the comparison is pending', async () => {
    fakeApi({
      ...loggedIn,
      'GET /api/v1/deployments/3': {
        body: { ...regression, id: 3, comparison: { ...regression.comparison!, status: 'pending', result: null } },
      },
    })
    renderApp('/deployments/3')
    expect(await screen.findByText(/Comparing. The after window closes at/)).toBeTruthy()
    expect(screen.queryByRole('table', { name: 'Before and after' })).toBeNull()
  })

  it('explains a deployment that was never compared', async () => {
    fakeApi({
      ...loggedIn,
      'GET /api/v1/deployments/4': { body: { ...regression, id: 4, state: 'failure', comparison: null } },
    })
    renderApp('/deployments/4')
    expect(await screen.findByRole('heading', { level: 2, name: 'Not compared' })).toBeTruthy()
    expect(screen.getByText('DeployRadar compares a deployment once it succeeds.')).toBeTruthy()
  })

  it('says when a deployment does not exist', async () => {
    fakeApi({ ...loggedIn, 'GET /api/v1/deployments/99': apiError(404, 'not_found') })
    renderApp('/deployments/99')
    expect(await screen.findByText('There is no deployment 99.')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'All deployments' }).getAttribute('href')).toBe('/deployments')
  })
})
