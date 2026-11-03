import { useQuery } from '@tanstack/react-query'
import type { Location } from 'react-router'
import { fetchSession } from './api/auth.ts'

export const sessionKey = ['session'] as const

const home = '/services'

export function useSession() {
  return useQuery({
    queryKey: sessionKey,
    queryFn: ({ signal }) => fetchSession(signal),
    staleTime: 60_000,
    // Notices a session that expired while the tab was in the background.
    refetchOnWindowFocus: true,
  })
}

// loginPath sends the browser to the login page, remembering where it was.
export function loginPath(from: Pick<Location, 'pathname' | 'search' | 'hash'>): string {
  const next = from.pathname + from.search + from.hash
  return next === '/' ? '/login' : `/login?next=${encodeURIComponent(next)}`
}

// safeNext keeps the post-login redirect inside the dashboard, so a link
// such as /login?next=//evil.example cannot send a user who has just logged
// in to another site.
export function safeNext(raw: string | null): string {
  if (!raw?.startsWith('/')) {
    return home
  }
  let url: URL
  try {
    url = new URL(raw, window.location.origin)
  } catch {
    return home
  }
  if (url.origin !== window.location.origin || url.pathname === '/login') {
    return home
  }
  return url.pathname + url.search + url.hash
}
