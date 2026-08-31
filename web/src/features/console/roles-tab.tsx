// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import {
  KeyRound,
  Layers,
  Pencil,
  Plus,
  ShieldCheck,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
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
import { GroupHierarchySection } from './roles-groups-section'
import { ModelGovernanceSection } from './roles-model-section'
import {
  classOptionsFor,
  FormError,
  PermissionMatrix,
  scopeLabel,
} from './roles-shared'

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
      <DelegationAuthorityCard query={authority} />

      <GrantsSection
        catalog={cat}
        grants={grantItems}
        roles={roleItems}
        workspaces={wsItems}
        agentGroups={agItems}
        loading={grants.isLoading}
        isError={grants.isError}
        refetch={() => void grants.refetch()}
        canAdmin={canAdmin}
      />

      <RolesSection
        catalog={cat}
        roles={roleItems}
        groups={groupItems}
        loading={roles.isLoading}
        isError={roles.isError}
        refetch={() => void roles.refetch()}
        canAdmin={canAdmin}
      />

      <GroupsSection
        catalog={cat}
        groups={groupItems}
        loading={groups.isLoading}
        isError={groups.isError}
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
        <h2 className="text-base font-semibold text-foreground">
          {t('roles.authority.title')}
        </h2>
      </div>
      {query.isLoading ? (
        <div className="flex py-3">
          <Spinner size="sm" />
        </div>
      ) : query.isError ? (
        // A failed fetch must NOT read as "you have no authority" — say it couldn't load.
        <p className="mt-2 text-sm text-muted-foreground">
          {t('roles.authority.loadError')}
        </p>
      ) : query.data?.superadmin ? (
        <p className="mt-2 text-sm text-muted-foreground">
          {t('roles.authority.superadmin')}
        </p>
      ) : (query.data?.domains.length ?? 0) === 0 ? (
        <p className="mt-2 text-sm text-warning">{t('roles.authority.none')}</p>
      ) : (
        <div className="mt-2 flex flex-col gap-3">
          <p className="text-sm text-muted-foreground">
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
                    <span className="text-xs text-muted-foreground">
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
                      className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground"
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
  refetch: () => void
  canAdmin: boolean
}) {
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
          <h2 className="text-base font-semibold text-foreground">
            {t('console:roles.grants.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:roles.grants.caption')}
          </p>
        </div>
        {canAdmin && (
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
        <ErrorState retry={refetch} />
      ) : grants.length === 0 ? (
        <EmptyState
          title={t('console:roles.grants.none')}
          icon={<ShieldCheck />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:roles.grants.subject')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:roles.grants.role')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:roles.grants.scope')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {grants.map((g) => (
                <tr key={g.id} className="border-t border-border align-top">
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {g.subject_kind === 'role'
                        ? `role:${g.subject_ref}`
                        : g.subject_kind === 'group'
                          ? `group:${g.subject_ref}`
                          : g.subject_ref}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="neutral">{g.role}</Badge>
                    {g.role_custom && (
                      <span className="ml-1 text-xs text-muted-foreground">
                        {t('console:roles.grantForm.groupCustom')}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-foreground">
                    {scopeLabel(t, g.scope_tree, g.scope_ref)}
                    {g.scope_class && (
                      <span className="ml-1 text-xs text-muted-foreground">
                        {t('console:roles.authority.classLabel', {
                          class: g.scope_class,
                        })}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-right">
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
          </table>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {createOpen && (
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

function GrantForm({
  catalog,
  roles,
  workspaces,
  agentGroups,
  onClose,
}: {
  catalog?: RBACCatalogDTO
  roles: CustomRoleDTO[]
  workspaces: { slug: string; name: string }[]
  agentGroups: { slug: string; name: string }[]
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [subjectKind, setSubjectKind] = useState<'user' | 'role' | 'group'>(
    'user',
  )
  const [subjectRef, setSubjectRef] = useState('')
  const [subjectRole, setSubjectRole] = useState('viewer')
  // The role selector encodes both built-in and custom roles ("b:<name>" / "c:<name>").
  const [roleSel, setRoleSel] = useState<string | null>(null)
  const [scopeTree, setScopeTree] = useState<
    'tenant' | 'workspace' | 'agent_group'
  >('tenant')
  const [scopeRef, setScopeRef] = useState<string | null>(null)
  const [scopeClass, setScopeClass] = useState('any')
  const [note, setNote] = useState('')

  const groupsQuery = useQuery({
    queryKey: consoleKeys.groups(activeTenant),
    queryFn: () => consoleApi.listGroups(),
    enabled: subjectKind === 'group',
  })

  const roleOptions: ComboboxOption[] = useMemo(() => {
    const builtin = (catalog?.builtin_roles ?? []).map((r) => ({
      value: `b:${r}`,
      label: `${r} · ${t('console:roles.grantForm.groupBuiltin')}`,
      keywords: [r],
    }))
    const custom = roles.map((c) => ({
      value: `c:${c.name}`,
      label: `${c.display_name || c.name} · ${t('console:roles.grantForm.groupCustom')}`,
      keywords: [c.name],
    }))
    return [...builtin, ...custom]
  }, [catalog, roles, t])

  const mutation = usePrivilegedMutation<void, ScopedGrantDTO>({
    mutationFn: () => {
      const isCustom = roleSel?.startsWith('c:') ?? false
      const role = roleSel ? roleSel.slice(2) : ''
      const body: ScopedGrantDTO = {
        subject_kind: subjectKind,
        subject_ref: subjectKind === 'role' ? subjectRole : subjectRef.trim(),
        role,
        role_custom: isCustom,
        scope_tree: scopeTree,
        scope_ref: scopeTree === 'tenant' ? undefined : (scopeRef ?? undefined),
        scope_class: scopeClass === 'any' ? undefined : scopeClass,
        note: note.trim() || undefined,
      }
      return consoleApi.createGrant(body)
    },
    invalidateKeys: () => [
      consoleKeys.grants(activeTenant),
      consoleKeys.delegationAuthority(activeTenant),
    ],
    successMessage: t('console:roles.grants.created'),
    onDone: onClose,
  })

  const subjectValid = subjectKind === 'role' || subjectRef.trim() !== ''
  const scopeValid = scopeTree === 'tenant' || !!scopeRef
  const valid = subjectValid && !!roleSel && scopeValid

  // The class options a scope tree admits — see classOptionsFor for the rule and why a
  // module kind is not offerable outside a tenant scope.
  const classOptions = classOptionsFor(scopeTree, catalog)

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('console:roles.grantForm.title')}</DialogTitle>
        <DialogDescription>
          {t('console:roles.grants.caption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('console:roles.grantForm.subjectKind')}
          htmlFor="g-subkind"
        >
          <Select
            value={subjectKind}
            onValueChange={(v) =>
              setSubjectKind(v as 'user' | 'role' | 'group')
            }
          >
            <SelectTrigger id="g-subkind">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user">
                {t('console:roles.grantForm.subjectUser')}
              </SelectItem>
              <SelectItem value="role">
                {t('console:roles.grantForm.subjectRole')}
              </SelectItem>
              <SelectItem value="group">
                {t('console:roles.grantForm.subjectGroup')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {subjectKind === 'user' && (
          <Field
            label={t('console:roles.grantForm.userId')}
            htmlFor="g-userid"
            description={t('console:roles.grantForm.userIdHint')}
            required
          >
            <Input
              id="g-userid"
              value={subjectRef}
              onChange={(e) => setSubjectRef(e.target.value)}
              mono
            />
          </Field>
        )}
        {subjectKind === 'role' && (
          <Field
            label={t('console:roles.grantForm.builtinRole')}
            htmlFor="g-subrole"
          >
            <Select value={subjectRole} onValueChange={setSubjectRole}>
              <SelectTrigger
                id="g-subrole"
                aria-label={t('console:roles.grantForm.builtinRole')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(catalog?.builtin_roles ?? []).map((r) => (
                  <SelectItem key={r} value={r}>
                    {r}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        )}
        {subjectKind === 'group' && (
          <Field
            label={t('console:granular.groupSubjects.colName')}
            htmlFor="g-groupid"
            required
          >
            <Combobox
              id="g-groupid"
              options={(groupsQuery.data?.groups ?? []).map((g) => ({
                value: g.id,
                label: g.display_name || g.id,
                keywords: [g.display_name, g.external_id].filter(
                  Boolean,
                ) as string[],
              }))}
              value={subjectRef || null}
              onChange={(v) => setSubjectRef(v ?? '')}
              placeholder={t('console:granular.groupSubjects.selectGroup')}
            />
          </Field>
        )}

        <Field
          label={t('console:roles.grantForm.role')}
          htmlFor="g-role"
          description={t('console:roles.grantForm.roleHint')}
          required
        >
          <Combobox
            id="g-role"
            options={roleOptions}
            value={roleSel}
            onChange={setRoleSel}
            placeholder={t('console:roles.grantForm.scopeRefPlaceholder')}
          />
        </Field>

        <Field
          label={t('console:roles.grantForm.scopeTree')}
          htmlFor="g-scopetree"
        >
          <Select
            value={scopeTree}
            onValueChange={(v) => {
              setScopeTree(v as 'tenant' | 'workspace' | 'agent_group')
              setScopeRef(null)
              setScopeClass('any')
            }}
          >
            <SelectTrigger
              id="g-scopetree"
              aria-label={t('console:roles.grantForm.scopeTree')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="tenant">
                {t('console:roles.grantForm.scopeTenant')}
              </SelectItem>
              <SelectItem value="workspace">
                {t('console:roles.grantForm.scopeWorkspace')}
              </SelectItem>
              <SelectItem value="agent_group">
                {t('console:roles.grantForm.scopeAgentGroup')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {scopeTree === 'workspace' && (
          <Field
            label={t('console:roles.grantForm.scopeRefWorkspace')}
            htmlFor="g-ws"
            required
          >
            <Combobox
              id="g-ws"
              options={workspaces.map((w) => ({
                value: w.slug,
                label: w.name,
                keywords: [w.slug],
              }))}
              value={scopeRef}
              onChange={setScopeRef}
              placeholder={t('console:roles.grantForm.scopeRefPlaceholder')}
            />
          </Field>
        )}
        {scopeTree === 'agent_group' && (
          <Field
            label={t('console:roles.grantForm.scopeRefGroup')}
            htmlFor="g-ag"
            required
          >
            <Combobox
              id="g-ag"
              options={agentGroups.map((a) => ({
                value: a.slug,
                label: a.name,
                keywords: [a.slug],
              }))}
              value={scopeRef}
              onChange={setScopeRef}
              placeholder={t('console:roles.grantForm.scopeRefPlaceholder')}
            />
          </Field>
        )}

        <Field
          label={t('console:roles.grantForm.scopeClass')}
          htmlFor="g-class"
          description={t('console:roles.grantForm.scopeClassHint')}
        >
          <Select value={scopeClass} onValueChange={setScopeClass}>
            <SelectTrigger
              id="g-class"
              aria-label={t('console:roles.grantForm.scopeClass')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="any">
                {t('console:roles.grantForm.scopeClassAny')}
              </SelectItem>
              {classOptions.map((k) => (
                <SelectItem key={k} value={k}>
                  {k}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field
          label={t('console:roles.grantForm.note')}
          htmlFor="g-note"
          description={t('console:roles.grantForm.noteHint')}
        >
          <Textarea
            id="g-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
          />
        </Field>

        <FormError error={mutation.error} />
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => mutation.mutate()}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:roles.grantForm.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}

// --- custom roles ------------------------------------------------------------

function RolesSection({
  catalog,
  roles,
  groups,
  loading,
  isError,
  refetch,
  canAdmin,
}: {
  catalog?: RBACCatalogDTO
  roles: CustomRoleDTO[]
  groups: PermGroupDTO[]
  loading: boolean
  isError: boolean
  refetch: () => void
  canAdmin: boolean
}) {
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
          <h2 className="text-base font-semibold text-foreground">
            {t('console:roles.defs.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:roles.defs.caption')}
          </p>
        </div>
        {canAdmin && (
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
        <ErrorState retry={refetch} />
      ) : roles.length === 0 ? (
        <EmptyState title={t('console:roles.defs.none')} icon={<KeyRound />} />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:roles.defs.name')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:roles.defs.permissions')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:roles.defs.groups')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {roles.map((r) => (
                <tr key={r.name} className="border-t border-border">
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {r.name}
                    </span>
                    {r.display_name && (
                      <span className="ml-2 text-muted-foreground">
                        {r.display_name}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="neutral">{r.permissions.length}</Badge>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {(r.groups ?? []).join(', ') || '—'}
                  </td>
                  <td className="px-3 py-2 text-right">
                    {canAdmin && (
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setEditing(r)}
                        >
                          <Pencil />
                          {t('console:roles.defs.edit')}
                        </Button>
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
          </table>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
          {createOpen && (
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
          {editing && (
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

function RoleForm({
  catalog,
  groups,
  existing,
  onClose,
}: {
  catalog?: RBACCatalogDTO
  groups: PermGroupDTO[]
  existing?: CustomRoleDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!existing
  const [name, setName] = useState(existing?.name ?? '')
  const [displayName, setDisplayName] = useState(existing?.display_name ?? '')
  const [description, setDescription] = useState(existing?.description ?? '')
  const [perms, setPerms] = useState<string[]>(existing?.permissions ?? [])
  const [selGroups, setSelGroups] = useState<string[]>(existing?.groups ?? [])
  // Structured subtraction: a live BASE minus an explicit exclusion set.
  const [baseRole, setBaseRole] = useState<string>(existing?.base_role ?? '')
  const [excludes, setExcludes] = useState<string[]>(existing?.excludes ?? [])

  const mutation = usePrivilegedMutation<void, CustomRoleDTO>({
    mutationFn: () => {
      const body: CustomRoleDTO = {
        name: name.trim(),
        display_name: displayName.trim() || undefined,
        description: description.trim() || undefined,
        base_role: baseRole || undefined,
        permissions: perms,
        groups: selGroups.length ? selGroups : undefined,
        excludes: excludes.length ? excludes : undefined,
      }
      return isEdit
        ? consoleApi.updateRole(existing.name, body)
        : consoleApi.createRole(body)
    },
    invalidateKeys: () => [
      consoleKeys.roles(activeTenant),
      consoleKeys.delegationAuthority(activeTenant),
    ],
    successMessage: isEdit
      ? t('console:roles.defs.updated')
      : t('console:roles.defs.created'),
    onDone: onClose,
  })

  const nameValid = isEdit || /^[A-Za-z0-9._-]{1,64}$/.test(name.trim())
  const valid =
    nameValid && (perms.length > 0 || selGroups.length > 0 || baseRole !== '')

  const toggleGroup = (g: string) => {
    setSelGroups((prev) =>
      prev.includes(g) ? prev.filter((x) => x !== g) : [...prev, g],
    )
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:roles.roleForm.editTitle')
            : t('console:roles.roleForm.createTitle')}
        </DialogTitle>
        <DialogDescription>{t('console:roles.defs.caption')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('console:roles.roleForm.name')}
          htmlFor="r-name"
          description={t('console:roles.roleForm.nameHint')}
          required
        >
          <Input
            id="r-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            mono
            disabled={isEdit}
          />
        </Field>
        <Field
          label={t('console:roles.roleForm.displayName')}
          htmlFor="r-display"
        >
          <Input
            id="r-display"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        </Field>
        <Field label={t('console:roles.roleForm.description')} htmlFor="r-desc">
          <Textarea
            id="r-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={2}
          />
        </Field>
        <Field
          label={t('console:roles.roleForm.baseRole')}
          htmlFor="r-base"
          description={t('console:roles.roleForm.baseRoleHint')}
        >
          <Select
            value={baseRole || 'none'}
            onValueChange={(v) => setBaseRole(v === 'none' ? '' : v)}
          >
            <SelectTrigger id="r-base">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="none">
                {t('console:roles.roleForm.baseRoleNone')}
              </SelectItem>
              {(catalog?.builtin_roles ?? []).map((r) => (
                <SelectItem key={r} value={r}>
                  {r}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field
          label={t('console:roles.roleForm.permissions')}
          description={t('console:roles.roleForm.permissionsHint')}
        >
          <PermissionMatrix
            catalog={catalog}
            value={perms}
            onChange={setPerms}
          />
        </Field>
        <Field
          label={t('console:roles.roleForm.excludes')}
          description={t('console:roles.roleForm.excludesHint')}
        >
          <PermissionMatrix
            catalog={catalog}
            value={excludes}
            onChange={setExcludes}
            excluding
          />
        </Field>
        <Field
          label={t('console:roles.roleForm.groups')}
          description={t('console:roles.roleForm.groupsHint')}
        >
          {groups.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t('console:roles.roleForm.noGroups')}
            </p>
          ) : (
            <div className="flex flex-col gap-2">
              {groups.map((g) => (
                <label key={g.name} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={selGroups.includes(g.name)}
                    onCheckedChange={() => toggleGroup(g.name)}
                    aria-label={g.name}
                  />
                  <span className="font-mono text-xs text-foreground">
                    {g.name}
                  </span>
                  {g.display_name && (
                    <span className="text-muted-foreground">
                      {g.display_name}
                    </span>
                  )}
                </label>
              ))}
            </div>
          )}
        </Field>
        <FormError error={mutation.error} />
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => mutation.mutate()}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:roles.roleForm.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}

// --- permission-groups -------------------------------------------------------

function GroupsSection({
  catalog,
  groups,
  loading,
  isError,
  refetch,
  canAdmin,
}: {
  catalog?: RBACCatalogDTO
  groups: PermGroupDTO[]
  loading: boolean
  isError: boolean
  refetch: () => void
  canAdmin: boolean
}) {
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
          <h2 className="text-base font-semibold text-foreground">
            {t('console:roles.groups.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:roles.groups.caption')}
          </p>
        </div>
        {canAdmin && (
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
        <ErrorState retry={refetch} />
      ) : groups.length === 0 ? (
        <EmptyState title={t('console:roles.groups.none')} icon={<Layers />} />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:roles.groups.name')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:roles.groups.permissions')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <tr key={g.name} className="border-t border-border">
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {g.name}
                    </span>
                    {g.display_name && (
                      <span className="ml-2 text-muted-foreground">
                        {g.display_name}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="neutral">{g.permissions.length}</Badge>
                  </td>
                  <td className="px-3 py-2 text-right">
                    {canAdmin && (
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setEditing(g)}
                        >
                          <Pencil />
                          {t('console:roles.groups.edit')}
                        </Button>
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
          </table>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
          {createOpen && (
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
          {editing && (
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

function GroupForm({
  catalog,
  existing,
  onClose,
}: {
  catalog?: RBACCatalogDTO
  existing?: PermGroupDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!existing
  const [name, setName] = useState(existing?.name ?? '')
  const [displayName, setDisplayName] = useState(existing?.display_name ?? '')
  const [description, setDescription] = useState(existing?.description ?? '')
  const [perms, setPerms] = useState<string[]>(existing?.permissions ?? [])

  const mutation = usePrivilegedMutation<void, PermGroupDTO>({
    mutationFn: () => {
      const body: PermGroupDTO = {
        name: name.trim(),
        display_name: displayName.trim() || undefined,
        description: description.trim() || undefined,
        permissions: perms,
      }
      return isEdit
        ? consoleApi.updatePermGroup(existing.name, body)
        : consoleApi.createPermGroup(body)
    },
    invalidateKeys: () => [
      consoleKeys.permGroups(activeTenant),
      consoleKeys.delegationAuthority(activeTenant),
    ],
    successMessage: isEdit
      ? t('console:roles.groups.updated')
      : t('console:roles.groups.created'),
    onDone: onClose,
  })

  const nameValid = isEdit || /^[A-Za-z0-9._-]{1,64}$/.test(name.trim())
  const valid = nameValid && perms.length > 0

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:roles.groupForm.editTitle')
            : t('console:roles.groupForm.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {t('console:roles.groups.caption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('console:roles.groupForm.name')}
          htmlFor="pg-name"
          required
        >
          <Input
            id="pg-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            mono
            disabled={isEdit}
          />
        </Field>
        <Field
          label={t('console:roles.groupForm.displayName')}
          htmlFor="pg-display"
        >
          <Input
            id="pg-display"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        </Field>
        <Field
          label={t('console:roles.groupForm.description')}
          htmlFor="pg-desc"
        >
          <Textarea
            id="pg-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={2}
          />
        </Field>
        <Field label={t('console:roles.groupForm.permissions')}>
          <PermissionMatrix
            catalog={catalog}
            value={perms}
            onChange={setPerms}
          />
        </Field>
        <FormError error={mutation.error} />
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => mutation.mutate()}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:roles.groupForm.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
