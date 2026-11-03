import { configure } from '@testing-library/react'

// Test files run in parallel, so a first render can take longer than Testing
// Library's default of one second to settle without anything being wrong.
configure({ asyncUtilTimeout: 5_000 })
