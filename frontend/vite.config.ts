import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const owner = process.env.SWITCHBOARD_EDITION !== 'public'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // Replaced at build time so the public bundle drops the owner workspaces instead
  // of shipping them behind a runtime check.
  define: {
    __OWNER_EDITION__: JSON.stringify(owner),
  },
  clearScreen: false,
  server: {
    host: '127.0.0.1',
    port: 1420,
    strictPort: true,
  },
  envPrefix: ['VITE_', 'TAURI_ENV_'],
})
