/** Hands the verified installer to the shell. Only in the desktop shell: a
 * plain browser (the dev fixture) cannot execute anything, and the dialog
 * says so instead of pretending. */
export async function runInstaller(path: string, exit: boolean): Promise<void> {
  // Same probe the session layer uses: without the desktop runtime the invoke
  // below dies on an undefined global and surfaces a cryptic TypeError, which
  // tells the operator nothing. Refuse with words instead.
  if (!('__TAURI_INTERNALS__' in window)) {
    throw new Error('the browser shell has no desktop runtime to start installers')
  }
  const { invoke } = await import('@tauri-apps/api/core')
  await invoke('run_installer', { path, exit })
}
