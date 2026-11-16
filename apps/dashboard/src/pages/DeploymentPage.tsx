import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from 'react-router'
import { ApiError } from '../api/client.ts'
import {
  type Comparison,
  type ComparisonResult,
  type DeploymentDetail,
  type Verdict,
  formatConfidence,
  formatCountChange,
  formatPercentChange,
  formatPointChange,
  getDeployment,
  queryLabels,
  reasonVerdict,
  shortSha,
  splitReasons,
  verdictLabels,
} from '../api/deployments.ts'
import { StateBadge, VerdictBadge } from '../components/DeploymentBadges.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { AlertIcon, InfoIcon } from '../components/Icons.tsx'
import { formatDateTime, formatRatio, formatSeconds, formatTimeOfDay } from '../format.ts'
import { logsPath, servicesPath, tracesPath } from '../links.ts'

const pendingRefresh = 15_000
const findingTones: Record<Verdict, string> = {
  regression: 'finding--bad',
  improvement: 'finding--ok',
  insufficient_data: '',
  no_change: '',
}
const wholeNumber = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 })

export function DeploymentPage() {
  const { deploymentId = '' } = useParams()
  const deployment = useQuery({
    queryKey: ['deployment', deploymentId],
    queryFn: ({ signal }) => getDeployment(deploymentId, signal),
    // A pending comparison refreshes itself until its verdict lands.
    refetchInterval: (query) => (query.state.data?.comparison?.status === 'pending' ? pendingRefresh : false),
  })

  if (deployment.isPending) {
    return <p className="loading">Loading deployment…</p>
  }
  if (deployment.isError) {
    const missing =
      deployment.error instanceof ApiError &&
      (deployment.error.code === 'not_found' || deployment.error.code === 'invalid_argument')
    return (
      <section className="page">
        <div className="page__header">
          <div className="page__title">
            <p className="eyebrow">Deployment</p>
            <h1>{deploymentId}</h1>
          </div>
        </div>
        {missing ? (
          <p className="empty" role="alert">
            There is no deployment {deploymentId}.
          </p>
        ) : (
          <ErrorState error={deployment.error} />
        )}
        <p>
          <Link to="/deployments">All deployments</Link>
        </p>
      </section>
    )
  }
  return <DeploymentView deployment={deployment.data} />
}

// The page reads top-down in order of importance: what happened (the
// verdict and what to do next), then the evidence behind it, then reference
// details (the deployment's facts and its delivery history).
function DeploymentView({ deployment: d }: { deployment: DeploymentDetail }) {
  return (
    <section className="page">
      <div className="page__header">
        <div className="page__title">
          <p className="eyebrow">
            <Link to="/deployments">Deployments</Link>
          </p>
          <h1>
            {d.service} <span className="page__title-ref">{d.ref}</span>
          </h1>
          <p className="page__meta">
            <StateBadge state={d.state} />
            <span>{d.environment}</span>
            <span className="mono" title={d.sha}>
              {shortSha(d.sha)}
            </span>
            <span>{d.finished_at ? `finished ${formatDateTime(d.finished_at)}` : 'still running'}</span>
          </p>
          {d.description && <p className="page__subtitle">{d.description}</p>}
        </div>
      </div>

      <div className="stack">
        <VerdictPanel comparison={d.comparison} service={d.service} />
        {d.comparison?.result && <Evidence comparison={d.comparison} result={d.comparison.result} />}

        <div className="card">
          <div className="card__header">
            <h2>Deployment</h2>
          </div>
          <div className="card__body">
            <dl className="facts">
              <div>
                <dt>Repository</dt>
                <dd>{d.repository}</dd>
              </div>
              <div>
                <dt>Ref</dt>
                <dd>{d.ref}</dd>
              </div>
              <div>
                <dt>Commit</dt>
                <dd className="mono" title={d.sha}>
                  {d.sha.slice(0, 12)}
                </dd>
              </div>
              <div>
                <dt>Created by</dt>
                <dd>{d.creator || 'unknown'}</dd>
              </div>
              <div>
                <dt>Started</dt>
                <dd>{formatDateTime(d.created_at)}</dd>
              </div>
              <div>
                <dt>Finished</dt>
                <dd>{d.finished_at ? formatDateTime(d.finished_at) : 'still running'}</dd>
              </div>
            </dl>
          </div>
        </div>

        <div className="card">
          <div className="card__header">
            <h2>Status history</h2>
            <span className="card__meta">
              A status that arrives after a later or finished one is recorded, not applied.
            </span>
          </div>
          <div className="table-wrap">
            <table className="results" aria-label="Status history">
              <thead>
                <tr>
                  <th scope="col">Happened</th>
                  <th scope="col">State</th>
                  <th scope="col">Effect</th>
                  <th scope="col" className="narrow-hide">
                    Delivery
                  </th>
                </tr>
              </thead>
              <tbody>
                {d.changes.map((c) => (
                  <tr key={c.delivery_id}>
                    <td>{formatDateTime(c.state_at)}</td>
                    <td>
                      <StateBadge state={c.state} />
                    </td>
                    <td>{c.applied ? 'applied' : 'recorded only'}</td>
                    <td className="mono nowrap narrow-hide">{c.delivery_id}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </section>
  )
}

const verdictSentences: Record<Verdict, (service: string) => string> = {
  regression: (service) => `This deployment made ${service} worse.`,
  improvement: (service) => `This deployment made ${service} better.`,
  no_change: () => 'Error rate and p95 latency held steady.',
  insufficient_data: () => 'There was not enough data to judge this deployment.',
}

// VerdictPanel is the page's answer: the verdict, the finding that decided
// it, any caveats, and the next step.
function VerdictPanel({ comparison: c, service }: { comparison: Comparison | null; service: string }) {
  if (c === null) {
    return (
      <section className="verdict" aria-labelledby="verdict-title">
        <div className="verdict__main">
          <p className="eyebrow">Health after the deployment</p>
          <h2 id="verdict-title" className="verdict__title">
            Not compared
          </h2>
          <p className="verdict__text">DeployRadar compares a deployment once it succeeds.</p>
        </div>
      </section>
    )
  }
  if (c.status === 'pending' || c.status === 'failed' || c.result === null) {
    const failed = c.status === 'failed'
    return (
      <section className={`verdict verdict--${failed ? 'failed' : 'pending'}`} aria-labelledby="verdict-title">
        <div className="verdict__main">
          <p className="eyebrow">Health after the deployment</p>
          <h2 id="verdict-title" className="verdict__title">
            {failed ? 'Comparison failed' : 'Comparing…'}
          </h2>
          {failed ? (
            <p className="verdict__text" role="alert">
              It gave up after {c.attempts} attempts: {c.error}
            </p>
          ) : (
            <p className="verdict__text" role="status">
              Comparing. The after window closes at {formatDateTime(c.after.to)}; the verdict follows within a
              minute.
              {c.error && ` Retrying after attempt ${c.attempts}: ${c.error}`}
            </p>
          )}
        </div>
      </section>
    )
  }

  const r = c.result
  const { headline, rest } = splitReasons(r)
  const after = { from: new Date(c.after.from), to: new Date(c.after.to) }
  const span = { from: new Date(c.before.from), to: new Date(c.after.to) }
  const regression = r.verdict === 'regression'
  return (
    <section className={`verdict verdict--${r.verdict}`} aria-labelledby="verdict-title">
      <div className="verdict__main">
        <p className="eyebrow">Health after the deployment</p>
        <h2 id="verdict-title" className="verdict__title">
          {verdictLabels[r.verdict]}
          <span className="verdict__confidence">{formatConfidence(r.confidence)} confidence</span>
        </h2>
        <p className="verdict__text">{verdictSentences[r.verdict](service)}</p>
        {headline && <p className="verdict__headline">{headline}</p>}
        {rest.length > 0 && (
          <ul className="findings findings--compact" aria-label="Other findings">
            {rest.map((reason) => {
              // Each finding takes its own signal's tone; findings about the
              // comparison itself (a change in load, another deployment in a
              // window, too little data) are caveats, in amber.
              const verdict = reasonVerdict(reason, r)
              const caveat = verdict === null
              return (
                <li key={reason} className={`finding ${caveat ? 'finding--warn' : findingTones[verdict]}`}>
                  {verdict === 'regression' || caveat ? (
                    <AlertIcon className="finding__icon" />
                  ) : (
                    <InfoIcon className="finding__icon" />
                  )}
                  <span>{reason}</span>
                </li>
              )
            })}
          </ul>
        )}
      </div>
      <nav className="verdict__actions" aria-label="Look closer">
        <Link
          className={`button-link ${regression ? 'button-link--primary' : ''}`}
          to={tracesPath({ service, status: 'error' }, after)}
        >
          See failing traces
        </Link>
        <Link className="button-link" to={logsPath({ service, severity: 'error' }, after)}>
          Error logs
        </Link>
        <Link className="button-link" to={servicesPath(span)}>
          Overview
        </Link>
      </nav>
    </section>
  )
}

const signalTones: Partial<Record<Verdict, string>> = {
  regression: 'change--bad',
  improvement: 'change--ok',
}

// Evidence is the before/after table and the PromQL behind every number.
function Evidence({ comparison: c, result: r }: { comparison: Comparison; result: ComparisonResult }) {
  const minutes = Math.round((Date.parse(c.before.to) - Date.parse(c.before.from)) / 60_000)
  const gap = Math.round((Date.parse(c.after.from) - Date.parse(c.before.to)) / 60_000)
  return (
    <div className="card">
      <div className="card__header">
        <h2>Before and after</h2>
        <span className="card__meta">
          {minutes}-minute windows, {gap} {gap === 1 ? 'minute' : 'minutes'} apart
        </span>
      </div>
      <div className="table-wrap">
        <table className="results" aria-label="Before and after">
          <thead>
            <tr>
              <th scope="col">Measure</th>
              <th scope="col" className="results__number">
                Before
                <span className="th-note">
                  {formatTimeOfDay(c.before.from)}–{formatTimeOfDay(c.before.to)}
                </span>
              </th>
              <th scope="col" className="results__number">
                After
                <span className="th-note">
                  {formatTimeOfDay(c.after.from)}–{formatTimeOfDay(c.after.to)}
                </span>
              </th>
              <th scope="col" className="results__number">
                Change
              </th>
              <th scope="col" className="narrow-hide">
                Verdict
              </th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">Requests</th>
              <td className="results__number">{wholeNumber.format(r.before.requests)}</td>
              <td className="results__number">{wholeNumber.format(r.after.requests)}</td>
              <td className="results__number">{formatPercentChange(r.before.requests, r.after.requests)}</td>
              <td className="narrow-hide" />
            </tr>
            <tr>
              <th scope="row">5xx responses</th>
              <td className="results__number">{wholeNumber.format(r.before.errors)}</td>
              <td className="results__number">{wholeNumber.format(r.after.errors)}</td>
              <td className="results__number">{formatCountChange(r.before.errors, r.after.errors)}</td>
              <td className="narrow-hide" />
            </tr>
            <tr>
              <th scope="row">Error rate</th>
              <td className="results__number">{formatRatio(r.before.error_rate)}</td>
              <td className="results__number">{formatRatio(r.after.error_rate)}</td>
              <td className={`results__number ${signalTones[r.error_rate_verdict] ?? ''}`}>
                {formatPointChange(r.before.error_rate, r.after.error_rate)}
              </td>
              <td className="narrow-hide">
                <VerdictBadge verdict={r.error_rate_verdict} />
              </td>
            </tr>
            <tr>
              <th scope="row">p95 latency (non-5xx)</th>
              <td className="results__number">{formatSeconds(r.before.p95_seconds)}</td>
              <td className="results__number">{formatSeconds(r.after.p95_seconds)}</td>
              <td className={`results__number ${signalTones[r.p95_verdict] ?? ''}`}>
                {formatPercentChange(r.before.p95_seconds, r.after.p95_seconds)}
              </td>
              <td className="narrow-hide">
                <VerdictBadge verdict={r.p95_verdict} />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div className="card__body">
        <details className="queries">
          <summary>How these numbers were measured</summary>
          <p className="card__meta">
            Each value is an instant Prometheus query at the end of its window. Paste any of them into Prometheus to
            check it.
          </p>
          {queryLabels
            .filter(([key]) => r.queries[key] !== undefined)
            .map(([key, label]) => {
              const q = r.queries[key]
              return (
                <div key={key} className="query">
                  <h4>{label}</h4>
                  <pre className="code">{q.promql}</pre>
                  <p className="card__meta">
                    At {q.time}, range {q.range}.
                  </p>
                </div>
              )
            })}
        </details>
      </div>
    </div>
  )
}
