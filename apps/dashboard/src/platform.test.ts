import { describe, expect, it } from 'vitest'
import type { DependencyStatus } from './api/status.ts'
import { describeDependency, outages } from './platform.ts'

function report(...down: string[]) {
  const names = ['postgres', 'clickhouse', 'clickhouse_schema', 'prometheus', 'otel-collector']
  const dependencies: DependencyStatus[] = names.map((name) => ({
    name,
    status: down.includes(name) ? 'down' : 'up',
    required: name === 'postgres',
    latency_ms: 1,
  }))
  return { status: 'degraded' as const, checked_at: '2026-10-30T12:00:00Z', dependencies }
}

describe('outages', () => {
  it('lists what is down', () => {
    expect(outages(report('prometheus', 'otel-collector')).map((d) => d.name)).toEqual([
      'prometheus',
      'otel-collector',
    ])
  })

  it('does not blame the schema while ClickHouse itself is unreachable', () => {
    expect(outages(report('clickhouse', 'clickhouse_schema')).map((d) => d.name)).toEqual(['clickhouse'])
  })

  it('reports the schema when ClickHouse answers but is missing something', () => {
    expect(outages(report('clickhouse_schema')).map((d) => d.name)).toEqual(['clickhouse_schema'])
  })
})

describe('describeDependency', () => {
  it('says what still works while a store is down (NFR-1)', () => {
    expect(describeDependency('clickhouse').effect).toContain('Service metrics still work')
    expect(describeDependency('prometheus').effect).toContain('Trace and log search still work')
  })

  it('still says something useful about a dependency it does not know', () => {
    expect(describeDependency('redis')).toEqual({
      label: 'redis',
      problem: 'redis is down.',
      effect: 'Features that depend on it are unavailable.',
    })
  })
})
