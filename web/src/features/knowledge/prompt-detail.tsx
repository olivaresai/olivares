// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Plus, RotateCcw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusBadge } from '@/components/data/badges'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { HashChip } from './chips'
import { knowledgeApi, knowledgeKeys } from './api'
import { RevisionEditorDialog } from './prompt-editor'
import './i18n'
import type { RevisionDTO } from './types'

export interface PromptDetailSheetProps {
  promptId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function PromptDetailSheet({
  promptId,
  open,
  onOpenChange,
}: PromptDetailSheetProps) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('knowledge:prompt:write')

  const [addOpen, setAddOpen] = useState(false)
  const [rollbackRev, setRollbackRev] = useState<number | null>(null)

  const promptQuery = useQuery({
    queryKey: knowledgeKeys.prompt(activeTenant, promptId ?? ''),
    queryFn: () => knowledgeApi.getPrompt(promptId!),
    enabled: open && !!promptId,
  })
  const prompt = promptQuery.data

  const revisionsQuery = useQuery({
    queryKey: knowledgeKeys.revisions(activeTenant, promptId ?? ''),
    queryFn: () => knowledgeApi.listRevisions(promptId!),
    enabled: open && !!promptId,
  })
  const revisions = revisionsQuery.data?.items ?? []

  const rollback = usePrivilegedMutation<
    number,
    { prompt_id: string; current_rev: number }
  >({
    mutationFn: (rev) => knowledgeApi.rollbackPrompt(promptId!, { rev }),
    invalidateKeys: () => [
      knowledgeKeys.prompt(activeTenant, promptId ?? ''),
      knowledgeKeys.revisions(activeTenant, promptId ?? ''),
      knowledgeKeys.prompts(activeTenant),
    ],
    successMessage: t('rollback.done'),
    onDone: () => setRollbackRev(null),
  })

  // Newest-first so the latest revision is at the top.
  const sorted = [...revisions].sort((a, b) => b.rev - a.rev)

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{prompt?.name ?? t('prompts.detailTitle')}</SheetTitle>
          {prompt && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <StatusBadge status={prompt.status} />
              <Badge variant="neutral">
                {t('prompts.currentRev')}:{' '}
                <span className="font-mono tabular-nums">
                  {prompt.current_rev}
                </span>
              </Badge>
              {prompt.latest_hash && <HashChip value={prompt.latest_hash} />}
            </SheetDescription>
          )}
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {promptQuery.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-20 w-full" />
              ))}
            </div>
          ) : promptQuery.error instanceof ApiError &&
            promptQuery.error.isStepUpRequired ? (
            // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
            // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también.
            <StepUpRequiredState
              action="generic"
              onElevated={() => void promptQuery.refetch()}
            />
          ) : promptQuery.error instanceof ApiError &&
            promptQuery.error.isForbidden ? (
            <ForbiddenState />
          ) : promptQuery.error || !prompt ? (
            <ErrorState retry={() => promptQuery.refetch()} />
          ) : (
            <div className="flex flex-col gap-4">
              {canWrite && (
                <div className="flex flex-wrap gap-2">
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setAddOpen(true)}
                  >
                    <Plus />
                    {t('prompts.addRevision')}
                  </Button>
                </div>
              )}

              <Separator />

              <section className="flex flex-col gap-2">
                <h3 className="text-sm font-medium text-foreground">
                  {t('prompts.revisions')}
                </h3>
                <p className="text-xs text-muted-foreground">
                  {t('prompts.revisionsCaption')}
                </p>

                {revisionsQuery.isLoading ? (
                  <div className="flex flex-col gap-2">
                    {Array.from({ length: 3 }).map((_, i) => (
                      <Skeleton key={i} className="h-24 w-full" />
                    ))}
                  </div>
                ) : revisionsQuery.error instanceof ApiError &&
                  revisionsQuery.error.isStepUpRequired ? (
                  // Segunda decisión del mismo fichero: las revisiones se gatean aparte del
                  // prompt. Un barrido que sólo comprueba «el fichero menciona la ceremonia»
                  // no ve esto — por eso la guarda de clase compara POSICIONES.
                  <StepUpRequiredState
                    action="generic"
                    onElevated={() => void revisionsQuery.refetch()}
                  />
                ) : revisionsQuery.error instanceof ApiError &&
                  revisionsQuery.error.isForbidden ? (
                  <ForbiddenState />
                ) : revisionsQuery.error ? (
                  <ErrorState retry={() => revisionsQuery.refetch()} />
                ) : sorted.length === 0 ? (
                  <EmptyState title={t('prompts.revisionsEmpty')} />
                ) : (
                  <ol className="flex flex-col gap-3">
                    {sorted.map((r: RevisionDTO) => (
                      <RevisionItem
                        key={r.rev}
                        revision={r}
                        isCurrent={r.rev === prompt.current_rev}
                        canWrite={canWrite}
                        onRollback={() => setRollbackRev(r.rev)}
                      />
                    ))}
                  </ol>
                )}
              </section>
            </div>
          )}
        </ScrollArea>
      </SheetContent>

      {prompt && (
        <RevisionEditorDialog
          open={addOpen}
          onOpenChange={setAddOpen}
          prompt={prompt}
        />
      )}

      {prompt && (
        <ConfirmDialog
          open={rollbackRev != null}
          onOpenChange={(o) => !o && setRollbackRev(null)}
          title={t('rollback.title')}
          description={t('rollback.body', {
            name: prompt.name,
            rev: rollbackRev ?? '',
          })}
          tone="danger"
          confirmPhrase={t('rollback.phrase')}
          confirmLabel={t('rollback.confirm')}
          pending={rollback.isPending}
          onConfirm={() => rollbackRev != null && rollback.mutate(rollbackRev)}
        />
      )}
    </Sheet>
  )
}

function RevisionItem({
  revision,
  isCurrent,
  canWrite,
  onRollback,
}: {
  revision: RevisionDTO
  isCurrent: boolean
  canWrite: boolean
  onRollback: () => void
}) {
  const { t } = useTranslation('knowledge')
  return (
    <li className="rounded-lg border border-border bg-surface p-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="font-mono text-sm font-medium tabular-nums text-foreground">
            {t('prompts.rev')} {revision.rev}
          </span>
          {isCurrent && <Badge variant="success">{t('prompts.current')}</Badge>}
          {revision.label && <Badge variant="outline">{revision.label}</Badge>}
        </div>
        {canWrite && !isCurrent && (
          <Button variant="ghost" size="sm" onClick={onRollback}>
            <RotateCcw />
            {t('prompts.rollbackTo')}
          </Button>
        )}
      </div>
      <div className="mt-2">
        <KvList>
          <KvRow label={t('prompts.createdBy')} mono>
            {revision.created_by}
          </KvRow>
          <KvRow label={t('prompts.templateHash')} align="start">
            <HashChip value={revision.template_hash} />
          </KvRow>
          {revision.note && (
            <KvRow label={t('prompts.note')} align="start">
              {revision.note}
            </KvRow>
          )}
        </KvList>
      </div>
      <pre className="mt-2 max-h-48 overflow-auto rounded-md border border-border bg-muted px-2.5 py-2 font-mono text-xs whitespace-pre-wrap text-foreground">
        {revision.template}
      </pre>
    </li>
  )
}
