import { createRoot } from 'react-dom/client'
// Self-hosted type. Each face declares unicode ranges, so the browser fetches
// only the subsets and families the current appearance actually renders.
import '@fontsource-variable/ibm-plex-sans/wght.css'
import '@fontsource-variable/geist/wght.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import '@fontsource-variable/jetbrains-mono/wght.css'
import App from './App'
import './index.css'
import { isSafeModeChord, restartInSafeMode, safeMode } from './shared/runtime/safe-mode'
import { restoreLastTheme } from './features/appearance/ui/apply-appearance'

const root = document.getElementById('root')
if (!root) throw new Error('Application root is missing')

restoreLastTheme()

// Bound before React mounts: the way out of a broken stylesheet or plugin
// must work even when the shell never renders.
window.addEventListener('keydown', (event) => {
  if (!isSafeModeChord(event)) return
  event.preventDefault()
  restartInSafeMode(!safeMode)
}, { capture: true })

// The pending half of the boot sentinel (see public/boot-hook.js): the
// bundle is running, so the starting message hands the window to the app.
// If the first commit never happens, the hook puts a failure notice back.
document.getElementById('boot-sentinel')?.remove()

createRoot(root).render(<App />)
