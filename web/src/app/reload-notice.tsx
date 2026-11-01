// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'

/**
 * The one-shot flag the load guard (index.html) leaves when it reloads the
 * document because the old build's lazy chunk is gone (an upgrade with the
 * console open). The name MUST stay identical to the guard's string: that
 * script runs before any bundle, so the agreement is by name, not import.
 */
export const RELOAD_NOTICE_KEY = 'olivares.console.reloadNotice'

/**
 * Says "Updated, reloaded" once after the guard's upgrade hand-off, on the
 * app-wide toast surface, and forgets the flag. An ordinary boot says nothing.
 * Consumed in an effect so the Toaster (its sibling in Providers) is mounted,
 * and idempotent under StrictMode's double effect: the first run consumes.
 */
export function ReloadNotice() {
  const { t } = useTranslation()
  useEffect(() => {
    let flagged = false
    try {
      flagged = sessionStorage.getItem(RELOAD_NOTICE_KEY) !== null
      if (flagged) sessionStorage.removeItem(RELOAD_NOTICE_KEY)
    } catch {
      // Storage denied: the recovery already happened; its note is best-effort.
    }
    if (flagged) toast.success(t('states.updatedReloaded'))
  }, [t])
  return null
}
