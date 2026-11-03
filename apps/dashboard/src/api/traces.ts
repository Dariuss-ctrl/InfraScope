// Trace search and detail (apps/api/openapi.yaml, tag "traces").
import type { TraceFilters } from '../links.ts'
import { apiGet } from './client.ts'

export type TraceSummary = {
  trace_id: string
  root_service: string
  root_operation: string
  start_time: string
  duration_ns: number
  span_count: number
  has_error: boolean
}

export type TraceSearchResult = { traces: TraceSummary[]; next_cursor: string | null }

export type SpanEvent = { time: string; name: string; attributes?: Record<string, string> }

// Times are RFC 3339 with nanoseconds, more precision than Date keeps; see
// nanosBetween in waterfall.ts.
export type Span = {
  span_id: string
  parent_span_id: string | null
  service: string
  name: string
  kind: 'server' | 'client' | 'internal' | 'producer' | 'consumer' | 'unspecified'
  start_time: string
  duration_ns: number
  status: 'unset' | 'ok' | 'error'
  status_message?: string
  attributes?: Record<string, string>
  resource_attributes?: Record<string, string>
  events?: SpanEvent[]
}

export type Trace = {
  trace_id: string
  start_time: string
  end_time: string
  spans: Span[] // earliest first
  truncated: boolean // more spans exist than one response carries
}

export function getTrace(traceId: string, signal?: AbortSignal): Promise<Trace> {
  return apiGet<Trace>(`/api/v1/traces/${encodeURIComponent(traceId)}`, { signal })
}

export function searchTraces(
  filters: TraceFilters,
  from: Date,
  to: Date,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<TraceSearchResult> {
  return apiGet<TraceSearchResult>('/api/v1/traces', {
    params: { ...filters, from: from.toISOString(), to: to.toISOString(), cursor },
    signal,
  })
}
