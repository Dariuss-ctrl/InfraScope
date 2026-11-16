import { type TimeRange, writeTimeRange } from './timeRange.ts'

// Filters of the trace explorer, named as the API names them, so the URL a
// person shares is also the query the page sends.
export type TraceFilters = {
  service?: string
  operation?: string
  status?: 'ok' | 'error'
  sort?: 'duration'
  min_duration_ms?: number
  max_duration_ms?: number
}

// Filters of the log explorer, also named as the API names them.
export type LogFilters = {
  service?: string
  severity?: Severity
  q?: string
  trace_id?: string
  span_id?: string
}

export const severities = ['trace', 'debug', 'info', 'warn', 'error', 'fatal'] as const
export type Severity = (typeof severities)[number]

export function tracesPath(filters: TraceFilters, range: TimeRange): string {
  return withQuery('/traces', filters, range)
}

export function logsPath(filters: LogFilters, range: TimeRange): string {
  return withQuery('/logs', filters, range)
}

// Filters of the deployments list, named as the API names them.
export type DeploymentListFilters = { service?: string; environment?: string }

export function deploymentsPath(filters: DeploymentListFilters): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(filters)) {
    if (value) {
      params.set(key, value)
    }
  }
  const query = params.toString()
  return query ? `/deployments?${query}` : '/deployments'
}

export function deploymentPath(id: number | string): string {
  return `/deployments/${id}`
}

export function readDeploymentFilters(params: URLSearchParams): DeploymentListFilters {
  return {
    service: params.get('service')?.trim() || undefined,
    environment: params.get('environment')?.trim() || undefined,
  }
}

// servicesPath opens the overview over a time range.
export function servicesPath(range: TimeRange): string {
  return withQuery('/services', {}, range)
}

// tracePath opens a trace, optionally with one of its spans selected.
export function tracePath(traceId: string, spanId?: string | null): string {
  return spanId ? `/traces/${traceId}?span=${spanId}` : `/traces/${traceId}`
}

// readLogFilters is the inverse of logsPath. A malformed trace or span ID is
// kept, so the API can say what is wrong with it: dropping it would quietly
// show every log instead.
export function readLogFilters(params: URLSearchParams): LogFilters {
  const severity = params.get('severity')
  return {
    service: params.get('service')?.trim() || undefined,
    severity: severities.find((s) => s === severity),
    q: params.get('q')?.trim() || undefined,
    trace_id: params.get('trace_id')?.trim().toLowerCase() || undefined,
    span_id: params.get('span_id')?.trim().toLowerCase() || undefined,
  }
}

function withQuery(path: string, filters: object, range: TimeRange): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(filters)) {
    if (value !== undefined && value !== '') {
      params.set(key, String(value))
    }
  }
  const query = writeTimeRange(params, range).toString()
  return query ? `${path}?${query}` : path
}

// readTraceFilters is the inverse of tracesPath. Values the API would reject
// are dropped, so a mistyped link still opens a usable page.
export function readTraceFilters(params: URLSearchParams): TraceFilters {
  const status = params.get('status')
  return {
    service: params.get('service')?.trim() || undefined,
    operation: params.get('operation')?.trim() || undefined,
    status: status === 'ok' || status === 'error' ? status : undefined,
    sort: params.get('sort') === 'duration' ? 'duration' : undefined,
    min_duration_ms: wholeNumber(params.get('min_duration_ms')),
    max_duration_ms: wholeNumber(params.get('max_duration_ms')),
  }
}

function wholeNumber(raw: string | null): number | undefined {
  if (raw === null || raw.trim() === '') {
    return undefined
  }
  const n = Number(raw)
  return Number.isSafeInteger(n) && n >= 0 ? n : undefined
}
