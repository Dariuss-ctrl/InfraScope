import { Link } from 'react-router'
import { describeDependency, outages, useStatus } from '../platform.ts'
import { AlertIcon } from './Icons.tsx'

// StatusBanner says on every page which part of the platform is down and
// what that breaks, so an empty search is never mistaken for "no data".
// Missing indexes alone only slow searches down, so they warn rather than
// alarm.
export function StatusBanner() {
  const { data } = useStatus()
  const down = data ? outages(data) : []
  if (down.length === 0) {
    return null
  }
  const severity = down.every((d) => d.name === 'clickhouse_schema') ? 'warn' : 'bad'
  return (
    <div className={`banner banner--${severity}`} role="alert">
      <div className="banner__inner">
        <AlertIcon className="banner__icon" />
        <ul>
          {down.map((dependency) => {
            const { problem, effect } = describeDependency(dependency.name)
            return (
              <li key={dependency.name}>
                <strong>{problem}</strong> {effect}
              </li>
            )
          })}
        </ul>
        <Link to="/status">Platform status</Link>
      </div>
    </div>
  )
}
