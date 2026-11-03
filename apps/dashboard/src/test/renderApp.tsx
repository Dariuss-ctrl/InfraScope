import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { AppRoutes } from '../App.tsx'
import { createQueryClient } from '../queryClient.ts'
import { LocationProbe } from './LocationProbe.tsx'

// renderApp mounts the whole dashboard at path, with the production query
// rules (including the 401 handling) but without retry delays.
export function renderApp(path: string) {
  const client = createQueryClient()
  client.setDefaultOptions({ queries: { retry: false } })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <AppRoutes />
        <LocationProbe />
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return { client, location: () => screen.getByTestId('location').textContent }
}
