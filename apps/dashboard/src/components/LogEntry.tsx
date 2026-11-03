import { type ReactNode, useId, useState } from 'react'
import { Link } from 'react-router'
import { type LogRecord, severityLevel } from '../api/logs.ts'
import { formatDateTime } from '../format.ts'
import { tracePath } from '../links.ts'
import { AttributeTable } from './AttributeTable.tsx'
import { ChevronIcon } from './Icons.tsx'

type Props = {
  record: LogRecord
  // Where the record came from, shown in place of the link to its trace.
  // The trace page names the span instead, since the trace is already open.
  origin?: ReactNode
}

// LogEntry is one log record: a line to scan, which expands to the full
// body and attributes. Bodies are untrusted and rendered as text only.
export function LogEntry({ record, origin }: Props) {
  const [open, setOpen] = useState(false)
  const detailsId = useId()
  const level = severityLevel(record.severity_number)
  return (
    <li className={`log log--${level ?? 'unset'}`}>
      <div className="log__line">
        <time dateTime={record.timestamp}>{formatDateTime(record.timestamp)}</time>
        <span className="log__severity">{record.severity_text || level || 'unset'}</span>
        <span className="log__service">{record.service}</span>
        <span className="log__body">{record.body}</span>
        {origin ??
          (record.trace_id ? (
            <Link
              className="log__trace"
              to={tracePath(record.trace_id, record.span_id)}
              aria-label={`Trace ${record.trace_id}`}
            >
              Trace
            </Link>
          ) : (
            <span />
          ))}
        <button
          type="button"
          className="link log__toggle"
          aria-expanded={open}
          aria-controls={detailsId}
          onClick={() => setOpen(!open)}
        >
          {open ? 'Hide' : 'Details'}
          <ChevronIcon />
        </button>
      </div>
      {open && (
        <div id={detailsId} className="log__details">
          <pre className="code">{record.body}</pre>
          <dl className="facts">
            <div>
              <dt>Severity</dt>
              <dd>
                {record.severity_text || 'unset'} ({record.severity_number})
              </dd>
            </div>
            <div>
              <dt>Trace</dt>
              <dd className="mono">{record.trace_id ?? 'none'}</dd>
            </div>
            <div>
              <dt>Span</dt>
              <dd className="mono">{record.span_id ?? 'none'}</dd>
            </div>
          </dl>
          <AttributeTable label="Log attributes" attributes={record.attributes} />
        </div>
      )}
    </li>
  )
}
