// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EDIT FROM HERE, on a message the person sent: change its words, choose where the new
// session's files come from, and continue. Continue is the fork at that message: a new
// session from the conversation before it, with the edited words as what comes next; this
// session's conversation stays as it is, and the dialog says so. The new session starts on
// the folder as it is now unless the person chooses the files as they were at this
// message: then New session starts it in a worktree of its own, made from the checkpoint
// of the turn this message began. That turn is named by the engine's id of the accepted
// input, on the message and on the turn, never by the words or by counting. No file of
// this session or its folder goes back.
import { useQuery } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Textarea } from '@/components/ui/textarea'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { FromCheckpoint } from '@/features/agentops/run-create-dialog'
import type { RunCheckpointDTO, RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import './i18n'

export function EditFromHere({
  run,
  text,
  inputId,
  onContinue,
  onClose,
}: {
  run: RunDTO
  /** The message as it was sent. */
  text: string
  /** The engine's id of the accepted input this message is; without it, the files of
   * this message are not offered. */
  inputId?: string
  /** Starts the fork at this message with the edited words, and with the files of the
   * turn it began when the person chose them. */
  onContinue: (edited: string, from: FromCheckpoint | undefined) => void
  onClose: () => void
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can, principal } = useAuth()
  const boundary = useAuthBoundary()
  const fieldId = useId()
  const choiceId = useId()
  const [edited, setEdited] = useState(text)
  // The safe choice first: the folder as it is now, unless the person asks.
  const [files, setFiles] = useState<'now' | 'message'>('now')
  // Only the person who launched this session, and who may start one, is offered or read
  // for a message's files: the engine answers anyone else as if the session did not exist.
  const launched =
    run.acts_for?.kind === 'user' &&
    !!principal?.user_id &&
    run.acts_for.user_id === principal.user_id
  const canStart = launched && can('sessions:run:write') && !!run.workspace_ref
  // Only a message the engine names an input for asks for its turn.
  const asks = canStart && !!inputId
  const checkpoints = useQuery({
    queryKey: agentOpsKeys.runCheckpoints(activeTenant, run.run_ref),
    queryFn: ({ signal }) =>
      agentOpsApi.runCheckpoints(run.run_ref, { signal }),
    enabled: asks,
    // Each edit answers from a read of the turns made while it is open.
    refetchOnMount: 'always',
  })
  // A copy of its own needs a Git repository; a session in a worktree has one.
  const readsGit = asks && !run.worktree_branch && !!run.workspace_path
  const git = useQuery({
    queryKey: agentOpsKeys.runGit(activeTenant, boundary.epoch, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.gitStatus(run.run_ref, { signal }),
    enabled: readsGit,
  })
  // The engine answers 404 for a session folder that is not a Git repository.
  const notGit = git.error instanceof ApiError && git.error.status === 404
  // THE ANSWER FOR THIS EDIT, taken once: from the first read of the turns that answers
  // after it opened. A later read (the Turns pane's poll, the window's focus) changes
  // neither the offer nor the turn while it is open; opening it again answers anew. A
  // guarded update of this component during render.
  const [answer, setAnswer] = useState<{
    turn?: RunCheckpointDTO
    /** Why the files of this message are not offered (sessions words). */
    why?: string
  }>()
  if (
    asks &&
    !answer &&
    checkpoints.isFetchedAfterMount &&
    !(readsGit && git.isPending)
  ) {
    const turns =
      checkpoints.isSuccess && checkpoints.data.available
        ? checkpoints.data.turns
        : []
    // Its files are the one turn its input began, whole: none, two, or a clipped one
    // (a clipped turn still carries the id) name nothing.
    const own = turns.filter((c) => c.input_id === inputId)
    setAnswer(
      notGit
        ? { why: 'conversation.edit.notGit' }
        : checkpoints.isError
          ? { why: 'conversation.edit.filesUnread' }
          : own.length === 1 && !own[0].clipped
            ? { turn: own[0] }
            : { why: 'conversation.edit.noFiles' },
    )
  }
  const turn = asks ? answer?.turn : undefined
  const whyKey = inputId ? answer?.why : 'conversation.edit.noFiles'
  const why = canStart && whyKey ? t(whyKey) : null
  // Where the folder as it is now is: this session's, or the one its own copy came from.
  const nowNote = run.worktree_branch
    ? t('conversation.edit.ownCopy')
    : run.workspace_ref
      ? t('conversation.edit.keepNote')
      : null
  const choices = [
    { value: 'now', label: t('conversation.edit.keep'), note: nowNote },
    {
      value: 'message',
      label: t('conversation.edit.files'),
      note: t('conversation.edit.filesNote'),
    },
  ] as const
  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title={t('conversation.edit.title')}
      confirmLabel={t('conversation.continue')}
      confirmDisabled={!edited.trim()}
      hideAuditNotice
      onConfirm={() => {
        onContinue(
          edited,
          files === 'message' && turn
            ? { run: run.run_ref, turn: turn.seq }
            : undefined,
        )
        onClose()
      }}
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <label htmlFor={fieldId} className="text-caption text-text-2">
            {t('conversation.edit.message')}
          </label>
          <Textarea
            id={fieldId}
            rows={5}
            value={edited}
            onChange={(event) => setEdited(event.target.value)}
          />
        </div>
        {turn ? (
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-caption text-text-2">
              {t('conversation.edit.folder')}
            </legend>
            {choices.map(({ value, label, note }) => (
              <label key={value} className="flex items-start gap-2">
                <input
                  type="radio"
                  name={choiceId}
                  checked={files === value}
                  onChange={() => setFiles(value)}
                  aria-labelledby={`${choiceId}-${value}`}
                  aria-describedby={`${choiceId}-${value}-note`}
                  className="mt-1 size-4 shrink-0 accent-accent"
                />
                <span className="min-w-0">
                  <span
                    id={`${choiceId}-${value}`}
                    className="block text-body text-text-1"
                  >
                    {label}
                  </span>
                  <span
                    id={`${choiceId}-${value}-note`}
                    className="block text-caption text-text-2"
                  >
                    {note}
                  </span>
                </span>
              </label>
            ))}
          </fieldset>
        ) : (
          <>
            {nowNote ? (
              <p className="text-caption text-text-2">{nowNote}</p>
            ) : null}
            {why ? <p className="text-caption text-text-2">{why}</p> : null}
            {asks && !answer ? (
              <p role="status" className="text-caption text-text-2">
                {t('conversation.edit.filesReading')}
              </p>
            ) : null}
          </>
        )}
        <p className="text-caption text-muted-foreground">
          {t('conversation.edit.note')}
        </p>
      </div>
    </ConfirmDialog>
  )
}
