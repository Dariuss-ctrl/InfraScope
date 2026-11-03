import { describe, expect, it } from 'vitest'
import type { Span, Trace } from './api/traces.ts'
import { firstFailure, hotspot, layoutWaterfall, nanosBetween, serviceColors } from './waterfall.ts'

// span builds a span starting `at` microseconds after noon and lasting `us`.
function span(id: string, parent: string | null, at: number, us: number, extra: Partial<Span> = {}): Span {
  const ns = BigInt(at) * 1000n
  const seconds = ns / 1_000_000_000n
  const fraction = (ns % 1_000_000_000n).toString().padStart(9, '0')
  return {
    span_id: id,
    parent_span_id: parent,
    service: 'demo-service',
    name: id,
    kind: 'internal',
    start_time: `2026-10-30T12:00:${seconds.toString().padStart(2, '0')}.${fraction}Z`,
    duration_ns: us * 1000,
    status: 'unset',
    ...extra,
  }
}

function trace(...spans: Span[]): Trace {
  return {
    trace_id: '0af7651916cd43dd8448eb211c80319c',
    start_time: '2026-10-30T12:00:00Z',
    end_time: '2026-10-30T12:00:01Z',
    spans,
    truncated: false,
  }
}

const shape = (t: Trace) => layoutWaterfall(t).map((r) => `${'  '.repeat(r.depth)}${r.span.span_id}`)

describe('layoutWaterfall', () => {
  it('nests spans under their parents and orders siblings by start (AC-3.1)', () => {
    const t = trace(
      span('root', null, 0, 1000),
      span('query-2', 'root', 600, 100),
      span('handler', 'root', 100, 800),
      span('query-1', 'handler', 200, 300),
    )
    expect(shape(t)).toEqual(['root', '  handler', '    query-1', '  query-2'])
  })

  it('keeps a span whose parent is missing, at the top level and marked', () => {
    const rows = layoutWaterfall(trace(span('root', null, 0, 1000), span('orphan', 'not-ingested', 50, 10)))
    expect(rows.map((r) => [r.span.span_id, r.depth, r.missingParent])).toEqual([
      ['root', 0, false],
      ['orphan', 0, true],
    ])
  })

  it('shows every span even when the parent links loop', () => {
    const t = trace(span('root', null, 0, 1000), span('a', 'b', 10, 5), span('b', 'a', 20, 5), span('self', 'self', 30, 5))
    const rows = layoutWaterfall(t)
    expect(rows).toHaveLength(4)
    expect(new Set(rows.map((r) => r.span.span_id))).toEqual(new Set(['root', 'a', 'b', 'self']))
  })

  it('measures offsets from the trace start to the nanosecond', () => {
    const rows = layoutWaterfall(trace(span('root', null, 0, 10), span('child', 'root', 7, 1)))
    expect(rows[1].offsetNs).toBe(7000)
  })

  it('handles a chain far deeper than the call stack would allow', () => {
    const chain = [span('s0', null, 0, 10_000)]
    for (let i = 1; i < 10_000; i++) {
      chain.push(span(`s${i}`, `s${i - 1}`, i, 1))
    }
    const rows = layoutWaterfall(trace(...chain))
    expect(rows).toHaveLength(10_000)
    expect(rows[9_999].depth).toBe(9_999)
  })
})

describe('self time', () => {
  it('is the time no child covers, counting parallel children once', () => {
    // A 1000 µs request runs two overlapping queries (100–400 and 300–600)
    // and one that ends after it does (900–1200).
    const t = trace(
      span('root', null, 0, 1000),
      span('q1', 'root', 100, 300),
      span('q2', 'root', 300, 300),
      span('q3', 'root', 900, 300),
    )
    const root = layoutWaterfall(t)[0]
    expect(root.selfNs).toBe((1000 - 500 - 100) * 1000)
  })

  it('finds the span that spent the most time itself', () => {
    const t = trace(
      span('GET /orders', null, 0, 1000),
      span('SELECT orders', 'GET /orders', 50, 900),
      span('SELECT products', 'GET /orders', 960, 20),
    )
    expect(hotspot(layoutWaterfall(t))?.span.span_id).toBe('SELECT orders')
  })
})

describe('serviceColors', () => {
  it('colours services in order of appearance, the request’s own service first', () => {
    const t = trace(
      span('root', null, 0, 1000, { service: 'checkout' }),
      span('a', 'root', 10, 5, { service: 'inventory' }),
      span('b', 'root', 20, 5, { service: 'checkout' }),
    )
    expect([...serviceColors(layoutWaterfall(t))]).toEqual([
      ['checkout', 'var(--service-1)'],
      ['inventory', 'var(--service-2)'],
    ])
  })

  it('reuses colours after eight services', () => {
    const spans = Array.from({ length: 9 }, (_, i) => span(`s${i}`, null, i, 1, { service: `svc-${i}` }))
    expect(serviceColors(layoutWaterfall(trace(...spans))).get('svc-8')).toBe('var(--service-1)')
  })
})

describe('firstFailure', () => {
  it('picks the deepest failed span, where the error started', () => {
    const t = trace(
      span('POST /orders', null, 0, 1000, { status: 'error' }),
      span('insert', 'POST /orders', 10, 100, { status: 'error' }),
      span('lookup', 'POST /orders', 200, 100),
    )
    expect(firstFailure(layoutWaterfall(t))?.span.span_id).toBe('insert')
  })

  it('finds nothing in a healthy trace', () => {
    expect(firstFailure(layoutWaterfall(trace(span('root', null, 0, 10))))).toBeUndefined()
  })
})

describe('nanosBetween', () => {
  it('keeps the digits below a millisecond that Date drops', () => {
    expect(nanosBetween('2026-10-30T12:00:00.000000100Z', '2026-10-30T12:00:00.001000350Z')).toBe(1_000_250)
  })

  it('accepts whole seconds and offsets', () => {
    expect(nanosBetween('2026-10-30T12:00:00Z', '2026-10-30T15:00:01.5+03:00')).toBe(1_500_000_000)
  })
})
