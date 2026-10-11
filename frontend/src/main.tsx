import { createRoot } from 'react-dom/client'
import App from './App'
import './index.css'

const root = document.getElementById('root')
if (!root) throw new Error('Application root is missing')

// The pending half of the boot sentinel (see public/boot-hook.js): the
// bundle is running, so the starting message hands the window to the app.
// If the first commit never happens, the hook puts a failure notice back.
document.getElementById('boot-sentinel')?.remove()

createRoot(root).render(<App />)
