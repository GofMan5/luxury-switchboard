import { lazy, Suspense, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { AppShell } from './app/AppShell'
import { ServicesProvider } from './app/ServicesProvider'
import { isPluginRoute, routeFromHash, visibleNavigation, type AppRoute } from './app/navigation'
import { layoutStartRoute } from './app/rail'
import { useAppServices } from './app/services'
import { AppearanceRuntime } from './features/appearance/ui/AppearanceRuntime'

const OverviewPage = lazy(() => import('./features/overview/ui/OverviewPage'))
const ActivityPage = lazy(() => import('./features/activity/ui/ActivityPage'))
const ProvidersPage = lazy(() => import('./features/providers/ui/ProvidersPage'))
const ApiKeysPage = lazy(() => import('./features/api-keys/ui/ApiKeysPage'))
const SettingsPage = lazy(() => import('./features/settings/ui/SettingsPage'))
const InsightsPage = lazy(() => import('./features/insights/ui/InsightsPage'))
const ModelRoutesPage = lazy(() => import('./features/model-routes/ui/ModelRoutesPage'))
const RulesPage = lazy(() => import('./features/rules/ui/RulesPage'))
const TestsPage = lazy(() => import('./features/tests/ui/TestsPage'))
const GuardrailsPage = lazy(() => import('./features/guardrails/ui/GuardrailsPage'))
const AppearancePage = lazy(() => import('./features/appearance/ui/AppearancePage'))
const PluginsPage = lazy(() => import('./features/plugins/ui/PluginsPage'))
const PluginPageView = lazy(() => import('./features/plugins/ui/PluginPageView'))

// The public build never references these modules, so the bundler drops the
// publishing workspaces instead of shipping unreachable code.
const TunnelPage = lazy(() => import('./features/tunnel/ui/TunnelPage'))
const ClientsPage = lazy(() => import('./features/clients/ui/ClientsPage'))

export default function App() {
  return (
    <ServicesProvider>
      <AppearanceRuntime />
      <Switchboard />
    </ServicesProvider>
  )
}

function Switchboard() {
  const [route, setRoute] = useState<AppRoute>(() => routeFromHash(window.location.hash))
  // A layout's start page applies once, and only while the window still shows
  // the page it opened on: a deep link or the operator's first click wins.
  const startOpen = useRef(window.location.hash.replace(/^#\/?/u, '') === '')

  useEffect(() => {
    const update = () => {
      startOpen.current = false
      setRoute(routeFromHash(window.location.hash))
    }
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])

  const navigate = (next: AppRoute) => {
    startOpen.current = false
    window.location.hash = next
    setRoute(next)
  }

  const { capabilities, extensions } = useAppServices()
  const layout = useSyncExternalStore(extensions.subscribe, () => extensions.snapshot().layout)
  const sections = useMemo(() => visibleNavigation(capabilities ?? []), [capabilities])
  useEffect(() => {
    const start = startOpen.current ? layoutStartRoute(layout?.startPage, sections) : undefined
    if (start) navigate(start)
  }, [layout, sections])

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
        {route === 'rules' ? <RulesPage /> : null}
        {route === 'tests' ? <TestsPage /> : null}
        {route === 'guardrails' ? <GuardrailsPage /> : null}
        {route === 'tunnel' ? <TunnelPage /> : null}
        {route === 'clients' ? <ClientsPage /> : null}
        {route === 'appearance' ? <AppearancePage /> : null}
        {route === 'plugins' ? <PluginsPage /> : null}
        {isPluginRoute(route) ? <PluginPageView pageId={route.slice('plugin/'.length)} /> : null}
      </Suspense>
    </AppShell>
  )
}
