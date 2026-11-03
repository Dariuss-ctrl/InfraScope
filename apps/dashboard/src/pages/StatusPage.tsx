import { useQuery } from '@tanstack/react-query'
import { type SchemaItem, fetchSchemaReport } from '../api/diagnostics.ts'
import { ErrorState } from '../components/ErrorState.tsx'
import { InfoIcon } from '../components/Icons.tsx'
import { formatDateTime } from '../format.ts'
import { describeDependency, useStatus } from '../platform.ts'

// StatusPage is where the outage banner and the header badge lead: every
// dependency with what it breaks when down, and the schema check's detail,
// so a degraded platform can be diagnosed without reading server logs.
export function StatusPage() {
  const status = useStatus()
  const schema = useQuery({
    queryKey: ['schema-report'],
    queryFn: ({ signal }) => fetchSchemaReport(signal),
    refetchInterval: 15_000,
  })

  return (
    <section className="page">
      <div className="page__header">
        <div className="page__title">
          <h1>Platform status</h1>
          <p className="page__subtitle">
            What InfraScope depends on, and what stops working while each part is down.
          </p>
        </div>
        {status.data && <span className="card__meta">Checked {formatDateTime(status.data.checked_at)}</span>}
      </div>

      {status.isPending && <p className="loading">Checking…</p>}
      {status.isError && (
        <p className="state state--error" role="alert">
          The API is unreachable, so nothing behind it can be checked.
        </p>
      )}
      {status.data && (
        <div className="table-wrap">
          <table className="results" aria-label="Dependencies">
            <thead>
              <tr>
                <th scope="col">Dependency</th>
                <th scope="col">State</th>
                <th scope="col" className="results__number">
                  Probe
                </th>
                <th scope="col">While it is down</th>
              </tr>
            </thead>
            <tbody>
              {status.data.dependencies.map((d) => {
                const { label, effect } = describeDependency(d.name)
                return (
                  <tr key={d.name}>
                    <th scope="row">
                      {label}
                      {d.required && <span className="state"> (required)</span>}
                    </th>
                    <td>
                      <span className={d.status === 'up' ? 'badge badge--ok' : 'badge badge--error'}>{d.status}</span>
                    </td>
                    <td className="results__number">{d.latency_ms} ms</td>
                    <td>{effect}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      <div className="section-heading">
        <h2>Trace and log storage schema</h2>
        <p className="page__subtitle">The tables and skip indexes that trace and log searches rely on.</p>
      </div>
      {schema.isPending && <p className="loading">Checking the schema…</p>}
      {schema.isError && <ErrorState error={schema.error} />}
      {schema.data?.complete && (
        <p className="all-clear">Every table and index the searches rely on is present.</p>
      )}
      {schema.data && !schema.data.complete && (
        <>
          <div className="table-wrap">
            <table className="results" aria-label="Missing from the schema">
              <thead>
                <tr>
                  <th scope="col">Missing</th>
                  <th scope="col">Effect</th>
                </tr>
              </thead>
              <tbody>
                {schema.data.missing.map((item) => (
                  <tr key={`${item.kind} ${item.table} ${item.name}`}>
                    <td className="mono">{describeItem(item)}</td>
                    <td>{item.effect}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="notice">
            <InfoIcon />
            <span>
              A missing table is recreated when the OpenTelemetry Collector restarts. A missing index is
              not: add it back from its definition in{' '}
              <code>apps/api/internal/clickhouse/testdata/schema/</code>, then materialise it. The runbook
              section “ClickHouse schema incomplete” has the commands.
            </span>
          </p>
        </>
      )}
    </section>
  )
}

function describeItem(item: SchemaItem): string {
  return item.kind === 'table' ? `table ${item.table}` : `index ${item.name} on ${item.table}`
}
