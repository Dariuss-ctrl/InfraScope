import { type FormEvent, useState } from 'react'
import { formatDateTime } from '../format.ts'
import { type TimeRange, presets, resolveRange } from '../timeRange.ts'
import { ClockIcon } from './Icons.tsx'

type Props = { value: TimeRange; onChange: (next: TimeRange) => void }

export function TimeRangePicker({ value, onChange }: Props) {
  // The custom form edits local times; null while it is closed.
  const [custom, setCustom] = useState<{ from: string; to: string } | null>(null)
  const [invalid, setInvalid] = useState(false)

  function openCustom() {
    const { from, to } = resolveRange(value)
    setCustom({ from: localInput(from), to: localInput(to) })
    setInvalid(false)
  }

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!custom) {
      return
    }
    const from = new Date(custom.from)
    const to = new Date(custom.to)
    if (!(from < to)) {
      setInvalid(true)
      return
    }
    setCustom(null)
    onChange({ from, to })
  }

  return (
    <div className="range-picker">
      <div className="segmented" role="group" aria-label="Time range">
        {presets.map((p) => (
          <button
            key={p.id}
            type="button"
            aria-pressed={'preset' in value && value.preset === p.id}
            onClick={() => {
              setCustom(null)
              onChange({ preset: p.id })
            }}
          >
            {p.id}
          </button>
        ))}
        <button
          type="button"
          aria-pressed={'from' in value}
          aria-expanded={custom !== null}
          onClick={() => (custom ? setCustom(null) : openCustom())}
        >
          Custom
        </button>
      </div>
      {'from' in value && custom === null && (
        <span className="range-picker__fixed">
          <ClockIcon />
          {formatDateTime(value.from)} – {formatDateTime(value.to)}
        </span>
      )}
      {custom && (
        <form className="range-picker__custom" aria-label="Custom time range" onSubmit={apply}>
          <label>
            From
            <input
              type="datetime-local"
              step="1"
              required
              value={custom.from}
              onChange={(e) => setCustom({ ...custom, from: e.target.value })}
            />
          </label>
          <label>
            To
            <input
              type="datetime-local"
              step="1"
              required
              value={custom.to}
              onChange={(e) => setCustom({ ...custom, to: e.target.value })}
            />
          </label>
          <button type="submit" className="button--primary">
            Apply
          </button>
          {invalid && (
            <span className="form-error" role="alert">
              The start must be before the end.
            </span>
          )}
        </form>
      )}
    </div>
  )
}

// localInput formats a moment for a datetime-local input, in local time.
function localInput(t: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())}` +
    `T${pad(t.getHours())}:${pad(t.getMinutes())}:${pad(t.getSeconds())}`
  )
}
