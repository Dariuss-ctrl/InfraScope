import { useMutation, useQueryClient } from '@tanstack/react-query'
import { type FormEvent, useState } from 'react'
import { Navigate, useSearchParams } from 'react-router'
import { describeLoginError, fetchSession, login } from '../api/auth.ts'
import { Logo } from '../components/Icons.tsx'
import { StatusBadge } from '../components/StatusBadge.tsx'
import { safeNext, sessionKey, useSession } from '../session.ts'

export function LoginPage() {
  const [params] = useSearchParams()
  const next = safeNext(params.get('next'))
  const session = useSession()
  const queryClient = useQueryClient()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const attempt = useMutation({
    mutationFn: async () => {
      await login(username, password)
      return fetchSession()
    },
    onSuccess: (user) => queryClient.setQueryData(sessionKey, user),
  })

  if (session.data) {
    return <Navigate to={next} replace />
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    attempt.mutate()
  }

  return (
    <main className="login-page">
      <div className="login">
        <div className="login__header">
          <Logo className="login__mark" />
          <h1>Log in to InfraScope</h1>
          <p className="page__subtitle">Use the admin account from your <code>.env</code>.</p>
        </div>
        <form className="login__form" aria-label="Log in" onSubmit={submit}>
          <label>
            Username
            <input
              name="username"
              autoComplete="username"
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </label>
          <label>
            Password
            <input
              name="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          {attempt.isError && (
            <p className="form-error" role="alert">
              {describeLoginError(attempt.error)}
            </p>
          )}
          <button type="submit" className="button--primary" disabled={attempt.isPending}>
            {attempt.isPending ? 'Logging in…' : 'Log in'}
          </button>
        </form>
        <div className="login__status">
          <StatusBadge />
        </div>
      </div>
    </main>
  )
}
