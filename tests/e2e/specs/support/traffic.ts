import { request } from '@playwright/test'
import { demoURL } from './env.ts'

// browse sends read-only requests to the demo service, so there is fresh
// telemetry to look at. A rate needs a counter that rises between two
// samples, so journeys call it repeatedly while they wait, never just once.
export async function browse(count: number): Promise<void> {
  const demo = await request.newContext({ baseURL: demoURL })
  try {
    for (let i = 0; i < count; i++) {
      const path = `/products/${(i % 50) + 1}`
      const res = await demo.get(path)
      if (!res.ok()) {
        throw new Error(`GET ${path} = ${res.status()}`)
      }
    }
  } finally {
    await demo.dispose()
  }
}
