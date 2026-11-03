import { useSearchParams } from 'react-router'
import type { Point } from './api/services.ts'

// The time range every investigation view shares, kept in the URL: a preset
// relative to now (?range=6h), or fixed bounds (?from=…&to=…). A click on a
// chart produces fixed bounds, so a shared link shows the same requests.
export const presets = [
  { id: '15m', ms: 15 * 60_000 },
  { id: '1h', ms: 60 * 60_000 },
  { id: '6h', ms: 6 * 60 * 60_000 },
  { id: '24h', ms: 24 * 60 * 60_000 },
] as const

export type Preset = (typeof presets)[number]['id']
export type TimeRange = { preset: Preset } | { from: Date; to: Date }

export const defaultPreset: Preset = '1h'

export function readTimeRange(params: URLSearchParams): TimeRange {
  const from = parseTime(params.get('from'))
  const to = parseTime(params.get('to'))
  if (from && to && from < to) {
    return { from, to }
  }
  const preset = presets.find((p) => p.id === params.get('range'))
  return { preset: preset?.id ?? defaultPreset }
}

// writeTimeRange replaces the range in params and keeps every other filter.
export function writeTimeRange(params: URLSearchParams, range: TimeRange): URLSearchParams {
  const next = new URLSearchParams(params)
  next.delete('range')
  next.delete('from')
  next.delete('to')
  if ('preset' in range) {
    if (range.preset !== defaultPreset) {
      next.set('range', range.preset)
    }
  } else {
    next.set('from', range.from.toISOString())
    next.set('to', range.to.toISOString())
  }
  return next
}

export function resolveRange(range: TimeRange, now = new Date()): { from: Date; to: Date } {
  if ('from' in range) {
    return range
  }
  const length = presets.find((p) => p.id === range.preset)?.ms ?? 60 * 60_000
  return { from: new Date(now.getTime() - length), to: now }
}

// rangeKey identifies a range in query keys. Presets go by name, because
// their bounds move with the clock.
export function rangeKey(range: TimeRange): string {
  return 'preset' in range ? range.preset : `${range.from.toISOString()}/${range.to.toISOString()}`
}

export function useTimeRange(): [TimeRange, (next: TimeRange) => void] {
  const [params, setParams] = useSearchParams()
  return [readTimeRange(params), (next) => setParams((current) => writeTimeRange(current, next))]
}

// pointWindow is the span of time behind one chart point. A Prometheus rate
// at t covers the window ending at t, which the API sizes as two steps and
// at least a minute, so those are the requests to look at.
export function pointWindow(t: Date, stepSeconds: number): { from: Date; to: Date } {
  const window = Math.max(2 * stepSeconds, 60) * 1000
  return { from: new Date(t.getTime() - window), to: t }
}

export function nearestPoint(points: readonly Point[], t: Date): Point | undefined {
  let nearest: Point | undefined
  let distance = Infinity
  for (const point of points) {
    const d = Math.abs(Date.parse(point.t) - t.getTime())
    if (d < distance) {
      nearest = point
      distance = d
    }
  }
  return nearest
}

function parseTime(raw: string | null): Date | null {
  if (!raw) {
    return null
  }
  const t = new Date(raw)
  return Number.isNaN(t.getTime()) ? null : t
}
