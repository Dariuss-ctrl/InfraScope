import { describeError } from '../api/client.ts'
import { AlertIcon } from './Icons.tsx'

// ErrorState says why a view, or part of one, has nothing to show.
export function ErrorState({ error }: { error: unknown }) {
  return (
    <p className="state state--error" role="alert">
      <AlertIcon />
      {describeError(error)}
    </p>
  )
}
