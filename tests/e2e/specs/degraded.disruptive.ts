import { expect, test } from '@playwright/test'
import { clickhouse, compose, disruptive } from './support/stack.ts'

// NFR-1 and the schema diagnostics, against the real stack. These drop an
// index and stop ClickHouse, so they run only against a disposable stack.
test.skip(!disruptive, 'alters the stack: set E2E_DISRUPTIVE=1 and E2E_COMPOSE for a disposable one')
test.describe.configure({ mode: 'serial' })

test('a missing ClickHouse index is named with its effect, without reading server logs', async ({ page }) => {
  clickhouse('ALTER TABLE otel.otel_traces DROP INDEX idx_duration')
  try {
    const missing = page.getByRole('table', { name: 'Missing from the schema' })
    await expect(async () => {
      await page.goto('/status')
      await expect(missing).toContainText('index idx_duration on otel.otel_traces', { timeout: 3_000 })
    }).toPass({ timeout: 45_000, intervals: [3_000] })
    await expect(missing).toContainText('Trace searches by duration are slower.')
    await expect(page.getByRole('alert').filter({ hasText: 'missing tables or indexes' })).toBeVisible()
  } finally {
    // The fix from the runbook's "ClickHouse schema incomplete", so this
    // test also proves the documented commands work.
    clickhouse('ALTER TABLE otel.otel_traces ADD INDEX idx_duration Duration TYPE minmax GRANULARITY 1')
    clickhouse('ALTER TABLE otel.otel_traces MATERIALIZE INDEX idx_duration')
  }
  await expect(async () => {
    await page.goto('/status')
    await expect(page.getByText('Every table and index the searches rely on is present.')).toBeVisible({
      timeout: 3_000,
    })
  }).toPass({ timeout: 45_000, intervals: [3_000] })
})

test('a ClickHouse outage is shown on every page, and the overview keeps working', async ({ page }) => {
  compose('stop clickhouse')
  try {
    await page.goto('/traces')
    await expect(page.getByRole('alert').filter({ hasText: 'Trace and log storage (ClickHouse) is down.' })).toBeVisible({
      timeout: 30_000,
    })
    await expect(
      page.getByRole('alert').filter({ hasText: 'Trace and log storage (ClickHouse) is unavailable.' }),
    ).toBeVisible()

    // Metrics come from Prometheus, so the overview still works.
    await page.goto('/services')
    await expect(page.getByRole('article', { name: 'demo-service' })).toBeVisible()
  } finally {
    compose('up -d --wait --no-deps clickhouse')
  }
})
