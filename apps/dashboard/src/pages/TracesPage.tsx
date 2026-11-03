import type { FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { searchTraces } from '../api/traces.ts'
import { ErrorState } from '../components/ErrorState.tsx'
import { TimeRangePicker } from '../components/TimeRangePicker.tsx'
import { formatDateTime, formatNanos } from '../format.ts'
import { readTraceFilters, tracePath, tracesPath } from '../links.ts'
import { enteredValues, useBoundedSearch } from '../search.ts'
import { useTimeRange } from '../timeRange.ts'

export function TracesPage() {
  const [params] = useSearchParams()
  const [range, setRange] = useTimeRange()
  const navigate = useNavigate()
  const filters = readTraceFilters(params)

  const search = useBoundedSearch(['traces', filters], range, ({ from, to }, cursor, signal) =>
    searchTraces(filters, from, to, cursor, signal),
  )

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    navigate(tracesPath(readTraceFilters(enteredValues(event.currentTarget)), range))
  }

  const traces = search.data?.pages.flatMap((page) => page.traces) ?? []
  return (
    <section className="page">
      <div className="page__header">
        <div className="page__title">
          <h1>Traces</h1>
          <p className="page__subtitle">Find requests by service, operation, duration and status.</p>
        </div>
        <TimeRangePicker value={range} onChange={setRange} />
      </div>

      {/* Keyed by the URL, so back and forward refill the form. */}
      <form key={params.toString()} className="filters" aria-label="Trace filters" onSubmit={apply}>
        <label>
          Service
          <input name="service" defaultValue={filters.service} placeholder="any" />
        </label>
        <label>
          Operation
          <input name="operation" defaultValue={filters.operation} placeholder="any" />
        </label>
        <label>
          Min duration (ms)
          <input name="min_duration_ms" type="number" min="0" step="1" defaultValue={filters.min_duration_ms} />
        </label>
        <label>
          Max duration (ms)
          <input name="max_duration_ms" type="number" min="0" step="1" defaultValue={filters.max_duration_ms} />
        </label>
        <label>
          Status
          <select name="status" defaultValue={filters.status ?? ''}>
            <option value="">any</option>
            <option value="error">error</option>
            <option value="ok">ok</option>
          </select>
        </label>
        <label>
          Order
          <select name="sort" defaultValue={filters.sort ?? ''}>
            <option value="">newest first</option>
            <option value="duration">slowest first</option>
          </select>
        </label>
        <div className="filters__actions">
          <button type="submit" className="button--primary">
            Search
          </button>
          <button type="button" className="button--ghost" onClick={() => navigate(tracesPath({}, range))}>
            Clear
          </button>
        </div>
      </form>

      {search.isPending && <p className="loading">Searching…</p>}
      {search.isError && <ErrorState error={search.error} />}
      {search.isSuccess && traces.length === 0 && (
        <p className="empty">No traces match these filters in this time range.</p>
      )}
      {traces.length > 0 && (
        <div className="table-wrap">
          <table className="results" aria-label="Traces">
            <thead>
              <tr>
                <th scope="col">Started</th>
                <th scope="col">Operation</th>
                <th scope="col">Service</th>
                <th scope="col" className="results__number">
                  Duration
                </th>
                <th scope="col" className="results__number">
                  Spans
                </th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {traces.map((trace) => (
                <tr key={trace.trace_id}>
                  <td>{formatDateTime(trace.start_time)}</td>
                  <td>
                    <Link to={tracePath(trace.trace_id)}>{trace.root_operation}</Link>
                  </td>
                  <td>{trace.root_service}</td>
                  <td className="results__number">{formatNanos(trace.duration_ns)}</td>
                  <td className="results__number">{trace.span_count}</td>
                  <td>
                    {trace.has_error ? (
                      <span className="badge badge--error">error</span>
                    ) : (
                      <span className="badge badge--ok">ok</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
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
