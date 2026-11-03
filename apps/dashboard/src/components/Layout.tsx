import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, NavLink, Outlet, useNavigate } from 'react-router'
import { logout } from '../api/auth.ts'
import { describeError } from '../api/client.ts'
import { sessionKey, useSession } from '../session.ts'
import { views } from '../views.ts'
import { Logo } from './Icons.tsx'
import { StatusBadge } from './StatusBadge.tsx'
import { StatusBanner } from './StatusBanner.tsx'

export function Layout() {
  const session = useSession()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const signOut = useMutation({
    mutationFn: logout,
    onSuccess: () => {
      // Nothing fetched during the session may outlive it.
      queryClient.removeQueries()
      queryClient.setQueryData(sessionKey, null)
      navigate('/login', { replace: true })
    },
  })
  const username = session.data?.username ?? ''

  return (
    <div className="shell">
      <header className="topbar">
        <div className="topbar__inner">
          <Link to="/" className="brand">
            <Logo className="brand__mark" />
            InfraScope
          </Link>
          <nav aria-label="Main" className="topbar__nav">
            <ul className="tabs">
              {views.map((view) => (
                <li key={view.path}>
                  <NavLink to={`/${view.path}`}>{view.title}</NavLink>
                </li>
              ))}
            </ul>
          </nav>
          <div className="topbar__end">
            <Link to="/status" className="status-link">
              <StatusBadge />
            </Link>
            <div className="user">
              <span className="avatar" aria-hidden="true">
                {username.slice(0, 1)}
              </span>
              <span className="user__name">{username}</span>
              <button
                type="button"
                className="button--ghost"
                disabled={signOut.isPending}
                onClick={() => signOut.mutate()}
              >
                Log out
              </button>
            </div>
          </div>
        </div>
        {signOut.isError && (
          <p className="topbar__error form-error" role="alert">
            Logout failed: {describeError(signOut.error)}
          </p>
        )}
      </header>
      <StatusBanner />
      <main className="main">
        <Outlet />
      </main>
    </div>
  )
}
