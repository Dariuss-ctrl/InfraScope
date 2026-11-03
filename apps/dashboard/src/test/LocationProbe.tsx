import { useLocation } from 'react-router'

// Shows the router's current path and query, so tests can assert redirects.
export function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}
