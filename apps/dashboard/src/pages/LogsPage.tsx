import type { FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { searchLogs } from '../api/logs.ts'
import { ErrorState } from '../components/ErrorState.tsx'
import { InfoIcon } from '../components/Icons.tsx'
import { LogEntry } from '../components/LogEntry.tsx'
import { TimeRangePicker } from '../components/TimeRangePicker.tsx'
import { logsPath, readLogFilters, severities, tracePath } from '../links.ts'
import { enteredValues, useBoundedSearch } from '../search.ts'
import { useTimeRange } from '../timeRange.ts'

export function LogsPage() {
  const [params] = useSearchParams()
  const [range, setRange] = useTimeRange()
  const navigate = useNavigate()
  const filters = readLogFilters(params)
  const search = useBoundedSearch(['logs', filters], range, ({ from, to }, cursor, signal) =>
    searchLogs(filters, from, to, cursor, signal),
  )

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    navigate(logsPath(readLogFilters(enteredValues(event.currentTarget)), range))
  }

  const logs = search.data?.pages.flatMap((page) => page.logs) ?? []
  const nothingFound = filters.trace_id
    ? 'No logs were written during this trace in this time range.'
    : 'No log records match these filters in this time range.'
  return (
    <section className="page">
      <div className="page__header">
        <div className="page__title">
          <h1>Logs</h1>
          <p className="page__subtitle">
            Search structured logs and follow each one to the span that wrote it.
          </p>
        </div>
        <TimeRangePicker value={range} onChange={setRange} />
      </div>

      {filters.trace_id && (
        <p className="notice" role="note">
          <InfoIcon />
          <span>
            Logs written during trace{' '}
            <Link className="mono" to={tracePath(filters.trace_id, filters.span_id)}>
              {filters.trace_id}
            </Link>
            {filters.span_id && (
              <>
                {' '}
                in span <span className="mono">{filters.span_id}</span>
              </>
            )}
            .
          </span>
        </p>
      )}

      {/* Keyed by the URL, so back and forward refill the form. */}
      <form key={params.toString()} className="filters" aria-label="Log filters" onSubmit={apply}>
        <label>
          Service
          <input name="service" defaultValue={filters.service} placeholder="any" />
        </label>
        <label>
          Minimum severity
          <select name="severity" defaultValue={filters.severity ?? ''}>
            <option value="">any</option>
            {severities.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label>
          Text
          <input name="q" maxLength={200} defaultValue={filters.q} placeholder="words in the message" />
        </label>
        <label>
          Trace ID
          <input name="trace_id" className="mono" defaultValue={filters.trace_id} />
        </label>
        <label>
          Span ID
          <input name="span_id" className="mono" defaultValue={filters.span_id} />
        </label>
        <div className="filters__actions">
          <button type="submit" className="button--primary">
            Search
          </button>
          <button type="button" className="button--ghost" onClick={() => navigate(logsPath({}, range))}>
            Clear
          </button>
        </div>
      </form>

      {search.isPending && <p className="loading">Searching…</p>}
      {search.isError && <ErrorState error={search.error} />}
      {search.isSuccess && logs.length === 0 && <p className="empty">{nothingFound}</p>}
      {logs.length > 0 && (
        <div className="card">
          <ol className="logs" aria-label="Log records">
            {logs.map((record, i) => (
              <LogEntry key={`${record.timestamp}-${i}`} record={record} />
            ))}
          </ol>
        </div>
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
