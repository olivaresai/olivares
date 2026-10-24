// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useState } from 'react'

/** The time now, again every `intervalMs` (a second) while `ticking`: a clock for what is
 * running. Only the component that shows the clock ticks; the rest of the page does not
 * paint again. */
export function useNow(ticking: boolean, intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!ticking) return
    const tick = () => setNow(Date.now())
    const first = setTimeout(tick, 0)
    const every = setInterval(tick, intervalMs)
    return () => {
      clearTimeout(first)
      clearInterval(every)
    }
  }, [ticking, intervalMs])
  return now
}
