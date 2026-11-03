import { useQuery } from '@tanstack/react-query'
import { type DependencyStatus, type HealthReport, fetchStatus } from './api/status.ts'

// useStatus is the platform status every view shares: the badge, the outage
// banner and the status page read one query, refreshed every 15 s.
export function useStatus() {
  return useQuery({
    queryKey: ['status'],
    queryFn: ({ signal }) => fetchStatus(signal),
    refetchInterval: 15_000,
  })
}

export type Dependency = {
  label: string // what it is
  problem: string // the sentence for when it is down
  effect: string // what stops working, and what still works (NFR-1)
}

const dependencies: Record<string, Dependency> = {
  postgres: {
    label: 'Configuration database (PostgreSQL)',
    problem: 'The configuration database (PostgreSQL) is down.',
    effect: 'Logging in and checking sessions fail.',
  },
  clickhouse: {
    label: 'Trace and log storage (ClickHouse)',
    problem: 'Trace and log storage (ClickHouse) is down.',
    effect: 'Trace and log search are unavailable. Service metrics still work.',
  },
  clickhouse_schema: {
    label: 'Trace and log storage schema',
    problem: 'Trace and log storage is missing tables or indexes.',
    effect: 'Searches that rely on them are slow or fail.',
  },
  prometheus: {
    label: 'Metrics store (Prometheus)',
    problem: 'The metrics store (Prometheus) is down.',
    effect: 'The service overview is unavailable. Trace and log search still work.',
  },
  'otel-collector': {
    label: 'OpenTelemetry Collector',
    problem: 'The OpenTelemetry Collector is down.',
    effect: 'New telemetry is not being received. Stored data can still be searched.',
  },
}

export function describeDependency(name: string): Dependency {
  return (
    dependencies[name] ?? {
      label: name,
      problem: `${name} is down.`,
      effect: 'Features that depend on it are unavailable.',
    }
  )
}

// outages lists what is down. ClickHouse being unreachable also fails its
// schema check; reporting both would send people looking for missing
// indexes in a database that is not answering.
export function outages(report: HealthReport): DependencyStatus[] {
  const down = report.dependencies.filter((d) => d.status === 'down')
  return down.some((d) => d.name === 'clickhouse') ? down.filter((d) => d.name !== 'clickhouse_schema') : down
}
