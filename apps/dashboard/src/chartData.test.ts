import { describe, expect, it } from 'vitest'
import type { Point } from './api/services.ts'
import { hasValues, toLineData } from './chartData.ts'

function series(...values: (number | null)[]): Point[] {
  return values.map((v, i) => ({ t: new Date(Date.UTC(2026, 9, 30, 12, i)).toISOString(), v }))
}

const dots = (points: Point[]) => toLineData(points).map((p) => p.symbol)

describe('toLineData', () => {
  it('pairs each value with its time', () => {
    expect(toLineData(series(0.5))[0].value).toEqual([Date.UTC(2026, 9, 30, 12, 0), 0.5])
  })

  it('marks a value that has gaps on both sides, which a line alone would not show', () => {
    expect(dots(series(null, 0.004, null, 0.002))).toEqual(['none', 'circle', 'none', 'circle'])
  })

  it('draws connected values as a plain line', () => {
    expect(dots(series(1, 2, null, 3, 4))).toEqual(['none', 'none', 'none', 'none', 'none'])
  })

  it('treats zero as a value, not a gap', () => {
    expect(dots(series(0, 5, 0))).toEqual(['none', 'none', 'none'])
  })
})

describe('hasValues', () => {
  it('is false only when every point is empty', () => {
    expect(hasValues(series(null, null))).toBe(false)
    expect(hasValues(series(null, 0))).toBe(true)
    expect(hasValues([])).toBe(false)
  })
})
