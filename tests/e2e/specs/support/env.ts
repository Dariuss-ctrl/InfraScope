import path from 'node:path'

// Everything the journeys need to know about the stack under test comes
// from the same variables as .env, so `set -a; . ./.env; set +a` is enough.
export const adminUser = process.env.INFRASCOPE_ADMIN_USER ?? 'admin'

export function adminPassword(): string {
  const password = process.env.INFRASCOPE_ADMIN_PASSWORD
  if (!password) {
    throw new Error('INFRASCOPE_ADMIN_PASSWORD is not set; export the values from .env first')
  }
  return password
}

export const demoURL =
  process.env.DEMO_URL ?? `http://localhost:${process.env.DEMO_SERVICE_PORT ?? '8082'}`

// The setup project's session cookie. It is a credential, so it is git-ignored.
export const sessionFile = path.join(import.meta.dirname, '..', '..', '.auth', 'admin.json')
