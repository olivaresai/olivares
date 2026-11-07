// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TemplatesView — workspace templates catalog page.
// Lists the person's own templates first, then the built-ins folded in a closed
// disclosure, with a show-archived checkbox and a Create button that opens
// TemplateEditor. A row's name or Edit action opens TemplateEditor too.
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useQuery } from '@tanstack/react-query'
import { LayoutTemplate, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { Label } from '@/components/ui/label'
import { PageHeader } from '@/components/ui/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import { ListTruncationBadge } from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { templatesApi, templatesKeys } from './api'
import { TemplateCard } from './template-card'
import { TemplateEditor } from './template-editor'
import type { TemplateDTO } from './types'
import './i18n'

export function TemplatesView() {
  const { t } = useTranslation('workspace-templates')
  const { activeTenant, can } = useAuth()
  // Crear es `write` en el motor (templates.go:711). Ver el reparto completo en template-card.tsx.
  const canCreate = can('sessions:template:write')

  const [showArchived, setShowArchived] = useState(false)

  // Editor sheet state.
  const [editorOpen, setEditorOpen] = useState(false)
  const [editingTemplate, setEditingTemplate] = useState<
    TemplateDTO | undefined
  >()

  // Query — include_archived only when the checkbox is on.
  const params = { include_archived: showArchived || undefined }
  const query = useQuery({
    queryKey: templatesKeys.list(activeTenant, params),
    queryFn: () => templatesApi.list(params),
  })

  function openCreate() {
    setEditingTemplate(undefined)
    setEditorOpen(true)
  }

  function openEdit(template: TemplateDTO) {
    setEditingTemplate(template)
    setEditorOpen(true)
  }

  // "Apply to session" LAUNCHES a session under the template. It used to POST
  // /apply and toast; nothing was applied to anything. The launch dialog is where a
  // session is configured, so that is where a template that governs one belongs.
  function openLaunch(template: TemplateDTO) {
    useNewSessionDialog.getState().openAdvanced({ templateId: template.id })
  }

  const items = query.data?.items ?? []
  const yours = items.filter((tpl) => !tpl.builtin)
  const builtin = items.filter((tpl) => tpl.builtin)

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={LayoutTemplate}
        title={t('title')}
        description={t('subtitle')}
        primaryAction={
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

      <div className="flex items-center gap-1.5">
        <Checkbox
          id="show-archived"
          checked={showArchived}
          onCheckedChange={(v) => setShowArchived(v === true)}
        />
        <Label
          htmlFor="show-archived"
          className="cursor-pointer text-caption text-muted-foreground"
        >
          {t('catalog.showArchived')}
        </Label>
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
        <TemplateListSkeleton />
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
        <QueryErrorState
          error={query.error}
          retry={() => void query.refetch()}
        />
      ) : (
        <div className="flex flex-col gap-6">
          <section aria-labelledby="templates-yours">
            <h2
              id="templates-yours"
              className="mb-1 text-body font-semibold text-foreground"
            >
              {t('groups.yours')}
            </h2>
            {yours.length === 0 ? (
              <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border px-4 py-3">
                <p className="min-w-0 flex-1 text-caption text-muted-foreground">
                  {t('groups.yoursEmpty')}
                </p>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={!canCreate}
                  onClick={openCreate}
                >
                  <Plus />
                  {t('actions.create')}
                </Button>
              </div>
            ) : (
              <TemplateList
                items={yours}
                onEdit={openEdit}
                onApply={openLaunch}
              />
            )}
          </section>

          {builtin.length > 0 && (
            <details className="rounded-lg border border-border px-4">
              <summary className="cursor-pointer py-3 text-body font-semibold text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                {t('groups.builtin', { n: builtin.length })}
              </summary>
              <TemplateList
                items={builtin}
                onEdit={openEdit}
                onApply={openLaunch}
              />
            </details>
          )}
        </div>
      )}

      {/* Editor sheet — shared for create + edit */}
      <TemplateEditor
        open={editorOpen}
        onOpenChange={setEditorOpen}
        template={editingTemplate}
      />
    </div>
  )
}

/** One group's templates as rows. */
function TemplateList({
  items,
  onEdit,
  onApply,
}: {
  items: TemplateDTO[]
  onEdit: (template: TemplateDTO) => void
  onApply: (template: TemplateDTO) => void
}) {
  return (
    <ul className="divide-y divide-border">
      {items.map((tpl) => (
        <TemplateCard
          key={tpl.id}
          template={tpl}
          onEdit={onEdit}
          onApply={onApply}
        />
      ))}
    </ul>
  )
}

/** Skeleton rows shown while the initial list loads. */
function TemplateListSkeleton() {
  return (
    <div className="flex flex-col gap-2">
      {Array.from({ length: 4 }).map((_, i) => (
        <Skeleton key={i} className="h-12 w-full rounded-lg" />
      ))}
    </div>
  )
}
