import { describe, expect, it } from 'vitest'
import {
  type ComparisonResult,
  formatConfidence,
  formatCountChange,
  formatPercentChange,
  formatPointChange,
  reasonVerdict,
  splitReasons,
} from './deployments.ts'

describe('changes between the windows', () => {
  it('signs each change in the unit its measure is read in', () => {
    expect(formatPercentChange(3606.6, 1553.9)).toBe('-57%')
    expect(formatPercentChange(0.048, 0.051)).toBe('+6%')
    expect(formatPercentChange(1799.6, 1801.2)).toBe('0%')
    expect(formatPointChange(0.004, 0.198)).toBe('+19.4 pp')
    expect(formatPointChange(0.199, 0)).toBe('-19.9 pp')
    expect(formatPointChange(0, 0)).toBe('0 pp')
    expect(formatCountChange(7.2, 356.6)).toBe('+350')
    expect(formatCountChange(716.4, 0)).toBe('-716')
    expect(formatCountChange(1000, 4500)).toBe('+3,500')
  })

  it('has no percentage change from nothing', () => {
    expect(formatPercentChange(0, 0.03)).toBe('—')
    expect(formatPercentChange(null, 0.03)).toBe('—')
  })
})

describe('splitReasons', () => {
  const reasons = ['error rate 0.0% → 19.6% (+19.6 pp, p<0.001)', 'p95 (non-5xx) 91 ms → 93 ms (+2%)', 'traffic fell 57%']

  it('leads with the finding that decided the verdict', () => {
    const r = { verdict: 'regression', error_rate_verdict: 'regression', p95_verdict: 'no_change', reasons } as ComparisonResult
    expect(splitReasons(r)).toEqual({ headline: reasons[0], rest: [reasons[1], reasons[2]] })
  })

  it('leads with p95 when p95 decided it', () => {
    const r = { verdict: 'regression', error_rate_verdict: 'no_change', p95_verdict: 'regression', reasons } as ComparisonResult
    expect(splitReasons(r).headline).toBe(reasons[1])
  })

  it('leads with the first finding when no signal decided it', () => {
    const gate = ['before window: 57 requests, fewer than the 200 needed']
    const r = {
      verdict: 'insufficient_data',
      error_rate_verdict: 'insufficient_data',
      p95_verdict: 'insufficient_data',
      reasons: gate,
    } as ComparisonResult
    expect(splitReasons(r)).toEqual({ headline: gate[0], rest: [] })
  })
})

describe('formatConfidence', () => {
  it('rounds down to one decimal', () => {
    expect(formatConfidence(0.9002)).toBe('90.0%')
    expect(formatConfidence(0.99995)).toBe('99.9%')
    expect(formatConfidence(0)).toBe('0.0%')
  })

  it('never claims certainty, even when the p-value underflows to zero', () => {
    expect(formatConfidence(1)).toBe('99.9%')
  })
})

describe('reasonVerdict', () => {
  const result = { error_rate_verdict: 'regression', p95_verdict: 'improvement' } as ComparisonResult

  it("gives each finding its own signal's verdict", () => {
    expect(reasonVerdict('error rate 0.0% → 19.6% (+19.6 pp, p<0.001)', result)).toBe('regression')
    expect(reasonVerdict('p95 91 ms → 43 ms (-53%)', result)).toBe('improvement')
  })

  it('leaves findings about the data itself untoned', () => {
    expect(reasonVerdict('before window: 57 requests, fewer than the 200 needed', result)).toBeNull()
    expect(reasonVerdict('after window: no latency histogram, so p95 is unknown', result)).toBeNull()
  })
})
