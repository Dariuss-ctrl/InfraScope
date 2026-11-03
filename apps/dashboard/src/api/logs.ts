// Log search (apps/api/openapi.yaml, tag "logs").
import type { LogFilters, Severity } from '../links.ts'
import { apiGet } from './client.ts'

export type LogRecord = {
  timestamp: string
  service: string
  severity_number: number
  severity_text: string
  body: string // untrusted: rendered as text only
  trace_id: string | null
  span_id: string | null
  attributes?: Record<string, string>
}

export type LogSearchResult = { logs: LogRecord[]; next_cursor: string | null }

export function searchLogs(
  filters: LogFilters,
  from: Date,
  to: Date,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<LogSearchResult> {
  return apiGet<LogSearchResult>('/api/v1/logs', {
    params: { ...filters, from: from.toISOString(), to: to.toISOString(), cursor },
    signal,
  })
}

// severityLevel names the OpenTelemetry SeverityNumber range a record falls
// in (AC-4.1): 1–4 trace, 5–8 debug, 9–12 info, 13–16 warn, 17–20 error,
// 21–24 fatal. Zero means the producer set none.
export function severityLevel(severityNumber: number): Severity | null {
  const levels: Severity[] = ['trace', 'debug', 'info', 'warn', 'error', 'fatal']
  return levels[Math.floor((severityNumber - 1) / 4)] ?? null
}
