// DeployRadar deployments and their before/after health comparison
// (apps/api/openapi.yaml, tag "deployments").
import { apiGet } from './client.ts'

export type DeploymentState =
  | 'pending'
  | 'queued'
  | 'in_progress'
  | 'success'
  | 'failure'
  | 'error'
  | 'inactive'

export type Verdict = 'regression' | 'improvement' | 'no_change' | 'insufficient_data'
export type ComparisonStatus = 'pending' | 'done' | 'failed'

export type Deployment = {
  id: number
  provider: 'github'
  repository: string
  provider_deployment_id: string
  service: string // the service_name label the comparison measures
  environment: string // the deployment_environment_name label
  ref: string
  sha: string
  creator: string
  description: string
  created_at: string
  state: DeploymentState
  state_at: string // when the state happened at the provider
  finished_at: string | null // the first terminal state; null while running
  updated_at: string
}

export type DeploymentSummary = Deployment & {
  comparison_status: ComparisonStatus | null // null: never succeeded, so never compared
  verdict: Verdict | null // set once the comparison is done
  confidence: number | null
}

export type DeploymentList = { deployments: DeploymentSummary[]; next_cursor: string | null }

export type TimeWindow = { from: string; to: string }

export type WindowMeasurement = {
  requests: number // increase() extrapolates, so it may be fractional
  errors: number
  error_rate: number
  p95_seconds: number | null
}

export type PromQuery = { promql: string; time: string; range: string }

export type RouteCounts = { requests: number; errors: number; error_rate: number }

// RouteChange is one http_route's evidence; routes come worst change first.
export type RouteChange = { route: string; before: RouteCounts; after: RouteCounts }

export type ComparisonResult = {
  verdict: Verdict
  error_rate_verdict: Verdict
  p95_verdict: Verdict
  confidence: number
  reasons: string[]
  before: WindowMeasurement
  after: WindowMeasurement
  queries: Record<string, PromQuery>
  routes: RouteChange[]
  method_version: number
  computed_at: string
}

export type Comparison = {
  status: ComparisonStatus
  attempts: number
  before: TimeWindow
  after: TimeWindow
  error: string | null
  result: ComparisonResult | null
}

export type DeploymentChange = {
  delivery_id: string
  state: DeploymentState
  state_at: string
  applied: boolean // false: a later or finished state stood, so it was only recorded
  recorded_at: string
}

export type DeploymentDetail = Deployment & {
  changes: DeploymentChange[]
  comparison: Comparison | null
}

export type DeploymentFilters = { service?: string; environment?: string }

export function listDeployments(
  filters: DeploymentFilters,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<DeploymentList> {
  return apiGet<DeploymentList>('/api/v1/deployments', { params: { ...filters, cursor }, signal })
}

// deploymentsFinishedIn lists a service's deployments that finished between
// from and to, newest first: the markers on its charts. The API filters on
// when a deployment was created, so the query starts an hour earlier and the
// finish time decides.
export async function deploymentsFinishedIn(
  service: string,
  from: Date,
  to: Date,
  signal?: AbortSignal,
): Promise<DeploymentSummary[]> {
  const page = await apiGet<DeploymentList>('/api/v1/deployments', {
    params: {
      service,
      from: new Date(from.getTime() - 60 * 60 * 1000).toISOString(),
      to: to.toISOString(),
      limit: 50,
    },
    signal,
  })
  return page.deployments.filter((d) => {
    const finished = d.finished_at === null ? Number.NaN : Date.parse(d.finished_at)
    return finished >= from.getTime() && finished <= to.getTime()
  })
}

export function getDeployment(id: string, signal?: AbortSignal): Promise<DeploymentDetail> {
  return apiGet<DeploymentDetail>(`/api/v1/deployments/${encodeURIComponent(id)}`, { signal })
}

// The labels a person reads; the API's names stay in the URL and tooltips.
export const verdictLabels: Record<Verdict, string> = {
  regression: 'Regression',
  improvement: 'Improvement',
  no_change: 'No change',
  insufficient_data: 'Not enough traffic',
}

// The order the evidence is read in: each measure before, then after.
export const queryLabels: [key: string, label: string][] = [
  ['before_requests', 'Requests, before'],
  ['after_requests', 'Requests, after'],
  ['before_errors', '5xx responses, before'],
  ['after_errors', '5xx responses, after'],
  ['before_p95', 'p95 latency of non-5xx responses, before'],
  ['after_p95', 'p95 latency of non-5xx responses, after'],
  ['before_requests_by_route', 'Requests by route, before'],
  ['after_requests_by_route', 'Requests by route, after'],
  ['before_errors_by_route', '5xx responses by route, before'],
  ['after_errors_by_route', '5xx responses by route, after'],
]

// The smallest rise in a route's error rate worth pointing at: one
// percentage point, the same floor the verdict uses for the whole service.
const notableRise = 0.01

// worstRoute is the route whose error rate rose most, when it rose enough to
// name; null when no route stands out.
export function worstRoute(result: ComparisonResult): RouteChange | null {
  const first = result.routes[0]
  return first && first.after.error_rate - first.before.error_rate >= notableRise ? first : null
}

// routeName shows an empty http_route (a request no route matched) in words.
export function routeName(route: string): string {
  return route === '' ? '(no route)' : route
}

// routeTone colours a route's error-rate change when it moved by at least
// the notable amount either way.
export function routeTone(r: RouteChange): '' | 'change--bad' | 'change--ok' {
  const rise = r.after.error_rate - r.before.error_rate
  if (rise >= notableRise) {
    return 'change--bad'
  }
  return rise <= -notableRise ? 'change--ok' : ''
}

export function shortSha(sha: string): string {
  return sha.slice(0, 7)
}

// formatConfidence rounds down and stops at 99.9%: a statistical test never
// makes a verdict certain, even when its p-value is too small to represent.
export function formatConfidence(confidence: number): string {
  return `${Math.min(Math.floor(confidence * 1000) / 10, 99.9).toFixed(1)}%`
}

// splitReasons picks the finding that decided the verdict, to lead with,
// and keeps the others (the other signal, and any caveats) in their order.
export function splitReasons(result: ComparisonResult): { headline: string | null; rest: string[] } {
  const decisive = result.reasons.find((reason) => reasonVerdict(reason, result) === result.verdict)
  const headline = decisive ?? result.reasons[0] ?? null
  return { headline, rest: result.reasons.filter((reason) => reason !== headline) }
}

// Changes between the windows, signed, in the units each measure is read in.
export function formatPercentChange(before: number | null, after: number | null): string {
  if (before === null || after === null || before === 0) {
    return '—'
  }
  const pct = Math.round((after / before - 1) * 100)
  return pct === 0 ? '0%' : `${pct > 0 ? '+' : '-'}${Math.abs(pct)}%`
}

export function formatPointChange(before: number, after: number): string {
  const pp = Math.round((after - before) * 1000) / 10
  return pp === 0 ? '0 pp' : `${pp > 0 ? '+' : '-'}${Math.abs(pp).toFixed(1)} pp`
}

export function formatCountChange(before: number, after: number): string {
  const diff = Math.round(after) - Math.round(before)
  return diff === 0 ? '0' : `${diff > 0 ? '+' : '-'}${Math.abs(diff).toLocaleString('en-US')}`
}

// reasonVerdict is the verdict of the signal a finding describes, or null
// for caveats about the comparison itself: too little traffic, no histogram,
// a change in load, another deployment inside a window.
export function reasonVerdict(reason: string, result: ComparisonResult): Verdict | null {
  if (reason.startsWith('error rate ')) {
    return result.error_rate_verdict
  }
  if (reason.startsWith('p95 ')) {
    return result.p95_verdict
  }
  return null
}
