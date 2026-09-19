// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useAuth } from '@/lib/auth/context'
import { useWorkspaceStore } from '@/stores/workspace'

/**
 * THE EMPTY STATE OF A ROUTE THAT REFUSES TO GUESS A WORKSPACE.
 *
 * Six routes — the five communications doors and the protocol bindings — make no
 * request at all until a workspace is chosen, which is the right behaviour and is
 * deliberate. What they said about it was not: "Choose one in the workspace switcher"
 * names a control that is NOT ON THE SCREEN whenever the tenant holds one workspace,
 * because the switcher returns null below two of them. Measured on the seeded estate,
 * which has exactly one: the instruction could not be carried out.
 *
 * So the choice is offered here, where the reader is told it is needed. One workspace
 * is one button that selects it; several are a picker. The switcher keeps its own
 * rule — this is not a second switcher, it is the next action of THIS empty state,
 * and it disappears with the empty state as soon as a workspace is active.
 */
const WORKSPACE_PAGE = 1000

export function WorkspaceRequiredState({
  icon,
  title,
  description,
}: {
  icon?: ReactNode
  title: ReactNode
  description: NonNullable<ReactNode>
}) {
  const { t } = useTranslation('shared')
  const { activeTenant } = useAuth()
  const setActiveWorkspace = useWorkspaceStore((s) => s.setActiveWorkspace)
  const { data } = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant, { limit: WORKSPACE_PAGE }),
    queryFn: () => consoleApi.listWorkspaces({ limit: WORKSPACE_PAGE }),
    enabled: !!activeTenant,
    staleTime: 60_000,
  })
  const workspaces = data?.items ?? []

  // Nothing to offer is not a reason to invent a control: the sentence stands on its
  // own, exactly as it did before, and no dead button is drawn.
  let action: ReactNode = null
  if (workspaces.length === 1) {
    const only = workspaces[0]
    action = (
      <Button
        size="sm"
        variant="primary"
        onClick={() => setActiveWorkspace(only.id, only.name)}
      >
        {t('workspaceRequired.use', { name: only.name })}
      </Button>
    )
  } else if (workspaces.length > 1) {
    action = (
      <Select
        onValueChange={(id) => {
          const w = workspaces.find((x) => x.id === id)
          if (w) setActiveWorkspace(w.id, w.name)
        }}
      >
        <SelectTrigger
          className="w-56"
          aria-label={t('workspaceRequired.choose')}
        >
          <SelectValue placeholder={t('workspaceRequired.choose')} />
        </SelectTrigger>
        <SelectContent>
          {workspaces.map((w) => (
            <SelectItem key={w.id} value={w.id}>
              {w.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    )
  }

  return (
    <EmptyState
      icon={icon}
      title={title}
      description={description}
      action={action}
    />
  )
}
