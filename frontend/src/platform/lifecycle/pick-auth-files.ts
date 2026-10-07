/** Picks Codex auth files with the native dialog. Inside Tauri the shell command owns it;
 * a plain browser (the dev fixture) has no native picker, so callers fall back. */
export async function pickAuthFiles(): Promise<string[] | null> {
  try {
    const { invoke } = await import('@tauri-apps/api/core')
    return await invoke<string[]>('pick_auth_files')
  } catch {
    return null
  }
}
