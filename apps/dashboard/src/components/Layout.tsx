import { Link, NavLink, Outlet } from 'react-router'
import { views } from '../views.ts'
import { StatusBadge } from './StatusBadge.tsx'

export function Layout() {
  return (
    <div className="shell">
      <header className="shell__header">
        <Link to="/" className="shell__brand">
          InfraScope
        </Link>
        <nav aria-label="Main">
          <ul className="shell__nav">
            {views.map((view) => (
              <li key={view.path}>
                <NavLink to={`/${view.path}`}>{view.title}</NavLink>
              </li>
            ))}
          </ul>
        </nav>
        <StatusBadge />
      </header>
      <main className="shell__main">
        <Outlet />
      </main>
    </div>
  )
}
