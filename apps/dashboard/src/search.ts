import { useInfiniteQuery } from '@tanstack/react-query'
import { type TimeRange, rangeKey, resolveRange } from './timeRange.ts'

type Bounds = { from: Date; to: Date }

// useBoundedSearch pages through a search with "Load more". Every page uses
// the bounds the first page resolved, so a relative range such as "last
// hour" cannot move under its own cursor.
export function useBoundedSearch<Page extends { next_cursor: string | null }>(
  key: readonly unknown[],
  range: TimeRange,
  fetchPage: (bounds: Bounds, cursor: string | null, signal: AbortSignal) => Promise<Page>,
) {
  return useInfiniteQuery({
    queryKey: [...key, rangeKey(range)],
    initialPageParam: null as (Bounds & { cursor: string }) | null,
    queryFn: async ({ pageParam, signal }) => {
      const bounds = pageParam ?? resolveRange(range)
      const page = await fetchPage(bounds, pageParam?.cursor ?? null, signal)
      return { ...page, from: bounds.from, to: bounds.to }
    },
    getNextPageParam: (last) =>
      last.next_cursor ? { cursor: last.next_cursor, from: last.from, to: last.to } : undefined,
  })
}

// enteredValues reads a filter form: every field that has a value.
export function enteredValues(form: HTMLFormElement): URLSearchParams {
  const values = new URLSearchParams()
  for (const [name, value] of new FormData(form)) {
    if (typeof value === 'string' && value.trim() !== '') {
      values.set(name, value)
    }
  }
  return values
}
