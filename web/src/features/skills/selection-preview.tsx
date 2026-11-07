// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'
import { useWorkspaceStore } from '@/stores/workspace'
import { skillsApi, skillsKeys } from './api'
import type { SkillTargetKind } from './types'
import './i18n'

/** Read the engine's resolution; never assemble precedence in the browser. */
export function SelectionPreview({
  kind,
  id,
  profileRef,
  folderRef,
  current = false,
}: {
  kind: SkillTargetKind
  id: string
  profileRef?: string
  folderRef?: string
  current?: boolean
}) {
  const { t } = useTranslation('skills')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const on = useModuleOn('skills')
  const readable = on && can('skills:catalog:read')
  const query = useQuery({
    queryKey: [
      ...skillsKeys.selection(activeTenant, kind, id, profileRef),
      folderRef ?? '',
      boundary.epoch,
    ],
    queryFn: ({ signal }) =>
      skillsApi.selection(kind, id, signal, profileRef, folderRef),
    enabled: readable && !!activeTenant && !!id,
  })
  if (!readable || !activeTenant || !id) return null
  if (query.isError)
    return (
      <QueryErrorState error={query.error} retry={() => void query.refetch()} />
    )
  return (
    <div className="min-w-0 text-caption" aria-live="polite">
      {query.isPending ? (
        <p>{t('selection.loading')}</p>
      ) : (
        <>
          <p>
            {t(current ? 'selection.current' : 'selection.count', {
              count: (query.data.members ?? []).length,
            })}
          </p>
          <ul className="max-h-40 overflow-y-auto break-words text-muted-foreground">
            {(query.data.members ?? []).map((member) => (
              <li key={`${member.kind ?? 'skill'}:${member.name}`}>
                {member.name}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  )
}

/** Match the launch's department (the default when All departments is selected). */
export function SessionSkillsPreview({
  profileRef,
  folderRef,
}: {
  profileRef?: string
  folderRef?: string
}) {
  const { activeTenant, can } = useAuth()
  const { t } = useTranslation('skills')
  const on = useModuleOn('skills')
  const boundary = useAuthBoundary()
  const active = useWorkspaceStore((s) => s.activeWorkspace)
  const page = { limit: 1000 }
  const workspaces = useQuery({
    queryKey: [...consoleKeys.workspaces(activeTenant, page), boundary.epoch],
    queryFn: () => consoleApi.listWorkspaces(page),
    enabled:
      on &&
      can('skills:catalog:read') &&
      can('tenant:read') &&
      !!activeTenant &&
      !active,
  })
  if (!on || !can('skills:catalog:read') || !activeTenant) return null
  if (!active && workspaces.isError)
    return (
      <QueryErrorState
        error={workspaces.error}
        retry={() => void workspaces.refetch()}
      />
    )
  const id = active ?? workspaces.data?.items.find((w) => w.is_default)?.id
  if (!id)
    return (
      <p className="text-caption text-muted-foreground" role="status">
        {t(
          workspaces.isPending && can('tenant:read')
            ? 'selection.loading'
            : 'selection.unavailable',
        )}
      </p>
    )
  return (
    <SelectionPreview
      kind="workspace"
      id={id}
      profileRef={profileRef}
      folderRef={folderRef}
    />
  )
}
