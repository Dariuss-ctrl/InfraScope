import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { ApiError } from '../api/client.ts'
import { searchLogs } from '../api/logs.ts'
import { type Span, type SpanEvent, type Trace, getTrace } from '../api/traces.ts'
import { AttributeTable } from '../components/AttributeTable.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { AlertIcon, BoltIcon, InfoIcon, LinesIcon } from '../components/Icons.tsx'
import { LogEntry } from '../components/LogEntry.tsx'
import { dedent, formatDateTime, formatNanos } from '../format.ts'
import { logsPath } from '../links.ts'
import { useBoundedSearch } from '../search.ts'
import {
  type WaterfallRow,
  firstFailure,
  hotspot,
  layoutWaterfall,
  nanosBetween,
  serviceColors,
} from '../waterfall.ts'

export function TracePage() {
  const { traceId = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const trace = useQuery({
    queryKey: ['trace', traceId],
    queryFn: ({ signal }) => getTrace(traceId, signal),
  })

  if (trace.isPending) {
    return <p className="loading">Loading trace…</p>
  }
  if (trace.isError) {
    const expired = trace.error instanceof ApiError && trace.error.code === 'not_found'
    return (
      <section className="page">
        <div className="page__header">
          <div className="page__title">
            <p className="eyebrow">Trace</p>
            <h1 className="mono">{traceId}</h1>
          </div>
        </div>
        {expired ? (
          <p className="empty" role="alert">
            This trace is no longer available. It may have expired, or it was never exported.
          </p>
        ) : (
          <ErrorState error={trace.error} />
        )}
        <p>
          <Link to="/traces">Search traces</Link>
        </p>
      </section>
    )
  }

  // The selected span lives in the URL, so a link can point at it. Choosing
  // one replaces the history entry rather than adding to it.
  const select = (spanId: string) => {
    const next = new URLSearchParams(params)
    next.set('span', spanId)
    setParams(next, { replace: true })
  }
  return <TraceView trace={trace.data} selectedId={params.get('span')} onSelect={select} />
}

type TraceViewProps = {
  trace: Trace
  selectedId: string | null
  onSelect: (spanId: string) => void
}

function TraceView({ trace, selectedId, onSelect }: TraceViewProps) {
  const rows = layoutWaterfall(trace)
  const colors = serviceColors(rows)
  const total = Math.max(nanosBetween(trace.start_time, trace.end_time), 1)
  const slowest = hotspot(rows)
  const failure = firstFailure(rows)
  const failures = rows.filter((r) => r.span.status === 'error').length
  const selected = rows.find((r) => r.span.span_id === selectedId)
  const root = rows[0]?.span

  // Logs are searched over the trace's own bounds, with a minute either side
  // for clock skew between services.
  const minute = 60_000
  const during = {
    from: new Date(Date.parse(trace.start_time) - minute),
    to: new Date(Date.parse(trace.end_time) + minute),
  }

  return (
    <section className="page">
      <div className="page__header">
        <div className="page__title">
          <p className="eyebrow">Trace{root ? ` · ${root.service}` : ''}</p>
          <h1>{root?.name ?? 'Trace'}</h1>
        </div>
        <Link className="button-link" to={logsPath({ trace_id: trace.trace_id }, during)}>
          <LinesIcon />
          Logs for this trace
        </Link>
      </div>

      <div className="card summary">
        <dl className="facts">
          <Fact label="Trace" value={trace.trace_id} mono />
          <Fact label="Started" value={formatDateTime(trace.start_time)} />
          <Fact label="Duration" value={formatNanos(total)} />
          <Fact label="Spans" value={String(trace.spans.length)} />
          <Fact label="Errors" value={String(failures)} alert={failures > 0} />
        </dl>
      </div>

      {trace.truncated && (
        <p className="notice" role="note">
          <InfoIcon />
          <span>
            This trace is larger than the API returns at once, so only its earliest{' '}
            {trace.spans.length} spans are shown.
          </span>
        </p>
      )}

      <ul className="findings" aria-label="Findings">
        {slowest && slowest.selfNs > 0 && (
          <li className="finding">
            <BoltIcon className="finding__icon" />
            <span>
              Most time was spent in{' '}
              <button type="button" className="link" onClick={() => onSelect(slowest.span.span_id)}>
                {slowest.span.name}
              </button>{' '}
              ({slowest.span.service}): {formatNanos(slowest.selfNs)} of its own,{' '}
              {percent(slowest.selfNs, total)} of the trace.
            </span>
          </li>
        )}
        {failure && (
          <li className="finding finding--bad">
            <AlertIcon className="finding__icon" />
            <span>
              {failures === 1 ? '1 span failed' : `${failures} spans failed`}, starting in{' '}
              <button type="button" className="link" onClick={() => onSelect(failure.span.span_id)}>
                {failure.span.name}
              </button>
              .
            </span>
          </li>
        )}
      </ul>

      <section className="card" aria-label="Waterfall">
        <div className="card__header">
          <h2>Waterfall</h2>
          <span className="card__meta">
            {trace.spans.length} spans · {formatNanos(total)}
          </span>
        </div>
        <div className="waterfall__scroll">
          <div className="waterfall__axis" aria-hidden="true">
            <span>Span</span>
            <span className="waterfall__ticks">
              {[0, 0.25, 0.5, 0.75, 1].map((fraction) => (
                <span key={fraction} style={{ left: `${fraction * 100}%` }}>
                  {formatNanos(total * fraction)}
                </span>
              ))}
            </span>
            <span className="waterfall__duration">Duration</span>
          </div>
          <ol className="waterfall" aria-label="Spans">
            {rows.map((row) => (
              <WaterfallItem
                key={row.span.span_id}
                row={row}
                total={total}
                color={colors.get(row.span.service) ?? 'var(--accent)'}
                selected={row === selected}
                onSelect={() => onSelect(row.span.span_id)}
              />
            ))}
          </ol>
        </div>
      </section>

      {selected && (
        <SpanDetails
          row={selected}
          logs={logsPath({ trace_id: trace.trace_id, span_id: selected.span.span_id }, during)}
        />
      )}

      <TraceLogs trace={trace} during={during} selected={selected?.span} onSelect={onSelect} />
    </section>
  )
}

type TraceLogsProps = {
  trace: Trace
  during: { from: Date; to: Date }
  selected?: Span
  onSelect: (spanId: string) => void
}

// TraceLogs lists the log records written during the trace (FR-5), searched
// over the trace's own bounds, and can narrow them to the selected span.
// Each record names the span that wrote it, and naming it selects it.
function TraceLogs({ trace, during, selected, onSelect }: TraceLogsProps) {
  const [onlySelected, setOnlySelected] = useState(false)
  const filters = {
    trace_id: trace.trace_id,
    span_id: onlySelected ? selected?.span_id : undefined,
  }
  const search = useBoundedSearch(['trace-logs', filters], during, ({ from, to }, cursor, signal) =>
    searchLogs(filters, from, to, cursor, signal),
  )
  const spanNames = new Map(trace.spans.map((s) => [s.span_id, s.name]))
  const logs = search.data?.pages.flatMap((page) => page.logs) ?? []

  return (
    <section className="card" aria-label="Trace logs">
      <div className="card__header">
        <h2>Logs</h2>
        {selected && (
          <label className="toggle">
            <input
              type="checkbox"
              checked={onlySelected}
              onChange={(e) => setOnlySelected(e.target.checked)}
            />
            Only {selected.name}
          </label>
        )}
      </div>
      {search.isPending && <p className="loading card__body">Loading logs…</p>}
      {search.isError && (
        <div className="card__body">
          <ErrorState error={search.error} />
        </div>
      )}
      {search.isSuccess && logs.length === 0 && (
        <p className="empty empty--inset">
          {filters.span_id ? 'This span wrote no logs.' : 'No logs were written during this trace.'}
        </p>
      )}
      {logs.length > 0 && (
        <ol className="logs" aria-label="Trace log records">
          {logs.map((record, i) => {
            const spanId = record.span_id
            const name = spanId ? spanNames.get(spanId) : undefined
            return (
              <LogEntry
                key={`${record.timestamp}-${i}`}
                record={record}
                origin={
                  spanId && name ? (
                    <button type="button" className="link" onClick={() => onSelect(spanId)}>
                      {name}
                    </button>
                  ) : (
                    <span className="state">no span</span>
                  )
                }
              />
            )
          })}
        </ol>
      )}
      {search.hasNextPage && (
        <button
          type="button"
          className="load-more"
          disabled={search.isFetchingNextPage}
          onClick={() => void search.fetchNextPage()}
        >
          {search.isFetchingNextPage ? 'Loading…' : 'Load more'}
        </button>
      )}
    </section>
  )
}

type WaterfallItemProps = {
  row: WaterfallRow
  total: number
  color: string
  selected: boolean
  onSelect: () => void
}

function WaterfallItem({ row, total, color, selected, onSelect }: WaterfallItemProps) {
  const { span } = row
  const left = Math.min((row.offsetNs / total) * 100, 99.7)
  const width = Math.max((span.duration_ns / total) * 100, 0.3)
  const failed = span.status === 'error'
  return (
    <li className={failed ? 'waterfall__item waterfall__item--error' : 'waterfall__item'}>
      <button type="button" className="waterfall__row" aria-pressed={selected} onClick={onSelect}>
        <span className="waterfall__label" style={{ paddingLeft: `${row.depth * 0.875}rem` }}>
          <span className="swatch" style={{ background: color }} />
          {failed && <span className="badge badge--error">error</span>}
          {row.missingParent && <span className="badge">missing parent</span>}
          <span className="waterfall__name">{span.name}</span>
          <span className="waterfall__service">{span.service}</span>
        </span>
        <span className="waterfall__track" aria-hidden="true">
          <span
            className="waterfall__bar"
            style={{ left: `${left}%`, width: `${width}%`, background: failed ? 'var(--bad)' : color }}
          />
        </span>
        <span className="waterfall__duration">{formatNanos(span.duration_ns)}</span>
      </button>
    </li>
  )
}

function SpanDetails({ row, logs }: { row: WaterfallRow; logs: string }) {
  const { span } = row
  const statement = span.attributes?.['db.query.text']
  const events = span.events ?? []
  const status = span.status_message ? `${span.status}: ${span.status_message}` : span.status
  return (
    <section className="card" aria-label="Span details">
      <div className="card__header">
        <h2>{span.name}</h2>
        <Link className="button-link" to={logs}>
          <LinesIcon />
          Logs for this span
        </Link>
      </div>
      <div className="card__body sections">
        <dl className="facts">
          <Fact label="Service" value={span.service} />
          <Fact label="Kind" value={span.kind} />
          <Fact label="Starts at" value={`+${formatNanos(row.offsetNs)}`} />
          <Fact label="Duration" value={formatNanos(span.duration_ns)} />
          <Fact label="Self time" value={formatNanos(row.selfNs)} />
          <Fact label="Status" value={status} alert={span.status === 'error'} />
          <Fact label="Span" value={span.span_id} mono />
        </dl>
        {statement && (
          <div>
            <h3>Statement</h3>
            <pre className="code">{dedent(statement)}</pre>
          </div>
        )}
        {events.length > 0 && (
          <div>
            <h3>Events</h3>
            <ol className="span-events">
              {events.map((event, i) => (
                <li key={`${event.time}-${i}`}>
                  <SpanEventView event={event} />
                </li>
              ))}
            </ol>
          </div>
        )}
        <div>
          <h3>Attributes</h3>
          <AttributeTable label="Span attributes" attributes={span.attributes} shown={['db.query.text']} />
        </div>
        <div>
          <h3>Resource</h3>
          <AttributeTable label="Resource attributes" attributes={span.resource_attributes} />
        </div>
      </div>
    </section>
  )
}

// SpanEventView shows an exception's type, message and stack trace in full,
// so an error can be read without leaving the page (AC-3.2).
function SpanEventView({ event }: { event: SpanEvent }) {
  const attributes = event.attributes ?? {}
  if (event.name === 'exception') {
    return (
      <div className="exception">
        <strong>{attributes['exception.type'] ?? 'exception'}</strong>
        {attributes['exception.message'] && <p>{attributes['exception.message']}</p>}
        {attributes['exception.stacktrace'] && (
          <pre className="code">{attributes['exception.stacktrace']}</pre>
        )}
      </div>
    )
  }
  return (
    <div>
      <strong>{event.name}</strong> <span className="state">{formatDateTime(event.time)}</span>
      <AttributeTable label={`${event.name} attributes`} attributes={attributes} />
    </div>
  )
}

type FactProps = { label: string; value: string; mono?: boolean; alert?: boolean }

function Fact({ label, value, mono = false, alert = false }: FactProps) {
  const className = [mono && 'mono', alert && 'stat--alert'].filter(Boolean).join(' ')
  return (
    <div>
      <dt>{label}</dt>
      <dd className={className || undefined}>{value}</dd>
    </div>
  )
}

function percent(part: number, whole: number): string {
  return `${Math.round((part / whole) * 100)}%`
}
