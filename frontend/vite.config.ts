import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const edition = process.env.SWITCHBOARD_EDITION === 'public' ? 'public' : 'owner'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // Replaced at build time so the public bundle drops the owner workspaces instead
  // of shipping them behind a runtime check.
  define: {
    'import.meta.env.VITE_EDITION': JSON.stringify(edition),
    __OWNER_EDITION__: JSON.stringify(edition === 'owner'),
  },
  clearScreen: false,
  server: {
    host: '127.0.0.1',
    port: 1420,
    strictPort: true,
  },
  envPrefix: ['VITE_', 'TAURI_ENV_'],
})
