import { useSyncExternalStore } from 'react'

// Page tips: a short card that explains the page the first time someone
// opens it. Each can be dismissed on its own, or all of them skipped at
// once, and the header's "?" brings the current page's back. The choice is
// a convenience, kept in this browser only; when storage is unavailable
// (a private window, blocked site data) tips simply show and stay dismissed
// until the page is reloaded.

export type TipKey = 'services' | 'traces' | 'trace' | 'logs' | 'deployments' | 'deployment' | 'status'

type State = { off: boolean; dismissed: TipKey[] }

const storageKey = 'infrascope.tips'
const initial: State = { off: false, dismissed: [] }

function load(): State {
  try {
    const raw = window.localStorage.getItem(storageKey)
    if (!raw) {
      return initial
    }
    const parsed = JSON.parse(raw) as Partial<State>
    return {
      off: parsed.off === true,
      dismissed: Array.isArray(parsed.dismissed) ? parsed.dismissed.filter((k): k is TipKey => k in tips) : [],
    }
  } catch {
    return initial
  }
}

let state: State = load()
const listeners = new Set<() => void>()

function update(next: State) {
  state = next
  try {
    window.localStorage.setItem(storageKey, JSON.stringify(next))
  } catch {
    // Kept in memory for this page load only.
  }
  for (const listener of listeners) {
    listener()
  }
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

// useTip says whether a page's tip is showing.
export function useTip(key: TipKey | null): boolean {
  const current = useSyncExternalStore(subscribe, () => state)
  return key !== null && !current.off && !current.dismissed.includes(key)
}

export function dismissTip(key: TipKey) {
  if (!state.dismissed.includes(key)) {
    update({ ...state, dismissed: [...state.dismissed, key] })
  }
}

export function skipAllTips() {
  update({ ...state, off: true })
}

// showTip brings one page's tip back, and turns tips on again.
export function showTip(key: TipKey) {
  update({ off: false, dismissed: state.dismissed.filter((k) => k !== key) })
}

// reloadTips re-reads storage; tests use it to start from a known state.
export function reloadTips() {
  state = load()
  for (const listener of listeners) {
    listener()
  }
}

// tipFor maps a path to the page it shows.
export function tipFor(pathname: string): TipKey | null {
  const [first, second] = pathname.split('/').filter(Boolean)
  switch (first) {
    case 'services':
    case 'logs':
    case 'status':
      return first
    case 'traces':
      return second ? 'trace' : 'traces'
    case 'deployments':
      return second ? 'deployment' : 'deployments'
  }
  return null
}

export type Tip = { title: string; points: string[] }

// What each page is for, in two or three short points.
export const tips: Record<TipKey, Tip> = {
  services: {
    title: 'Services overview',
    points: [
      'Each card is one service: its request rate, error rate and latency for the time range at the top right.',
      'Click any point on a chart to open the requests behind that moment.',
      'Dashed lines mark deployments. A red one is a deployment DeployRadar found made things worse.',
    ],
  },
  traces: {
    title: 'Trace search',
    points: [
      'A trace is one request, followed through every service and query it touched.',
      'Filter by service, operation, duration and status, and sort slowest first to find what is slow.',
      'Open a trace to see where its time went.',
    ],
  },
  trace: {
    title: 'One request, step by step',
    points: [
      'Each bar is a span: one step of the request, nested under the step that called it.',
      'The findings above the waterfall name where most of the time went, and the first failure.',
      'Select a span for its details. The logs written during this request are listed below.',
    ],
  },
  logs: {
    title: 'Log search',
    points: [
      'Search logs by service, minimum severity and text, within the time range.',
      'A record written during a request links to the span that wrote it.',
    ],
  },
  deployments: {
    title: 'Deployments',
    points: [
      'Every deployment GitHub reports. After each success, DeployRadar compares error rate and p95 latency before and after it.',
      'A red edge marks a regression. Open a deployment to see why.',
    ],
  },
  deployment: {
    title: 'One deployment',
    points: [
      'The verdict comes first: what changed, how sure it is, and the route that changed most.',
      'Every number can be checked: "How these numbers were measured" holds the Prometheus query behind it.',
      '"See failing traces" opens the failed requests from after the deployment.',
    ],
  },
  status: {
    title: 'Platform status',
    points: [
      "Each of InfraScope's dependencies, and what stops working while it is down.",
      'The badge in the header always shows this summary.',
    ],
  },
}
