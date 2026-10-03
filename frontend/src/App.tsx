import { lazy, Suspense, useEffect, useState, type ComponentType } from 'react'
import { AppShell } from './app/AppShell'
import { ServicesProvider } from './app/ServicesProvider'
import { navigation, type AppRoute } from './app/navigation'

const OverviewPage = lazy(() => import('./features/overview/ui/OverviewPage'))
const ActivityPage = lazy(() => import('./features/activity/ui/ActivityPage'))
const ProvidersPage = lazy(() => import('./features/providers/ui/ProvidersPage'))
const ApiKeysPage = lazy(() => import('./features/api-keys/ui/ApiKeysPage'))
const SettingsPage = lazy(() => import('./features/settings/ui/SettingsPage'))
const InsightsPage = lazy(() => import('./features/insights/ui/InsightsPage'))
const ModelRoutesPage = lazy(() => import('./features/model-routes/ui/ModelRoutesPage'))
const TestsPage = lazy(() => import('./features/tests/ui/TestsPage'))
const GuardrailsPage = lazy(() => import('./features/guardrails/ui/GuardrailsPage'))

// The public build never references these modules, so the bundler drops the
// publishing workspaces instead of shipping unreachable code.
const TunnelPage: ComponentType | null = __OWNER_EDITION__ ? lazy(() => import('./features/tunnel/ui/TunnelPage')) : null
const ClientsPage: ComponentType | null = __OWNER_EDITION__ ? lazy(() => import('./features/clients/ui/ClientsPage')) : null
const SharedControlPage: ComponentType | null = __OWNER_EDITION__
  ? lazy(() => import('./features/shared-control/ui/SharedControlPage'))
  : null

export default function App() {
  return (
    <ServicesProvider>
      <Switchboard />
    </ServicesProvider>
  )
}

function Switchboard() {
  const [route, setRoute] = useState<AppRoute>(() => routeFromHash())

  useEffect(() => {
    const update = () => setRoute(routeFromHash())
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])

  const navigate = (next: AppRoute) => {
    window.location.hash = next
    setRoute(next)
  }

  return (
    <AppShell route={route} onNavigate={navigate}>
      <Suspense fallback={<div className="page-loading">Loading workspace…</div>}>
        {route === 'overview' ? <OverviewPage /> : null}
        {route === 'activity' ? <ActivityPage /> : null}
        {route === 'providers' ? <ProvidersPage /> : null}
        {route === 'keys' ? <ApiKeysPage /> : null}
        {route === 'settings' ? <SettingsPage /> : null}
        {route === 'insights' ? <InsightsPage /> : null}
        {route === 'routes' ? <ModelRoutesPage /> : null}
        {route === 'tests' ? <TestsPage /> : null}
        {route === 'guardrails' ? <GuardrailsPage /> : null}
        {route === 'tunnel' && TunnelPage ? <TunnelPage /> : null}
        {route === 'clients' && ClientsPage ? <ClientsPage /> : null}
        {route === 'shared' && SharedControlPage ? <SharedControlPage /> : null}
      </Suspense>
    </AppShell>
  )
}

function routeFromHash(): AppRoute {
  const value = window.location.hash.replace(/^#\/?/u, '') as AppRoute
  return navigation.some((section) => section.items.some((item) => item.id === value)) ? value : 'overview'
}
