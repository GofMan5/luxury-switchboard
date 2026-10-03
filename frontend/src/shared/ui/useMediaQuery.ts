import { useEffect, useState } from 'react'

/** Tracks one media query live, for layout branches that CSS alone cannot
 * express (a panel that exists at one width and only on demand at another). */
export function useMediaQuery(query: string): boolean {
  // jsdom has no matchMedia: the fallback is the wide layout, which is also
  // what every test asserts against.
  const [matches, setMatches] = useState(() => typeof window.matchMedia === 'function' ? window.matchMedia(query).matches : false)
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const list = window.matchMedia(query)
    const update = () => setMatches(list.matches)
    update()
    list.addEventListener('change', update)
    return () => list.removeEventListener('change', update)
  }, [query])
  return matches
}
