import { useInfiniteQuery } from '@tanstack/react-query'
import type { FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { type DeploymentList, listDeployments, shortSha } from '../api/deployments.ts'
import { HealthBadge, StateBadge } from '../components/DeploymentBadges.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { formatDateTime } from '../format.ts'
import { deploymentPath, deploymentsPath, readDeploymentFilters } from '../links.ts'
import { enteredValues } from '../search.ts'

// While any listed comparison is pending, the list refreshes itself, so a
// verdict appears without a reload once its after window has passed.
const pendingRefresh = 15_000

function hasPending(pages: DeploymentList[] | undefined): boolean {
  return pages?.some((page) => page.deployments.some((d) => d.comparison_status === 'pending')) ?? false
}

export function DeploymentsPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const filters = readDeploymentFilters(params)

  const list = useInfiniteQuery({
    queryKey: ['deployments', filters],
    queryFn: ({ pageParam, signal }) => listDeployments(filters, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor,
    refetchInterval: (query) => (hasPending(query.state.data?.pages) ? pendingRefresh : false),
  })

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    navigate(deploymentsPath(readDeploymentFilters(enteredValues(event.currentTarget))))
  }

  const deployments = list.data?.pages.flatMap((page) => page.deployments) ?? []
  return (
    <section className="page">
      <div className="page__header">
        <div className="page__title">
          <h1>Deployments</h1>
          <p className="page__subtitle">
            Each successful deployment's error rate and p95 latency, before against after.
          </p>
        </div>
      </div>

      <form key={params.toString()} className="filters" aria-label="Deployment filters" onSubmit={apply}>
        <label>
          Service
          <input name="service" defaultValue={filters.service} placeholder="any" />
        </label>
        <label>
          Environment
          <input name="environment" defaultValue={filters.environment} placeholder="any" />
        </label>
        <div className="filters__actions">
          <button type="submit" className="button--primary">
            Filter
          </button>
          <button type="button" className="button--ghost" onClick={() => navigate(deploymentsPath({}))}>
            Clear
          </button>
        </div>
      </form>

      {list.isPending && <p className="loading">Loading deployments…</p>}
      {list.isError && <ErrorState error={list.error} />}
      {list.isSuccess && deployments.length === 0 && (
        <p className="empty">
          No deployments yet. They arrive as GitHub deployment_status webhooks; see docs/runbook.md.
        </p>
      )}
      {deployments.length > 0 && (
        <div className="table-wrap">
          <table className="results" aria-label="Deployments">
            <thead>
              <tr>
                <th scope="col">Deployment</th>
                <th scope="col">Health after</th>
                <th scope="col">Service</th>
                <th scope="col">State</th>
                <th scope="col" className="results__number">
                  Deployed
                </th>
              </tr>
            </thead>
            <tbody>
              {deployments.map((d) => (
                // A regression marks its whole row, so it stands out in a long list.
                <tr key={d.id} className={d.verdict === 'regression' ? 'results__row--bad' : undefined}>
                  <td>
                    <Link className="strong-link" to={deploymentPath(d.id)}>
                      {d.ref}
                    </Link>
                    <div className="cell-note">
                      {d.description && `${d.description} · `}
                      <span className="mono">{shortSha(d.sha)}</span>
                    </div>
                  </td>
                  <td>
                    <HealthBadge status={d.comparison_status} verdict={d.verdict} confidence={d.confidence} />
                  </td>
                  <td>
                    {d.service}
                    <div className="cell-note">{d.environment}</div>
                  </td>
                  <td>
                    <StateBadge state={d.state} />
                  </td>
                  <td className="results__number">{formatDateTime(d.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {list.hasNextPage && (
        <button
          type="button"
          className="load-more"
          disabled={list.isFetchingNextPage}
          onClick={() => void list.fetchNextPage()}
        >
          {list.isFetchingNextPage ? 'Loading…' : 'Load more'}
        </button>
      )}
    </section>
  )
}
