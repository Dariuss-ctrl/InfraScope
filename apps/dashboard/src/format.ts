// Number and time formatting for every view. Three significant digits are
// enough to compare values at a glance; exact values are in the tooltips.
const significant = new Intl.NumberFormat('en-US', { maximumSignificantDigits: 3 })

export const noData = 'no data'

export function formatRate(perSecond: number | null): string {
  return perSecond === null ? noData : `${significant.format(perSecond)} req/s`
}

export function formatRatio(ratio: number | null): string {
  return ratio === null ? noData : `${significant.format(ratio * 100)}%`
}

export function formatSeconds(seconds: number | null): string {
  return seconds === null ? noData : formatNanos(seconds * 1e9)
}

export function formatNanos(ns: number): string {
  if (ns === 0) {
    return '0' // an axis starts at 0, not "0 ns"
  }
  if (ns < 1e3) {
    return `${significant.format(ns)} ns`
  }
  if (ns < 1e6) {
    return `${significant.format(ns / 1e3)} µs`
  }
  if (ns < 1e9) {
    return `${significant.format(ns / 1e6)} ms`
  }
  return `${significant.format(ns / 1e9)} s`
}

// dedent removes the blank lines around a block of text and the indentation
// its lines share, such as a SQL statement indented inside the code that
// sent it.
export function dedent(text: string): string {
  const lines = text.split('\n')
  while (lines.length > 0 && lines[0].trim() === '') {
    lines.shift()
  }
  while (lines.length > 0 && lines[lines.length - 1].trim() === '') {
    lines.pop()
  }
  const indents = lines.filter((l) => l.trim() !== '').map((l) => l.length - l.trimStart().length)
  const shared = indents.length > 0 ? Math.min(...indents) : 0
  return lines.map((l) => l.slice(shared)).join('\n')
}

const dateTime = new Intl.DateTimeFormat('en-GB', {
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hourCycle: 'h23',
})

// formatDateTime shows a moment in the viewer's time zone.
export function formatDateTime(t: Date | string): string {
  return dateTime.format(typeof t === 'string' ? new Date(t) : t)
}
