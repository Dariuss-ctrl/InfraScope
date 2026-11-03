import { expect, request, test } from '@playwright/test'
import { demoURL } from './support/env.ts'

// FR-3: a request found through the trace explorer opens as a waterfall, and
// the waterfall points at the span that took the time. The order-history
// query reads orders by email, which has no index while scenario S1 is on.
test('the trace explorer finds a request and its waterfall points at the slow query', async ({
  page,
}) => {
  const demo = await request.newContext({ baseURL: demoURL })
  const sent = new Date()
  const response = await demo.get('/orders?customer_email=customer42%40example.com')
  expect(response.ok()).toBe(true)
  const traceId = response.headers()['x-trace-id']
  await demo.dispose()

  // A window of a minute either side keeps the search to this request.
  const window = new URLSearchParams({
    service: 'demo-service',
    operation: 'GET /orders',
    sort: 'duration',
    from: new Date(sent.getTime() - 60_000).toISOString(),
    to: new Date(sent.getTime() + 60_000).toISOString(),
  })
  const result = page.locator(`a[href="/traces/${traceId}"]`)
  await expect(async () => {
    await page.goto(`/traces?${window}`)
    await expect(result).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 60_000, intervals: [2_000] })

  await result.click()
  await expect(page).toHaveURL(new RegExp(`/traces/${traceId}$`))
  await expect(page.getByRole('heading', { level: 1, name: 'GET /orders' })).toBeVisible()

  const findings = page.getByRole('list', { name: 'Findings' })
  await expect(findings).toContainText('Most time was spent in')
  await findings.getByRole('button').first().click()
  const details = page.getByRole('region', { name: 'Span details' })
  await expect(details.locator('pre.code')).toContainText('customer_email = $1')
  await page.screenshot({ path: test.info().outputPath('trace.png'), fullPage: true })

  await page.getByRole('link', { name: 'Logs for this trace' }).click()
  await expect(page).toHaveURL(new RegExp(`/logs\\?trace_id=${traceId}&from=[^&]+&to=[^&]+$`))
})
