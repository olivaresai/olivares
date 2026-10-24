// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { usePaneWidthsStore, type PaneWidthPatch } from '@/stores/pane-widths'
import { useUiState } from './use-ui-state'

/** Key presses and drags arrive in bursts; one save carries the burst. */
const SAVE_DELAY_MS = 400

/**
 * Keeps the panel widths on the engine (the `pane_widths` of the person's interface state):
 * reads them once per identity into the store the layouts read, and saves each change after
 * a short pause. Renders nothing; the app layout mounts it once.
 *
 * With no engine to keep them (signed out, the consoleviews module off) a width still
 * applies for this visit, it just is not remembered.
 */
export function PaneWidthsSync() {
  const { t } = useTranslation('common')
  const { available, query, save, scope } = useUiState()
  // A first read that was refused or failed says the engine will not take a save either (a
  // role that may not read its interface state): the width applies for this visit and no
  // save is sent. A later refresh that fails (a network blip) says nothing of the kind.
  const keeps = available && !(query.isError && query.data === undefined)
  // The person, not the credential: a rotated token keeps the widths on screen (the engine's
  // copy is read again under the new generation, but the store keeps what it has).
  const owner =
    scope?.key && scope.tenant ? `${scope.tenant}/${scope.key}` : null
  const latest = useRef({ save, t, live: scope?.live })
  useEffect(() => {
    latest.current = { save, t, live: scope?.live }
  })

  useEffect(() => {
    const { attach } = usePaneWidthsStore.getState()
    if (!owner || !keeps) {
      attach(owner, null)
      return
    }
    let pending: Record<string, number> = {}
    let timer: ReturnType<typeof setTimeout> | undefined
    const flush = () => {
      clearTimeout(timer)
      timer = undefined
      if (Object.keys(pending).length === 0) return
      const pane_widths = pending
      pending = {}
      const { save, t, live } = latest.current
      save({ pane_widths }).catch(() => {
        // A sign-out or tenant switch ends the identity the width belonged to: not a failure.
        if (live?.()) toast.error(t('panes.saveFailed'))
      })
    }
    attach(owner, (patch: PaneWidthPatch) => {
      pending = { ...pending, ...patch }
      clearTimeout(timer)
      timer = setTimeout(flush, SAVE_DELAY_MS)
    })
    // Leaving (another page set, another identity) sends what is waiting.
    return flush
  }, [owner, keeps])

  const { isSuccess, data } = query
  useEffect(() => {
    if (owner && isSuccess)
      usePaneWidthsStore.getState().hydrate(owner, data.pane_widths ?? {})
  }, [owner, isSuccess, data])

  return null
}
