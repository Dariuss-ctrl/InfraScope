import { useQuery } from '@tanstack/react-query'
import { type ComponentProps, Suspense, lazy, useId } from 'react'
import { Link, useNavigate } from 'react-router'
import {
  type Point,
  type ServiceMetrics,
  type ServiceSummary,
  listServices,
  serviceMetrics,
} from '../api/services.ts'
import { hasValues } from '../chartData.ts'
import { ErrorState } from '../components/ErrorState.tsx'
import { AlertIcon, ClockIcon } from '../components/Icons.tsx'
import { TimeRangePicker } from '../components/TimeRangePicker.tsx'
import { formatRate, formatRatio, formatSeconds } from '../format.ts'
import { type TraceFilters, tracesPath } from '../links.ts'
import {
  type TimeRange,
  nearestPoint,
  pointWindow,
  rangeKey,
  resolveRange,
  useTimeRange,
} from '../timeRange.ts'

// ECharts is most of the dashboard's code, so it loads with the first chart
// rather than with the login page.
const TimeSeriesChart = lazy(() =>
  import('../components/TimeSeriesChart.tsx').then((m) => ({ default: m.TimeSeriesChart })),
)

// A relative range follows the clock, so its numbers keep refreshing.
function refreshInterval(range: TimeRange): number | false {
  return 'preset' in range ? 30_000 : false
}

export function ServicesPage() {
  const [range, setRange] = useTimeRange()
  const services = useQuery({
    queryKey: ['services', rangeKey(range)],
    queryFn: ({ signal }) => {
      const { from, to } = resolveRange(range)
      return listServices(from, to, signal)
    },
    refetchInterval: refreshInterval(range),
  })

  return (
    <section className="page">
      <div className="page__header">
        <div className="page__title">
          <h1>Services</h1>
          <p className="page__subtitle">
            Request rate, errors and latency for every service that reported traffic.
          </p>
        </div>
        <TimeRangePicker value={range} onChange={setRange} />
      </div>
      {services.isPending && <p className="loading">Loading services…</p>}
      {services.isError && <ErrorState error={services.error} />}
      {services.data?.length === 0 && (
        <p className="empty">No service reported any requests in this time range.</p>
      )}
      <div className="stack">
        {services.data?.map((summary) => (
          <ServiceCard key={summary.name} summary={summary} range={range} />
        ))}
      </div>
    </section>
  )
}

function ServiceCard({ summary, range }: { summary: ServiceSummary; range: TimeRange }) {
  const headingId = useId()
  const navigate = useNavigate()
  const service = summary.name
  const metrics = useQuery({
    queryKey: ['service-metrics', service, rangeKey(range)],
    queryFn: async ({ signal }) => {
      const { from, to } = resolveRange(range)
      return { from, to, ...(await serviceMetrics(service, from, to, signal)) }
    },
    refetchInterval: refreshInterval(range),
  })

  // Opens the traces behind one chart point: this service, the window the
  // point summarises, and whatever the chart is about (AC-2.3).
  function openTraces(points: readonly Point[], t: Date, filters: TraceFilters) {
    const point = nearestPoint(points, t)
    if (point && metrics.data) {
      const window = pointWindow(new Date(point.t), metrics.data.step_seconds)
      navigate(tracesPath({ service, ...filters }, window))
    }
  }

  const errors = summary.error_rate
  return (
    <article className="card" aria-labelledby={headingId}>
      <div className="card__header">
        <h2 id={headingId}>{service}</h2>
        <div className="chips">
          <Link className="chip" to={tracesPath({ service, sort: 'duration' }, range)}>
            <ClockIcon />
            Slowest traces
          </Link>
          <Link className="chip" to={tracesPath({ service, status: 'error' }, range)}>
            <AlertIcon />
            Failed traces
          </Link>
        </div>
      </div>
      <div className="card__body">
        <dl className="facts kpis">
          <Stat label="Requests" value={formatRate(summary.request_rate)} />
          <Stat label="Errors" value={formatRatio(errors)} alert={errors !== null && errors > 0} />
          <Stat label="p50" value={formatSeconds(summary.latency_seconds.p50)} />
          <Stat label="p95" value={formatSeconds(summary.latency_seconds.p95)} />
          <Stat label="p99" value={formatSeconds(summary.latency_seconds.p99)} />
        </dl>
        {metrics.isError && <ErrorState error={metrics.error} />}
        {metrics.data && (
          <Suspense fallback={<p className="loading">Loading charts…</p>}>
            <ServiceCharts service={service} data={metrics.data} onSelect={openTraces} />
          </Suspense>
        )}
      </div>
    </article>
  )
}

type ChartData = ServiceMetrics & { from: Date; to: Date }
type SelectPoint = (points: readonly Point[], t: Date, filters: TraceFilters) => void

function ServiceCharts({ service, data, onSelect }: { service: string; data: ChartData; onSelect: SelectPoint }) {
  const { series, from, to } = data
  return (
    <div className="service-card__charts">
      <ChartFigure
        caption="Request rate"
        label={`Request rate for ${service}`}
        series={[{ name: 'requests', points: series.request_rate, color: '--chart-requests' }]}
        from={from}
        to={to}
        format={formatRate}
        onSelect={(t) => onSelect(series.request_rate, t, {})}
      />
      <ChartFigure
        caption="Error rate"
        label={`Error rate for ${service}`}
        series={[{ name: '5xx', points: series.error_rate, color: '--chart-errors' }]}
        from={from}
        to={to}
        format={formatRatio}
        onSelect={(t) => onSelect(series.error_rate, t, { status: 'error' })}
      />
      <ChartFigure
        caption="Latency"
        label={`Latency for ${service}`}
        series={[
          { name: 'p50', points: series.latency_p50_seconds, color: '--chart-p50' },
          { name: 'p95', points: series.latency_p95_seconds, color: '--chart-p95' },
          { name: 'p99', points: series.latency_p99_seconds, color: '--chart-p99' },
        ]}
        from={from}
        to={to}
        format={formatSeconds}
        onSelect={(t) => onSelect(series.latency_p95_seconds, t, { sort: 'duration' })}
      />
    </div>
  )
}

// ChartFigure says "no data" in words when there is nothing to draw (AC-2.2),
// rather than showing an empty pair of axes.
function ChartFigure({ caption, ...chart }: { caption: string } & ComponentProps<typeof TimeSeriesChart>) {
  return (
    <figure className="chart-panel">
      <figcaption>{caption}</figcaption>
      {chart.series.some((s) => hasValues(s.points)) ? (
        <TimeSeriesChart {...chart} />
      ) : (
        <p className="chart chart--empty">No data in this range</p>
      )}
    </figure>
  )
}

function Stat({ label, value, alert = false }: { label: string; value: string; alert?: boolean }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd className={alert ? 'stat--alert' : undefined}>{value}</dd>
    </div>
  )
}
