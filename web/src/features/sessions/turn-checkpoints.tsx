// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT EACH TURN CHANGED, and a way back: one row per message the session took,
// newest first, with the files that turn changed. "Rewind" puts the session's folder back
// as it was before that turn, after a confirmation that says how many files it touches,
// and puts the turn's message in the message box to edit and send again; "Undo" puts the
// folder back as the rewind found it. The tool keeps its own conversation: the rewind
// changes files only, and the confirmation says so.
import { useQuery } from '@tanstack/react-query'
import { Undo2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { PathText } from '@/components/ui/path-text'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type {
  RunCheckpointDTO,
  RunDTO,
  RunRewindDTO,
} from '@/features/agentops/types'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  readSessionDraft,
  sessionDraftKey,
  writeSessionDraft,
} from '@/lib/session-drafts'
import './i18n'

/** While the agent works the list follows it; a finished session does not change. */
const WORKING_REFRESH_MS = 5_000

export interface RewindRequest {
  seq: number
  to: 'start' | 'end'
  /** The message to offer again; absent for an undo, and when only part of it is kept. */
  prompt?: string
}

/**
 * The folder put back as it was before a turn (`start`) or as the rewind found it (`end`):
 * one privileged act, and what the folder holds is read again afterwards. Only the Turns
 * list's Rewind and Undo go through it: "Edit from here" puts no folder back, its new
 * session starts from a message's files in a worktree of its own.
 */
export function useRewindRun(
  run: RunDTO,
  onDone: (done: RunRewindDTO, request: RewindRequest) => void,
) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  return usePrivilegedMutation<RewindRequest, RunRewindDTO>({
    mutationFn: ({ seq, to }, authority) =>
      agentOpsApi.rewindRun(run.run_ref, seq, to, authority),
    // What the folder holds changed, so the files and the Git status read it again.
    invalidateKeys: [
      agentOpsKeys.runCheckpoints(activeTenant, run.run_ref),
      agentOpsKeys.runChanges(activeTenant, run.run_ref),
      agentOpsKeys.runGit(activeTenant, boundary.epoch, run.run_ref),
    ],
    successMessage: (done) =>
      t(done.to === 'start' ? 'checkpoints.rewound' : 'checkpoints.undone', {
        n: done.files.length,
      }),
    onDone,
  })
}

export function TurnCheckpoints({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant, principal, can } = useAuth()
  const canWrite = can('sessions:run:write')
  const working = run.state === 'running' || run.state === 'idle'
  const [target, setTarget] = useState<RunCheckpointDTO | null>(null)
  // The newest turn when a rewind was made: its end is where Undo puts the folder.
  const [undoSeq, setUndoSeq] = useState<number | null>(null)
  const key = agentOpsKeys.runCheckpoints(activeTenant, run.run_ref)
  const checkpoints = useQuery({
    queryKey: key,
    queryFn: ({ signal }) =>
      agentOpsApi.runCheckpoints(run.run_ref, { signal }),
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const turns = checkpoints.data?.available ? checkpoints.data.turns : []
  const newest = turns.at(-1)?.seq
  const draftKey = sessionDraftKey(activeTenant, principal, run.run_ref)
  const rewind = useRewindRun(run, (_done, { to, prompt }) => {
    setTarget(null)
    setUndoSeq(to === 'start' ? (newest ?? null) : null)
    // The message goes after what the person already typed, as a quote does.
    if (to === 'start' && prompt) {
      const draft = readSessionDraft(draftKey)
      writeSessionDraft(draftKey, draft ? `${draft}\n\n${prompt}` : prompt)
    }
  })

  if (turns.length === 0) return null
  // Every file this turn or a later one changed is what the rewind touches.
  const touched = target
    ? new Set(
        turns
          .filter((turn) => turn.seq >= target.seq)
          .flatMap((turn) => turn.files.map((f) => f.path)),
      ).size
    : 0

  return (
    <section data-testid="turn-checkpoints" className="flex flex-col gap-1.5">
      <div>
        <h3 className="text-caption font-medium text-foreground">
          {t('checkpoints.title')}
        </h3>
        <p className="text-caption text-muted-foreground">
          {t('checkpoints.hint')}
        </p>
      </div>
      {undoSeq !== null && canWrite ? (
        <div>
          <Button
            variant="outline"
            size="sm"
            disabled={rewind.isPending}
            onClick={() => rewind.mutate({ seq: undoSeq, to: 'end' })}
          >
            {t('checkpoints.undo')}
          </Button>
        </div>
      ) : null}
      <ol className="flex flex-col gap-1">
        {[...turns].reverse().map((turn) => (
          <li
            key={turn.seq}
            data-testid="turn-checkpoint"
            className="rounded-md border border-border px-2 py-1.5"
          >
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <p
                  className="truncate text-caption text-foreground"
                  title={turn.prompt}
                >
                  {turn.prompt.split('\n', 1)[0] || t('checkpoints.noWords')}
                </p>
                <p className="text-caption text-muted-foreground">
                  {t('checkpoints.turn', { n: turn.seq })}
                  {' · '}
                  <RelTimeLabel ts={turn.at} />
                  {' · '}
                  {t('checkpoints.filesChanged', { n: turn.files.length })}
                  {turn.truncated ? '+' : ''}
                </p>
              </div>
              {canWrite ? (
                <Button
                  variant="ghost"
                  size="sm"
                  className="shrink-0"
                  onClick={() => setTarget(turn)}
                >
                  <Undo2 className="size-3.5" aria-hidden />
                  {t('checkpoints.rewind')}
                </Button>
              ) : null}
            </div>
            {turn.files.length > 0 ? (
              <details className="mt-1">
                <summary className="cursor-pointer text-caption text-muted-foreground">
                  {t('checkpoints.showFiles')}
                </summary>
                <ul className="mt-1 flex flex-col gap-0.5">
                  {turn.files.map((f) => (
                    <li key={f.path} className="flex items-center gap-2">
                      <PathText
                        value={f.path}
                        copy={false}
                        className="flex-1"
                      />
                      <span className="shrink-0 text-caption text-muted-foreground">
                        {t(`checkpoints.status.${f.status}`)}
                      </span>
                    </li>
                  ))}
                </ul>
              </details>
            ) : null}
          </li>
        ))}
      </ol>
      <ConfirmDialog
        open={target !== null}
        onOpenChange={(open) => {
          if (!open) setTarget(null)
        }}
        title={t('checkpoints.confirm.title', { n: target?.seq ?? 0 })}
        description={t('checkpoints.confirm.description', { n: touched })}
        tone="danger"
        confirmLabel={t('checkpoints.confirm.action')}
        pending={rewind.isPending}
        onConfirm={() =>
          target &&
          rewind.mutate({
            seq: target.seq,
            to: 'start',
            prompt: target.clipped ? undefined : target.prompt,
          })
        }
      >
        <p className="text-caption text-muted-foreground">
          {t('checkpoints.confirm.conversation')}
        </p>
      </ConfirmDialog>
    </section>
  )
}
