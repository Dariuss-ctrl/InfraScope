import { configure } from '@testing-library/react'
import { beforeEach } from 'vitest'
import { reloadTips } from '../tips.ts'

// Test files run in parallel, so a first render can take longer than Testing
// Library's default of one second to settle without anything being wrong.
configure({ asyncUtilTimeout: 5_000 })

// Page tips are off unless a test turns them on (tips.test.tsx), so they
// never cover what other tests look for.
beforeEach(() => {
  window.localStorage.setItem('infrascope.tips', JSON.stringify({ off: true, dismissed: [] }))
  reloadTips()
})
