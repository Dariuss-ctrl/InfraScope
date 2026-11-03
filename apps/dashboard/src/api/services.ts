// Service metrics (apps/api/openapi.yaml, tag "services"). Values are null
// where Prometheus has no data, which the views show as "no data", never 0.
import { apiGet } from './client.ts'

export type Point = { t: string; v: number | null }

export type ServiceSummary = {
  name: string
  request_rate: number | null
  error_rate: number | null
  latency_seconds: { p50: number | null; p95: number | null; p99: number | null }
}

export type ServiceMetrics = {
  service: string
  step_seconds: number
  series: {
    request_rate: Point[]
    error_rate: Point[]
    latency_p50_seconds: Point[]
    latency_p95_seconds: Point[]
    latency_p99_seconds: Point[]
  }
}

export async function listServices(
  from: Date,
  to: Date,
  signal?: AbortSignal,
): Promise<ServiceSummary[]> {
  const page = await apiGet<{ services: ServiceSummary[] }>('/api/v1/services', {
    params: { from: from.toISOString(), to: to.toISOString() },
    signal,
  })
  return page.services
}

export function serviceMetrics(
  service: string,
  from: Date,
  to: Date,
  signal?: AbortSignal,
): Promise<ServiceMetrics> {
  return apiGet<ServiceMetrics>(`/api/v1/services/${encodeURIComponent(service)}/metrics`, {
    params: { from: from.toISOString(), to: to.toISOString() },
    signal,
  })
}
