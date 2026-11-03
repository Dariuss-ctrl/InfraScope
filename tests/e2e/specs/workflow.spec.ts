import { expect, request, test } from '@playwright/test'
import { demoURL } from './support/env.ts'

// The investigation Phase 3 exists for (SPEC FR-5, AC-5.2), end to end and
// without typing an ID: the overview → the service's slowest traces → the
// waterfall → the span that took the time and its statement → the logs
// written during it → back to the same span → back to the overview.
//
// The order-history query is the slow request: while scenario S1 is on,
// orders.customer_email has no index, so the query scans the table.
test('a slow request is traced from the overview to its cause and back', async ({ page }) => {
  test.setTimeout(240_000)
  const demo = await request.newContext({ baseURL: demoURL })
  const orderHistory = async () => {
    for (const customer of [1, 8, 15]) {
      const response = await demo.get(`/orders?customer_email=customer${customer}%40example.com`)
      expect(response.ok()).toBe(true)
    }
  }

  // 1. The overview shows the service's latency. Traffic keeps flowing while
  //    waiting, because a rate needs a counter that rises between samples.
  await page.goto('/services')
  const card = page.getByRole('article', { name: 'demo-service' })
  const p95 = card.locator('dt', { hasText: /^p95$/ }).locator('xpath=following-sibling::dd[1]')
  await expect(async () => {
    await orderHistory()
    await page.reload()
    await expect(p95).toHaveText(/\d/, { timeout: 3_000 })
  }).toPass({ timeout: 120_000, intervals: [5_000] })

  // 2. Its slowest requests, then the slowest order-history request.
  await card.getByRole('link', { name: 'Slowest traces' }).click()
  await expect(page).toHaveURL(/\/traces\?service=demo-service&sort=duration/)
  await page.getByRole('table', { name: 'Traces' }).getByRole('link', { name: 'GET /orders' }).first().click()
  await expect(page).toHaveURL(/\/traces\/[0-9a-f]{32}$/)
  const tracePath = new URL(page.url()).pathname

  // 3. The waterfall says where the time went, and the statement says why:
  //    a lookup by customer_email.
  await expect(page.getByRole('heading', { level: 1, name: 'GET /orders' })).toBeVisible()
  const findings = page.getByRole('list', { name: 'Findings' })
  await expect(findings).toContainText('Most time was spent in SELECT')
  await findings.getByRole('button', { name: 'SELECT', exact: true }).click()
  const details = page.getByRole('region', { name: 'Span details' })
  await expect(details.locator('pre.code')).toContainText('WHERE customer_email = $1')

  // 4. The logs written during the trace include the slow-query warning,
  //    attributed to that same span. Logs can arrive a moment after spans.
  const warning = page
    .getByRole('list', { name: 'Trace log records' })
    .getByRole('listitem')
    .filter({ hasText: 'slow query' })
    .first()
  await expect(async () => {
    await page.reload()
    await expect(warning).toBeVisible({ timeout: 3_000 })
  }).toPass({ timeout: 60_000, intervals: [3_000] })
  await expect(warning.getByRole('button', { name: 'SELECT', exact: true })).toBeVisible()

  // 5. Out to the log explorer and back to the same span by its link.
  await page.getByRole('link', { name: 'Logs for this trace' }).click()
  await expect(page).toHaveURL(/\/logs\?trace_id=[0-9a-f]{32}&/)
  const record = page
    .getByRole('list', { name: 'Log records' })
    .getByRole('listitem')
    .filter({ hasText: 'slow query' })
    .first()
  await record.getByRole('link', { name: /^Trace / }).click()
  await expect(page).toHaveURL(new RegExp(`${tracePath}\\?span=[0-9a-f]{16}$`))
  await expect(page.getByRole('region', { name: 'Span details' }).locator('pre.code')).toContainText(
    'customer_email',
  )

  // 6. Diagnosis made; back to the overview.
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Services' }).click()
  await expect(page.getByRole('article', { name: 'demo-service' })).toBeVisible()
  await demo.dispose()
})
