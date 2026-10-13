// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useWorkspaceStore } from '@/stores/workspace'
import { skillsApi, skillsKeys } from './api'
import type {
  SkillInstallRequest,
  SkillInstallResult,
  SkillPack,
} from './types'

interface InstallIntent {
  input: SkillInstallRequest
  workspace: string
  key: string
  // Retained on the intent itself so assurance continuation and explicit retries
  // both reuse the published revision after an assignment failure.
  result?: SkillInstallResult
  // The published revision carries skills that act when loaded: it is installed
  // and left for the operator to review and assign with approval.
  held?: boolean
}
const directoryPage = { limit: 1000 }
const selectClass =
  'h-9 w-full min-w-0 rounded-md border border-ctl-border bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'

export function InstallPack({ packs }: { packs: SkillPack[] }) {
  const { t } = useTranslation('skills')
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const canInstall = can('skills:catalog:write')
  const canAssign = can('skills:assignment:write') && can('tenant:read')
  const activeWorkspace = useWorkspaceStore((s) => s.activeWorkspace)
  const [chosenWorkspace, setChosenWorkspace] = useState<string | null>(null)
  const [gitURL, setGitURL] = useState('')
  const [packName, setPackName] = useState('')
  const [ref, setRef] = useState('')
  const [subdir, setSubdir] = useState('')
  const [intent, setIntent] = useState<InstallIntent | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [done, setDone] = useState<string | null>(null)
  const builtins = useQuery({
    queryKey: skillsKeys.builtins(activeTenant),
    queryFn: ({ signal }) =>
      skillsApi.builtins({ tenant: activeTenant, signal }),
  })
  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant, directoryPage),
    queryFn: () => consoleApi.listWorkspaces(directoryPage),
    enabled: canInstall && canAssign,
  })
  const targets = workspaces.data?.items ?? []
  const defaultWorkspace =
    targets.find((w) => w.id === activeWorkspace)?.id ??
    (targets.length === 1 ? targets[0].id : '')
  const workspace = canAssign ? (chosenWorkspace ?? defaultWorkspace) : ''
  const install = usePrivilegedMutation<InstallIntent, SkillInstallResult>({
    mutationFn: async (action, authority) => {
      const request = { tenant: activeTenant, ...authority }
      const previouslyPublished = !!action.result
      const result =
        action.result ??
        (await skillsApi.install(action.input, action.key, request))
      authority.dispatchGuard()
      action.result = result
      // Publication is committed even if the following assignment is refused.
      if (!previouslyPublished)
        void queryClient.invalidateQueries({
          queryKey: skillsKeys.packs(activeTenant),
        })
      action.held = result.revision.members.some(
        (member) => (member.active_content?.length ?? 0) > 0,
      )
      if (action.workspace && !action.held) {
        try {
          if (
            previouslyPublished &&
            (await skillsApi.hasWorkspacePin(
              action.workspace,
              result.pack.id,
              result.revision.id,
              result.revision.members.map((member) => member.name),
              request,
            ))
          )
            return result
          await skillsApi.assign(
            {
              target_kind: 'workspace',
              target_id: action.workspace,
              pack_revision_id: result.revision.id,
            },
            request,
          )
        } catch (error) {
          authority.dispatchGuard()
          // The shared hook still reports authorization/assurance. Record partial
          // completion here too: those errors deliberately bypass feature onError.
          setFailure(
            t('install.assignmentFailed', {
              reason:
                error instanceof Error
                  ? error.message
                  : t('install.unavailable'),
            }),
          )
          throw error
        }
      }
      return result
    },
    invalidateKeys: (result) => [
      skillsKeys.packs(activeTenant),
      skillsKeys.packAssignments(activeTenant, result.pack.id),
    ],
    successMessage: (_, action) => installedMessage(action),
    onDone: (_, action) => {
      setDone(installedMessage(action))
      setFailure(null)
      setIntent(null)
    },
    onError: (error, action) => {
      setFailure(
        t(action.result ? 'install.assignmentFailed' : 'install.failed', {
          reason:
            error instanceof Error ? error.message : t('install.unavailable'),
        }),
      )
      return false
    },
  })
  const installedMessage = (action: InstallIntent) =>
    t(
      action.held
        ? 'install.held'
        : action.workspace
          ? 'install.assigned'
          : 'install.done',
    )
  const begin = (input: SkillInstallRequest) => {
    const action = { input, workspace, key: crypto.randomUUID() }
    setIntent(action)
    setFailure(null)
    setDone(null)
    install.mutate(action)
  }
  const submitGit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const url = new URL(gitURL)
    const name =
      url.pathname
        .split('/')
        .filter(Boolean)
        .at(-1)
        ?.replace(/\.git$/, '') || 'skills'
    begin({
      name: packName.trim() || name,
      source: {
        kind: 'git',
        url: gitURL,
        ref: ref.trim() || 'HEAD',
        subdir: subdir.trim(),
      },
    })
  }
  const busy =
    install.isPending ||
    (canInstall && canAssign && (workspaces.isLoading || !!workspaces.error))
  return (
    <section
      className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4"
      aria-label={t('install.title')}
    >
      <h2 className="text-label font-medium">{t('install.title')}</h2>
      {canInstall && canAssign && (
        <Field
          label={t('install.workspace')}
          description={t('install.workspaceHint')}
        >
          <select
            className={selectClass}
            value={workspace}
            onChange={(e) => setChosenWorkspace(e.target.value)}
            disabled={busy || workspaces.isLoading || !!workspaces.error}
          >
            <option value="">{t('install.catalogOnly')}</option>
            {targets.map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
          </select>
        </Field>
      )}
      {canInstall && canAssign && workspaces.error && (
        <QueryErrorState
          error={workspaces.error}
          retry={() => void workspaces.refetch()}
        />
      )}
      {done && (
        <p role="status" className="text-body text-text-2">
          {done}
        </p>
      )}
      {failure && intent && (
        <div className="flex flex-wrap items-center gap-2">
          <p role="alert" className="text-body text-danger">
            {failure}
          </p>
          <Button
            disabled={
              install.isPending || (intent.result ? !canAssign : !canInstall)
            }
            onClick={() => {
              setFailure(null)
              install.mutate(intent)
            }}
          >
            {t(intent.result ? 'install.retryAssignment' : 'install.retry')}
          </Button>
        </div>
      )}
      <details open>
        <summary className="cursor-pointer text-label font-medium">
          {t('install.builtins')}
        </summary>
        {builtins.isLoading && <p role="status">{t('install.loading')}</p>}
        {builtins.error ? (
          <QueryErrorState
            error={builtins.error}
            retry={() => void builtins.refetch()}
          />
        ) : (
          <ul className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {(builtins.data?.items ?? []).map((pack) => {
              const installed = packs.some((p) => p.name === pack.id)
              return (
                <li
                  key={pack.id}
                  className="flex min-w-0 items-center justify-between gap-2 rounded-md border border-border p-2"
                >
                  <span className="min-w-0 break-all text-caption font-mono">
                    {pack.id}
                  </span>
                  {canInstall && (
                    <Button
                      size="sm"
                      disabled={busy || installed}
                      aria-label={t('install.pack', { name: pack.id })}
                      onClick={() =>
                        begin({
                          name: pack.id,
                          source: { kind: 'builtin', ref: pack.id },
                        })
                      }
                    >
                      {t(installed ? 'install.inCatalog' : 'install.submit')}
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </details>
      {canInstall && (
        <details>
          <summary className="cursor-pointer text-label font-medium">
            {t('install.git')}
          </summary>
          <form className="mt-3 flex flex-col gap-3" onSubmit={submitGit}>
            <Field label={t('install.gitURL')} required>
              <Input
                type="url"
                required
                value={gitURL}
                onChange={(e) => setGitURL(e.target.value)}
                disabled={busy}
                placeholder="https://github.com/owner/skills"
              />
            </Field>
            <Field
              label={t('install.name')}
              description={t('install.nameHint')}
            >
              <Input
                value={packName}
                onChange={(e) => setPackName(e.target.value)}
                disabled={busy}
                maxLength={128}
              />
            </Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t('install.ref')}>
                <Input
                  value={ref}
                  onChange={(e) => setRef(e.target.value)}
                  disabled={busy}
                  placeholder="HEAD"
                />
              </Field>
              <Field label={t('install.subdir')}>
                <Input
                  value={subdir}
                  onChange={(e) => setSubdir(e.target.value)}
                  disabled={busy}
                />
              </Field>
            </div>
            <Button type="submit" disabled={busy} className="self-start">
              {t('install.gitSubmit')}
            </Button>
          </form>
        </details>
      )}
    </section>
  )
}
