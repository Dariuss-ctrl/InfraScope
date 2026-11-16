import {
  type ComparisonStatus,
  type DeploymentState,
  type Verdict,
  formatConfidence,
  verdictLabels,
} from '../api/deployments.ts'

// Red, amber and green only ever mean status: a regression is red, an
// improvement green, too little traffic amber, no change neutral.
const verdictTones: Record<Verdict, string> = {
  regression: 'badge--error',
  improvement: 'badge--ok',
  insufficient_data: 'badge--warn',
  no_change: '',
}

export function VerdictBadge({ verdict, confidence }: { verdict: Verdict; confidence?: number | null }) {
  const title = confidence == null ? verdict : `${verdict}, confidence ${formatConfidence(confidence)}`
  return (
    <span className={`badge ${verdictTones[verdict]}`} title={title}>
      {verdictLabels[verdict]}
    </span>
  )
}

const stateTones: Partial<Record<DeploymentState, string>> = {
  success: 'badge--ok',
  failure: 'badge--error',
  error: 'badge--error',
}

export function StateBadge({ state }: { state: DeploymentState }) {
  return <span className={`badge ${stateTones[state] ?? ''}`}>{state.replace('_', ' ')}</span>
}

// HealthBadge is a list row's comparison: its verdict once done, otherwise
// where it stands.
export function HealthBadge({
  status,
  verdict,
  confidence,
}: {
  status: ComparisonStatus | null
  verdict: Verdict | null
  confidence: number | null
}) {
  if (verdict !== null) {
    return <VerdictBadge verdict={verdict} confidence={confidence} />
  }
  switch (status) {
    case 'pending':
      return <span className="badge">comparing…</span>
    case 'failed':
      return <span className="badge badge--warn">comparison failed</span>
    default:
      return <span className="muted">not compared</span>
  }
}
