// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A session's lifecycle actions (interrupt the turn, stop, resume, clean up, delete), shared
// by the session card and the session header, and driven by the same capability model the
// card shows. Delete's question is shared with a session row's menu too.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  ArrowRightLeft,
  CirclePause,
  Disc3,
  Play,
  Square,
  Trash2,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { DiscardWorktreeOption } from '@/features/agentops/discard-worktree-option'
import { useTurnInterrupt } from '@/features/agentops/turn-interrupt'
import type { RunDTO } from '@/features/agentops/types'
import { currentControlFence } from '@/features/agentops/work-fence'
import { useClientSettings } from '@/features/settings/preferences'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sessionsKeys } from './api'
import { canHandoffSession, startsAgain, type Capability } from './provenance'
import { DisabledTip, IconTip } from './icon-tip'
import { MoveSessionDialog } from './session-move'
import './i18n'

/**
 * Interrupt, as an icon. A disabled one keeps its reason reachable by keyboard: the
 * button cannot take focus, so a focusable span around it carries the tooltip.
 */
function InterruptIcon({
  label,
  hint,
  fenceUnavailable,
  disabled,
  onClick,
}: {
  label: string
  hint: string
  fenceUnavailable: string | null
  disabled: boolean
  onClick: () => void
}) {
  const button = (
    <Button
      variant="ghost"
      size="icon"
      aria-label={label}
      onClick={onClick}
      disabled={disabled}
    >
      <CirclePause />
    </Button>
  )
  if (disabled)
    return (
      <DisabledTip reason={`${label}. ${fenceUnavailable ?? hint}`}>
        {button}
      </DisabledTip>
    )
  return <IconTip label={`${label}. ${hint}`}>{button}</IconTip>
}

/** The real lifecycle controls, driven by the SAME capability model shown above, so a
 * button can never appear that the block just said was unavailable. */
export function RunActions({
  run,
  caps,
  onClose,
  only,
  compact = false,
}: {
  run?: RunDTO
  caps: Capability[]
  onClose: () => void
  /** Limit to these actions (the session header shows Interrupt, Stop and Resume). */
  only?: readonly Capability['id'][]
  /**
   * The thread header's form: Interrupt and Stop as icon buttons (a name and a hover each,
   * Stop in the danger tone, asking first when the person's Stop setting says so), Resume
   * as the one labelled button. The session card keeps the labelled buttons.
   */
  compact?: boolean
}) {
  const { t } = useTranslation('sessions')
  const { t: tOps } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const turn = useTurnInterrupt(run)
  const qc = useQueryClient()
  const { t: tSettings } = useTranslation('settings')
  const confirmStop = useClientSettings((s) => s.confirmStop)
  const [confirm, setConfirm] = useState<null | 'cleanup' | 'delete' | 'stop'>(
    null,
  )
  // The person's confirmation to discard the session's git worktree and branch with
  // unmerged or uncommitted work; asked only of a session that has one.
  const [discardWorktree, setDiscardWorktree] = useState(false)
  const closeConfirm = () => {
    setConfirm(null)
    setDiscardWorktree(false)
  }
  // "Move to…": the same session on another account of its tool, or a linked session
  // in another tool. Offered where Resume is, for a session that has a conversation.
  const [moveOpen, setMoveOpen] = useState(false)

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
    onSuccess: () => {
      setConfirm(null)
      invalidate()
    },
    onError: onErr,
  })
  const resume = useMutation({
    mutationFn: () => agentOpsApi.resume(run?.run_ref as string),
    onSuccess: invalidate,
    onError: onErr,
  })
  const cleanup = useMutation({
    mutationFn: () =>
      agentOpsApi.cleanup(run?.run_ref as string, discardWorktree),
    onSuccess: () => {
      closeConfirm()
      invalidate()
    },
    onError: onErr,
  })

  const allow = (id: Capability['id']) =>
    (!only || only.includes(id)) && caps.some((c) => c.id === id && c.available)
  // A Stop question whose session stopped meanwhile is dropped, so it cannot come back
  // by itself the next time Stop is offered.
  if (confirm === 'stop' && !allow('stop')) setConfirm(null)
  if (!run) return null
  if (
    !allow('interrupt') &&
    !allow('stop') &&
    !allow('resume') &&
    !allow('cleanup') &&
    !allow('delete')
  )
    return null

  // ONE STOP QUESTION FOR BOTH FORMS: the header's icon and the card's labelled button
  // both ask first while the person's Stop setting says so (#2549).
  const stopConfirm = (
    <ConfirmDialog
      open={confirm === 'stop'}
      onOpenChange={(o) => !o && setConfirm(null)}
      title={tSettings('stopConfirm.title')}
      description={tSettings('stopConfirm.description')}
      confirmLabel={tSettings('stopConfirm.confirm')}
      pending={stop.isPending}
      onConfirm={() => stop.mutate()}
    />
  )

  if (compact)
    return (
      <div className="flex items-center gap-0.5" data-slot="thread-actions">
        {allow('interrupt') && turn.offered && (
          <InterruptIcon
            label={tOps('live.interrupt')}
            hint={tOps('live.interruptHint')}
            fenceUnavailable={
              turn.fenceUnavailable
                ? tOps('live.interruptFenceUnavailable')
                : null
            }
            disabled={!turn.allowed || turn.pending}
            onClick={turn.interrupt}
          />
        )}
        {allow('stop') && (
          <IconTip label={t('card.actions.stop')}>
            {/* STOP IS QUIET UNTIL IT IS MEANT: a ghost icon whose glyph is the danger
                tone, tinted on hover. The loud, filled version belongs to the confirm. */}
            <Button
              variant="ghost"
              size="icon"
              aria-label={t('card.actions.stop')}
              className="text-bad hover:bg-bad-soft hover:text-bad"
              onClick={() => (confirmStop ? setConfirm('stop') : stop.mutate())}
              disabled={stop.isPending}
            >
              <Square />
            </Button>
          </IconTip>
        )}
        {allow('resume') && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => resume.mutate()}
            disabled={resume.isPending}
          >
            <Play />
            {startsAgain(run)
              ? t('card.actions.startAgain')
              : t('card.actions.resume')}
          </Button>
        )}
        {(allow('resume') || allow('stop')) &&
          !startsAgain(run) &&
          canHandoffSession(run) && (
            <>
              <IconTip label={t('move.open')}>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t('move.open')}
                  onClick={() => setMoveOpen(true)}
                >
                  <ArrowRightLeft />
                </Button>
              </IconTip>
              {/* Mounted only while open: it reads profiles and the handoff preview, and a
                card that never opens it must not pay for either. */}
              {moveOpen && (
                <MoveSessionDialog
                  key={run.run_ref}
                  run={run}
                  onClose={() => setMoveOpen(false)}
                />
              )}
            </>
          )}
        {stopConfirm}
      </div>
    )

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
          onClick={() => (confirmStop ? setConfirm('stop') : stop.mutate())}
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
      {(allow('resume') || allow('stop')) &&
        !startsAgain(run) &&
        canHandoffSession(run) && (
          <>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setMoveOpen(true)}
            >
              <ArrowRightLeft className="size-3.5" />
              {t('move.open')}
            </Button>
            {moveOpen && (
              <MoveSessionDialog
                key={run.run_ref}
                run={run}
                onClose={() => setMoveOpen(false)}
              />
            )}
          </>
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
      {stopConfirm}
      <ConfirmDialog
        open={confirm === 'cleanup'}
        onOpenChange={(o) => !o && closeConfirm()}
        title={t('card.actions.cleanup')}
        description={t('card.actions.cleanupHint')}
        confirmLabel={t('card.actions.cleanup')}
        pending={cleanup.isPending}
        onConfirm={() => cleanup.mutate()}
      >
        {run.worktree_branch && run.state !== 'cleaned' && (
          <DiscardWorktreeOption
            branch={run.worktree_branch}
            checked={discardWorktree}
            onCheckedChange={setDiscardWorktree}
          />
        )}
      </ConfirmDialog>
      <DeleteRunDialog
        // A cleanup made for one run says nothing about the next one the card shows.
        key={run.run_ref}
        run={run}
        open={confirm === 'delete'}
        onOpenChange={(o) => !o && closeConfirm()}
        onDeleted={onClose}
      />
    </div>
  )
}

/**
 * Delete, asked first: the session card's button and a session row's menu (#2795). A
 * stopped or failed session is cleaned up first: the server deletes only a cleaned
 * record, and "Delete" is one action for the person asking.
 */
export function DeleteRunDialog({
  run,
  open,
  onOpenChange,
  onDeleted,
  onCloseAutoFocus,
}: {
  run: RunDTO
  open: boolean
  onOpenChange: (open: boolean) => void
  onDeleted?: () => void
  /** Where focus goes on close, for a dialog opened from a menu (no trigger). */
  onCloseAutoFocus?: (e: Event) => void
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  // The person's confirmation to discard the session's git worktree and branch with
  // unmerged or uncommitted work; asked only of a session that has one.
  const [discardWorktree, setDiscardWorktree] = useState(false)
  // Released is the run's state NOW, or a cleanup this dialog already made: the card keeps
  // the dialog mounted while the run changes under it, and a retry after a cleanup that
  // went through must not clean up again (refused: state=cleaned).
  const [cleanedHere, setCleanedHere] = useState(false)
  const cleaned = run.state === 'cleaned' || cleanedHere
  const close = () => {
    setDiscardWorktree(false)
    onOpenChange(false)
  }
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
    void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
  }
  const del = useMutation({
    mutationFn: async () => {
      if (!cleaned) {
        await agentOpsApi.cleanup(run.run_ref, discardWorktree)
        setCleanedHere(true)
      }
      return agentOpsApi.deleteRun(run.run_ref)
    },
    onSuccess: () => {
      close()
      invalidate()
      onDeleted?.()
    },
    onError: (err: unknown) => {
      // Released but not deleted is a state of its own: the lists must show it.
      invalidate()
      toast.error(
        err instanceof ApiError ? err.message : t('card.actionFailed'),
      )
    },
  })
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={(o) => (o ? onOpenChange(true) : close())}
      tone="danger"
      title={t('card.actions.delete')}
      description={t('card.actions.deleteHint')}
      confirmLabel={t('card.actions.delete')}
      pending={del.isPending}
      onConfirm={() => del.mutate()}
      onCloseAutoFocus={onCloseAutoFocus}
    >
      {run.worktree_branch && !cleaned && (
        <DiscardWorktreeOption
          branch={run.worktree_branch}
          checked={discardWorktree}
          onCheckedChange={setDiscardWorktree}
        />
      )}
    </ConfirmDialog>
  )
}
