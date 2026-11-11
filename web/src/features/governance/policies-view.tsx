// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { useId, useState } from 'react'
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
import { PagePrimaryAction } from '@/components/ui/page-actions'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { ApiError } from '@/lib/api/errors'
import { QueryErrorState } from '@/components/layout/query-error-state'

/**
 * PoliciesView lists governance policies (ABAC deny + approval rules) and hosts the
 * typed editor and the danger delete. Read gates on governance:policy:read; create /
 * edit / delete on governance:policy:admin. An enabled policy is enforced from its save:
 * the engine's request authorizer always composes the governance evaluator
 * (cmd/olivares/boot.go, auth.NewAuthorizer(gov.RequestEvaluator(), ...)) and each write
 * invalidates the tenant's cached rule set after commit (modules/governance/policy.go).
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
      {canAdmin && (
        <PagePrimaryAction>
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
        </PagePrimaryAction>
      )}
      {can('governance:rbac:read') && <RolesTable />}

      <p className="text-caption text-muted-foreground">
        {t('policies.caption')}
      </p>

      <DataTable
        columns={columns}
        // This table edits the three built-in kinds. A kind a composition registers
        // is listed by the API but is managed where that composition says.
        data={(policies.data?.items ?? []).filter(
          (p) =>
            p.kind === 'abac' ||
            p.kind === 'approval' ||
            p.kind === 'grant_expiry',
        )}
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

interface RoleRow {
  id: string
  name: string
  does: string
  /** Members who hold it; absent where nobody counted them (a custom role). */
  people?: number
  builtIn: boolean
}

/**
 * RolesTable lists the engine's built-in roles (GET /rbac/catalog, in its order),
 * each with the line the product's documentation gives it, how many people hold
 * it, then the organization's own roles. The count needs the people read: a
 * person without it sees the rest. A failed people or custom-roles read keeps
 * the rows and says so below them; an edition without custom roles shows the
 * built-in ones, and the page's closed Business row («Custom policies, roles and
 * grants», businessRows) names what it adds.
 */
function RolesTable() {
  const { t } = useTranslation(['governance', 'auth'])
  const { activeTenant, can } = useAuth()
  const headingId = useId()
  const countPeople = can('user:read')
  const catalog = useQuery({
    queryKey: consoleKeys.rbacCatalog(),
    queryFn: () => consoleApi.rbacCatalog(),
    staleTime: 5 * 60_000,
  })
  const members = useQuery({
    queryKey: consoleKeys.members(activeTenant),
    queryFn: () => consoleApi.listMembers(),
    enabled: countPeople,
    staleTime: 60_000,
  })
  const custom = useQuery({
    queryKey: consoleKeys.roles(activeTenant),
    queryFn: () => consoleApi.listRoles(),
  })
  // An edition without custom roles refuses their read: the built-in ones stand.
  const customError =
    custom.error instanceof ApiError && custom.error.isEditionRefusal
      ? null
      : custom.error
  const sideError = members.error ?? customError
  // A partial page of people would undercount: no number then.
  const people = members.data?.has_more ? undefined : members.data?.items
  const rows: RoleRow[] = [
    ...(catalog.data?.builtin_roles ?? []).map((role) => ({
      id: role,
      // A role the console has no words for yet shows its name and no line. The
      // lines quote docs-site/src/content/docs/how-to/govern-and-approve.md (and
      // its de, es, fr, ja, ru and zh copies); never a list of permissions.
      name: t(`auth:roles.${role}`, { defaultValue: role }),
      does: t(`policies.roles.does.${role}`, { defaultValue: '' }),
      people: people?.filter((m) => m.role === role).length,
      builtIn: true,
    })),
    ...(custom.data?.items ?? []).map((role) => ({
      id: `custom:${role.name}`,
      name: role.display_name || role.name,
      does:
        role.description ||
        (role.base_role
          ? t('policies.roles.from', {
              role: t(`auth:roles.${role.base_role}`),
            })
          : ''),
      builtIn: false,
    })),
  ]
  const columns: TableColumn<RoleRow, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('policies.roles.role'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    { accessorKey: 'does', header: t('policies.roles.can') },
    ...(countPeople
      ? [
          {
            accessorKey: 'people',
            header: t('policies.roles.people'),
            cell: ({ row }) => row.original.people ?? '—',
          } as TableColumn<RoleRow, unknown>,
        ]
      : []),
    {
      accessorKey: 'builtIn',
      header: t('policies.roles.kind'),
      cell: ({ row }) => (
        <Badge variant={row.original.builtIn ? 'neutral' : 'info'}>
          {row.original.builtIn
            ? t('policies.roles.builtIn')
            : t('policies.roles.custom')}
        </Badge>
      ),
    },
  ]

  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-2">
      <h2 id={headingId} className="text-body font-medium text-foreground">
        {t('policies.roles.title')}
      </h2>
      <DataTable
        columns={columns}
        data={rows}
        isLoading={catalog.isLoading || members.isLoading || custom.isLoading}
        error={catalog.error}
        onRetry={() => catalog.refetch()}
        getRowId={(r) => r.id}
        label={t('policies.roles.title')}
        empty={
          <EmptyState
            title={t('policies.roles.title')}
            description={t('policies.roles.empty')}
          />
        }
      />
      {sideError && !catalog.error ? (
        <QueryErrorState
          error={sideError}
          retry={() => {
            void members.refetch()
            void custom.refetch()
          }}
        />
      ) : null}
    </section>
  )
}
