import { describe, expect, it } from 'vitest'
import { dedent, formatNanos, formatRate, formatRatio, formatSeconds, noData } from './format.ts'

describe('formatRate', () => {
  it.each([
    [12.3456, '12.3 req/s'],
    [0.0333, '0.0333 req/s'],
    [1234.5, '1,230 req/s'],
    [0, '0 req/s'],
  ])('%s → %s', (value, want) => expect(formatRate(value)).toBe(want))
})

describe('formatRatio', () => {
  it.each([
    [0.025, '2.5%'],
    [0, '0%'],
    [1, '100%'],
  ])('%s → %s', (value, want) => expect(formatRatio(value)).toBe(want))
})

describe('durations', () => {
  it.each([
    [0.0000052, '5.2 µs'],
    [0.0042, '4.2 ms'],
    [0.1234, '123 ms'],
    [1.5, '1.5 s'],
  ])('%s s → %s', (value, want) => expect(formatSeconds(value)).toBe(want))

  it('keeps nanoseconds below a microsecond', () => {
    expect(formatNanos(830)).toBe('830 ns')
  })

  it('shows zero without a unit, as an axis starts', () => {
    expect(formatNanos(0)).toBe('0')
  })
})

describe('dedent', () => {
  it('removes the indentation a statement had in the code that sent it', () => {
    const sql = '\n\t\tSELECT id\n\t\tFROM orders\n\t\t  WHERE a = $1\n\n\t\tLIMIT $2\n\t'
    expect(dedent(sql)).toBe('SELECT id\nFROM orders\n  WHERE a = $1\n\nLIMIT $2')
  })

  it('leaves unindented text alone', () => {
    expect(dedent('SELECT 1')).toBe('SELECT 1')
  })
})

it('shows a missing value as no data, never as zero (AC-2.2)', () => {
  expect([formatRate(null), formatRatio(null), formatSeconds(null)]).toEqual([noData, noData, noData])
})
