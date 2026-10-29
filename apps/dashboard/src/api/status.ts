// Client for GET /api/v1/status (apps/api/openapi.yaml, HealthReport).

export type DependencyStatus = {
  name: string
  status: 'up' | 'down'
  required: boolean
  latency_ms: number
}

export type HealthReport = {
  status: 'ok' | 'degraded' | 'unavailable'
  checked_at: string
  dependencies: DependencyStatus[]
}

export async function fetchStatus(signal?: AbortSignal): Promise<HealthReport> {
  const res = await fetch('/api/v1/status', {
    signal,
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`status endpoint answered ${res.status}`)
  }
  return (await res.json()) as HealthReport
}

// One line a person can act on, naming whatever is down.
export function describeStatus(report: HealthReport): string {
  if (report.status === 'ok') {
    return 'All systems up'
  }
  const down = report.dependencies
    .filter((d) => d.status === 'down')
    .map((d) => d.name)
    .join(', ')
  const prefix = report.status === 'degraded' ? 'Degraded' : 'Unavailable'
  return `${prefix}: ${down} down`
}
