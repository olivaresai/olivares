// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TemplateCard — displays a single workspace template in the catalog grid.
// Shows name, description, version, author, and a builtin lock badge.
// Actions: Apply to session, Edit (opens editor sheet), Duplicate (calls API),
// Archive (with confirmation, blocked for built-in templates).
//
// ⛔ "Apply to session" LAUNCHES a session under the template. It used to POST
// /apply and raise a toast, and nothing else happened anywhere: the endpoint answered
// `applied:true, conflicts:[]` unconditionally and no run had ever carried a template.
// The dialog promised, in seven languages, that applying overwrites the session's
// settings — so the fix is to make that true, not to soften the sentence. The launch
// dialog is where a session is configured, and the engine merges the template into it
// before the governance gates.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Copy, Lock, MoreHorizontal, Pencil, Play, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { templatesApi, templatesKeys } from './api'
import type { ApplyResult, TemplateDTO } from './types'
import './i18n'

export interface TemplateCardProps {
  template: TemplateDTO
  onEdit: (template: TemplateDTO) => void
  /** Open the session launch dialog with this template pre-selected. */
  onApply: (template: TemplateDTO) => void
}

export function TemplateCard({ template, onEdit, onApply }: TemplateCardProps) {
  /**
   * ⛔ LAS CINCO ACCIONES NO COMPARTEN PERMISO, y el reparto se DERIVA del motor
   *    (`modules/sessions/templates.go:709-715`), no de lo que parezca razonable:
   *
   *      POST   /templates                 -> sessions:template:write   crear
   *      PUT    /templates/{id}            -> sessions:template:write   editar
   *      POST   /templates/{id}/duplicate  -> sessions:template:write   duplicar
   *      DELETE /templates/{id}            -> sessions:template:admin   archivar
   *      POST   /templates/{id}/apply      -> sessions:template:read    LANZAR
   *
   *    Lanzar pide sólo LECTURA, que es contraintuitivo —lanzar crea trabajo— y es justo la razón
   *    de derivarlo en vez de suponerlo: un reparto «de sentido común» habría pedido `write` para
   *    lanzar y escondido el botón a quien el motor sí deja lanzar.
   *
   *    Y esto NO sustituye a la comprobación del motor: es lo que evita ofrecer un botón que va a
   *    ser rechazado. Cuando el motor rechace igualmente, `workErrorReason` enseña por qué (C-14).
   */
  const { can } = useAuth()
  const canWrite = can('sessions:template:write')
  const canAdmin = can('sessions:template:admin')
  const canLaunch = can('sessions:template:read')
  const { t } = useTranslation('workspace-templates')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const [confirmArchive, setConfirmArchive] = useState(false)
  const [confirmApply, setConfirmApply] = useState(false)

  // The engine's verdict, asked BEFORE the launch dialog opens: a template declaring a
  // term the launch cannot keep refuses that launch, and saying so here beats letting
  // the operator fill in a form for a session that will be rejected.
  const apply = useMutation({
    mutationFn: () => templatesApi.apply(template.id),
    onSuccess: (result: ApplyResult) => {
      setConfirmApply(false)
      if (!result.applied) {
        toast.error(
          t('apply.unenforceable', {
            reasons: (result.unenforceable ?? []).join('; '),
          }),
        )
        return
      }
      onApply(template)
    },
    onError: () => toast.error(t('errors.applyFailed')),
  })

  const duplicate = useMutation({
    mutationFn: () =>
      templatesApi.duplicate(template.id, t('actions.duplicateName', { name: template.name })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: templatesKeys.all(activeTenant) })
      toast.success(t('actions.duplicate'))
    },
    onError: () => toast.error(t('errors.duplicateFailed')),
  })

  const archive = useMutation({
    mutationFn: () => templatesApi.remove(template.id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: templatesKeys.all(activeTenant) })
      setConfirmArchive(false)
      toast.success(t('actions.archive'))
    },
    onError: () => toast.error(t('errors.archiveFailed')),
  })

  const isArchived = !!template.archived_at

  return (
    <>
      <Card className="flex flex-col">
        <CardHeader>
          <div className="flex min-w-0 flex-1 items-start gap-2">
            {template.builtin && (
              <Lock
                className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                aria-label={t('catalog.builtin')}
              />
            )}
            <CardTitle as="h2" className="min-w-0 truncate text-sm font-semibold">
              {template.name}
            </CardTitle>
          </div>
          {/* Actions menu */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('actions.edit')}
                className="shrink-0"
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                onSelect={() => setConfirmApply(true)}
                disabled={!canLaunch || isArchived || apply.isPending}
              >
                <Play className="size-4" />
                {t('actions.apply')}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onSelect={() => onEdit(template)}
                disabled={!canWrite || template.builtin}
              >
                <Pencil className="size-4" />
                {t('actions.edit')}
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() => duplicate.mutate()}
                disabled={!canWrite || duplicate.isPending}
              >
                <Copy className="size-4" />
                {t('actions.duplicate')}
              </DropdownMenuItem>
              {!isArchived && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    onSelect={() => setConfirmArchive(true)}
                    disabled={!canAdmin || template.builtin}
                    className="text-danger focus:text-danger"
                  >
                    <Trash2 className="size-4" />
                    {t('actions.archive')}
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </CardHeader>

        <CardContent className="flex-1">
          {template.description ? (
            <CardDescription className="line-clamp-2 text-xs">
              {template.description}
            </CardDescription>
          ) : (
            <CardDescription className="text-xs italic text-muted-foreground/60">
              —
            </CardDescription>
          )}
        </CardContent>

        <CardFooter className="flex flex-wrap items-center gap-1.5">
          <Badge variant="outline">
            {t('catalog.version', { version: template.version })}
          </Badge>
          {template.builtin && (
            <Badge variant="accent">{t('catalog.builtin')}</Badge>
          )}
          {isArchived && (
            <Badge variant="neutral">{t('catalog.archived')}</Badge>
          )}
          <span className="ml-auto truncate text-xs text-muted-foreground">
            {t('catalog.author', { author: template.author })}
          </span>
        </CardFooter>
      </Card>

      <ConfirmDialog
        open={confirmArchive}
        onOpenChange={setConfirmArchive}
        title={t('actions.confirmArchive')}
        description={t('actions.confirmArchiveHint')}
        tone="danger"
        confirmLabel={t('actions.archive')}
        pending={archive.isPending}
        onConfirm={() => archive.mutate()}
        hideAuditNotice
      />

      <ConfirmDialog
        open={confirmApply}
        onOpenChange={setConfirmApply}
        title={t('actions.confirmApply')}
        description={t('actions.confirmApplyHint')}
        confirmLabel={t('actions.apply')}
        pending={apply.isPending}
        onConfirm={() => apply.mutate()}
        hideAuditNotice
      />
    </>
  )
}
