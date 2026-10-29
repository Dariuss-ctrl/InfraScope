import { Navigate, Route, Routes } from 'react-router'
import { Layout } from './components/Layout.tsx'
import { NotFoundPage } from './pages/NotFoundPage.tsx'
import { PlannedPage } from './pages/PlannedPage.tsx'
import { views } from './views.ts'

// Routes are separate from the router so tests can mount them in memory.
export function AppRoutes() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Navigate to="/services" replace />} />
        {views.map((view) => (
          <Route key={view.path} path={view.path} element={<PlannedPage {...view} />} />
        ))}
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  )
}
