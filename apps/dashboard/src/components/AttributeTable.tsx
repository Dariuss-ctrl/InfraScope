type Props = {
  label: string
  attributes?: Record<string, string>
  shown?: string[] // keys already displayed elsewhere on the page
}

// AttributeTable lists telemetry attributes by key. Telemetry is untrusted
// input, so values are only ever rendered as text.
export function AttributeTable({ label, attributes = {}, shown = [] }: Props) {
  const entries = Object.entries(attributes)
    .filter(([key]) => !shown.includes(key))
    .sort(([a], [b]) => a.localeCompare(b))
  if (entries.length === 0) {
    return <p className="state">None</p>
  }
  return (
    <table className="attributes" aria-label={label}>
      <tbody>
        {entries.map(([key, value]) => (
          <tr key={key}>
            <th scope="row">{key}</th>
            <td>{value}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
