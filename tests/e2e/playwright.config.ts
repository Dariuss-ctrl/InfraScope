import { defineConfig, devices } from '@playwright/test'
import { sessionFile } from './specs/support/env.ts'

// The journeys run against a stack that is already up (make up), through the
// dashboard's published port, exactly as a person would use it. Set
// E2E_BROWSER_CHANNEL=msedge (or chrome) to drive an installed browser instead
// of Playwright's own Chromium.
const browser = {
  ...devices['Desktop Chrome'],
  channel: process.env.E2E_BROWSER_CHANNEL || undefined,
}

export default defineConfig({
  testDir: './specs',
  // One admin account and one login rate limit are shared by every journey.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: process.env.BASE_URL ?? `http://localhost:${process.env.DASHBOARD_PORT ?? '8080'}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'setup', testMatch: /\.setup\.ts$/, use: browser },
    {
      name: 'journeys',
      testMatch: /\.spec\.ts$/,
      dependencies: ['setup'],
      use: { ...browser, storageState: sessionFile },
    },
    // Last, because they stop containers: see specs/support/stack.ts.
    {
      name: 'disruptive',
      testMatch: /\.disruptive\.ts$/,
      dependencies: ['journeys'],
      use: { ...browser, storageState: sessionFile },
    },
  ],
})
