// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { GitBranch, RefreshCw } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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
import { useAuth } from '@/lib/auth/context'
import { currentControlFence } from '@/features/agentops/work-fence'
import { useStepUpOwner, type StepUpAttempt } from '@/stores/step-up'
import './i18n'

export function SessionGit({ run }: { run: RunDTO }) {
  const boundary = useAuthBoundary()
  // Discard a draft and pending UI when the run or authenticated actor changes.
  return (
    <SessionGitPanel
      key={`${boundary.epoch}:${run.run_ref}`}
      run={run}
      boundary={boundary}
    />
  )
}

function SessionGitPanel({
  run,
  boundary,
}: {
  run: RunDTO
  boundary: AuthBoundary
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const client = useQueryClient()
  const captureOwner = useStepUpOwner()
  const id = useId()
  const [message, setMessage] = useState('')
  const [branchName, setBranchName] = useState('')
  const [creating, setCreating] = useState(false)
  const key = [
    ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
    'git',
    run.run_ref,
  ]
  const status = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => agentOpsApi.gitStatus(run.run_ref, { signal }),
    enabled: !!activeTenant && !!run.workspace_path && run.state !== 'cleaned',
    refetchInterval:
      run.state === 'running' || run.state === 'idle' ? 5_000 : false,
    retry: false,
  })
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
      if (!can('sessions:run:write') || !status.data?.writable)
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
      if (action === 'commit') setMessage('')
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
  const data = status.data
  const staged =
    data?.files.some((f) => f.index !== ' ' && f.index !== '?') ?? false
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
      {status.isError ? (
        <p role="alert" className="text-caption text-destructive">
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
            <p className="text-caption text-muted-foreground">
              {t('git.readOnly')}
            </p>
          ) : null}
          {!data.files.length ? (
            <p className="text-caption text-muted-foreground">
              {t('git.clean')}
            </p>
          ) : (
            <ul className="max-h-60 overflow-y-auto">
              {data.files.map((file) => (
                <li
                  key={file.path}
                  className="flex min-w-0 items-center gap-1 border-b border-border py-1"
                >
                  <span
                    className="shrink-0 font-mono text-caption"
                    aria-label={t('git.fileStatus', {
                      index: file.index,
                      worktree: file.worktree,
                    })}
                  >
                    {file.index}
                    {file.worktree}
                  </span>
                  <span
                    className="min-w-0 flex-1 break-all font-mono text-caption"
                    title={file.path}
                  >
                    {file.path}
                  </span>
                  {file.index !== ' ' && file.index !== '?' ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={disabled}
                      aria-label={t('git.unstage', { path: file.path })}
                      onClick={() => act('unstage', { paths: [file.path] })}
                    >
                      {t('git.unstageButton')}
                    </Button>
                  ) : null}
                  {file.worktree !== ' ' ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={disabled}
                      aria-label={t('git.stage', { path: file.path })}
                      onClick={() => act('stage', { paths: [file.path] })}
                    >
                      {t('git.stageButton')}
                    </Button>
                  ) : null}
                </li>
              ))}
            </ul>
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
            <label htmlFor={`${id}-message`} className="text-caption">
              {t('git.commitMessage')}
            </label>
            <Input
              id={`${id}-message`}
              value={message}
              maxLength={8192}
              disabled={disabled}
              onChange={(e) => setMessage(e.target.value)}
            />
            <p className="text-caption text-muted-foreground">
              {t('git.commitHint')}
            </p>
            <Button
              size="sm"
              type="submit"
              className="self-start"
              disabled={disabled || !staged || !message.trim()}
            >
              {mutation.isPending ? t('git.working') : t('git.commit')}
            </Button>
          </form>
        </>
      ) : null}
    </section>
  )
}
