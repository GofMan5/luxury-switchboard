// The module-side half of the boot sentinel. The hook in
// frontend/public/boot-hook.js keeps the window lit until the app commits
// its first real frame; this call is how the app says it did. Missing hook
// (tests, non-Tauri embeds) is a no-op, not an error.
export function markBootReady(): void {
  const hook = (window as typeof window & { __dshBootReady?: () => void }).__dshBootReady
  hook?.()
}
