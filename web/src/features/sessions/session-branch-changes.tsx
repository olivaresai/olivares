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
// A session with no worktree of its own works in its Git folder directly: the same view
// shows that folder's uncommitted changes against its last commit
// (modules/sessions/run_folder_diff.go).
import { useMutation, useQuery } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { useId, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { Button } from '@/components/ui/button'
import { FileTypeIcon } from '@/components/ui/file-type-icon'
import { DiffDialog } from '@/components/ui/diff-dialog'
import { OpenInEditor } from '@/features/shared/open-in-editor'
import { firstChangedLine } from '@/lib/editor-links'
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
import { cn } from '@/lib/utils'
import { useReviewed } from '@/stores/preferences'
import { isLiveRun } from './provenance'
import { reviewCommentText } from './review-comment'
import './i18n'

/** While the agent works its commits arrive; a finished session does not change. */
const WORKING_REFRESH_MS = 5_000
/** A commit id is shown by its first twelve digits, as git's short form. */
const SHORT_ID = 12

/** What a 409 for a session's own Git folder means, by the reason the engine names;
 * without one, the general words. */
const CANNOT_COMPARE: Partial<Record<string, string>> = {
  no_commit: 'branchChanges.folder.noCommit',
  head_unreadable: 'branchChanges.folder.headUnreadable',
  diff_unreadable: 'branchChanges.folder.diffUnreadable',
  folder_mismatch: 'branchChanges.folder.folderMismatch',
}

/** The line a comment is about: the version it is in, and its number as typed. */
export interface CommentPlace {
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
  // A session with no worktree of its own works in its Git folder: the engine compares
  // the folder as it is now with its last commit, and the view says so in those words.
  const folder = !run.worktree_branch
  const sides = folder
    ? {
        original: t('branchChanges.folder.original'),
        modified: t('branchChanges.folder.modified'),
      }
    : undefined
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
  // A branch's two commits fix what its diff shows: a reviewed mark holds for
  // them. A folder's other side is the folder as it is now: a mark holds only
  // while the folder's read says when it last wrote the file and its size.
  // Without them the row cannot tell the file is unchanged and shows no mark;
  // the diff holds the mark for its two texts.
  const written = useFolderChanges(run, folder).data?.files
  const revision = (path: string | null) => {
    const row = data?.files.find((f) => f.path === path)
    if (!folder) return JSON.stringify([data?.base, data?.head, row])
    const at = written?.find((f) => f.path === path)
    return at ? JSON.stringify([data?.base, row, at]) : null
  }
  // A Git folder the engine cannot compare with a commit (none yet, say) answers 409:
  // said plainly, not as a failure, and asked again while the session works, since the
  // agent may commit in its own shell.
  const reason =
    folder && diff.error instanceof ApiError && diff.error.status === 409
      ? diff.error.code
      : null
  // A branch name renders through PathText, as every path and branch does: cut in the
  // middle when long, whole on hover, copied whole.
  const sinceBranch = data?.branch ? (
    <Trans
      t={t}
      i18nKey="branchChanges.folder.since"
      components={{ branch: <PathText value={data.branch} /> }}
    />
  ) : null
  const compared = !data
    ? null
    : !folder
      ? t('branchChanges.range', {
          base: data.base.slice(0, SHORT_ID),
          head: data.head.slice(0, SHORT_ID),
        })
      : data.branch
        ? sinceBranch
        : t('branchChanges.folder.sinceCommit', {
            base: data.base.slice(0, SHORT_ID),
          })
  const [reviewed, setReviewed] = useReviewed(
    `${run.run_ref}:branch:${open}`,
    revision(open) ?? JSON.stringify(file.data ?? null),
  )

  return (
    <section
      data-testid="session-branch-changes"
      className="flex flex-col gap-2"
    >
      <div>
        <h3 className="text-caption font-medium text-foreground">
          {folder ? t('branchChanges.folder.title') : t('branchChanges.title')}
        </h3>
        {folder ? null : (
          <p className="text-caption text-muted-foreground">
            {t('branchChanges.hint')}
          </p>
        )}
        {compared ? (
          <p
            data-testid="branch-changes-range"
            className={cn(
              'text-caption text-muted-foreground',
              folder ? 'break-words' : 'break-all font-mono',
            )}
          >
            {compared}
          </p>
        ) : null}
      </div>
      {open !== null ? (
        <DiffDialog title={open} onClose={() => openFile(null)}>
          <Button
            variant="ghost"
            size="sm"
            className="self-start"
            onClick={() => openFile(null)}
          >
            <ArrowLeft className="size-3.5" />
            {folder ? t('changes.back') : t('branchChanges.back')}
          </Button>
          <PathText value={open} wrap />
          {run.workspace_path && file.data && !file.data.binary ? (
            <OpenInEditor
              root={run.workspace_path}
              file={open}
              line={firstChangedLine(file.data.original, file.data.modified)}
            />
          ) : null}
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
              <label className="flex items-center gap-2 text-caption">
                <Checkbox
                  checked={reviewed}
                  onCheckedChange={(checked) => setReviewed(checked === true)}
                />
                {t('shared:diff.reviewed')}
              </label>
              <CodeDiff
                original={file.data.original}
                modified={file.data.modified}
                language="text"
                originalLabel={sides?.original ?? t('branchChanges.original')}
                modifiedLabel={sides?.modified ?? t('branchChanges.modified')}
                onLineClick={(side, line) =>
                  setPlace({ side, line: String(line) })
                }
              />
              {file.data.truncated ? (
                <p className="text-caption text-muted-foreground">
                  {folder
                    ? t('branchChanges.folder.fileTruncated')
                    : t('branchChanges.fileTruncated')}
                </p>
              ) : null}
              <LineCommentForm
                run={run}
                path={open}
                original={file.data.original}
                modified={file.data.modified}
                place={place}
                onPlace={setPlace}
                comparison={sides}
              />
            </>
          )}
        </DiffDialog>
      ) : null}
      {diff.isPending ? (
        <p className="text-caption text-muted-foreground">
          {folder ? t('changes.loading') : t('branchChanges.loading')}
        </p>
      ) : reason !== null ? (
        <p className="text-caption text-muted-foreground">
          {t(CANNOT_COMPARE[reason] ?? 'branchChanges.folder.cannotCompare')}
        </p>
      ) : diff.isError ? (
        <p className="text-caption text-danger">
          {folder ? t('changes.error') : t('branchChanges.error')}
        </p>
      ) : data!.files.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {folder ? t('branchChanges.folder.empty') : t('branchChanges.empty')}
        </p>
      ) : (
        <ul
          hidden={open !== null}
          className="flex flex-col"
          data-testid="branch-changes-list"
        >
          {data!.files.map((f) => (
            <li key={f.path}>
              <button
                type="button"
                onClick={() => openFile(f.path)}
                className="flex w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
              >
                <FileTypeIcon
                  path={f.path}
                  className="size-3.5 shrink-0 text-muted-foreground"
                />
                <PathText value={f.path} copy={false} className="flex-1" />
                <LineCounts file={f} />
                <ReviewedMark
                  id={`${run.run_ref}:branch:${f.path}`}
                  revision={revision(f.path)}
                />
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

/** The files the session wrote in its folder, newest first, read again while it
 * works; not read at all while `enabled` is false. */
export function useFolderChanges(run: RunDTO, enabled = true) {
  const { activeTenant } = useAuth()
  const working = run.state === 'running' || run.state === 'idle'
  return useQuery({
    queryKey: agentOpsKeys.runChanges(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.changes(run.run_ref, { signal }),
    enabled,
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
}

/** «Reviewed» on a changed file's row, once the person marked it so in its
 * diff; nothing where the row cannot tell the file is unchanged (null). */
export function ReviewedMark({
  id,
  revision,
}: {
  id: string
  revision: string | null
}) {
  const { t } = useTranslation('sessions')
  const [reviewed] = useReviewed(id, revision ?? '')
  return revision !== null && reviewed ? (
    <Badge variant="neutral">{t('shared:diff.reviewed')}</Badge>
  ) : null
}

/** The lines a changed file gained and lost, as `+N −M`; nothing when not counted. */
export function LineCounts({
  file,
}: {
  file: { added?: number; removed?: number }
}) {
  if (file.added === undefined && file.removed === undefined) return null
  return (
    <span className="flex shrink-0 gap-1 text-caption tabular-nums">
      {file.added ? <span className="text-success">+{file.added}</span> : null}
      {file.removed ? (
        <span className="text-danger">−{file.removed}</span>
      ) : null}
    </span>
  )
}

/**
 * Sends a comment on one line of the open file back to the session as its next message:
 * the run's input route, with the place beside it so the ledger records it. A disabled
 * Send says why (the session is not running, no line is chosen, the file has no such
 * line, nothing is written). A refused send keeps what was typed.
 */
export function LineCommentForm({
  run,
  path,
  original,
  modified,
  place,
  onPlace,
  comparison,
}: {
  run: RunDTO
  path: string
  original: string
  modified: string
  place: CommentPlace
  comparison?: { original: string; modified: string }
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
  const version =
    comparison?.[place.side] ??
    t(
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
        reviewCommentText(
          where,
          lineText ?? '',
          comment,
          comparison?.[place.side],
        ),
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
            className="h-9 rounded-md border border-ctl-border bg-surface px-2 text-caption text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="modified">
              {comparison?.modified ?? t('branchChanges.modified')}
            </option>
            <option value="original">
              {comparison?.original ?? t('branchChanges.original')}
            </option>
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
