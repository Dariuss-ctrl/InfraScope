import { Navigate, Outlet, useLocation } from 'react-router'
import { describeError } from '../api/client.ts'
import { loginPath, useSession } from '../session.ts'

// RequireSession guards every investigation route. Without a session the
// login page takes over and returns here afterwards. An outage is reported
// as an outage, not as "logged out": the session may well still be valid,
// which is why the API answers 503 rather than 401.
export function RequireSession() {
  const location = useLocation()
  const session = useSession()

  // A failed background re-check keeps the user in the app if a session
  // was already confirmed.
  if (session.data) {
    return <Outlet />
  }
  if (session.data === null) {
    return <Navigate to={loginPath(location)} replace />
  }
  if (session.isError) {
    return (
      <section className="page page--narrow" role="alert">
        <h1>Can't check your session</h1>
        <p>{describeError(session.error)}</p>
        <button type="button" onClick={() => void session.refetch()}>
          Try again
        </button>
      </section>
    )
  }
  return <p className="loading session-check">Checking your session…</p>
}
