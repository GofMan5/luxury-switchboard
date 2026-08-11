import { invoke } from '@tauri-apps/api/core'

export async function restartApp(): Promise<void> {
  await invoke('restart_app')
}
