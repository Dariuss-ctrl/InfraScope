import { useQuery } from '@tanstack/react-query'
import { describeStatus, fetchStatus } from '../api/status.ts'

// Shows the API's view of its dependencies, so a storage outage is visible
// before a page fails to load (NFR-1).
export function StatusBadge() {
  const { data, isPending, isError } = useQuery({
    queryKey: ['status'],
    queryFn: ({ signal }) => fetchStatus(signal),
    refetchInterval: 15_000,
  })

  if (isPending) {
    return <span className="status status--pending">Checking…</span>
  }
  if (isError) {
    return (
      <span className="status status--unavailable" role="status">
        API unreachable
      </span>
    )
  }
  return (
    <span className={`status status--${data.status}`} role="status">
      {describeStatus(data)}
    </span>
  )
}
