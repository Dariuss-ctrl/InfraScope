import { describe, expect, it } from 'vitest'
import {
  nearestPoint,
  pointWindow,
  rangeKey,
  readTimeRange,
  resolveRange,
  writeTimeRange,
} from './timeRange.ts'

const params = (query: string) => new URLSearchParams(query)
const at = (iso: string) => new Date(iso)

describe('readTimeRange', () => {
  it('defaults to the last hour', () => {
    expect(readTimeRange(params(''))).toEqual({ preset: '1h' })
  })

  it('reads a preset', () => {
    expect(readTimeRange(params('range=6h'))).toEqual({ preset: '6h' })
  })

  it('prefers fixed bounds, which is what a chart click produces', () => {
    expect(
      readTimeRange(params('range=6h&from=2026-10-30T12:00:00Z&to=2026-10-30T13:00:00Z')),
    ).toEqual({ from: at('2026-10-30T12:00:00Z'), to: at('2026-10-30T13:00:00Z') })
  })

  it.each([
    'range=7d',
    'from=yesterday&to=today',
    'from=2026-10-30T13:00:00Z&to=2026-10-30T12:00:00Z',
    'from=2026-10-30T12:00:00Z',
  ])('falls back to the default for %s', (query) => {
    expect(readTimeRange(params(query))).toEqual({ preset: '1h' })
  })
})

describe('writeTimeRange', () => {
  it('replaces the range and keeps every other filter', () => {
    const next = writeTimeRange(params('service=api&range=6h'), {
      from: at('2026-10-30T12:00:00Z'),
      to: at('2026-10-30T12:05:00Z'),
    })
    expect(next.toString()).toBe(
      'service=api&from=2026-10-30T12%3A00%3A00.000Z&to=2026-10-30T12%3A05%3A00.000Z',
    )
  })

  it('leaves the default out of the URL', () => {
    expect(writeTimeRange(params('range=6h&service=api'), { preset: '1h' }).toString()).toBe(
      'service=api',
    )
  })

  it('round-trips through the URL', () => {
    const range = { from: at('2026-10-30T12:00:00.250Z'), to: at('2026-10-30T12:00:01Z') }
    expect(readTimeRange(writeTimeRange(params(''), range))).toEqual(range)
  })
})

describe('resolveRange', () => {
  it('measures a preset back from now', () => {
    const now = at('2026-10-30T12:00:00Z')
    expect(resolveRange({ preset: '15m' }, now)).toEqual({ from: at('2026-10-30T11:45:00Z'), to: now })
  })

  it('keeps fixed bounds as they are', () => {
    const range = { from: at('2026-10-30T10:00:00Z'), to: at('2026-10-30T11:00:00Z') }
    expect(resolveRange(range)).toEqual(range)
  })
})

describe('rangeKey', () => {
  it('names a preset, because its bounds move with the clock', () => {
    expect(rangeKey({ preset: '6h' })).toBe('6h')
  })
})

describe('pointWindow', () => {
  it('covers the two steps a rate point was computed over', () => {
    expect(pointWindow(at('2026-10-30T12:10:00Z'), 120)).toEqual({
      from: at('2026-10-30T12:06:00Z'),
      to: at('2026-10-30T12:10:00Z'),
    })
  })

  it('never covers less than a minute', () => {
    expect(pointWindow(at('2026-10-30T12:10:00Z'), 15).from).toEqual(at('2026-10-30T12:09:00Z'))
  })
})

describe('nearestPoint', () => {
  const points = [
    { t: '2026-10-30T12:00:00Z', v: 1 },
    { t: '2026-10-30T12:01:00Z', v: null },
    { t: '2026-10-30T12:02:00Z', v: 3 },
  ]

  it('picks the closest point, with or without a value', () => {
    expect(nearestPoint(points, at('2026-10-30T12:01:20Z'))).toBe(points[1])
  })

  it('has nothing to pick in an empty series', () => {
    expect(nearestPoint([], at('2026-10-30T12:00:00Z'))).toBeUndefined()
  })
})
