import { expect, test as setup } from '@playwright/test'
import { adminPassword, adminUser, sessionFile } from './support/env.ts'

// Logs in once and saves the session, so the journeys start logged in and
// the run stays well inside the login rate limit.
setup('log in', async ({ page }) => {
  await page.goto('/login')
  await page.getByLabel('Username').fill(adminUser)
  await page.getByLabel('Password').fill(adminPassword())
  await page.getByRole('button', { name: 'Log in' }).click()
  await expect(page).toHaveURL(/\/services$/)
  // A first visit shows a tip on every page; the journeys act like someone
  // who skipped them, so no tip covers what they click.
  await page.getByRole('button', { name: 'Skip all tips' }).click()
  await page.context().storageState({ path: sessionFile })
})
