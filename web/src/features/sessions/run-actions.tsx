// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A session's lifecycle actions (interrupt the turn, stop, resume, clean up, delete), shared
// by the session card and the session header, and driven by the same capability model the
// card shows.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { CirclePause, Disc3, Play, Square, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useTurnInterrupt } from '@/features/agentops/turn-interrupt'
import type { RunDTO } from '@/features/agentops/types'
import { currentControlFence } from '@/features/agentops/work-fence'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sessionsKeys } from './api'
import { startsAgain, type Capability } from './provenance'
import './i18n'

/** The real lifecycle controls, driven by the SAME capability model shown above, so a
 * button can never appear that the block just said was unavailable. */
export function RunActions({
  run,
  caps,
  onClose,
  only,
}: {
  run?: RunDTO
  caps: Capability[]
  onClose: () => void
  /** Limit to these actions (the session header shows Interrupt, Stop and Resume). */
  only?: readonly Capability['id'][]
}) {
  const { t } = useTranslation('sessions')
  const { t: tOps } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const turn = useTurnInterrupt(run)
  const qc = useQueryClient()
  const [confirm, setConfirm] = useState<null | 'cleanup' | 'delete'>(null)

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
    void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
  }
  const onErr = (err: unknown) =>
    toast.error(err instanceof ApiError ? err.message : t('card.actionFailed'))

  const stop = useMutation({
    mutationFn: async () =>
      agentOpsApi.stop(
        run?.run_ref as string,
        run ? await currentControlFence(run) : undefined,
      ),
    onSuccess: invalidate,
    onError: onErr,
  })
  const resume = useMutation({
    mutationFn: () => agentOpsApi.resume(run?.run_ref as string),
    onSuccess: invalidate,
    onError: onErr,
  })
  const cleanup = useMutation({
    mutationFn: () => agentOpsApi.cleanup(run?.run_ref as string),
    onSuccess: () => {
      setConfirm(null)
      invalidate()
    },
    onError: onErr,
  })
  const del = useMutation({
    // A stopped or failed session is cleaned up first: the server deletes only a
    // cleaned record, and "Delete" is one action for the person asking.
    mutationFn: async () => {
      const ref = run?.run_ref as string
      if (run?.state !== 'cleaned') await agentOpsApi.cleanup(ref)
      return agentOpsApi.deleteRun(ref)
    },
    onSuccess: () => {
      setConfirm(null)
      invalidate()
      onClose()
    },
    onError: onErr,
  })

  const allow = (id: Capability['id']) =>
    (!only || only.includes(id)) && caps.some((c) => c.id === id && c.available)
  if (!run) return null
  if (
    !allow('interrupt') &&
    !allow('stop') &&
    !allow('resume') &&
    !allow('cleanup') &&
    !allow('delete')
  )
    return null

  return (
    <div className="flex flex-wrap items-center gap-2">
      {allow('interrupt') && turn.offered && (
        <Button
          variant="secondary"
          size="sm"
          title={
            turn.fenceUnavailable
              ? tOps('live.interruptFenceUnavailable')
              : tOps('live.interruptHint')
          }
          onClick={turn.interrupt}
          disabled={!turn.allowed || turn.pending}
        >
          <CirclePause className="size-3.5" />
          {tOps('live.interrupt')}
        </Button>
      )}
      {allow('stop') && (
        <Button
          variant="secondary"
          size="sm"
          onClick={() => stop.mutate()}
          disabled={stop.isPending}
        >
          <Square className="size-3.5" />
          {t('card.actions.stop')}
        </Button>
      )}
      {allow('resume') && (
        <Button
          variant="secondary"
          size="sm"
          onClick={() => resume.mutate()}
          disabled={resume.isPending}
        >
          <Play className="size-3.5" />
          {startsAgain(run)
            ? t('card.actions.startAgain')
            : t('card.actions.resume')}
        </Button>
      )}
      {allow('cleanup') && (
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setConfirm('cleanup')}
        >
          <Disc3 className="size-3.5" />
          {t('card.actions.cleanup')}
        </Button>
      )}
      {allow('delete') && (
        <Button
          variant="destructive"
          size="sm"
          onClick={() => setConfirm('delete')}
        >
          <Trash2 className="size-3.5" />
          {t('card.actions.delete')}
        </Button>
      )}
      <ConfirmDialog
        open={confirm === 'cleanup'}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={t('card.actions.cleanup')}
        description={t('card.actions.cleanupHint')}
        confirmLabel={t('card.actions.cleanup')}
        pending={cleanup.isPending}
        onConfirm={() => cleanup.mutate()}
      />
      <ConfirmDialog
        open={confirm === 'delete'}
        onOpenChange={(o) => !o && setConfirm(null)}
        tone="danger"
        title={t('card.actions.delete')}
        description={t('card.actions.deleteHint')}
        confirmLabel={t('card.actions.delete')}
        pending={del.isPending}
        onConfirm={() => del.mutate()}
      />
    </div>
  )
}
