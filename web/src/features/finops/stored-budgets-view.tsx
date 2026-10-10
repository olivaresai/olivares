// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import {
  useQueries,
  useQuery,
  useQueryClient,
  useMutation,
} from '@tanstack/react-query'
import { Trash2, Wallet } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useModuleOn } from '@/stores/modules'
import { IntelPage, AsyncSection, ListTruncationBadge } from '@/features/_intel'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { BudgetCard } from './budget-components'
import { finopsApi, finopsKeys } from './api'
import type { Budget } from './types'
import './i18n'

export function StoredBudgetsView() {
  const { activeTenant, can } = useAuth()
  const on = useModuleOn('finops')
  if (!activeTenant || !on || !can('finops:budget:read')) return null
  return <TenantStoredBudgets key={activeTenant} />
}
function TenantStoredBudgets() {
  const { t } = useTranslation(['finops', 'common'])
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()
  const report = useFailedActionReporter()
  const [deleting, setDeleting] = useState<Budget | null>(null)
  const budgetsQ = useQuery({
    queryKey: finopsKeys.budgets(activeTenant),
    queryFn: () => finopsApi.budgets(),
  })
  const budgets = budgetsQ.data?.items ?? []
  const statuses = useQueries({
    queries: budgets.map((b) => ({
      queryKey: finopsKeys.budgetStatus(activeTenant, b.id),
      queryFn: () => finopsApi.budgetStatus(b.id, { tenant: activeTenant }),
    })),
  })
  const del = useMutation({
    mutationFn: (id: string) => finopsApi.deleteBudget(id),
    onSuccess: () => {
      toast.success(t('budgets.deleteDialog.deleted'))
      void qc.invalidateQueries({ queryKey: finopsKeys.budgets(activeTenant) })
      setDeleting(null)
    },
    onError: (e: unknown) => {
      if (e instanceof ApiError && (e.isForbidden || e.isStepUpRequired))
        setDeleting(null)
      report(e)
    },
  })
  const canRemove = can('finops:budget:write')
  return (
    <IntelPage icon={Wallet} title={t('budgets.title')}>
      <ListTruncationBadge
        query={budgetsQ}
        label={t('budgets.truncated', { n: budgets.length })}
        hint={t('budgets.truncatedHint')}
      />
      <AsyncSection query={budgetsQ} skeletonHeight={160}>
        {(list) =>
          list.items.length === 0 ? (
            <EmptyState
              title={t('budgets.empty')}
              description={t('budgets.storedEmptyHint')}
            />
          ) : (
            <div className="grid gap-3 md:grid-cols-2">
              {budgets.map((b, i) => (
                <AsyncSection
                  key={b.id}
                  query={statuses[i]}
                  skeletonHeight={160}
                >
                  {(status) => (
                    <BudgetCard
                      status={status}
                      actions={
                        canRemove ? (
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={t('budgets.deleteAction', {
                              name: b.name,
                            })}
                            onClick={() => setDeleting(b)}
                          >
                            <Trash2 />
                          </Button>
                        ) : undefined
                      }
                    />
                  )}
                </AsyncSection>
              ))}
            </div>
          )
        }
      </AsyncSection>
      <Dialog
        open={canRemove && deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('budgets.deleteDialog.title')}</DialogTitle>
            <DialogDescription>
              {t('budgets.deleteDialog.description', {
                name: deleting?.name ?? '',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="secondary"
              disabled={del.isPending}
              onClick={() => setDeleting(null)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive-solid"
              disabled={del.isPending}
              onClick={() => deleting && del.mutate(deleting.id)}
            >
              {t('budgets.deleteDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </IntelPage>
  )
}
