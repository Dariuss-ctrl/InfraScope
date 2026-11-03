import { describe, expect, it } from 'vitest'
import { readTraceFilters, tracesPath } from './links.ts'

describe('readTraceFilters', () => {
  it('reads every filter the API accepts', () => {
    const params = new URLSearchParams(
      'service=demo-service&operation=GET+%2Forders&status=error&sort=duration' +
        '&min_duration_ms=100&max_duration_ms=5000',
    )
    expect(readTraceFilters(params)).toEqual({
      service: 'demo-service',
      operation: 'GET /orders',
      status: 'error',
      sort: 'duration',
      min_duration_ms: 100,
      max_duration_ms: 5000,
    })
  })

  it('drops values the API would reject, so a mistyped link still works', () => {
    const params = new URLSearchParams(
      'service=+&status=maybe&sort=asc&min_duration_ms=-5&max_duration_ms=1.5',
    )
    expect(readTraceFilters(params)).toEqual({
      service: undefined,
      operation: undefined,
      status: undefined,
      sort: undefined,
      min_duration_ms: undefined,
      max_duration_ms: undefined,
    })
  })

  it('round-trips through tracesPath', () => {
    const filters = { service: 'api', status: 'ok' as const, min_duration_ms: 0 }
    const path = tracesPath(filters, { preset: '6h' })
    expect(path).toBe('/traces?service=api&status=ok&min_duration_ms=0&range=6h')
    expect(readTraceFilters(new URL(path, 'http://dashboard.test').searchParams)).toMatchObject(filters)
  })
})
