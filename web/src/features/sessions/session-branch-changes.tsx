// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE SESSION'S WORKTREE BRANCH HOLDS, against where it began (K4.A2): the paths
// its commits changed since the branch left the workspace's current commit, and for one
// path its text at that base beside its text at the branch tip, in the existing diff
// view. It is what a person reviews after opening the work a handoff names. Committed
// work only: uncommitted edits are what session-changes.tsx shows. The engine reads git's
// objects and runs nothing in the session's folder (modules/sessions/run_worktree_diff.go).
import { useMutation, useQuery } from '@tanstack/react-query'
import { ArrowLeft, FileText } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { CodeDiff } from '@/components/ui/code-diff'
import { Input } from '@/components/ui/input'
import { PathText } from '@/components/ui/path-text'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { sessionTurnBody } from '@/features/agentops/session-turn'
import type { RunDTO } from '@/features/agentops/types'
import { currentControlFence } from '@/features/agentops/work-fence'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { isLiveRun } from './provenance'
import { reviewCommentText } from './review-comment'
import './i18n'

/** While the agent works its commits arrive; a finished session does not change. */
const WORKING_REFRESH_MS = 5_000
/** A commit id is shown by its first twelve digits, as git's short form. */
const SHORT_ID = 12

/** The line a comment is about: the version it is in, and its number as typed. */
interface CommentPlace {
  side: 'original' | 'modified'
  line: string
}
const NO_PLACE: CommentPlace = { side: 'modified', line: '' }

/** The lines of a file's text, without the empty one a final newline leaves. */
function linesOf(text: string): string[] {
  const lines = text.split('\n')
  if (lines[lines.length - 1] === '') lines.pop()
  return lines
}

export function SessionBranchChanges({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const [open, setOpen] = useState<string | null>(null)
  // The line a comment is about: picked by clicking its number in the diff, or typed in
  // the comment form. It starts over with each file.
  const [place, setPlace] = useState<CommentPlace>(NO_PLACE)
  const openFile = (path: string | null) => {
    setOpen(path)
    setPlace(NO_PLACE)
  }
  const working = run.state === 'running' || run.state === 'idle'
  const diff = useQuery({
    queryKey: agentOpsKeys.runWorktreeDiff(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.worktreeDiff(run.run_ref, { signal }),
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const file = useQuery({
    queryKey: agentOpsKeys.runWorktreeDiffFile(activeTenant, run.run_ref, open),
    queryFn: ({ signal }) =>
      agentOpsApi.worktreeDiffFile(run.run_ref, open!, { signal }),
    enabled: open !== null,
    // The header's range follows the branch, so the open file does too.
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const data = diff.data

  return (
    <section
      data-testid="session-branch-changes"
      className="flex flex-col gap-2"
    >
      <div>
        <h3 className="text-caption font-medium text-foreground">
          {t('branchChanges.title')}
        </h3>
        <p className="text-caption text-muted-foreground">
          {t('branchChanges.hint')}
        </p>
        {data ? (
          <p
            data-testid="branch-changes-range"
            className="break-all font-mono text-caption text-muted-foreground"
          >
            {t('branchChanges.range', {
              base: data.base.slice(0, SHORT_ID),
              head: data.head.slice(0, SHORT_ID),
            })}
          </p>
        ) : null}
      </div>
      {open !== null ? (
        <div className="flex min-w-0 flex-col gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="self-start"
            onClick={() => openFile(null)}
          >
            <ArrowLeft className="size-3.5" />
            {t('branchChanges.back')}
          </Button>
          <PathText value={open} wrap />
          {file.isPending ? (
            <p className="text-caption text-muted-foreground">
              {t('branchChanges.loading')}
            </p>
          ) : file.isError ? (
            <p className="text-caption text-danger">
              {t('branchChanges.error')}
            </p>
          ) : file.data.binary ? (
            <p className="text-caption text-muted-foreground">
              {t('branchChanges.binary')}
            </p>
          ) : (
            <>
              <CodeDiff
                original={file.data.original}
                modified={file.data.modified}
                language="text"
                originalLabel={t('branchChanges.original')}
                modifiedLabel={t('branchChanges.modified')}
                onLineClick={(side, line) =>
                  setPlace({ side, line: String(line) })
                }
              />
              {file.data.truncated ? (
                <p className="text-caption text-muted-foreground">
                  {t('branchChanges.fileTruncated')}
                </p>
              ) : null}
              <LineCommentForm
                run={run}
                path={open}
                original={file.data.original}
                modified={file.data.modified}
                place={place}
                onPlace={setPlace}
              />
            </>
          )}
        </div>
      ) : diff.isPending ? (
        <p className="text-caption text-muted-foreground">
          {t('branchChanges.loading')}
        </p>
      ) : diff.isError ? (
        <p className="text-caption text-danger">{t('branchChanges.error')}</p>
      ) : data!.files.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {t('branchChanges.empty')}
        </p>
      ) : (
        <ul className="flex flex-col" data-testid="branch-changes-list">
          {data!.files.map((f) => (
            <li key={f.path}>
              <button
                type="button"
                onClick={() => openFile(f.path)}
                className="flex w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
              >
                <FileText
                  className="size-3.5 shrink-0 text-muted-foreground"
                  aria-hidden
                />
                <PathText value={f.path} copy={false} className="flex-1" />
                <Badge variant="neutral">
                  {t(`branchChanges.status.${f.status}`)}
                </Badge>
              </button>
            </li>
          ))}
          {data!.truncated ? (
            <li className="px-1.5 pt-1 text-caption text-muted-foreground">
              {t('branchChanges.truncated', { count: data!.files.length })}
            </li>
          ) : null}
        </ul>
      )}
    </section>
  )
}

/**
 * Sends a comment on one line of the open file back to the session as its next message:
 * the run's input route, with the place beside it so the ledger records it. A disabled
 * Send says why (the session is not running, no line is chosen, the file has no such
 * line, nothing is written). A refused send keeps what was typed.
 */
function LineCommentForm({
  run,
  path,
  original,
  modified,
  place,
  onPlace,
}: {
  run: RunDTO
  path: string
  original: string
  modified: string
  place: CommentPlace
  onPlace: (place: CommentPlace) => void
}) {
  const { t } = useTranslation('sessions')
  const { can } = useAuth()
  const [comment, setComment] = useState('')
  const lineId = useId()
  const versionId = useId()
  const commentId = useId()
  const reasonId = useId()
  const line = Number(place.line)
  const validLine = place.line !== '' && Number.isInteger(line) && line >= 1
  const lineText = validLine
    ? linesOf(place.side === 'original' ? original : modified)[line - 1]
    : undefined
  const version = t(
    place.side === 'original'
      ? 'branchChanges.comment.atBase'
      : 'branchChanges.comment.atTip',
  )
  const reason = !can('sessions:run:write')
    ? t('branchChanges.comment.noPermission')
    : !isLiveRun(run)
      ? t('branchChanges.comment.notLive')
      : !validLine
        ? t('branchChanges.comment.needLine')
        : lineText === undefined
          ? t('branchChanges.comment.noSuchLine', { line, version })
          : comment.trim() === ''
            ? t('branchChanges.comment.empty')
            : null

  const send = useMutation({
    mutationFn: async () => {
      const where = { path, line, side: place.side }
      const body = sessionTurnBody(
        run,
        reviewCommentText(where, lineText ?? '', comment),
      )
      return agentOpsApi.reviewComment(
        run.run_ref,
        body,
        where,
        await currentControlFence(run),
      )
    },
    // Cleared only once the session accepted it: a refusal leaves the text to resend.
    onSuccess: () => {
      setComment('')
      toast.success(t('branchChanges.comment.sent'))
    },
    onError: (err) =>
      toast.error(
        err instanceof ApiError
          ? err.message
          : t('branchChanges.comment.failed'),
      ),
  })

  return (
    <form
      data-testid="branch-line-comment"
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (reason === null && !send.isPending) send.mutate()
      }}
    >
      <h4 className="text-caption font-medium text-foreground">
        {t('branchChanges.comment.title')}
      </h4>
      <div className="flex flex-wrap gap-2">
        <div className="flex flex-col gap-1">
          <label
            htmlFor={lineId}
            className="text-caption text-muted-foreground"
          >
            {t('branchChanges.comment.line')}
          </label>
          <Input
            id={lineId}
            type="number"
            min={1}
            className="w-24"
            value={place.line}
            onChange={(e) => onPlace({ ...place, line: e.target.value })}
          />
        </div>
        <div className="flex flex-col gap-1">
          <label
            htmlFor={versionId}
            className="text-caption text-muted-foreground"
          >
            {t('branchChanges.comment.version')}
          </label>
          <select
            id={versionId}
            value={place.side}
            onChange={(e) =>
              onPlace({
                ...place,
                side: e.target.value as CommentPlace['side'],
              })
            }
            className="h-9 rounded-md border border-border-strong bg-surface px-2 text-caption text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="modified">{t('branchChanges.modified')}</option>
            <option value="original">{t('branchChanges.original')}</option>
          </select>
        </div>
      </div>
      <div className="flex flex-col gap-1">
        <label
          htmlFor={commentId}
          className="text-caption text-muted-foreground"
        >
          {t('branchChanges.comment.text')}
        </label>
        <Textarea
          id={commentId}
          rows={3}
          value={comment}
          onChange={(e) => setComment(e.target.value)}
        />
      </div>
      {reason ? (
        <p id={reasonId} className="text-caption text-muted-foreground">
          {reason}
        </p>
      ) : null}
      <Button
        type="submit"
        size="sm"
        className="self-start"
        disabled={reason !== null || send.isPending}
        aria-describedby={reason ? reasonId : undefined}
      >
        {t('branchChanges.comment.send')}
      </Button>
    </form>
  )
}
