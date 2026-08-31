// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Undo2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { deployApi, deployKeys } from './api'
import './i18n'
import type { DefinitionDTO, RevisionDTO } from './types'

export interface RevisionsSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  definitionId: string | null
  name: string
  /** Current desired version — marks the current row and is excluded as a target. */
  currentVersion: number
  /** Whether the operator may roll back (deploy:deployment:write). */
  canWrite: boolean
}

export function RevisionsSheet({
  open,
  onOpenChange,
  definitionId,
  name,
  currentVersion,
  canWrite,
}: RevisionsSheetProps) {
  const { t } = useTranslation(['deploy', 'common'])
  const { activeTenant } = useAuth()
  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)
  const [note, setNote] = useState('')

  const query = useQuery({
    queryKey: deployKeys.revisions(activeTenant, definitionId ?? ''),
    queryFn: () => deployApi.listRevisions(definitionId!),
    enabled: open && !!definitionId,
  })
  const revisions = query.data?.items ?? []

  const rollback = usePrivilegedMutation<number, DefinitionDTO>({
    mutationFn: (toVersion) =>
      deployApi.rollback(definitionId!, {
        to_version: toVersion,
        ...(note.trim() ? { note: note.trim() } : {}),
      }),
    invalidateKeys: () => [
      deployKeys.definition(activeTenant, definitionId ?? ''),
      deployKeys.definitions(activeTenant),
      deployKeys.revisions(activeTenant, definitionId ?? ''),
    ],
    successMessage: t('rollback.done'),
    onDone: () => {
      setRollbackTarget(null)
      setNote('')
    },
  })

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{t('revisions.title')}</SheetTitle>
          <SheetDescription>
            {t('revisions.subtitle', { name })}
          </SheetDescription>
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {query.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-20 w-full" />
              ))}
            </div>
          ) : query.error instanceof ApiError &&
            query.error.isStepUpRequired ? (
            // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
            // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que leerlo
            // primero sustituía la pantalla por «no tienes autorización» —falso, y sin salida—.
            //
            // DEFENSA EN PROFUNDIDAD, y lo digo porque en esta campaña ya presenté dos veces como
            // «camino vivo» algo que no lo era: HOY esta ruta no emite el código. Los emisores medidos
            // son las dos escrituras de `modules/governance` y las 21 llamadas a `requireAAL3` de
            // `core/api`, todas cubiertas ya. Esto se arregla porque el defecto es de FORMA y sobrevive
            // al día en que el gate llegue aquí, no porque alguien lo esté sufriendo ahora.
            <StepUpRequiredState
              action="generic"
              onElevated={() => void query.refetch()}
            />
          ) : query.error instanceof ApiError && query.error.isForbidden ? (
            <ForbiddenState />
          ) : query.error ? (
            <ErrorState retry={() => query.refetch()} />
          ) : revisions.length === 0 ? (
            <EmptyState title={t('revisions.empty')} />
          ) : (
            <ol className="flex flex-col gap-3">
              {revisions.map((r: RevisionDTO) => {
                const isCurrent = r.version === currentVersion
                return (
                  <li
                    key={r.version}
                    className="rounded-lg border border-border bg-surface p-3"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono text-sm font-medium tabular-nums text-foreground">
                        {t('revisions.version')} {r.version}
                      </span>
                      <div className="flex items-center gap-2">
                        {isCurrent && (
                          <Badge variant="success">
                            {t('revisions.current')}
                          </Badge>
                        )}
                        {canWrite && !isCurrent && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => {
                              setNote('')
                              setRollbackTarget(r.version)
                            }}
                          >
                            <Undo2 />
                            {t('revisions.rollback')}
                          </Button>
                        )}
                      </div>
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                      {r.created_by && (
                        <span className="font-mono">{r.created_by}</span>
                      )}
                      {r.created_at && (
                        <>
                          <span>·</span>
                          <RelTimeLabel ts={r.created_at} />
                        </>
                      )}
                    </div>
                    <div className="mt-2 flex flex-wrap items-center gap-1.5 text-xs">
                      <span className="font-mono text-muted-foreground">
                        {r.spec_hash.slice(0, 12)}
                      </span>
                      {r.source_ref && (
                        <Badge variant="outline" className="font-mono">
                          {r.source_ref}
                        </Badge>
                      )}
                    </div>
                    {r.note && (
                      <p className="mt-2 text-xs text-muted-foreground">
                        {r.note}
                      </p>
                    )}
                  </li>
                )
              })}
            </ol>
          )}
        </ScrollArea>

        <p className="text-xs text-muted-foreground">
          {t('revisions.caption')}
        </p>
      </SheetContent>

      {/* Rollback — MEDIUM risk → danger + a typed phrase. The optional change
          note is collected here and sent with the rollback. */}
      <ConfirmDialog
        open={rollbackTarget != null}
        onOpenChange={(o) => {
          if (!o) setRollbackTarget(null)
        }}
        title={t('rollback.title')}
        description={
          rollbackTarget != null
            ? t('rollback.body', { version: rollbackTarget })
            : undefined
        }
        tone="danger"
        confirmPhrase={t('rollback.phrase')}
        confirmLabel={t('rollback.confirm')}
        pending={rollback.isPending}
        onConfirm={() => {
          if (rollbackTarget != null) rollback.mutate(rollbackTarget)
        }}
      >
        <div className="mt-2 flex flex-col gap-1.5">
          <Label htmlFor="rollback-note">{t('rollback.noteLabel')}</Label>
          <Textarea
            id="rollback-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder={t('rollback.notePlaceholder')}
            rows={2}
          />
        </div>
      </ConfirmDialog>
    </Sheet>
  )
}
