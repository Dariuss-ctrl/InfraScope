import type { View } from '../views.ts'

// Placeholder for a view that a later phase builds. It states what is coming
// and where it is specified, rather than pretending to work.
export function PlannedPage({ title, summary, requirement, phase }: View) {
  return (
    <section className="page">
      <h1>{title}</h1>
      <p className="page__summary">{summary}</p>
      <p className="page__planned">
        Planned for Phase {phase}. Specified as {requirement} in <code>docs/SPEC.md</code>.
      </p>
    </section>
  )
}
