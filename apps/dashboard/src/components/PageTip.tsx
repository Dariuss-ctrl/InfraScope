import { useEffect, useId } from 'react'
import { type TipKey, dismissTip, skipAllTips, tips, useTip } from '../tips.ts'
import { InfoIcon } from './Icons.tsx'

// PageTip is the explanation of the current page, shown the first time it is
// opened. It sits in a corner and takes no focus, so someone who knows the
// page can carry on and ignore it; Escape, "Got it" or "Skip all tips" puts
// it away.
export function PageTip({ tip: key }: { tip: TipKey | null }) {
  const showing = useTip(key)
  const titleId = useId()

  useEffect(() => {
    if (!showing || key === null) {
      return
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        dismissTip(key)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showing, key])

  if (!showing || key === null) {
    return null
  }
  const tip = tips[key]
  return (
    <aside className="page-tip" role="dialog" aria-modal="false" aria-labelledby={titleId}>
      <h2 id={titleId} className="page-tip__title">
        <InfoIcon />
        {tip.title}
      </h2>
      <ul className="page-tip__points">
        {tip.points.map((point) => (
          <li key={point}>{point}</li>
        ))}
      </ul>
      <div className="page-tip__actions">
        <button type="button" className="button--primary" onClick={() => dismissTip(key)}>
          Got it
        </button>
        <button type="button" className="button--ghost" onClick={skipAllTips}>
          Skip all tips
        </button>
      </div>
    </aside>
  )
}
