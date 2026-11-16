import { cleanup, fireEvent, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeApi, loggedIn } from './test/fakeApi.ts'
import { renderApp } from './test/renderApp.tsx'
import { reloadTips, tipFor } from './tips.ts'

// A fresh browser: nothing dismissed, tips on.
beforeEach(() => {
  window.localStorage.removeItem('infrascope.tips')
  reloadTips()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const traces = { 'GET /api/v1/traces': { body: { traces: [], next_cursor: null } } }

function stored(): { off: boolean; dismissed: string[] } {
  return JSON.parse(window.localStorage.getItem('infrascope.tips') ?? '{}')
}

describe('page tips', () => {
  it('explains a page the first time it is opened, without taking focus', async () => {
    fakeApi(loggedIn)
    renderApp('/services')
    const tip = await screen.findByRole('dialog', { name: 'Services overview' })
    expect(tip.textContent).toContain('Click any point on a chart')
    expect(tip.getAttribute('aria-modal')).toBe('false')
    expect(tip.contains(document.activeElement)).toBe(false)
  })

  it('remembers a dismissed tip, and shows the next page its own', async () => {
    fakeApi({ ...loggedIn, ...traces })
    renderApp('/services')
    fireEvent.click(await screen.findByRole('button', { name: 'Got it' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(stored().dismissed).toEqual(['services'])

    fireEvent.click(screen.getByRole('link', { name: 'Traces' }))
    expect(await screen.findByRole('dialog', { name: 'Trace search' })).toBeTruthy()
    fireEvent.click(screen.getByRole('link', { name: 'Services' }))
    await screen.findByRole('heading', { level: 1, name: 'Services' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('skips every tip at once, for someone who knows their way around', async () => {
    fakeApi({ ...loggedIn, ...traces })
    renderApp('/services')
    fireEvent.click(await screen.findByRole('button', { name: 'Skip all tips' }))
    expect(stored().off).toBe(true)
    fireEvent.click(screen.getByRole('link', { name: 'Traces' }))
    await screen.findByRole('heading', { level: 1, name: 'Traces' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('brings the current page’s tip back from the header', async () => {
    fakeApi(loggedIn)
    renderApp('/services')
    fireEvent.click(await screen.findByRole('button', { name: 'Skip all tips' }))
    fireEvent.click(screen.getByRole('button', { name: 'Explain this page' }))
    expect(await screen.findByRole('dialog', { name: 'Services overview' })).toBeTruthy()
    expect(stored().off).toBe(false)
  })

  it('closes with Escape', async () => {
    fakeApi(loggedIn)
    renderApp('/services')
    await screen.findByRole('dialog', { name: 'Services overview' })
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('keeps working when the browser refuses storage', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    fakeApi(loggedIn)
    renderApp('/services')
    fireEvent.click(await screen.findByRole('button', { name: 'Got it' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    vi.restoreAllMocks()
  })

  it('knows which page a path shows', () => {
    expect(tipFor('/services')).toBe('services')
    expect(tipFor('/traces')).toBe('traces')
    expect(tipFor('/traces/0123')).toBe('trace')
    expect(tipFor('/deployments')).toBe('deployments')
    expect(tipFor('/deployments/7')).toBe('deployment')
    expect(tipFor('/logs')).toBe('logs')
    expect(tipFor('/status')).toBe('status')
    expect(tipFor('/alerts')).toBeNull()
    expect(tipFor('/')).toBeNull()
  })
})
