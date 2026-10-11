// Shared copy rules for codex UI. Copy that two screens must never disagree
// about lives here, next to the feature it explains, not in each screen.

// The backend's refusal sentences (the active-route guard among them) are
// short and user-facing, so they travel verbatim; anything this long is a
// dump (a provider list, an error blob) that has no place in a confirm
// dialog, so it is cut at a word boundary instead.
const LOGOUT_ERROR_MAX = 200

export function logoutErrorCopy(error: string): string {
  const trimmed = error.trim()
  if (trimmed.length <= LOGOUT_ERROR_MAX) return trimmed
  const cut = trimmed.slice(0, LOGOUT_ERROR_MAX)
  const boundary = cut.lastIndexOf(' ')
  return `${boundary > 0 ? cut.slice(0, boundary) : cut}…`
}
