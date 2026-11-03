import { MutationCache, QueryCache, QueryClient } from '@tanstack/react-query'
import { ApiError } from './api/client.ts'
import { sessionKey } from './session.ts'

// createQueryClient applies two rules to every request. A 401 anywhere ends
// the session, so the login page takes over and brings the user back
// afterwards. And only a missing answer is retried: an API error is the
// API's considered reply, and a 503 has already waited out its timeout.
export function createQueryClient(): QueryClient {
  const client = new QueryClient({
    queryCache: new QueryCache({ onError: (error) => endSessionIfRejected(client, error) }),
    mutationCache: new MutationCache({ onError: (error) => endSessionIfRejected(client, error) }),
    defaultOptions: {
      queries: {
        retry: (failures, error) =>
          failures < 2 && error instanceof ApiError && error.code === 'api_unreachable',
        refetchOnWindowFocus: false,
      },
    },
  })
  return client
}

function endSessionIfRejected(client: QueryClient, error: unknown) {
  if (error instanceof ApiError && error.code === 'unauthenticated') {
    client.setQueryData(sessionKey, null)
  }
}
