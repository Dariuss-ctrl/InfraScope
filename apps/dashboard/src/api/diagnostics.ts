// Schema diagnostics (apps/api/openapi.yaml, tag "diagnostics"): the detail
// behind a failed clickhouse_schema check, which the public status report
// does not name.
import { apiGet } from './client.ts'

export type SchemaItem = {
  kind: 'table' | 'index'
  table: string // with its database, such as otel.otel_logs
  name: string // the index; for a table, the table itself
  effect: string // what goes wrong while it is missing
}

export type SchemaReport = { checked_at: string; complete: boolean; missing: SchemaItem[] }

export function fetchSchemaReport(signal?: AbortSignal): Promise<SchemaReport> {
  return apiGet<SchemaReport>('/api/v1/diagnostics/schema', { signal })
}
