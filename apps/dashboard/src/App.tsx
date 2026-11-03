import type { ReactElement } from 'react'
import { Navigate, Route, Routes } from 'react-router'
import { Layout } from './components/Layout.tsx'
import { RequireSession } from './components/RequireSession.tsx'
import { LoginPage } from './pages/LoginPage.tsx'
import { LogsPage } from './pages/LogsPage.tsx'
import { NotFoundPage } from './pages/NotFoundPage.tsx'
import { PlannedPage } from './pages/PlannedPage.tsx'
import { ServicesPage } from './pages/ServicesPage.tsx'
import { StatusPage } from './pages/StatusPage.tsx'
import { TracePage } from './pages/TracePage.tsx'
import { TracesPage } from './pages/TracesPage.tsx'
import { views } from './views.ts'

// The views built so far. The others still say which phase builds them.
const pages: Partial<Record<string, ReactElement>> = {
  services: <ServicesPage />,
  traces: <TracesPage />,
  logs: <LogsPage />,
}

// Routes are separate from the router so tests can mount them in memory.
export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<RequireSession />}>
        <Route element={<Layout />}>
          <Route index element={<Navigate to="/services" replace />} />
          {views.map((view) => (
            <Route
              key={view.path}
              path={view.path}
              element={pages[view.path] ?? <PlannedPage {...view} />}
            />
          ))}
          <Route path="traces/:traceId" element={<TracePage />} />
          <Route path="status" element={<StatusPage />} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Route>
    </Routes>
  )
}
