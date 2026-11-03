import type { Span, Trace } from './api/traces.ts'

export type WaterfallRow = {
  span: Span
  depth: number
  offsetNs: number // from the start of the trace
  selfNs: number // time no child covers: where the span itself spent it
  missingParent: boolean // the parent is not in this trace (AC-3.1)
}

// layoutWaterfall orders a trace's spans as a tree: each span below its
// parent, siblings by start time. Every span appears exactly once, even when
// its parent is missing or, in malformed telemetry, the parent links loop.
export function layoutWaterfall(trace: Trace): WaterfallRow[] {
  const offsets = new Map(trace.spans.map((s) => [s, nanosBetween(trace.start_time, s.start_time)]))
  const offsetOf = (span: Span) => offsets.get(span) ?? 0
  const byStart = (a: Span, b: Span) =>
    offsetOf(a) - offsetOf(b) || (a.span_id < b.span_id ? -1 : a.span_id > b.span_id ? 1 : 0)

  const ids = new Set(trace.spans.map((s) => s.span_id))
  const children = new Map<string, Span[]>()
  const roots: Span[] = []
  for (const span of trace.spans) {
    const parent = span.parent_span_id
    if (parent !== null && parent !== span.span_id && ids.has(parent)) {
      const siblings = children.get(parent)
      if (siblings) {
        siblings.push(span)
      } else {
        children.set(parent, [span])
      }
    } else {
      roots.push(span)
    }
  }

  const rows: WaterfallRow[] = []
  const placed = new Set<Span>()
  // Depth-first with an explicit stack: a 10,000-span chain must not
  // overflow the call stack.
  const place = (root: Span, missingParent: boolean) => {
    const stack = [{ span: root, depth: 0 }]
    while (stack.length > 0) {
      const { span, depth } = stack.pop() as { span: Span; depth: number }
      if (placed.has(span)) {
        continue
      }
      placed.add(span)
      const kids = (children.get(span.span_id) ?? []).sort(byStart)
      rows.push({
        span,
        depth,
        offsetNs: offsetOf(span),
        selfNs: selfTime(span, kids, offsetOf),
        missingParent: depth === 0 && missingParent,
      })
      for (let i = kids.length - 1; i >= 0; i--) {
        stack.push({ span: kids[i], depth: depth + 1 })
      }
    }
  }
  for (const root of roots.sort(byStart)) {
    place(root, root.parent_span_id !== null)
  }
  // Spans in a parent loop never hang below a root, but must still show.
  for (const span of [...trace.spans].sort(byStart)) {
    if (!placed.has(span)) {
      place(span, true)
    }
  }
  return rows
}

// selfTime is the part of a span's duration that none of its children
// covers. Children that run in parallel are counted once.
function selfTime(span: Span, children: Span[], offsetOf: (s: Span) => number): number {
  const start = offsetOf(span)
  const end = start + span.duration_ns
  const intervals = children
    .map((c) => [Math.max(start, offsetOf(c)), Math.min(end, offsetOf(c) + c.duration_ns)])
    .filter(([from, to]) => to > from)
    .sort((a, b) => a[0] - b[0])
  let covered = 0
  let reached = start
  for (const [from, to] of intervals) {
    if (to > reached) {
      covered += to - Math.max(from, reached)
      reached = to
    }
  }
  return Math.max(0, span.duration_ns - covered)
}

// serviceColors gives each service in a trace a colour token, in order of
// first appearance, so the request's own service always gets the first.
// There are eight; a ninth service shares the first colour.
export function serviceColors(rows: readonly WaterfallRow[]): Map<string, string> {
  const colors = new Map<string, string>()
  for (const { span } of rows) {
    if (!colors.has(span.service)) {
      colors.set(span.service, `var(--service-${(colors.size % 8) + 1})`)
    }
  }
  return colors
}

// hotspot is the span that spent the most time itself: usually the answer
// to "why was this request slow".
export function hotspot(rows: readonly WaterfallRow[]): WaterfallRow | undefined {
  let best: WaterfallRow | undefined
  for (const row of rows) {
    if (!best || row.selfNs > best.selfNs) {
      best = row
    }
  }
  return best
}

// firstFailure is the deepest failed span, where an error starts before it
// propagates up to the request; the earliest one if several are as deep.
export function firstFailure(rows: readonly WaterfallRow[]): WaterfallRow | undefined {
  let best: WaterfallRow | undefined
  for (const row of rows) {
    if (row.span.status === 'error' && (!best || row.depth > best.depth)) {
      best = row
    }
  }
  return best
}

// nanosBetween is later minus earlier for RFC 3339 times with nanoseconds.
// Date keeps only milliseconds, and spans inside one request are often
// microseconds apart, so the digits below a millisecond are added back.
export function nanosBetween(earlier: string, later: string): number {
  const a = splitTime(earlier)
  const b = splitTime(later)
  return (b.ms - a.ms) * 1e6 + (b.ns - a.ns)
}

function splitTime(iso: string): { ms: number; ns: number } {
  const match = /^(.+T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(iso)
  if (!match) {
    return { ms: Date.parse(iso), ns: 0 }
  }
  const fraction = (match[2] ?? '').padEnd(9, '0')
  return {
    ms: Date.parse(`${match[1]}.${fraction.slice(0, 3)}${match[3]}`),
    ns: Number(fraction.slice(3)),
  }
}
