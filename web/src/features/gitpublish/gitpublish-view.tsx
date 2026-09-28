// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Governed Git publication (modules/gitpublish): the approved publication targets, each
// target's binding and intents, and the governed requests — push, pull request, merge — with
// what each answer means. See DESIGN-NOTE.md in the assignment's evidence directory.
import { useQuery } from '@tanstack/react-query'
import { GitBranchPlus, Plus, RefreshCcw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AsyncSection, IntelPage, SectionCard } from '@/features/_intel'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { useAuth } from '@/lib/auth/context'
import { useWorkspaceStore } from '@/stores/workspace'
import { gitpublishApi, gitpublishKeys } from './api'
import { IntentSheet } from './intent-sheet'
import { actionAuthority } from './model'
import { TargetFormDialog } from './target-form'
import { TargetSheet } from './target-sheet'
import './i18n'

export function GitPublicationView() {
  const { t } = useTranslation('gitpublish')
  const { activeTenant, can } = useAuth()
  const { activeWorkspace } = useWorkspaceStore()
  const [targetId, setTargetId] = useState<string | null>(null)
  const [intentId, setIntentId] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const targets = useQuery({
    queryKey: gitpublishKeys.targets(activeTenant),
    queryFn: () => gitpublishApi.targets(),
  })
  const isAdmin = can(actionAuthority('target').permission)

  return (
    <IntelPage
      icon={GitBranchPlus}
      title={t('title')}
      description={t('subtitle')}
      actions={
        <Button
          variant="outline"
          size="sm"
          onClick={() => void targets.refetch()}
        >
          <RefreshCcw className="size-4" aria-hidden="true" />
          {t('actions.refresh')}
        </Button>
      }
      primaryAction={
        isAdmin && activeWorkspace ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus className="size-4" aria-hidden="true" />
            {t('actions.newTarget')}
          </Button>
        ) : undefined
      }
    >
      <p className="rounded-md border border-border bg-muted/30 px-3 py-2 text-caption text-muted-foreground">
        {t('notice')}
      </p>
      <SectionCard
        title={t('targets.title')}
        description={t('targets.description')}
        noPadding
      >
        <AsyncSection query={targets}>
          {(data) =>
            data.items.length === 0 ? (
              <EmptyState
                className="py-8"
                title={t('targets.empty')}
                description={
                  isAdmin ? t('targets.emptyAdmin') : t('targets.emptyReader')
                }
              />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-left text-body">
                  <caption className="sr-only">{t('targets.title')}</caption>
                  <thead className="border-b border-border text-caption text-muted-foreground">
                    <tr>
                      <th scope="col" className="px-4 py-2 font-medium">
                        {t('columns.target')}
                      </th>
                      <th scope="col" className="px-4 py-2 font-medium">
                        {t('columns.workspace')}
                      </th>
                      <th scope="col" className="px-4 py-2 font-medium">
                        {t('columns.pushPrefix')}
                      </th>
                      <th scope="col" className="px-4 py-2 font-medium">
                        {t('columns.mergeBases')}
                      </th>
                      <th scope="col" className="px-4 py-2 font-medium">
                        {t('columns.version')}
                      </th>
                      <th scope="col" className="px-4 py-2">
                        <span className="sr-only">{t('columns.actions')}</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {data.items.map((tg) => (
                      <tr key={tg.id}>
                        <td className="px-4 py-2 font-mono text-caption">
                          {tg.id}
                        </td>
                        <td className="px-4 py-2 font-mono text-caption">
                          {tg.workspace_id}
                        </td>
                        <td className="px-4 py-2 font-mono text-caption">
                          {tg.push_prefix}
                        </td>
                        <td className="px-4 py-2">
                          <span className="flex flex-wrap gap-1">
                            {tg.merge_bases.map((b) => (
                              <Badge key={b} variant="outline">
                                {b}
                              </Badge>
                            ))}
                          </span>
                        </td>
                        <td className="px-4 py-2 tabular-nums">{tg.version}</td>
                        <td className="px-4 py-2 text-right">
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-label={t('targets.open', { id: tg.id })}
                            onClick={() => setTargetId(tg.id)}
                          >
                            {t('actions.open')}
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>

      {targetId ? (
        <TargetSheet
          targetId={targetId}
          onClose={() => setTargetId(null)}
          onOpenIntent={setIntentId}
        />
      ) : null}
      {intentId ? (
        <IntentSheet
          intentId={intentId}
          onClose={() => setIntentId(null)}
          onOpenIntent={setIntentId}
        />
      ) : null}
      {creating && activeWorkspace ? (
        <TargetFormDialog
          workspaceId={activeWorkspace}
          onClose={() => setCreating(false)}
          onOpenIntent={setIntentId}
        />
      ) : null}
    </IntelPage>
  )
}
