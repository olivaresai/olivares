// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useQuery } from '@tanstack/react-query'
import {
  KeyRound,
  Layers,
  Pencil,
  Plus,
  ShieldCheck,
  Trash2,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Spinner } from '@/components/ui/spinner'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  type CustomRoleDTO,
  type DelegationAuthorityDTO,
  type PermGroupDTO,
  type RBACCatalogDTO,
  type ScopedGrantDTO,
} from './api'
import { AccessReviewSection } from './roles-access-review-section'
import { InheritanceFiltersSection } from './roles-filters-section'
import { GroupHierarchySection } from './roles-groups-section'
import { ModelGovernanceSection } from './roles-model-section'
import { scopeLabel } from './roles-shared'
import { StaticTable } from '@/components/data/static-table'
import { PANEL_EXTENSIONS } from '@/features/extensions'

/**
 * RolesTab is the FASE X Roles & delegation panel — the console UI over the
 * scoped-administration engine (custom roles, permission-groups, scoped grants)
 * that enforces. An admin authors reusable roles, bundles permissions, and
 * delegates scoped administration; the panel SHOWS the delegation ceiling so the
 * operator stays within bounds, but the BACKEND remains the authority — every write is
 * re-checked against canDelegate and self-audited. Reached behind RBAC
 * (governance:rbac:read|admin = tenant admin) and, for authoring, an AAL3 step-up.
 */
export function RolesTab() {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:rbac:read')
  const canAdmin = can('governance:rbac:admin')

  const catalog = useQuery({
    queryKey: consoleKeys.rbacCatalog(),
    queryFn: () => consoleApi.rbacCatalog(),
    enabled: canRead,
    staleTime: 5 * 60_000,
  })
  const authority = useQuery({
    queryKey: consoleKeys.delegationAuthority(activeTenant),
    queryFn: () => consoleApi.delegationAuthority(),
    enabled: canRead,
  })
  const roles = useQuery({
    queryKey: consoleKeys.roles(activeTenant),
    queryFn: () => consoleApi.listRoles(),
    enabled: canRead,
  })
  const groups = useQuery({
    queryKey: consoleKeys.permGroups(activeTenant),
    queryFn: () => consoleApi.listPermGroups(),
    enabled: canRead,
  })
  const grants = useQuery({
    queryKey: consoleKeys.grants(activeTenant),
    queryFn: () => consoleApi.listGrants(),
    enabled: canRead,
  })
  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant),
    queryFn: () => consoleApi.listWorkspaces(),
    enabled: canRead && can('tenant:read'),
  })
  const agentGroups = useQuery({
    queryKey: consoleKeys.agentGroups(activeTenant),
    queryFn: () => consoleApi.listAgentGroups(),
    enabled: canRead && can('agent:read'),
  })

  if (!canRead) {
    return (
      <div className="pt-4">
        <EmptyState
          description={t('console:roles.readOnlyNoticeHint')}
          title={t('console:roles.readOnlyNotice')}
          icon={<ShieldCheck />}
        />
      </div>
    )
  }

  const cat = catalog.data
  const roleItems = roles.data?.items ?? []
  const groupItems = groups.data?.items ?? []
  const grantItems = grants.data?.items ?? []
  const wsItems = workspaces.data?.items ?? []
  const agItems = agentGroups.data?.items ?? []

  return (
    <div className="flex flex-col gap-8 pt-4">
      {!PANEL_EXTENSIONS.authorizationForms && (
        <p className="text-body text-muted-foreground">
          {t('common:edition.editingRequiresBusiness')}
        </p>
      )}
      <DelegationAuthorityCard query={authority} />

      <GrantsSection
        catalog={cat}
        grants={grantItems}
        roles={roleItems}
        workspaces={wsItems}
        agentGroups={agItems}
        loading={grants.isLoading}
        isError={grants.isError}
        error={grants.error}
        refetch={() => void grants.refetch()}
        canAdmin={canAdmin}
      />

      <InheritanceFiltersSection
        catalog={cat}
        workspaces={wsItems}
        agentGroups={agItems}
        canAdmin={canAdmin}
      />

      <RolesSection
        catalog={cat}
        roles={roleItems}
        groups={groupItems}
        loading={roles.isLoading}
        isError={roles.isError}
        error={roles.error}
        refetch={() => void roles.refetch()}
        canAdmin={canAdmin}
      />

      <GroupsSection
        catalog={cat}
        groups={groupItems}
        loading={groups.isLoading}
        isError={groups.isError}
        error={groups.error}
        refetch={() => void groups.refetch()}
        canAdmin={canAdmin}
      />

      <GroupHierarchySection canAdmin={canAdmin} />

      <ModelGovernanceSection />

      <AccessReviewSection />
    </div>
  )
}

// --- delegation authority (the ceiling, shown honestly) ----------------------

function DelegationAuthorityCard({
  query,
}: {
  query: { data?: DelegationAuthorityDTO; isLoading: boolean; isError: boolean }
}) {
  const { t } = useTranslation('console')
  return (
    <section className="rounded-lg border border-border bg-muted/20 p-4">
      <div className="flex items-center gap-2">
        <ShieldCheck className="size-4 text-accent-text" />
        <h2 className="text-heading text-foreground">
          {t('roles.authority.title')}
        </h2>
      </div>
      {query.isLoading ? (
        <div className="flex py-3">
          <Spinner size="sm" />
        </div>
      ) : query.isError ? (
        // A failed fetch must NOT read as "you have no authority" — say it couldn't load.
        <p className="mt-2 text-body text-muted-foreground">
          {t('roles.authority.loadError')}
        </p>
      ) : query.data?.superadmin ? (
        <p className="mt-2 text-body text-muted-foreground">
          {t('roles.authority.superadmin')}
        </p>
      ) : (query.data?.domains.length ?? 0) === 0 ? (
        <p className="mt-2 text-body text-warning">
          {t('roles.authority.none')}
        </p>
      ) : (
        <div className="mt-2 flex flex-col gap-3">
          <p className="text-body text-muted-foreground">
            {t('roles.authority.caption')}
          </p>
          <ul className="flex flex-col gap-3">
            {query.data?.domains.map((d, i) => (
              <li
                key={i}
                className="rounded-md border border-border bg-background p-3"
              >
                <div className="flex items-center gap-2">
                  <Badge variant="neutral">
                    {scopeLabel(t, d.scope_tree, d.scope_ref)}
                  </Badge>
                  {d.scope_class && (
                    <span className="text-caption text-muted-foreground">
                      {t('roles.authority.classLabel', {
                        class: d.scope_class,
                      })}
                    </span>
                  )}
                </div>
                <div className="mt-2 flex max-h-24 flex-wrap gap-1 overflow-y-auto">
                  {d.permissions.map((p) => (
                    <code
                      key={p}
                      className="rounded bg-muted px-1.5 py-0.5 font-mono text-caption text-muted-foreground"
                    >
                      {p}
                    </code>
                  ))}
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}

// --- scoped grants -----------------------------------------------------------

function GrantsSection({
  catalog,
  grants,
  roles,
  workspaces,
  agentGroups,
  loading,
  isError,
  error,
  refetch,
  canAdmin,
}: {
  catalog?: RBACCatalogDTO
  grants: ScopedGrantDTO[]
  roles: CustomRoleDTO[]
  workspaces: { slug: string; name: string }[]
  agentGroups: { slug: string; name: string }[]
  loading: boolean
  isError: boolean
  /** The read's error, for the one error mapping (QueryErrorState). */
  error?: unknown
  refetch: () => void
  canAdmin: boolean
}) {
  const GrantForm = PANEL_EXTENSIONS.authorizationForms?.GrantForm
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [createOpen, setCreateOpen] = useState(false)
  const [revoke, setRevoke] = useState<ScopedGrantDTO | null>(null)

  const revokeMutation = usePrivilegedMutation<string, void>({
    mutationFn: (id) => consoleApi.revokeGrant(id),
    invalidateKeys: () => [
      consoleKeys.grants(activeTenant),
      consoleKeys.delegationAuthority(activeTenant),
    ],
    successMessage: t('console:roles.grants.revoked'),
    onDone: () => setRevoke(null),
  })

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-heading text-foreground">
            {t('console:roles.grants.title')}
          </h2>
          <p className="max-w-2xl text-body text-muted-foreground">
            {t('console:roles.grants.caption')}
          </p>
        </div>
        {canAdmin && GrantForm && (
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('console:roles.grants.create')}
          </Button>
        )}
      </div>
      {loading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : isError ? (
        <QueryErrorState error={error} retry={refetch} />
      ) : grants.length === 0 ? (
        <EmptyState
          action={
            canAdmin && GrantForm ? (
              <Button onClick={() => setCreateOpen(true)}>
                <Plus />
                {t('console:roles.grants.create')}
              </Button>
            ) : undefined
          }
          description={t('console:roles.grants.noneHint')}
          title={t('console:roles.grants.none')}
          icon={<ShieldCheck />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <StaticTable>
            <thead>
              <tr>
                <th>{t('console:roles.grants.subject')}</th>
                <th>{t('console:roles.grants.role')}</th>
                <th>{t('console:roles.grants.scope')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {grants.map((g) => (
                <tr key={g.id} className="align-top">
                  <td>
                    <span className="font-mono text-caption text-foreground">
                      {g.subject_kind === 'role'
                        ? `role:${g.subject_ref}`
                        : g.subject_kind === 'group'
                          ? `group:${g.subject_ref}`
                          : g.subject_ref}
                    </span>
                  </td>
                  <td>
                    <Badge variant="neutral">{g.role}</Badge>
                    {g.role_custom && (
                      <span className="ml-1 text-caption text-muted-foreground">
                        {t('console:roles.grantForm.groupCustom')}
                      </span>
                    )}
                  </td>
                  <td className="text-foreground">
                    {scopeLabel(t, g.scope_tree, g.scope_ref)}
                    {g.scope_class && (
                      <span className="ml-1 text-caption text-muted-foreground">
                        {t('console:roles.authority.classLabel', {
                          class: g.scope_class,
                        })}
                      </span>
                    )}
                  </td>
                  <td className="text-right">
                    {canAdmin && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setRevoke(g)}
                      >
                        <Trash2 />
                        {t('console:roles.grants.revoke')}
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </StaticTable>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {createOpen && GrantForm && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <GrantForm
                catalog={catalog}
                roles={roles}
                workspaces={workspaces}
                agentGroups={agentGroups}
                onClose={() => setCreateOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={revoke !== null}
        onOpenChange={(o) => !o && setRevoke(null)}
        title={t('console:roles.grants.revokeTitle')}
        description={t('console:roles.grants.revokeBody')}
        confirmLabel={t('console:roles.grants.revoke')}
        tone="danger"
        pending={revokeMutation.isPending}
        onConfirm={() => revoke?.id && revokeMutation.mutate(revoke.id)}
      />
    </section>
  )
}

function RolesSection({
  catalog,
  roles,
  groups,
  loading,
  isError,
  error,
  refetch,
  canAdmin,
}: {
  catalog?: RBACCatalogDTO
  roles: CustomRoleDTO[]
  groups: PermGroupDTO[]
  loading: boolean
  isError: boolean
  /** The read's error, for the one error mapping (QueryErrorState). */
  error?: unknown
  refetch: () => void
  canAdmin: boolean
}) {
  const RoleForm = PANEL_EXTENSIONS.authorizationForms?.RoleForm
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [editing, setEditing] = useState<CustomRoleDTO | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [del, setDel] = useState<CustomRoleDTO | null>(null)

  const deleteMutation = usePrivilegedMutation<string, void>({
    mutationFn: (name) => consoleApi.deleteRole(name),
    // A role definition feeds the actor's own ceiling (via admin-capable grants that
    // reference it), so refresh the delegation-authority card too.
    invalidateKeys: () => [
      consoleKeys.roles(activeTenant),
      consoleKeys.delegationAuthority(activeTenant),
    ],
    successMessage: t('console:roles.defs.deleted'),
    onDone: () => setDel(null),
  })

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-heading text-foreground">
            {t('console:roles.defs.title')}
          </h2>
          <p className="max-w-2xl text-body text-muted-foreground">
            {t('console:roles.defs.caption')}
          </p>
        </div>
        {canAdmin && RoleForm && (
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('console:roles.defs.create')}
          </Button>
        )}
      </div>
      {loading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : isError ? (
        <QueryErrorState error={error} retry={refetch} />
      ) : roles.length === 0 ? (
        <EmptyState
          action={
            canAdmin && RoleForm ? (
              <Button onClick={() => setCreateOpen(true)}>
                <Plus />
                {t('console:roles.defs.create')}
              </Button>
            ) : undefined
          }
          description={t('console:roles.defs.noneHint')}
          title={t('console:roles.defs.none')}
          icon={<KeyRound />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <StaticTable>
            <thead>
              <tr>
                <th>{t('console:roles.defs.name')}</th>
                <th>{t('console:roles.defs.permissions')}</th>
                <th>{t('console:roles.defs.groups')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {roles.map((r) => (
                <tr key={r.name}>
                  <td>
                    <span className="font-mono text-caption text-foreground">
                      {r.name}
                    </span>
                    {r.display_name && (
                      <span className="ml-2 text-muted-foreground">
                        {r.display_name}
                      </span>
                    )}
                  </td>
                  <td>
                    <Badge variant="neutral">{r.permissions.length}</Badge>
                  </td>
                  <td className="text-muted-foreground">
                    {(r.groups ?? []).join(', ') || '—'}
                  </td>
                  <td className="text-right">
                    {canAdmin && (
                      <div className="flex justify-end gap-1">
                        {RoleForm && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setEditing(r)}
                          >
                            <Pencil />
                            {t('console:roles.defs.edit')}
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setDel(r)}
                        >
                          <Trash2 />
                          {t('console:roles.defs.delete')}
                        </Button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </StaticTable>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
          {createOpen && RoleForm && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <RoleForm
                catalog={catalog}
                groups={groups}
                onClose={() => setCreateOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
          {editing && RoleForm && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <RoleForm
                catalog={catalog}
                groups={groups}
                existing={editing}
                onClose={() => setEditing(null)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={del !== null}
        onOpenChange={(o) => !o && setDel(null)}
        title={t('console:roles.defs.deleteTitle')}
        description={t('console:roles.defs.deleteBody')}
        confirmLabel={t('console:roles.defs.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => del && deleteMutation.mutate(del.name)}
      />
    </section>
  )
}

function GroupsSection({
  catalog,
  groups,
  loading,
  isError,
  error,
  refetch,
  canAdmin,
}: {
  catalog?: RBACCatalogDTO
  groups: PermGroupDTO[]
  loading: boolean
  isError: boolean
  /** The read's error, for the one error mapping (QueryErrorState). */
  error?: unknown
  refetch: () => void
  canAdmin: boolean
}) {
  const GroupForm = PANEL_EXTENSIONS.authorizationForms?.GroupForm
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [editing, setEditing] = useState<PermGroupDTO | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [del, setDel] = useState<PermGroupDTO | null>(null)

  const deleteMutation = usePrivilegedMutation<string, void>({
    mutationFn: (name) => consoleApi.deletePermGroup(name),
    invalidateKeys: () => [
      consoleKeys.permGroups(activeTenant),
      consoleKeys.delegationAuthority(activeTenant),
    ],
    successMessage: t('console:roles.groups.deleted'),
    onDone: () => setDel(null),
  })

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-heading text-foreground">
            {t('console:roles.groups.title')}
          </h2>
          <p className="max-w-2xl text-body text-muted-foreground">
            {t('console:roles.groups.caption')}
          </p>
        </div>
        {canAdmin && GroupForm && (
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('console:roles.groups.create')}
          </Button>
        )}
      </div>
      {loading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : isError ? (
        <QueryErrorState error={error} retry={refetch} />
      ) : groups.length === 0 ? (
        <EmptyState
          action={
            canAdmin && GroupForm ? (
              <Button onClick={() => setCreateOpen(true)}>
                <Plus />
                {t('console:roles.groups.create')}
              </Button>
            ) : undefined
          }
          description={t('console:roles.groups.noneHint')}
          title={t('console:roles.groups.none')}
          icon={<Layers />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <StaticTable>
            <thead>
              <tr>
                <th>{t('console:roles.groups.name')}</th>
                <th>{t('console:roles.groups.permissions')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <tr key={g.name}>
                  <td>
                    <span className="font-mono text-caption text-foreground">
                      {g.name}
                    </span>
                    {g.display_name && (
                      <span className="ml-2 text-muted-foreground">
                        {g.display_name}
                      </span>
                    )}
                  </td>
                  <td>
                    <Badge variant="neutral">{g.permissions.length}</Badge>
                  </td>
                  <td className="text-right">
                    {canAdmin && (
                      <div className="flex justify-end gap-1">
                        {GroupForm && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setEditing(g)}
                          >
                            <Pencil />
                            {t('console:roles.groups.edit')}
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setDel(g)}
                        >
                          <Trash2 />
                          {t('console:roles.groups.delete')}
                        </Button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </StaticTable>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
          {createOpen && GroupForm && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <GroupForm
                catalog={catalog}
                onClose={() => setCreateOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
          {editing && GroupForm && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <GroupForm
                catalog={catalog}
                existing={editing}
                onClose={() => setEditing(null)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={del !== null}
        onOpenChange={(o) => !o && setDel(null)}
        title={t('console:roles.groups.deleteTitle')}
        description={t('console:roles.groups.deleteBody')}
        confirmLabel={t('console:roles.groups.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => del && deleteMutation.mutate(del.name)}
      />
    </section>
  )
}
