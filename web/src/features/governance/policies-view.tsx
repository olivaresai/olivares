// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { ForbiddenState } from '@/components/ui/error-state'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import { PolicyEditorDialog } from './policy-editor'
import './i18n'
import type { AbacSpec, PolicyDTO } from './types'

/**
 * PoliciesView lists governance policies (ABAC deny + approval rules) and hosts the
 * typed editor and the danger delete. Read gates on governance:policy:read; create /
 * edit / delete on governance:policy:admin. Enforcement is honest: a caption notes an
 * enabled policy is authored + audited but may be inert if its evaluator is unwired —
 * the UI never implies "enforced" from enabled=true.
 */
export function PoliciesView() {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:policy:read')
  const canAdmin = can('governance:policy:admin')

  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<PolicyDTO | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<PolicyDTO | null>(null)

  const policies = useQuery({
    queryKey: governanceKeys.policies(activeTenant),
    queryFn: () => governanceApi.listPolicies(),
    enabled: canRead,
  })

  const remove = usePrivilegedMutation({
    mutationFn: () => governanceApi.deletePolicy(confirmDelete!.id!),
    invalidateKeys: () => [governanceKeys.policies(activeTenant)],
    successMessage: t('policyRemove.done'),
    onDone: () => setConfirmDelete(null),
  })

  if (!canRead) return <ForbiddenState />

  const columns: TableColumn<PolicyDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('policies.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'kind',
      header: t('policies.kind'),
      cell: ({ row }) => (
        <Badge variant={row.original.kind === 'abac' ? 'accent' : 'info'}>
          {row.original.kind === 'abac'
            ? t('policies.kindAbac')
            : t('policies.kindApproval')}
        </Badge>
      ),
    },
    {
      accessorKey: 'enabled',
      header: t('policies.enabled'),
      cell: ({ row }) => (
        <Badge variant={row.original.enabled ? 'success' : 'neutral'}>
          {row.original.enabled
            ? t('policies.enabledYes')
            : t('policies.enabledNo')}
        </Badge>
      ),
    },
    {
      id: 'rules',
      header: t('policies.rules'),
      cell: ({ row }) => {
        if (row.original.kind !== 'abac') return '—'
        const count =
          (row.original.spec as AbacSpec | undefined)?.rules?.length ?? 0
        return (
          <span className="font-mono tabular-nums text-muted-foreground">
            {t('policies.ruleCount', { count })}
          </span>
        )
      },
    },
    ...(canAdmin
      ? [
          {
            id: 'actions',
            header: '',
            cell: ({ row }: { row: { original: PolicyDTO } }) => (
              <div className="flex items-center justify-end">
                <Button
                  variant="destructive"
                  size="icon"
                  aria-label={t('policyRemove.confirm')}
                  onClick={(e) => {
                    e.stopPropagation()
                    setConfirmDelete(row.original)
                  }}
                >
                  <Trash2 />
                </Button>
              </div>
            ),
          } as TableColumn<PolicyDTO, unknown>,
        ]
      : []),
  ]

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">{t('policies.caption')}</p>
      <p className="text-xs text-muted-foreground">
        {t('policies.enforcementCaption')}
      </p>

      <DataTable
        columns={columns}
        data={policies.data?.items ?? []}
        isLoading={policies.isLoading}
        error={policies.error}
        onRetry={() => policies.refetch()}
        searchable
        searchPlaceholder={t('policies.search')}
        getRowId={(r) => r.id ?? r.name}
        onRowClick={
          canAdmin
            ? (r) => {
                setEditing(r)
                setEditorOpen(true)
              }
            : undefined
        }
        empty={
          <EmptyState
            title={t('empty.policies.title')}
            description={t('empty.policies.description')}
          />
        }
        toolbar={
          canAdmin ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => {
                setEditing(null)
                setEditorOpen(true)
              }}
            >
              <Plus />
              {t('policies.newPolicy')}
            </Button>
          ) : undefined
        }
      />

      <PolicyEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        policy={editing}
      />

      {confirmDelete && (
        <ConfirmDialog
          open={!!confirmDelete}
          onOpenChange={(o) => !o && setConfirmDelete(null)}
          title={t('policyRemove.title')}
          description={t('policyRemove.body')}
          tone="danger"
          confirmLabel={t('policyRemove.confirm')}
          pending={remove.isPending}
          onConfirm={() => remove.mutate(undefined)}
        />
      )}
    </div>
  )
}
