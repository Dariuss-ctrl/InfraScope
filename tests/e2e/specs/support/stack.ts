import { execFileSync } from 'node:child_process'
import path from 'node:path'

// Disruptive journeys stop containers and alter the schema of the stack
// under test, so they run only when E2E_DISRUPTIVE=1 says that stack is
// disposable, as CI's is. E2E_COMPOSE is its docker compose command, run
// from the repository root.
export const disruptive = process.env.E2E_DISRUPTIVE === '1' && Boolean(process.env.E2E_COMPOSE)

const root = path.join(import.meta.dirname, '..', '..', '..', '..')

export function compose(args: string): string {
  return execFileSync('bash', ['-c', `${process.env.E2E_COMPOSE} ${args}`], {
    cwd: root,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  })
}

// clickhouse runs one statement as the stack's own ClickHouse user, the way
// the runbook does.
export function clickhouse(statement: string): string {
  return compose(
    `exec -T clickhouse sh -c 'clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" -q "${statement}"'`,
  )
}
