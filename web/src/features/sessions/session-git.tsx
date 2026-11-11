// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { GitBranch, Minus, Plus, RefreshCw } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { DisabledReason } from '@/components/ui/disabled-reason'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import { PathText } from '@/components/ui/path-text'
import { Textarea } from '@/components/ui/textarea'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import {
  useAuthBoundary,
  type AuthBoundary,
} from '@/features/agentops/auth-boundary'
import type {
  RunDTO,
  RunGitAction,
  RunGitRequest,
} from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { chordHint } from '@/lib/keybindings/model'
import { currentControlFence } from '@/features/agentops/work-fence'
import { useStepUpOwner, type StepUpAttempt } from '@/stores/step-up'
import { SessionPush, type GitStatus } from './session-push'
import './i18n'

/** Where the Git panel sits: `panel` is the Changes pane's, which also pushes and opens the
 * pull request; `lane` is a race session's, whose commit is merged into the session, never
 * published; `init` is the header's, only an Initialize Git button where the folder is not
 * a repository, with the panel's read and write path. */
type GitVariant = 'panel' | 'lane' | 'init'

/** The engine's 404 for a session folder that is not a Git repository, when it names it:
 * a state no poll changes, so the status is not read again by itself. */
const notRepository = (error: unknown) =>
  error instanceof ApiError && error.code === 'not_a_git_repository'

/** The session folder's Git status: one read that the panel, the header's button and the
 * Changes pane share through its cache. `follow` re-reads it while the session works;
 * `cached` only sees what another reader fetched, and asks for nothing itself. */
export function useRunGit(
  run: RunDTO | undefined,
  epoch: number,
  read: 'once' | 'follow' | 'cached',
) {
  const { activeTenant } = useAuth()
  return useQuery({
    queryKey: agentOpsKeys.runGit(activeTenant, epoch, run?.run_ref ?? ''),
    queryFn: ({ signal }): Promise<GitStatus> =>
      agentOpsApi.gitStatus(run!.run_ref, { signal }),
    enabled:
      read !== 'cached' &&
      !!activeTenant &&
      !!run?.workspace_path &&
      run?.state !== 'cleaned',
    refetchInterval: (query) =>
      read === 'follow' &&
      (run?.state === 'running' || run?.state === 'idle') &&
      !notRepository(query.state.error)
        ? 5_000
        : false,
    retry: false,
  })
}

/** The session folder's Git, in one of its places (`GitVariant`). */
export function SessionGit({
  run,
  variant = 'panel',
}: {
  run: RunDTO
  variant?: GitVariant
}) {
  const boundary = useAuthBoundary()
  // Discard a draft and pending UI when the run or authenticated actor changes.
  return (
    <SessionGitPanel
      key={`${boundary.epoch}:${run.run_ref}`}
      run={run}
      boundary={boundary}
      variant={variant}
    />
  )
}

function SessionGitPanel({
  run,
  boundary,
  variant,
}: {
  run: RunDTO
  boundary: AuthBoundary
  variant: GitVariant
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const client = useQueryClient()
  const captureOwner = useStepUpOwner()
  const id = useId()
  const [message, setMessage] = useState('')
  const [branchName, setBranchName] = useState('')
  const [creating, setCreating] = useState(false)
  // The subject of the commit made here: the pull request's title starts from it.
  const [subject, setSubject] = useState('')
  const key = agentOpsKeys.runGit(activeTenant, boundary.epoch, run.run_ref)
  // The header's button reads once; the panel and a race session's panel follow a live session.
  const status = useRunGit(
    run,
    boundary.epoch,
    variant === 'init' ? 'once' : 'follow',
  )
  // A turn that ends may have changed the folder: its Git status, and the uncommitted
  // changes the Changes view lists, are read at once, not on the next poll. The engine
  // shows a session that answered its turn as idle.
  const turnState = useRef(run.state)
  useEffect(() => {
    const ended = turnState.current === 'running' && run.state !== 'running'
    turnState.current = run.state
    if (!ended) return
    for (const queryKey of [
      agentOpsKeys.runGit(activeTenant, boundary.epoch, run.run_ref),
      agentOpsKeys.runWorktreeDiff(activeTenant, run.run_ref),
    ])
      void client.invalidateQueries(
        { queryKey },
        // The header and the panel both follow the turn: a read in flight is kept.
        { cancelRefetch: false },
      )
  }, [run.state, run.run_ref, activeTenant, boundary.epoch, client])
  const mutation = useMutation({
    mutationKey: [...key, 'action'],
    mutationFn: async ({
      action,
      body,
      attempt,
    }: {
      action: RunGitAction
      body: RunGitRequest
      attempt: StepUpAttempt
    }) => {
      attempt.dispatchGuard()
      // A folder with no repository has no status to say it is writable; the engine
      // refuses an init the folder does not permit.
      if (
        !can('sessions:run:write') ||
        (action !== 'init' && !status.data?.writable)
      )
        throw new Error(t('git.readOnly'))
      const options = {
        tenant: activeTenant,
        signal: attempt.signal,
        dispatchGuard: attempt.dispatchGuard,
        sessionEffects: attempt.sessionEffects,
      }
      const fence = await currentControlFence(run, options)
      attempt.dispatchGuard()
      return agentOpsApi.gitAction(
        run.run_ref,
        action,
        fence === undefined ? body : { ...body, work_lease_fence: fence },
        options,
      )
    },
    onSuccess: async (_answer, { action, body, attempt }) => {
      if (!attempt.current()) return
      if (action === 'commit') {
        setMessage('')
        setSubject(body.message?.split('\n')[0] ?? '')
      }
      if (action === 'branch' && body.create) {
        setBranchName('')
        setCreating(false)
      }
      await Promise.all([
        client.invalidateQueries({ queryKey: key }),
        client.invalidateQueries({
          queryKey: agentOpsKeys.runChanges(activeTenant, run.run_ref),
        }),
        client.invalidateQueries({
          queryKey: agentOpsKeys.runWorktreeDiff(activeTenant, run.run_ref),
        }),
      ])
    },
  })
  const writable = can('sessions:run:write') && status.data?.writable === true
  const disabled = !writable || mutation.isPending || status.isError
  const act = (action: RunGitAction, body: RunGitRequest) =>
    mutation.mutate({ action, body, attempt: captureOwner().begin() })
  if (!run.workspace_path || run.state === 'cleaned') return null
  // The engine answers 404 for a session folder that is not a Git repository.
  const noRepository =
    status.error instanceof ApiError && status.error.status === 404
  const initButton =
    noRepository && can('sessions:run:write') ? (
      <Button
        variant="secondary"
        size="sm"
        disabled={mutation.isPending}
        onClick={() => act('init', {})}
      >
        <GitBranch className="size-3.5" aria-hidden="true" />
        {t('git.init')}
      </Button>
    ) : null
  if (variant === 'init') return initButton
  const data = status.data
  const stagedFiles =
    data?.files.filter((f) => f.index !== ' ' && f.index !== '?') ?? []
  const staged = stagedFiles.length > 0
  // Why Commit cannot run, the first that applies. What the panel already says (the person
  // may not write, the status failed) describes it where it stands, said once. While a
  // request runs it says Working… and needs no reason.
  const saidAbove = !writable
    ? `${id}-read-only`
    : status.isError
      ? `${id}-status`
      : undefined
  const commitReason =
    mutation.isPending || saidAbove
      ? undefined
      : !staged
        ? t('git.nothingStaged')
        : !message.trim()
          ? t('git.noMessage')
          : undefined
  return (
    <section
      className="flex min-w-0 flex-col gap-2 py-2"
      aria-labelledby={`${id}-title`}
    >
      <div className="flex items-center justify-between gap-2">
        <h3
          id={`${id}-title`}
          className="flex items-center gap-1.5 text-caption font-medium"
        >
          <GitBranch className="size-3.5" aria-hidden="true" />
          {t('git.title')}
        </h3>
        <Button
          variant="ghost"
          size="sm"
          aria-label={t('git.refresh')}
          disabled={status.isFetching || mutation.isPending}
          onClick={() => void status.refetch()}
        >
          <RefreshCw className="size-3.5" aria-hidden="true" />
        </Button>
      </div>
      {status.isPending ? (
        <p className="text-caption text-muted-foreground" role="status">
          {t('git.loading')}
        </p>
      ) : null}
      {noRepository ? (
        <div className="flex flex-wrap items-center gap-2">
          <p id={`${id}-status`} className="text-caption text-muted-foreground">
            {notRepository(status.error)
              ? t('git.noRepositoryChanges')
              : t('git.noRepository')}
          </p>
          {initButton}
        </div>
      ) : status.isError ? (
        <p
          id={`${id}-status`}
          role="alert"
          className="text-caption text-destructive"
        >
          {status.error.message}
        </p>
      ) : null}
      {mutation.isError ? (
        <p role="alert" className="text-caption text-destructive">
          {mutation.error.message}
        </p>
      ) : null}
      {data ? (
        <>
          <div className="flex items-end gap-2">
            <div className="min-w-0 flex-1">
              <label
                htmlFor={`${id}-branch`}
                className="text-caption text-muted-foreground"
              >
                {t('git.branch')}
              </label>
              <select
                id={`${id}-branch`}
                value={data.branch}
                disabled={disabled}
                onChange={(e) =>
                  act('branch', { name: e.target.value, create: false })
                }
                className="h-8 w-full min-w-0 rounded-ctl border border-ctl-border bg-canvas px-2 text-body focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
              >
                {!data.branch ? (
                  <option value="">{t('git.detached')}</option>
                ) : null}
                {data.branch && !data.branches.includes(data.branch) ? (
                  <option value={data.branch}>{data.branch}</option>
                ) : null}
                {data.branches.map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
              </select>
            </div>
            <Button
              variant="ghost"
              size="sm"
              disabled={disabled}
              onClick={() => setCreating(!creating)}
            >
              {t('git.newBranch')}
            </Button>
          </div>
          {creating ? (
            <form
              className="flex flex-col gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                if (!disabled && branchName.trim())
                  act('branch', { name: branchName.trim(), create: true })
              }}
            >
              <label htmlFor={`${id}-branch-name`} className="text-caption">
                {t('git.branchName')}
              </label>
              <Input
                id={`${id}-branch-name`}
                value={branchName}
                maxLength={512}
                disabled={disabled}
                onChange={(e) => setBranchName(e.target.value)}
              />
              <div className="flex gap-2">
                <Button
                  size="sm"
                  type="submit"
                  disabled={disabled || !branchName.trim()}
                >
                  {t('git.createBranch')}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  type="button"
                  onClick={() => setCreating(false)}
                >
                  {t('git.cancel')}
                </Button>
              </div>
            </form>
          ) : null}
          {!writable ? (
            <p
              id={`${id}-read-only`}
              className="text-caption text-muted-foreground"
            >
              {t('git.readOnly')}
            </p>
          ) : null}
          {!data.files.length ? (
            <p className="text-caption text-muted-foreground">
              {t('git.clean')}
            </p>
          ) : (
            <>
              <GitFiles
                label={t('git.staged')}
                files={stagedFiles}
                action="unstage"
                disabled={disabled}
                onAct={(path) => act('unstage', { paths: [path] })}
              />
              <GitFiles
                label={t('git.unstaged')}
                files={data.files.filter((f) => f.worktree !== ' ')}
                action="stage"
                disabled={disabled}
                onAct={(path) => act('stage', { paths: [path] })}
              />
            </>
          )}
          {data.truncated ? (
            <p className="text-caption text-muted-foreground">
              {t('git.truncated')}
            </p>
          ) : null}
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              if (!disabled && staged && message.trim())
                act('commit', { message })
            }}
          >
            <Field
              label={t('git.commitMessage')}
              htmlFor={`${id}-message`}
              description={t('git.commitHint')}
            >
              <Textarea
                value={message}
                rows={2}
                maxLength={8192}
                disabled={disabled}
                onChange={(e) => setMessage(e.target.value)}
                aria-keyshortcuts="Control+Enter Meta+Enter"
                // Enter starts a new line; Ctrl or ⌘ with Enter commits.
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                    e.preventDefault()
                    e.currentTarget.form?.requestSubmit()
                  }
                }}
              />
            </Field>
            <DisabledReason
              mode="native"
              disabled={!!commitReason}
              reason={commitReason ?? t('git.commit')}
            >
              <Button
                size="sm"
                type="submit"
                // While the folder has uncommitted changes, Commit is the next step.
                variant={data.files.length ? 'primary' : 'secondary'}
                className="self-start"
                disabled={mutation.isPending || !!saidAbove}
                aria-describedby={saidAbove}
                aria-keyshortcuts="Control+Enter Meta+Enter"
              >
                {mutation.isPending ? t('git.working') : t('git.commit')}
                {/* Drawn, not text: the button's name stays Commit (WCAG 2.5.3).
                    A key inside a button takes the button's ink, and a disabled
                    button fades as a whole: the chip dims with Commit. */}
                <Kbd
                  aria-hidden
                  data-keys={chordHint('Mod+Enter')}
                  className="before:content-[attr(data-keys)]"
                />
              </Button>
            </DisabledReason>
          </form>
          {variant === 'panel' ? (
            <SessionPush
              run={run}
              status={data}
              uncommitted={data.files.length > 0}
              subject={subject}
            />
          ) : null}
        </>
      ) : null}
    </section>
  )
}

/** One of the panel's two lists of the folder's files: what is staged, each with Unstage,
 * or what is not, each with Stage. A file changed in both is in both; an empty list is not
 * shown. */
function GitFiles({
  label,
  files,
  action,
  disabled,
  onAct,
}: {
  label: string
  files: GitStatus['files']
  action: 'stage' | 'unstage'
  disabled: boolean
  onAct: (path: string) => void
}) {
  const { t } = useTranslation('sessions')
  const id = useId()
  if (!files.length) return null
  const Icon = action === 'stage' ? Plus : Minus
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <h4 id={id} className="text-caption font-medium">
        {label}{' '}
        <span className="font-normal text-muted-foreground tabular-nums">
          {files.length}
        </span>
      </h4>
      <BoxedList
        role="list"
        aria-labelledby={id}
        className="max-h-60 overflow-y-auto"
      >
        {files.map((file) => (
          <BoxedListRow key={file.path} role="listitem">
            <span
              className="shrink-0 font-mono text-caption"
              aria-label={t('git.fileStatus', {
                index: file.index,
                worktree: file.worktree,
              })}
            >
              {action === 'stage' ? file.worktree : file.index}
            </span>
            <PathText value={file.path} copy={false} className="flex-1" />
            <Button
              variant="ghost"
              size="icon-sm"
              disabled={disabled}
              aria-label={
                action === 'stage'
                  ? t('git.stage', { path: file.path })
                  : t('git.unstage', { path: file.path })
              }
              onClick={() => onAct(file.path)}
            >
              <Icon aria-hidden="true" />
            </Button>
          </BoxedListRow>
        ))}
      </BoxedList>
    </div>
  )
}
