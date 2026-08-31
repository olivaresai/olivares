// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Layers, Pencil, Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import type { TenantRequestOptions } from '@/lib/api/client'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  type ModelAccessDTO,
  type ModelGroupDTO,
} from './api'
import { FormError } from './roles-shared'

/**
 * ModelGovernanceSection surfaces model-groups and model-access rules.
 * Model-groups are named sets of models (explicit refs + family/tier selectors).
 * Model-access rules carry an allow/forbid effect and gate model use by subject.
 * Permission checks for the models namespace are managed internally — the backend
 * remains the authority; this panel is an authoring interface only.
 */
export function ModelGovernanceSection() {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const tenantRequest: TenantRequestOptions = { tenant: activeTenant }
  const canReadGroups = can('models:model-group:read')
  const canWriteGroups = can('models:model-group:write')
  const canReadAccess = can('models:model-access:read')
  const canAdminAccess = can('models:model-access:admin')

  // ⛔ ESTA SECCIÓN DUPLICA DOS LISTAS DEL MÓDULO `models`, y las duplicaba también sin techo:
  //    el contraste de las midió como dos de los tres consumidores que se quedaban fuera
  //    cuando cerré aquel módulo. Las reglas de acceso deciden QUIÉN PUEDE USAR QUÉ MODELO.
  //    ⛔ AQUI VIVIA `const listParams = { limit: 1000 }`. Se retira al integrar el 2026-08-28:
  //    `main` sirve estas dos listas por `tenantRequest` (TenantRequestOptions), que ademas de
  //    poner el techo las ata al inquilino activo. El techo literal 1000 era la forma anterior y
  //    quedaria por DEBAJO del gobernado sin decirlo. La preocupacion de arriba sigue valiendo:
  //    lo que cambia es quien pone el techo, no que haga falta.
  const modelGroupsQuery = useQuery({
    queryKey: consoleKeys.modelGroups(activeTenant),
    queryFn: () => consoleApi.listModelGroups(tenantRequest),
    enabled: canReadGroups,
  })
  const modelAccessQuery = useQuery({
    queryKey: consoleKeys.modelAccess(activeTenant),
    queryFn: () => consoleApi.listModelAccess(tenantRequest),
    enabled: canReadAccess,
  })

  return (
    <>
      {modelGroupsQuery.data?.has_more && !modelGroupsQuery.error ? (
        <div>
          <Badge
            variant="warning"
            title={t('console:granular.modelGroups.truncatedHint')}
          >
            {t('console:granular.modelGroups.truncated')}
          </Badge>
        </div>
      ) : null}
      {modelAccessQuery.data?.has_more && !modelAccessQuery.error ? (
        <div>
          <Badge
            variant="warning"
            title={t('console:granular.modelAccess.truncatedHint')}
          >
            {t('console:granular.modelAccess.truncated')}
          </Badge>
        </div>
      ) : null}
      <ModelGroupsSection
        items={modelGroupsQuery.data?.items ?? []}
        loading={modelGroupsQuery.isLoading}
        isError={modelGroupsQuery.isError}
        refetch={() => void modelGroupsQuery.refetch()}
        canRead={canReadGroups}
        canWrite={canWriteGroups}
      />
      <ModelAccessSection
        items={modelAccessQuery.data?.items ?? []}
        loading={modelAccessQuery.isLoading}
        isError={modelAccessQuery.isError}
        refetch={() => void modelAccessQuery.refetch()}
        canRead={canReadAccess}
        canAdmin={canAdminAccess}
      />
    </>
  )
}

// --- model-groups -------------------------------------------------------------

function ModelGroupsSection({
  items,
  loading,
  isError,
  refetch,
  canRead,
  canWrite,
}: {
  items: ModelGroupDTO[]
  loading: boolean
  isError: boolean
  refetch: () => void
  canRead: boolean
  canWrite: boolean
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [editing, setEditing] = useState<ModelGroupDTO | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [del, setDel] = useState<ModelGroupDTO | null>(null)

  const deleteMutation = usePrivilegedMutation<
    { id: string; tenant: string | null },
    void
  >({
    mutationFn: ({ id, tenant }) => consoleApi.deleteModelGroup(id, { tenant }),
    invalidateKeys: (_data, { tenant }) => [consoleKeys.modelGroups(tenant)],
    successMessage: t('console:granular.modelGroups.deleted'),
    onDone: () => setDel(null),
  })

  if (!canRead) {
    return (
      <section className="flex flex-col gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:granular.modelGroups.title')}
          </h2>
        </div>
        <EmptyState
          title={t('console:roles.readOnlyNotice')}
          icon={<ShieldCheck />}
        />
      </section>
    )
  }

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:granular.modelGroups.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:granular.modelGroups.caption')}
          </p>
        </div>
        {canWrite && (
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('console:granular.modelGroups.create')}
          </Button>
        )}
      </div>
      {loading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : isError ? (
        <ErrorState retry={refetch} />
      ) : items.length === 0 ? (
        <EmptyState
          title={t('console:granular.modelGroups.none')}
          icon={<Layers />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelGroups.colName')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelGroups.colMembers')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelGroups.colFamilies')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelGroups.colTiers')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {items.map((g) => (
                <tr key={g.id ?? g.name} className="border-t border-border">
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {g.name}
                    </span>
                    {g.description && (
                      <span className="ml-2 text-muted-foreground">
                        {g.description}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="neutral">{g.member_refs.length}</Badge>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {g.family_selectors.join(', ') || '—'}
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {g.tier_selectors.join(', ') || '—'}
                  </td>
                  <td className="px-3 py-2 text-right">
                    {canWrite && (
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setEditing(g)}
                        >
                          <Pencil />
                          {t('console:granular.modelGroups.edit')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setDel(g)}
                        >
                          <Trash2 />
                          {t('console:granular.modelGroups.delete')}
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
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {createOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <ModelGroupForm onClose={() => setCreateOpen(false)} />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {editing && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <ModelGroupForm
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
        title={t('console:granular.modelGroups.deleteTitle')}
        description={t('console:granular.modelGroups.deleteBody')}
        confirmLabel={t('console:granular.modelGroups.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() =>
          del?.id && deleteMutation.mutate({ id: del.id, tenant: activeTenant })
        }
      />
    </section>
  )
}

function ModelGroupForm({
  existing,
  onClose,
}: {
  existing?: ModelGroupDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!existing

  const [name, setName] = useState(existing?.name ?? '')
  const [memberRefs, setMemberRefs] = useState(
    (existing?.member_refs ?? []).join('\n'),
  )
  const [familySelectors, setFamilySelectors] = useState(
    (existing?.family_selectors ?? []).join('\n'),
  )
  const [tierSelectors, setTierSelectors] = useState(
    (existing?.tier_selectors ?? []).join('\n'),
  )
  const [description, setDescription] = useState(existing?.description ?? '')

  const splitLines = (s: string) =>
    s
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)

  const mutation = usePrivilegedMutation<
    { body: ModelGroupDTO; id?: string; tenant: string | null },
    ModelGroupDTO
  >({
    mutationFn: ({ body, id, tenant }) =>
      id
        ? consoleApi.updateModelGroup(id, body, { tenant })
        : consoleApi.createModelGroup(body, { tenant }),
    invalidateKeys: (_data, { tenant }) => [consoleKeys.modelGroups(tenant)],
    successMessage: isEdit
      ? t('console:granular.modelGroups.updated')
      : t('console:granular.modelGroups.created'),
    onDone: onClose,
  })

  const refs = splitLines(memberRefs)
  const families = splitLines(familySelectors)
  const tiers = splitLines(tierSelectors)
  const nameValid = name.trim().length > 0
  const selectorValid =
    refs.length > 0 || families.length > 0 || tiers.length > 0
  const valid = nameValid && selectorValid

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:granular.modelGroups.editTitle')
            : t('console:granular.modelGroups.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {t('console:granular.modelGroups.caption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('console:granular.modelGroups.name')}
          htmlFor="mg-name"
          required
        >
          <Input
            id="mg-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            mono
            disabled={isEdit}
          />
        </Field>
        <Field
          label={t('console:granular.modelGroups.memberRefs')}
          htmlFor="mg-members"
          description={t('console:granular.modelGroups.memberRefsHint')}
        >
          <Textarea
            id="mg-members"
            value={memberRefs}
            onChange={(e) => setMemberRefs(e.target.value)}
            rows={4}
          />
        </Field>
        <Field
          label={t('console:granular.modelGroups.familySelectors')}
          htmlFor="mg-families"
          description={t('console:granular.modelGroups.familySelectorsHint')}
        >
          <Textarea
            id="mg-families"
            value={familySelectors}
            onChange={(e) => setFamilySelectors(e.target.value)}
            rows={3}
          />
        </Field>
        <Field
          label={t('console:granular.modelGroups.tierSelectors')}
          htmlFor="mg-tiers"
          description={t('console:granular.modelGroups.tierSelectorsHint')}
        >
          <Textarea
            id="mg-tiers"
            value={tierSelectors}
            onChange={(e) => setTierSelectors(e.target.value)}
            rows={3}
          />
        </Field>
        <Field
          label={t('console:granular.modelGroups.description')}
          htmlFor="mg-desc"
        >
          <Textarea
            id="mg-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
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
          onClick={() =>
            mutation.mutate({
              body: {
                name: name.trim(),
                member_refs: splitLines(memberRefs),
                family_selectors: splitLines(familySelectors),
                tier_selectors: splitLines(tierSelectors),
                description: description.trim() || undefined,
              },
              id: isEdit ? existing.id : undefined,
              tenant: activeTenant,
            })
          }
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:granular.modelGroups.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}

// --- model-access rules -------------------------------------------------------

function ModelAccessSection({
  items,
  loading,
  isError,
  refetch,
  canRead,
  canAdmin,
}: {
  items: ModelAccessDTO[]
  loading: boolean
  isError: boolean
  refetch: () => void
  canRead: boolean
  canAdmin: boolean
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [editing, setEditing] = useState<ModelAccessDTO | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [del, setDel] = useState<ModelAccessDTO | null>(null)

  const deleteMutation = usePrivilegedMutation<
    { id: string; tenant: string | null },
    void
  >({
    mutationFn: ({ id, tenant }) =>
      consoleApi.deleteModelAccess(id, { tenant }),
    invalidateKeys: (_data, { tenant }) => [consoleKeys.modelAccess(tenant)],
    successMessage: t('console:granular.modelAccess.deleted'),
    onDone: () => setDel(null),
  })

  if (!canRead) {
    return (
      <section className="flex flex-col gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:granular.modelAccess.title')}
          </h2>
        </div>
        <EmptyState
          title={t('console:roles.readOnlyNotice')}
          icon={<ShieldCheck />}
        />
      </section>
    )
  }

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:granular.modelAccess.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:granular.modelAccess.caption')}
          </p>
        </div>
        {canAdmin && (
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('console:granular.modelAccess.create')}
          </Button>
        )}
      </div>
      {loading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : isError ? (
        <ErrorState retry={refetch} />
      ) : items.length === 0 ? (
        <EmptyState
          title={t('console:granular.modelAccess.none')}
          icon={<ShieldCheck />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelAccess.colSubject')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelAccess.colTarget')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelAccess.colEffect')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelAccess.colWorkspace')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.modelAccess.colSurfaces')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {items.map((a) => (
                <tr key={a.id} className="border-t border-border">
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {a.subject_kind}:{a.subject_ref}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {a.target_kind}:{a.target_ref}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <Badge
                      variant={a.effect === 'forbid' ? 'danger' : 'neutral'}
                    >
                      {a.effect === 'forbid'
                        ? t('console:granular.modelAccess.effectForbid')
                        : t('console:granular.modelAccess.effectAllow')}
                    </Badge>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {a.workspace_ref ||
                      t('console:granular.modelAccess.tenantWide')}
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {a.surfaces.length > 0
                      ? a.surfaces.join(', ')
                      : t('console:granular.modelAccess.allSurfaces')}
                  </td>
                  <td className="px-3 py-2 text-right">
                    {canAdmin && (
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setEditing(a)}
                        >
                          <Pencil />
                          {t('console:granular.modelAccess.edit')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setDel(a)}
                        >
                          <Trash2 />
                          {t('console:granular.modelAccess.delete')}
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
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {createOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <ModelAccessForm onClose={() => setCreateOpen(false)} />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {editing && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <ModelAccessForm
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
        title={t('console:granular.modelAccess.deleteTitle')}
        description={t('console:granular.modelAccess.deleteBody')}
        confirmLabel={t('console:granular.modelAccess.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() =>
          del?.id && deleteMutation.mutate({ id: del.id, tenant: activeTenant })
        }
      />
    </section>
  )
}

function ModelAccessForm({
  existing,
  onClose,
}: {
  existing?: ModelAccessDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!existing

  const [subjectKind, setSubjectKind] = useState(
    existing?.subject_kind ?? 'user',
  )
  const [subjectRef, setSubjectRef] = useState(existing?.subject_ref ?? '')
  const [targetKind, setTargetKind] = useState(existing?.target_kind ?? 'model')
  const [targetRef, setTargetRef] = useState(existing?.target_ref ?? '')
  const [effect, setEffect] = useState(existing?.effect ?? 'allow')
  const [workspaceRef, setWorkspaceRef] = useState(
    existing?.workspace_ref ?? '',
  )
  const [surfaces, setSurfaces] = useState(
    (existing?.surfaces ?? []).join('\n'),
  )
  const [description, setDescription] = useState(existing?.description ?? '')

  const splitLines = (s: string) =>
    s
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)

  const mutation = usePrivilegedMutation<
    { body: ModelAccessDTO; id?: string; tenant: string | null },
    ModelAccessDTO
  >({
    mutationFn: ({ body, id, tenant }) =>
      id
        ? consoleApi.updateModelAccess(id, body, { tenant })
        : consoleApi.createModelAccess(body, { tenant }),
    invalidateKeys: (_data, { tenant }) => [consoleKeys.modelAccess(tenant)],
    successMessage: isEdit
      ? t('console:granular.modelAccess.updated')
      : t('console:granular.modelAccess.created'),
    onDone: onClose,
  })

  const valid =
    subjectKind.length > 0 &&
    subjectRef.trim().length > 0 &&
    targetKind.length > 0 &&
    targetRef.trim().length > 0

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:granular.modelAccess.editTitle')
            : t('console:granular.modelAccess.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {t('console:granular.modelAccess.caption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('console:granular.modelAccess.subjectKind')}
          htmlFor="ma-subkind"
        >
          <Select value={subjectKind} onValueChange={setSubjectKind}>
            <SelectTrigger id="ma-subkind">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user">
                {t('console:granular.modelAccess.subjectUser')}
              </SelectItem>
              <SelectItem value="role">
                {t('console:granular.modelAccess.subjectRole')}
              </SelectItem>
              <SelectItem value="agent_group">
                {t('console:granular.modelAccess.subjectAgentGroup')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field
          label={t('console:granular.modelAccess.subjectRef')}
          htmlFor="ma-subref"
          description={t('console:granular.modelAccess.subjectRefHint')}
          required
        >
          <Input
            id="ma-subref"
            value={subjectRef}
            onChange={(e) => setSubjectRef(e.target.value)}
            mono
          />
        </Field>
        <Field
          label={t('console:granular.modelAccess.targetKind')}
          htmlFor="ma-tgtkind"
        >
          <Select value={targetKind} onValueChange={setTargetKind}>
            <SelectTrigger id="ma-tgtkind">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="model">
                {t('console:granular.modelAccess.targetModel')}
              </SelectItem>
              <SelectItem value="model_group">
                {t('console:granular.modelAccess.targetModelGroup')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field
          label={t('console:granular.modelAccess.targetRef')}
          htmlFor="ma-tgtref"
          description={t('console:granular.modelAccess.targetRefHint')}
          required
        >
          <Input
            id="ma-tgtref"
            value={targetRef}
            onChange={(e) => setTargetRef(e.target.value)}
            mono
          />
        </Field>
        <Field
          label={t('console:granular.modelAccess.effect')}
          htmlFor="ma-effect"
          description={t('console:granular.modelAccess.effectHint')}
        >
          <Select value={effect} onValueChange={setEffect}>
            <SelectTrigger
              id="ma-effect"
              aria-label={t('console:granular.modelAccess.effect')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="allow">
                {t('console:granular.modelAccess.effectAllow')}
              </SelectItem>
              <SelectItem value="forbid">
                {t('console:granular.modelAccess.effectForbid')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field
          label={t('console:granular.modelAccess.workspaceRef')}
          htmlFor="ma-ws"
          description={t('console:granular.modelAccess.workspaceRefHint')}
        >
          <Input
            id="ma-ws"
            value={workspaceRef}
            onChange={(e) => setWorkspaceRef(e.target.value)}
            mono
          />
        </Field>
        <Field
          label={t('console:granular.modelAccess.surfaces')}
          htmlFor="ma-surfaces"
          description={t('console:granular.modelAccess.surfacesHint')}
        >
          <Textarea
            id="ma-surfaces"
            value={surfaces}
            onChange={(e) => setSurfaces(e.target.value)}
            rows={3}
          />
        </Field>
        <Field
          label={t('console:granular.modelAccess.description')}
          htmlFor="ma-desc"
        >
          <Textarea
            id="ma-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
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
          onClick={() =>
            mutation.mutate({
              body: {
                subject_kind: subjectKind,
                subject_ref: subjectRef.trim(),
                target_kind: targetKind,
                target_ref: targetRef.trim(),
                effect,
                workspace_ref: workspaceRef.trim() || undefined,
                surfaces: splitLines(surfaces),
                description: description.trim() || undefined,
              },
              id: isEdit ? existing.id : undefined,
              tenant: activeTenant,
            })
          }
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:granular.modelAccess.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
