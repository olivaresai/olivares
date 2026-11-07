// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A REVERSIBLE LIST ACTION IS UNDONE, NOT CONFIRMED. Archive, snooze, mark done and unpin
// raise a notice for a few seconds with Undo in it, and Ctrl+Z (⌘Z) in the rail takes the
// same Undo while the notice is up. This is the one place that builds the notice, so the
// four actions cannot disagree on how long it lasts or what Undo checks.
//
// ⛔ UNDO NEVER OVERWRITES. Before it changes anything it reads the runs again; if any of
//    them is no longer as the action left it (another device, another person), nothing is
//    undone and the person is told so.
import { useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sessionsKeys } from './api'
import './i18n'

/** How long the notice, and so Undo, stays up. The toast pauses while it is hovered or focused. */
export const UNDO_MS = 5000

export interface UndoOffer {
  /** What was done, to which session: `Archived "name"`. */
  message: string
  /** Each run the action changed, and what it must still read for Undo to be safe. */
  expect: readonly { ref: string; still: (run: RunDTO) => boolean }[]
  /** Puts the previous state back, through the routes that already do it. */
  restore: () => Promise<unknown>
  /** The organization the action was made in; absent is the active one. */
  tenant?: string | null
  /** Runs once an Undo that worked has re-read the sessions. */
  then?: () => void
}

interface Latest {
  id: string | number
  undo: () => void
}

/** The newest offer still on screen: what Ctrl+Z takes. */
let latest: Latest | null = null

/** Takes the newest offer's Undo and closes its notice. False when there is none. */
export function undoLatest(): boolean {
  const offer = latest
  if (!offer) return false
  latest = null
  toast.dismiss(offer.id)
  offer.undo()
  return true
}

/** `offer(...)` raises the notice for a reversible action that just succeeded. */
export function useOfferUndo(): (offer: UndoOffer) => void {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  return useCallback(
    (offer) => {
      const tenant = offer.tenant ?? activeTenant
      // Success or not, the engine's answer is the truth: re-read it.
      const refresh = () =>
        Promise.all([
          qc.invalidateQueries({ queryKey: agentOpsKeys.all(tenant) }),
          qc.invalidateQueries({ queryKey: sessionsKeys.all(tenant) }),
        ])
      const undo = async () => {
        try {
          const now = await Promise.all(
            offer.expect.map((e) => agentOpsApi.getRun(e.ref, { tenant })),
          )
          if (now.every((run, i) => offer.expect[i].still(run))) {
            await offer.restore()
            await refresh()
            offer.then?.()
            return
          }
          toast.warning(t('undo.changed'))
        } catch (err) {
          toast.error(err instanceof ApiError ? err.message : t('undo.failed'))
        }
        void refresh()
      }
      const entry: Latest = { id: 0, undo: () => void undo() }
      const forget = () => {
        if (latest === entry) latest = null
      }
      entry.id = toast.success(offer.message, {
        duration: UNDO_MS,
        action: {
          label: t('rowActions.undo'),
          onClick: () => {
            forget()
            entry.undo()
          },
        },
        onDismiss: forget,
        onAutoClose: forget,
      })
      latest = entry
    },
    [t, activeTenant, qc],
  )
}
