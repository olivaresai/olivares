// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { LucideIcon } from 'lucide-react'
import { X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'

export interface BulkAction {
  id: string
  label: string
  icon?: LucideIcon
  destructive?: boolean
  run: (id: string) => Promise<void>
}

export interface BulkActionBarProps {
  onClear: () => void
  actions: readonly BulkAction[]
  selectedIds: readonly string[]
}

/**
 * Runs bulk work one item at a time so every server-side audit record has a
 * stable order. Selection remains owned by the caller and is not cleared after
 * partial failure, allowing the operator to inspect or retry it deliberately.
 */
export function BulkActionBar({
  onClear,
  actions,
  selectedIds,
}: BulkActionBarProps) {
  const { t } = useTranslation(['common', 'errors'])
  const [confirmAction, setConfirmAction] = useState<BulkAction | null>(null)
  const [runningActionId, setRunningActionId] = useState<string | null>(null)
  const [runningTotal, setRunningTotal] = useState(0)
  const [completed, setCompleted] = useState(0)
  const selectedCount = selectedIds.length
  const pending = runningActionId !== null

  if (selectedCount === 0 && !pending) return null

  const execute = async (action: BulkAction) => {
    const ids = [...selectedIds]
    setConfirmAction(null)
    setRunningActionId(action.id)
    setRunningTotal(ids.length)
    setCompleted(0)

    let succeeded = 0
    const failures: unknown[] = []
    for (let index = 0; index < ids.length; index += 1) {
      try {
        await action.run(ids[index])
        succeeded += 1
      } catch (error) {
        failures.push(error)
      }
      setCompleted(index + 1)
    }

    const summary = t('common:bulk.summary', {
      success: succeeded,
      failed: failures.length,
    })
    if (failures.length === 0) {
      toast.success(summary)
    } else if (
      failures.every((error) => error instanceof ApiError && error.isForbidden)
    ) {
      toast.warning(t('common:privileged.notAuthorizedToast'), {
        description: summary,
      })
    } else {
      toast.error(t('errors:generic'), { description: summary })
    }

    setRunningActionId(null)
    setRunningTotal(0)
  }

  return (
    <>
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-accent-text/40 bg-accent-soft px-3 py-2">
        <span
          role="status"
          aria-live="polite"
          className="mr-auto text-sm font-medium text-foreground"
        >
          {pending
            ? t('common:bulk.progress', {
                current: completed,
                total: runningTotal,
              })
            : t('common:bulk.selected', { count: selectedCount })}
        </span>
        {actions.map((action) => {
          const Icon = action.icon
          return (
            <Button
              key={action.id}
              type="button"
              size="sm"
              variant={action.destructive ? 'destructive' : 'secondary'}
              disabled={pending}
              onClick={() => {
                if (action.destructive) setConfirmAction(action)
                else void execute(action)
              }}
            >
              {runningActionId === action.id ? (
                <Spinner size="sm" aria-hidden />
              ) : Icon ? (
                <Icon aria-hidden />
              ) : null}
              {action.label}
            </Button>
          )
        })}
        <Button
          type="button"
          size="sm"
          variant="ghost"
          disabled={pending}
          onClick={onClear}
        >
          <X aria-hidden />
          {t('common:bulk.clear')}
        </Button>
      </div>

      <ConfirmDialog
        open={confirmAction !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmAction(null)
        }}
        title={t('common:bulk.confirmTitle')}
        description={t('common:bulk.confirmDescription', {
          action: confirmAction?.label ?? '',
          count: selectedCount,
        })}
        confirmLabel={confirmAction?.label}
        tone="danger"
        pending={pending}
        onConfirm={() => {
          if (confirmAction) void execute(confirmAction)
        }}
      />
    </>
  )
}
