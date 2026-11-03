import { describe, expect, it } from 'vitest'
import { loginPath, safeNext } from './session.ts'

describe('safeNext', () => {
  it.each(['/traces?service=api#span', '/logs', '/traces/4bf92f3577b34da6a3ce929d0e0e4736'])(
    'keeps the in-app path %s',
    (raw) => expect(safeNext(raw)).toBe(raw),
  )

  it.each([
    null,
    '',
    'traces',
    ' /logs',
    '//evil.example/phish',
    '/\\evil.example',
    'https://evil.example/',
    'javascript:alert(1)',
    '/login',
    '/login?next=%2Flogs',
  ])('replaces %j with the services view', (raw) => expect(safeNext(raw)).toBe('/services'))
})

describe('loginPath', () => {
  it('remembers the page with its filters and anchor', () => {
    expect(loginPath({ pathname: '/traces', search: '?service=api', hash: '#span' })).toBe(
      '/login?next=%2Ftraces%3Fservice%3Dapi%23span',
    )
  })

  it('needs nothing to remember for the home page', () => {
    expect(loginPath({ pathname: '/', search: '', hash: '' })).toBe('/login')
  })
})
