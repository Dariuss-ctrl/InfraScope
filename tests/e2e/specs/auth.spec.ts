import { expect, test } from '@playwright/test'
import { adminPassword, adminUser } from './support/env.ts'

// This journey is about logging in, so it starts without the saved session.
test.use({ storageState: { cookies: [], origins: [] } })

// NFR-3: every investigation page needs a session, and a session is ended on
// the server, not just forgotten by the page. This journey makes two login
// attempts, well inside the login rate limit.
test('protected pages need a session that logging out really ends', async ({ page }) => {
  await page.goto('/traces?service=demo-service')
  await expect(page).toHaveURL(/\/login\?next=%2Ftraces%3Fservice%3Ddemo-service$/)
  await expect(page.getByRole('status')).toBeVisible() // platform status, even before login

  await page.getByLabel('Username').fill(adminUser)
  await page.getByLabel('Password').fill('definitely not the password')
  await page.getByRole('button', { name: 'Log in' }).click()
  await expect(page.getByRole('alert')).toHaveText('Invalid username or password.')

  await page.getByLabel('Password').fill(adminPassword())
  await page.getByRole('button', { name: 'Log in' }).click()
  await expect(page).toHaveURL(/\/traces\?service=demo-service$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Traces' })).toBeVisible()
  await expect(page.getByText(adminUser, { exact: true })).toBeVisible()

  // The session lives in an HttpOnly cookie, so it survives a reload.
  await page.reload()
  await expect(page.getByRole('heading', { level: 1, name: 'Traces' })).toBeVisible()

  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect((await page.request.get('/api/v1/auth/me')).status()).toBe(401)

  await page.goto('/services')
  await expect(page).toHaveURL(/\/login\?next=%2Fservices$/)
})
