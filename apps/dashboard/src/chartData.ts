import type { Point } from './api/services.ts'

export type LinePoint = { value: [number, number | null]; symbol: 'circle' | 'none' }

// toLineData prepares a series for a line chart. Lines are drawn without
// dots, so a value with gaps on both sides, such as a quiet service's only
// busy minute, would be invisible. Those values get a dot.
export function toLineData(points: readonly Point[]): LinePoint[] {
  return points.map((point, i) => ({
    value: [Date.parse(point.t), point.v],
    symbol: point.v !== null && isGap(points[i - 1]) && isGap(points[i + 1]) ? 'circle' : 'none',
  }))
}

// A neighbour is a gap when it is missing or has no value. Zero is a value.
function isGap(point: Point | undefined): boolean {
  return point === undefined || point.v === null
}

export function hasValues(points: readonly Point[]): boolean {
  return points.some((point) => point.v !== null)
}
