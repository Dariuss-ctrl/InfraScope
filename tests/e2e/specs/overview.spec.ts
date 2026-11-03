import { expect, test } from '@playwright/test'
import { browse } from './support/traffic.ts'

// FR-2: the overview shows real request rates and latency for a service, and
// a click on its latency chart opens the slowest traces of that moment.
test('the overview shows live service metrics and leads to the traces behind a chart', async ({
  page,
}) => {
  await page.goto('/services')
  const card = page.getByRole('article', { name: 'demo-service' })

  await expect(async () => {
    await browse(20)
    await page.reload()
    const rate = card.getByRole('definition').first()
    await expect(rate).toHaveText(/^[\d.,]+ req\/s$/, { timeout: 5_000 })
    await expect(rate).not.toHaveText('0 req/s', { timeout: 1_000 })
  }).toPass({ timeout: 90_000, intervals: [5_000] })

  const latency = card.getByRole('img', { name: 'Latency for demo-service' })
  await expect(latency.locator('svg path').first()).toBeAttached()
  await page.screenshot({ path: test.info().outputPath('overview.png'), fullPage: true })

  // The newest data is at the right-hand end of the time axis.
  const box = await latency.boundingBox()
  expect(box).not.toBeNull()
  await latency.click({ position: { x: (box?.width ?? 0) - 24, y: (box?.height ?? 0) / 2 } })
  await expect(page).toHaveURL(/\/traces\?service=demo-service&sort=duration&from=[^&]+&to=[^&]+$/)
})
