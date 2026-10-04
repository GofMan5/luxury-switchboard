/** Hands the verified installer to the shell. Only in the desktop shell: a
 * plain browser (the dev fixture) cannot execute anything, and the dialog
 * says so instead of pretending. */
export async function runInstaller(path: string, exit: boolean): Promise<void> {
  const { invoke } = await import('@tauri-apps/api/core')
  await invoke('run_installer', { path, exit })
}
