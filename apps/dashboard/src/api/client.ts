// Client for the InfraScope API (apps/api/openapi.yaml). Every failure
// becomes an ApiError carrying the envelope's code, so a view can tell
// "storage unavailable" from "invalid filter" without parsing messages.

// The API's error codes, plus two for answers that never came from it: no
// response at all, and a response that is not an error envelope.
export type ErrorCode =
  | 'invalid_argument'
  | 'unauthenticated'
  | 'forbidden'
  | 'not_found'
  | 'rate_limited'
  | 'not_implemented'
  | 'storage_unavailable'
  | 'metrics_unavailable'
  | 'database_unavailable'
  | 'query_timeout'
  | 'internal'
  | 'api_unreachable'
  | 'unexpected'

export type FieldError = { field: string; message: string }

export class ApiError extends Error {
  readonly status: number
  readonly code: ErrorCode
  readonly fields: readonly FieldError[]
  // Seconds to wait before trying again, from Retry-After (429 responses).
  readonly retryAfter: number | null

  constructor(
    status: number,
    code: ErrorCode,
    message: string,
    fields: readonly FieldError[] = [],
    retryAfter: number | null = null,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = fields
    this.retryAfter = retryAfter
  }
}

// Query parameters. Empty values are left out, because the API rejects
// parameters it cannot use rather than ignoring them.
export type Params = Record<string, string | number | null | undefined>

export function apiGet<T>(
  path: string,
  options: { params?: Params; signal?: AbortSignal } = {},
): Promise<T> {
  return request<T>('GET', withParams(path, options.params), undefined, options.signal)
}

export function apiPost<T = void>(path: string, body?: unknown): Promise<T> {
  return request<T>('POST', path, body)
}

async function request<T>(
  method: string,
  path: string,
  body: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
      credentials: 'same-origin',
    })
  } catch (err) {
    if (signal?.aborted) {
      throw err // a cancelled request is not an outage
    }
    throw new ApiError(0, 'api_unreachable', 'The API is unreachable.')
  }
  if (res.ok) {
    return (res.status === 204 ? undefined : await res.json()) as T
  }
  throw await toApiError(res)
}

type Envelope = { error?: { code?: string; message?: string; fields?: FieldError[] } }

async function toApiError(res: Response): Promise<ApiError> {
  const envelope = (await res.json().catch(() => null)) as Envelope | null
  const error = envelope?.error
  if (error?.code && error.message) {
    return new ApiError(
      res.status,
      error.code as ErrorCode,
      error.message,
      error.fields ?? [],
      retryAfter(res),
    )
  }
  // No envelope means something in front of the API answered: usually
  // nginx with a 502 while the API restarts.
  if (res.status === 502 || res.status === 503 || res.status === 504) {
    return new ApiError(res.status, 'api_unreachable', 'The API is unreachable.')
  }
  return new ApiError(res.status, 'unexpected', `The API answered ${res.status}.`)
}

function retryAfter(res: Response): number | null {
  const raw = res.headers.get('Retry-After')
  if (raw === null) {
    return null
  }
  const seconds = Number(raw)
  return Number.isInteger(seconds) && seconds >= 0 ? seconds : null
}

function withParams(path: string, params?: Params): string {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(params ?? {})) {
    if (value !== undefined && value !== null && value !== '') {
      query.set(key, String(value))
    }
  }
  const encoded = query.toString()
  return encoded ? `${path}?${encoded}` : path
}

// describeError turns any failure into one sentence a person can act on.
export function describeError(err: unknown): string {
  if (!(err instanceof ApiError)) {
    return 'Something went wrong in the dashboard.'
  }
  switch (err.code) {
    case 'storage_unavailable':
      return 'Trace and log storage (ClickHouse) is unavailable.'
    case 'metrics_unavailable':
      return 'The metrics store (Prometheus) is unavailable.'
    case 'database_unavailable':
      return 'The configuration database (PostgreSQL) is unavailable.'
    case 'query_timeout':
      return 'The query ran out of time. Narrow the time range or add filters.'
    case 'api_unreachable':
      return 'The API is unreachable.'
    case 'invalid_argument':
      return err.fields.length > 0
        ? err.fields.map((f) => `${f.field}: ${f.message}`).join('; ')
        : err.message
    default:
      return err.message
  }
}
