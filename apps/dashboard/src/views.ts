// The investigation views, shared by the navigation and the routes.
export type View = {
  path: string
  title: string
  summary: string
  requirement: string
  phase: number
}

export const views: readonly View[] = [
  {
    path: 'services',
    title: 'Services',
    summary:
      'Request rate, error rate and latency percentiles for every service, with a click-through from any spike to the traces behind it.',
    requirement: 'FR-2',
    phase: 3,
  },
  {
    path: 'traces',
    title: 'Traces',
    summary:
      'Search requests by service, operation, duration and status, then inspect one as a span waterfall.',
    requirement: 'FR-3',
    phase: 3,
  },
  {
    path: 'logs',
    title: 'Logs',
    summary:
      'Search structured logs by time, severity, service, text and trace ID, with links back to the trace that wrote them.',
    requirement: 'FR-4',
    phase: 3,
  },
  {
    path: 'alerts',
    title: 'Alerts',
    summary:
      'Threshold rules on error rate, latency and throughput, with the history of every state change.',
    requirement: 'FR-6',
    phase: 4,
  },
]
