// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useState } from 'react'

/**
 * True while the viewport is at least `px` wide. The same shape as `useIsPhone`, for the
 * places where BEHAVIOUR (not only layout) changes with the width — a tab list that is a
 * vertical list on a wide screen and a scrolling strip on a narrow one moves its focus with
 * different arrow keys, which CSS alone cannot change.
 */
export function useMinWidth(px: number): boolean {
  const query = `(min-width: ${px}px)`
  const [wide, setWide] = useState(
    () =>
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia(query).matches,
  )
  useEffect(() => {
    if (
      typeof window === 'undefined' ||
      typeof window.matchMedia !== 'function'
    )
      return
    const list = window.matchMedia(query)
    const sync = () => setWide(list.matches)
    sync()
    list.addEventListener('change', sync)
    return () => list.removeEventListener('change', sync)
  }, [query])
  return wide
}
