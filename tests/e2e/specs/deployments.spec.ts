import { createHmac } from 'node:crypto'
import { type APIRequestContext, expect, test } from '@playwright/test'

// FR-9: a deployment reported by signed GitHub webhooks shows up in the
// Deployments view with its whole status history, including a status that
// arrived too late to apply. The comparison itself waits for its windows to
// pass, so here it is still pending; the integration tests cover the verdicts.
const secret = process.env.INFRASCOPE_GITHUB_WEBHOOK_SECRET

test.skip(!secret, 'INFRASCOPE_GITHUB_WEBHOOK_SECRET is not set, so webhooks are off')

function statusEvent(deploymentId: number, service: string, state: string, at: Date): string {
  return JSON.stringify({
    action: 'created',
    deployment_status: { id: Date.now(), state, environment: 'local', created_at: at.toISOString() },
    deployment: {
      id: deploymentId,
      sha: '0123456789abcdef0123456789abcdef01234567',
      ref: 'main',
      environment: 'local',
      description: 'e2e release',
      payload: { service },
      created_at: at.toISOString(),
      creator: { login: 'e2e' },
    },
    repository: { name: service, full_name: `e2e/${service}` },
  })
}

async function deliver(request: APIRequestContext, delivery: string, body: string) {
  const signature = createHmac('sha256', secret ?? '').update(body).digest('hex')
  const response = await request.post('/api/v1/webhooks/github', {
    headers: {
      'Content-Type': 'application/json',
      'X-GitHub-Event': 'deployment_status',
      'X-GitHub-Delivery': delivery,
      'X-Hub-Signature-256': `sha256=${signature}`,
    },
    data: body,
  })
  expect(response.status()).toBe(200)
  return (await response.json()) as { outcome: string; applied?: boolean; comparison_queued?: boolean }
}

test('a reported deployment appears with its status history', async ({ page, request }) => {
  const deploymentId = Date.now()
  const service = `e2e-${deploymentId}`
  const started = new Date(Date.now() - 60_000)
  const finished = new Date(Date.now() - 30_000)

  expect((await deliver(request, `${service}-1`, statusEvent(deploymentId, service, 'in_progress', started))).outcome).toBe(
    'recorded',
  )
  const success = statusEvent(deploymentId, service, 'success', finished)
  expect((await deliver(request, `${service}-2`, success)).comparison_queued).toBe(true)
  expect((await deliver(request, `${service}-2`, success)).outcome).toBe('duplicate')
  expect((await deliver(request, `${service}-3`, statusEvent(deploymentId, service, 'in_progress', started))).applied).toBe(
    false,
  )

  await page.goto(`/deployments?service=${service}`)
  const row = page.getByRole('table', { name: 'Deployments' }).getByRole('row').nth(1)
  await expect(row).toContainText(service)
  await expect(row).toContainText('success')
  await expect(row).toContainText('comparing…')

  await row.getByRole('link', { name: 'main', exact: true }).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(`${service} main`)
  // The verdict panel leads the page; here the comparison is still waiting for its windows.
  await expect(page.getByRole('heading', { level: 2, name: 'Comparing…' })).toBeVisible()
  await expect(page.getByText(/Comparing\. The after window closes at/)).toBeVisible()
  const history = page.getByRole('table', { name: 'Status history' }).getByRole('row')
  await expect(history).toHaveCount(4)
  await expect(history.nth(3)).toContainText('recorded only')
})

test('a delivery signed with the wrong secret is refused', async ({ request }) => {
  const body = statusEvent(Date.now(), 'e2e-forged', 'success', new Date())
  const response = await request.post('/api/v1/webhooks/github', {
    headers: {
      'Content-Type': 'application/json',
      'X-GitHub-Event': 'deployment_status',
      'X-GitHub-Delivery': `forged-${Date.now()}`,
      'X-Hub-Signature-256': `sha256=${createHmac('sha256', 'not-the-secret').update(body).digest('hex')}`,
    },
    data: body,
  })
  expect(response.status()).toBe(401)
  expect(((await response.json()) as { error: { code: string } }).error.code).toBe('invalid_signature')
})
