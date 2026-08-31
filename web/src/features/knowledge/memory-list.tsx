// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { ClassificationBadge, ResidencyBadge } from './chips'
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'
import type { MemoryDTO } from './types'

/** The full memory list (cards). */
export function MemoryList({
  entries,
  isLoading,
  error,
  onRetry,
  canWrite,
}: {
  entries: MemoryDTO[]
  isLoading: boolean
  error: unknown
  onRetry: () => void
  canWrite: boolean
}) {
  const { t } = useTranslation('knowledge')

  if (isLoading) {
    return (
      <div className="flex flex-col gap-2">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-24 w-full" />
        ))}
      </div>
    )
  }
  // ⛔ ASEGURAMIENTO ANTES QUE ROL, y el orden no es estilo: `isForbidden` es SÓLO el status 403
  // (lib/api/errors.ts:59), y un `step_up_required` viaja TAMBIÉN como 403. Leerlo primero
  // sustituía la lista entera por «no tienes autorización, pide acceso a un administrador» —
  // falso, y sin salida: el operador SÍ tiene el permiso, lo que está por debajo de AAL3 es la
  // sesión. `onRetry` es el reintento natural una vez elevada.
  if (error instanceof ApiError && error.isStepUpRequired) {
    return <StepUpRequiredState action="generic" onElevated={onRetry} />
  }
  if (error instanceof ApiError && error.isForbidden) {
    return <ForbiddenState />
  }
  if (error) {
    return <ErrorState retry={onRetry} />
  }
  if (entries.length === 0) {
    return (
      <EmptyState
        title={t('memory.empty')}
        description={t('memory.emptyHint')}
      />
    )
  }
  return (
    <ul className="flex flex-col gap-2">
      {entries.map((m) => (
        <MemoryRow key={m.id} entry={m} canWrite={canWrite} />
      ))}
    </ul>
  )
}

/** One memory entry card. Content is the already-redacted store value (minimum-data). */
export function MemoryRow({
  entry,
  canWrite,
}: {
  entry: MemoryDTO
  canWrite: boolean
}) {
  const { t } = useTranslation('knowledge')
  const { activeTenant } = useAuth()
  const [confirmDelete, setConfirmDelete] = useState(false)

  const remove = usePrivilegedMutation<void, { deleted: boolean }>({
    mutationFn: () => knowledgeApi.deleteMemory(entry.id),
    invalidateKeys: () => [knowledgeKeys.memory(activeTenant)],
    successMessage: t('memoryRemove.done'),
    onDone: () => setConfirmDelete(false),
  })

  return (
    <li className="rounded-lg border border-border bg-surface p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="font-mono text-sm font-medium text-foreground">
            {entry.key}
          </span>
          <span className="font-mono text-xs text-muted-foreground">
            {entry.agent_ref}
          </span>
        </div>
        {canWrite && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setConfirmDelete(true)}
            aria-label={t('memory.delete')}
          >
            <Trash2 />
            {t('memory.delete')}
          </Button>
        )}
      </div>

      {entry.content && (
        <p className="mt-2 line-clamp-3 text-xs text-muted-foreground">
          {entry.content}
        </p>
      )}

      <div className="mt-2 flex flex-wrap items-center gap-1.5 text-xs">
        <ClassificationBadge value={entry.classification} />
        <ResidencyBadge value={entry.residency_region} />
        {entry.expires_at ? (
          <Badge variant="info">
            {t('memory.expires')}: <RelTimeLabel ts={entry.expires_at} />
          </Badge>
        ) : (
          <Badge variant="outline">{t('memory.noExpiry')}</Badge>
        )}
        <span className="text-muted-foreground">
          {t('memory.createdBy')}:{' '}
          <span className="font-mono text-foreground">{entry.created_by}</span>
        </span>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t('memoryRemove.title')}
        description={t('memoryRemove.body')}
        tone="danger"
        confirmLabel={t('memoryRemove.confirm')}
        pending={remove.isPending}
        onConfirm={() => remove.mutate()}
      />
    </li>
  )
}

/** The admin "purge expired" action — HIGH risk: danger + confirmPhrase. */
export function MemoryPurgeButton({ agent }: { agent?: string }) {
  const { t } = useTranslation('knowledge')
  const { activeTenant } = useAuth()
  const [open, setOpen] = useState(false)

  const purge = usePrivilegedMutation<void, { purged: number }>({
    mutationFn: () =>
      knowledgeApi.purgeMemory(agent ? { agent_ref: agent } : undefined),
    invalidateKeys: () => [knowledgeKeys.memory(activeTenant)],
    successMessage: t('memoryPurge.done'),
    onDone: () => setOpen(false),
  })

  const agentSuffix = agent ? t('memoryPurge.agentSuffix', { agent }) : ''

  return (
    <>
      <Button variant="destructive" size="sm" onClick={() => setOpen(true)}>
        <Trash2 />
        {t('memory.purge')}
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={t('memoryPurge.title')}
        description={t('memoryPurge.body', { agentSuffix })}
        tone="danger"
        confirmPhrase={t('memoryPurge.phrase')}
        confirmLabel={t('memoryPurge.confirm')}
        pending={purge.isPending}
        onConfirm={() => purge.mutate()}
      />
    </>
  )
}
