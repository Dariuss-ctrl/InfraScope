import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiError, fakeApi } from '../test/fakeApi.ts'
import { ApiError, apiGet, apiPost, describeError } from './client.ts'

afterEach(() => {
  vi.unstubAllGlobals()
})

async function failure(request: Promise<unknown>): Promise<ApiError> {
  try {
    await request
  } catch (err) {
    if (err instanceof ApiError) {
      return err
    }
    throw err
  }
  throw new Error('expected the request to fail')
}

function answer(response: () => Response | Promise<Response>) {
  vi.stubGlobal('fetch', vi.fn(async () => response()))
}

describe('apiGet', () => {
  it('sends only the parameters that have values', async () => {
    const api = fakeApi({ 'GET /api/v1/traces': { body: { traces: [] } } })
    await apiGet('/api/v1/traces', {
      params: { service: 'api', status: '', operation: undefined, cursor: null, limit: 50 },
    })
    expect(api.sent()).toEqual(['GET /api/v1/traces?service=api&limit=50'])
  })

  it('turns the error envelope into a typed error', async () => {
    fakeApi({
      'GET /api/v1/traces': {
        status: 400,
        body: {
          error: {
            code: 'invalid_argument',
            message: 'Invalid query.',
            fields: [{ field: 'from', message: 'must be before to' }],
          },
        },
      },
    })
    const err = await failure(apiGet('/api/v1/traces'))
    expect(err).toMatchObject({ status: 400, code: 'invalid_argument', retryAfter: null })
    expect(describeError(err)).toBe('from: must be before to')
  })

  it('names the store that is down', async () => {
    fakeApi({ 'GET /api/v1/logs': apiError(503, 'storage_unavailable') })
    expect(describeError(await failure(apiGet('/api/v1/logs')))).toBe(
      'Trace and log storage (ClickHouse) is unavailable.',
    )
  })

  it('reports a proxy error page as an unreachable API', async () => {
    answer(() => new Response('<h1>502 Bad Gateway</h1>', { status: 502 }))
    expect((await failure(apiGet('/api/v1/services'))).code).toBe('api_unreachable')
  })

  it('reports a network failure as an unreachable API', async () => {
    answer(() => {
      throw new TypeError('Failed to fetch')
    })
    expect((await failure(apiGet('/api/v1/services'))).code).toBe('api_unreachable')
  })

  it('names a status it does not expect', async () => {
    answer(() => new Response('short and stout', { status: 418 }))
    const err = await failure(apiGet('/api/v1/services'))
    expect(err).toMatchObject({ code: 'unexpected', message: 'The API answered 418.' })
  })

  it('leaves a cancelled request cancelled rather than calling it an outage', async () => {
    const controller = new AbortController()
    controller.abort()
    answer(() => {
      throw new DOMException('The operation was aborted.', 'AbortError')
    })
    await expect(apiGet('/api/v1/services', { signal: controller.signal })).rejects.toThrow(
      'aborted',
    )
  })
})

describe('apiPost', () => {
  it('sends JSON and accepts an empty 204', async () => {
    const api = fakeApi({ 'POST /api/v1/auth/login': { status: 204 } })
    await expect(apiPost('/api/v1/auth/login', { username: 'a', password: 'b' })).resolves.toBe(
      undefined,
    )
    expect(api.requests[0]).toMatchObject({
      body: { username: 'a', password: 'b' },
      contentType: 'application/json',
    })
  })

  it('reads how long to wait from Retry-After', async () => {
    fakeApi({ 'POST /api/v1/auth/login': apiError(429, 'rate_limited', { 'Retry-After': '12' }) })
    const err = await failure(apiPost('/api/v1/auth/login', {}))
    expect(err).toMatchObject({ code: 'rate_limited', retryAfter: 12 })
  })
})
