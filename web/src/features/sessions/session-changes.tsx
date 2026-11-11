// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE SESSION CHANGED, beside its conversation (TARGET §2, the Conductor concept): the
// files in the session's folder that changed since it started, newest first, and the text of
// one of them on click. The engine reads the folder without running anything in it
// (modules/sessions/run_changes.go).
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Checkbox } from '@/components/ui/checkbox'
import { Button } from '@/components/ui/button'
import { FileTypeIcon } from '@/components/ui/file-type-icon'
import { PathText } from '@/components/ui/path-text'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { DiffDialog } from '@/components/ui/diff-dialog'
import {
  LineCommentForm,
  LineCounts,
  SessionBranchChanges,
  type CommentPlace,
} from './session-branch-changes'
import { CodeDiff } from '@/components/ui/code-diff'
import { headUnavailable } from '@/features/shared/head-unavailable'
import { OpenInEditor } from '@/features/shared/open-in-editor'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { firstChangedLine } from '@/lib/editor-links'
import { ApiError } from '@/lib/api/errors'
import { formatBytes } from '@/lib/format'
import { useReviewed } from '@/stores/preferences'
import { TurnCheckpoints } from './turn-checkpoints'
import './i18n'

/** While the agent works the list follows it; a finished session does not change. */
const WORKING_REFRESH_MS = 5_000

export function SessionChanges({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const [open, setOpen] = useState<string | null>(null)
  const [place, setPlace] = useState<CommentPlace>({
    side: 'modified',
    line: '',
  })
  const openFile = (path: string | null) => {
    setOpen(path)
    setPlace({ side: 'modified', line: '' })
  }
  const [scope, setScope] = useState('session')
  const working = run.state === 'running' || run.state === 'idle'
  const changes = useQuery({
    queryKey: agentOpsKeys.runChanges(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.changes(run.run_ref, { signal }),
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const checkpoints = useQuery({
    queryKey: agentOpsKeys.runCheckpoints(activeTenant, run.run_ref),
    queryFn: ({ signal }) =>
      agentOpsApi.runCheckpoints(run.run_ref, { signal }),
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  // A session with no worktree of its own whose folder is a Git repository: the engine
  // compares the folder with its last commit (`working_tree`), or answers 409 while the
  // folder has no commit yet. A folder that is not a repository (404) offers nothing more.
  const folderDiff = useQuery({
    queryKey: agentOpsKeys.runWorktreeDiff(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.worktreeDiff(run.run_ref, { signal }),
    enabled: !run.worktree_branch && !!run.workspace_path,
  })
  const uncommitted =
    folderDiff.data?.working_tree === true ||
    (folderDiff.error instanceof ApiError && folderDiff.error.status === 409)
  const git = useQuery({
    queryKey: agentOpsKeys.runGit(activeTenant, boundary.epoch, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.gitStatus(run.run_ref, { signal }),
    enabled: scope === 'working',
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const gitFile = git.data?.files.filter((f) => f.path === open).at(-1)
  const emptyFile = {
    path: open!,
    size: 0,
    text: '',
    binary: false,
    truncated: false,
  }
  const turns = checkpoints.data?.available ? checkpoints.data.turns : []
  const turn =
    scope === 'latest'
      ? turns.at(-1)
      : turns.find((t) => String(t.seq) === scope)
  const baseline = scope === 'session' ? turns[0] : turn
  const tip = scope === 'session' ? turns.at(-1) : turn
  const scopedFiles = turn
    ? turn.files
    : scope === 'session' && turns.length
      ? checkpoints.data?.files
      : undefined
  const file = useQuery({
    queryKey: [
      ...agentOpsKeys.runChanges(activeTenant, run.run_ref),
      'file',
      scope,
      scope === 'working' ? gitFile : undefined,
      baseline?.seq,
      tip?.seq,
      open,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi
        .changedFile(run.run_ref, open!, {
          signal,
          ...(tip
            ? {
                turn: tip.seq,
                side:
                  scope === 'session' ? ('current' as const) : ('end' as const),
              }
            : {}),
        })
        .catch((error: unknown) => {
          if (
            scope === 'working' &&
            (gitFile?.index === 'D' || gitFile?.worktree === 'D') &&
            error instanceof ApiError &&
            error.status === 404
          )
            return emptyFile
          throw error
        }),
    enabled: open !== null,
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  // Compare the selected snapshot range; older sessions without checkpoints use HEAD.
  // Binary and cut-off files cannot be compared.
  const head = useQuery({
    queryKey: [
      ...agentOpsKeys.runChanges(activeTenant, run.run_ref),
      'file',
      scope,
      scope === 'working' ? gitFile : undefined,
      baseline?.seq,
      tip?.seq,
      open,
      'head',
    ],
    queryFn: ({ signal }) =>
      agentOpsApi
        .changedFile(run.run_ref, open!, {
          signal,
          ...(baseline
            ? { turn: baseline.seq, side: 'start' as const }
            : { rev: 'HEAD' as const }),
        })
        .catch((error: unknown) => {
          if (
            scope === 'working' &&
            gitFile?.index === '?' &&
            error instanceof ApiError &&
            error.status === 404
          )
            return emptyFile
          throw error
        }),
    enabled:
      open !== null &&
      file.data !== undefined &&
      !file.data.binary &&
      !file.data.truncated,
    refetchOnWindowFocus: false,
  })
  const data = changes.data
  const files =
    scope === 'working'
      ? [...new Map((git.data?.files ?? []).map((f) => [f.path, f])).values()]
      : (scopedFiles ?? data?.files ?? [])
  const [reviewed, setReviewed] = useReviewed(
    `${run.run_ref}:${scope}:${open}`,
    JSON.stringify([head.data, file.data]),
  )
  // The diff and editor link share the same baseline.
  const committed =
    file.data &&
    !file.data.truncated &&
    head.data &&
    !head.data.binary &&
    !head.data.truncated
      ? head.data.text
      : undefined
  const firstChange =
    file.data && !file.data.binary
      ? firstChangedLine(committed, file.data.text)
      : undefined
  // The folder Olivares created for the run is named by the run's id: no name to show.
  const title =
    data?.folder && data.folder !== run.run_ref
      ? t('changes.title', { folder: data.folder })
      : t('changes.titleNoFolder')

  const scopeControl = (
    <select
      aria-label={t('changes.scope')}
      value={scope}
      onChange={(event) => {
        setScope(event.target.value)
        openFile(null)
      }}
      className="min-w-0 rounded-md border border-ctl-border bg-background p-1 text-caption"
    >
      <option value="session">{t('changes.session')}</option>
      <option value="working">{t('shared:diff.working')}</option>
      {run.worktree_branch ? (
        <option value="branch">{t('shared:diff.branch')}</option>
      ) : uncommitted ? (
        <option value="branch">{t('branchChanges.folder.title')}</option>
      ) : null}
      <option value="latest" disabled={!turns.length}>
        {t('changes.latest')}
      </option>
      {[...turns].reverse().map((turn) => (
        <option key={turn.seq} value={turn.seq}>
          {t('checkpoints.turn', { n: turn.seq })}
        </option>
      ))}
    </select>
  )

  return (
    <section data-testid="session-changes" className="flex flex-col gap-2">
      <div>
        <h3 className="text-caption font-medium text-foreground">{title}</h3>
        <p className="text-caption text-muted-foreground">
          {t('changes.hint')}
        </p>
      </div>
      {open === null ? scopeControl : null}
      {checkpoints.isError ? (
        <p role="status" className="text-caption text-danger">
          {t('changes.error')}
        </p>
      ) : null}
      {open === null && scope === 'session' ? (
        <TurnCheckpoints run={run} />
      ) : null}
      {scope === 'branch' ? (
        <SessionBranchChanges key={run.run_ref} run={run} />
      ) : (
        <>
          {open !== null ? (
            <DiffDialog title={open} onClose={() => openFile(null)}>
              {scopeControl}
              <div className="flex flex-wrap items-center justify-between gap-x-2">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => openFile(null)}
                >
                  <ArrowLeft className="size-3.5" />
                  {t('changes.back')}
                </Button>
                {run.workspace_path ? (
                  <OpenInEditor
                    root={run.workspace_path}
                    file={open}
                    line={firstChange}
                  />
                ) : null}
              </div>
              <PathText value={open} wrap />
              {file.isPending ? (
                <p className="text-caption text-muted-foreground">
                  {t('changes.loading')}
                </p>
              ) : file.isError ? (
                <p className="text-caption text-danger">{t('changes.error')}</p>
              ) : file.data.binary || head.data?.binary ? (
                <p className="text-caption text-muted-foreground">
                  {tip
                    ? t('branchChanges.binary')
                    : t('changes.binary', {
                        size: formatBytes(file.data.size),
                      })}
                </p>
              ) : (
                <>
                  {committed !== undefined ? (
                    <>
                      <label className="flex items-center gap-2 text-caption">
                        <Checkbox
                          checked={reviewed}
                          onCheckedChange={(checked) =>
                            setReviewed(checked === true)
                          }
                        />
                        {t('shared:diff.reviewed')}
                      </label>
                      <CodeDiff
                        original={committed}
                        modified={file.data.text}
                        language="text"
                        originalLabel={baseline ? t('changes.before') : 'HEAD'}
                        modifiedLabel={t('changes.after')}
                        height="55vh"
                        onLineClick={(side, line) =>
                          setPlace({ side, line: String(line) })
                        }
                      />
                      <LineCommentForm
                        key={`${scope}:${open}`}
                        run={run}
                        path={open}
                        original={committed}
                        modified={file.data.text}
                        place={place}
                        onPlace={setPlace}
                        comparison={{
                          original: baseline
                            ? `${t('checkpoints.turn', { n: baseline.seq })} · ${t('changes.before')}`
                            : 'HEAD',
                          modified:
                            tip && scope !== 'session'
                              ? `${t('checkpoints.turn', { n: tip.seq })} · ${t('changes.after')}`
                              : t('shared:diff.working'),
                        }}
                      />
                    </>
                  ) : (
                    <>
                      <pre
                        data-testid="changes-file"
                        className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border bg-muted p-2 font-mono text-caption text-foreground"
                      >
                        {file.data.text}
                      </pre>
                      {(
                        baseline ? head.isError : headUnavailable(head.error)
                      ) ? (
                        <p role="status" className="text-caption text-danger">
                          {t('changes.error')}
                        </p>
                      ) : null}
                    </>
                  )}
                  {file.data.truncated || head.data?.truncated ? (
                    <p className="text-caption text-muted-foreground">
                      {t('changes.fileTruncated')}
                    </p>
                  ) : null}
                </>
              )}
            </DiffDialog>
          ) : null}
          {changes.isPending ||
          checkpoints.isPending ||
          (scope === 'working' && git.isPending) ? (
            <p className="text-caption text-muted-foreground">
              {t('changes.loading')}
            </p>
          ) : changes.isError || (scope === 'working' && git.isError) ? (
            <p className="text-caption text-danger">{t('changes.error')}</p>
          ) : !data?.folder ? (
            <p className="text-caption text-muted-foreground">
              {t('changes.none')}
            </p>
          ) : files.length === 0 ? (
            <p className="text-caption text-muted-foreground">
              {t('changes.empty')}
            </p>
          ) : (
            <ul
              hidden={open !== null}
              className="flex flex-col"
              data-testid="changes-list"
            >
              {files.map((f) => (
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
                    {'status' in f ? <LineCounts file={f} /> : null}
                    {'modified_at' in f ? (
                      <RelTimeLabel
                        ts={f.modified_at}
                        className="shrink-0 text-caption text-muted-foreground"
                      />
                    ) : null}
                  </button>
                </li>
              ))}
              {(turn?.truncated ??
              (turns.length
                ? checkpoints.data?.truncated
                : data?.truncated)) ? (
                <li className="px-1.5 pt-1 text-caption text-muted-foreground">
                  {t('changes.truncated', { count: files.length })}
                </li>
              ) : null}
            </ul>
          )}
        </>
      )}
    </section>
  )
}
