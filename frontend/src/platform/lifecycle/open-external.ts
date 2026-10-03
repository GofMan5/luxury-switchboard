/** Opens a link in the system browser. Inside Tauri the shell command owns it;
 * in a plain browser (the dev fixture) a window.open is the same gesture. */
export async function openExternal(url: string): Promise<void> {
  try {
    const { invoke } = await import('@tauri-apps/api/core')
    await invoke('open_url', { url })
  } catch {
    window.open(url, '_blank', 'noopener')
  }
}
