// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TemplatesView — workspace templates catalog page.
// Shows a filter toolbar (built-in / custom toggle + show-archived checkbox),
// a responsive card grid, and a Create button that opens TemplateEditor.
// Edit actions on cards also open TemplateEditor in edit mode.
import { useQuery } from '@tanstack/react-query'
import { LayoutTemplate, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { EmptyState } from '@/components/ui/empty-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Label } from '@/components/ui/label'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { RunCreateDialog } from '@/features/agentops/run-create-dialog'
import { ListTruncationBadge } from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { templatesApi, templatesKeys } from './api'
import { TemplateCard } from './template-card'
import { TemplateEditor } from './template-editor'
import type { TemplateDTO } from './types'
import './i18n'

type FilterType = 'all' | 'builtin' | 'custom'

export function TemplatesView() {
  const { t } = useTranslation('workspace-templates')
  const { activeTenant, can } = useAuth()
  // Crear es `write` en el motor (templates.go:711). Ver el reparto completo en template-card.tsx.
  const canCreate = can('sessions:template:write')

  // Filter state.
  const [filter, setFilter] = useState<FilterType>('all')
  const [showArchived, setShowArchived] = useState(false)

  // Editor sheet state.
  const [editorOpen, setEditorOpen] = useState(false)
  const [editingTemplate, setEditingTemplate] = useState<
    TemplateDTO | undefined
  >()

  // "Apply to session" LAUNCHES a session under the template. It used to POST
  // /apply and toast; nothing was applied to anything. The launch dialog is where a
  // session is configured, so that is where a template that governs one belongs.
  const [launchOpen, setLaunchOpen] = useState(false)
  const [launchTemplate, setLaunchTemplate] = useState<
    TemplateDTO | undefined
  >()

  // Query — include_archived only when the checkbox is on.
  const query = useQuery({
    queryKey: templatesKeys.list(activeTenant, {
      builtin: filter === 'all' ? undefined : filter === 'builtin',
      include_archived: showArchived || undefined,
    }),
    queryFn: () =>
      templatesApi.list({
        builtin: filter === 'all' ? undefined : filter === 'builtin',
        include_archived: showArchived || undefined,
      }),
  })

  function openCreate() {
    setEditingTemplate(undefined)
    setEditorOpen(true)
  }

  function openEdit(template: TemplateDTO) {
    setEditingTemplate(template)
    setEditorOpen(true)
  }

  function openLaunch(template: TemplateDTO) {
    setLaunchTemplate(template)
    setLaunchOpen(true)
  }

  const items = query.data?.items ?? []

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={LayoutTemplate}
        title={t('title')}
        description={t('subtitle')}
        actions={
          <Button
            variant="primary"
            size="sm"
            disabled={!canCreate}
            onClick={openCreate}
          >
            <Plus />
            {t('actions.create')}
          </Button>
        }
      />

      {/* Filter toolbar */}
      <div className="flex flex-wrap items-center gap-4">
        <Select
          value={filter}
          onValueChange={(v) => setFilter(v as FilterType)}
        >
          <SelectTrigger
            className="h-7 w-auto min-w-[9rem] text-xs"
            aria-label={t('catalog.filterAll')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('catalog.filterAll')}</SelectItem>
            <SelectItem value="builtin">
              {t('catalog.filterBuiltin')}
            </SelectItem>
            <SelectItem value="custom">{t('catalog.filterCustom')}</SelectItem>
          </SelectContent>
        </Select>

        <div className="flex items-center gap-1.5">
          <Checkbox
            id="show-archived"
            checked={showArchived}
            onCheckedChange={(v) => setShowArchived(v === true)}
          />
          <Label
            htmlFor="show-archived"
            className="cursor-pointer text-xs text-muted-foreground"
          >
            {t('catalog.showArchived')}
          </Label>
        </div>

        {/* Item count badge */}
        {!query.isLoading && !query.error && (
          <Badge variant="neutral" className="ml-auto">
            {items.length}
          </Badge>
        )}
      </div>

      <ListTruncationBadge
        query={query}
        label={t('truncation.label', {
          n: query.data?.items?.length,
        })}
        hint={t('truncation.hint')}
        className="px-0 pt-0"
        filas={items.length}
      />

      {/* Content area */}
      {query.isLoading ? (
        <TemplateGridSkeleton />
      ) : query.error instanceof ApiError && query.error.isStepUpRequired ? (
        // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
        // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también, así que
        // leerlo primero acusaba al operador de un permiso que SÍ tiene, y sin salida.
        <StepUpRequiredState
          action="generic"
          onElevated={() => void query.refetch()}
        />
      ) : query.error instanceof ApiError && query.error.isForbidden ? (
        <ForbiddenState />
      ) : query.error ? (
        <ErrorState retry={() => void query.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState
          icon={<LayoutTemplate />}
          title={t('catalog.noTemplates')}
          description={t('catalog.noTemplatesHint')}
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {items.map((tpl) => (
            <TemplateCard
              key={tpl.id}
              template={tpl}
              onEdit={openEdit}
              onApply={openLaunch}
            />
          ))}
        </div>
      )}

      {/* Editor sheet — shared for create + edit */}
      <TemplateEditor
        open={editorOpen}
        onOpenChange={setEditorOpen}
        template={editingTemplate}
      />

      {/* The session this template governs. */}
      <RunCreateDialog
        open={launchOpen}
        onOpenChange={setLaunchOpen}
        initialTemplateId={launchTemplate?.id}
      />
    </div>
  )
}

/** Skeleton grid shown while the initial list loads. */
function TemplateGridSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
      {Array.from({ length: 6 }).map((_, i) => (
        <Skeleton key={i} className="h-40 w-full rounded-lg" />
      ))}
    </div>
  )
}
