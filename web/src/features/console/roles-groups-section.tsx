// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { GitBranch, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { consoleApi, consoleKeys, type GroupDTO } from './api'
import { FormError } from './roles-shared'

/** The tenant roles a group may be mapped to, and the sentinel the picker uses
 * for "no mapping" (an empty Select value is indistinguishable from unset). */
const GROUP_ROLES = ['viewer', 'editor', 'admin', 'owner'] as const
const NO_ROLE = '__none__'

export function GroupHierarchySection({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()

  const query = useQuery({
    queryKey: consoleKeys.groups(activeTenant),
    queryFn: () => consoleApi.listGroups(),
  })

  const [editing, setEditing] = useState<GroupDTO | null>(null)
  const [clearing, setClearing] = useState<GroupDTO | null>(null)
  const [mapping, setMapping] = useState<GroupDTO | null>(null)

  const clearMutation = usePrivilegedMutation<string, { id: string }>({
    mutationFn: (id) => consoleApi.setGroupParent(id, ''),
    invalidateKeys: () => [consoleKeys.groups(activeTenant)],
    successMessage: t('console:granular.groupSubjects.parentSet'),
    onDone: () => setClearing(null),
  })

  const roleMutation = usePrivilegedMutation<
    { id: string; role: string },
    { id: string; mapped_role: string }
  >({
    mutationFn: ({ id, role }) => consoleApi.setGroupRole(id, role),
    invalidateKeys: () => [consoleKeys.groups(activeTenant)],
    successMessage: t('console:granular.groupSubjects.roleSet'),
    onDone: () => setMapping(null),
  })

  const groups = query.data?.groups ?? []

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-base font-semibold text-foreground">
          {t('console:granular.groupSubjects.title')}
        </h2>
        <p className="max-w-2xl text-sm text-muted-foreground">
          {t('console:granular.groupSubjects.caption')}
        </p>
      </div>

      {query.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : query.isError ? (
        <ErrorState retry={() => void query.refetch()} />
      ) : groups.length === 0 ? (
        <EmptyState
          title={t('console:granular.groupSubjects.noGroups')}
          icon={<GitBranch />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.groupSubjects.colName')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.groupSubjects.colParent')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.groupSubjects.colMembers')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:granular.groupSubjects.colRole')}
                </th>
                {canAdmin && <th className="px-3 py-2" />}
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <tr key={g.id} className="border-t border-border">
                  <td className="px-3 py-2 font-mono text-xs text-foreground">
                    {g.display_name || g.id}
                  </td>
                  <td className="px-3 py-2 text-foreground">
                    {g.parent_group_id
                      ? (groups.find((p) => p.id === g.parent_group_id)
                          ?.display_name ?? g.parent_group_id)
                      : t('console:granular.groupSubjects.noParent')}
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="neutral">{g.members}</Badge>
                  </td>
                  <td className="px-3 py-2">
                    {g.mapped_role ? (
                      <Badge variant="accent">{g.mapped_role}</Badge>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </td>
                  {canAdmin && (
                    <td className="px-3 py-2 text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setMapping(g)}
                        >
                          {g.mapped_role
                            ? t('console:granular.groupSubjects.changeRole')
                            : t('console:granular.groupSubjects.mapRole')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setEditing(g)}
                        >
                          {t('console:granular.groupSubjects.setParent')}
                        </Button>
                        {g.parent_group_id && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setClearing(g)}
                          >
                            <Trash2 />
                            {t('console:granular.groupSubjects.clearParent')}
                          </Button>
                        )}
                      </div>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
      >
        <DialogContent className="max-w-lg">
          {editing && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <SetParentForm
                group={editing}
                allGroups={groups}
                onClose={() => setEditing(null)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={mapping !== null}
        onOpenChange={(o) => !o && setMapping(null)}
      >
        <DialogContent className="max-w-lg">
          {mapping && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <MapRoleForm
                group={mapping}
                pending={roleMutation.isPending}
                error={roleMutation.error}
                onSubmit={(role) =>
                  roleMutation.mutate({ id: mapping.id, role })
                }
                onClose={() => setMapping(null)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={clearing !== null}
        onOpenChange={(o) => !o && setClearing(null)}
        title={t('console:granular.groupSubjects.clearParentTitle')}
        description={t('console:granular.groupSubjects.clearParentBody', {
          child: clearing?.display_name ?? '',
        })}
        confirmLabel={t('console:granular.groupSubjects.clearParent')}
        tone="danger"
        pending={clearMutation.isPending}
        onConfirm={() => clearing && clearMutation.mutate(clearing.id)}
      />
    </section>
  )
}

function SetParentForm({
  group,
  allGroups,
  onClose,
}: {
  group: GroupDTO
  allGroups: GroupDTO[]
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [parentId, setParentId] = useState<string | null>(
    group.parent_group_id || null,
  )

  const candidates = allGroups.filter((g) => g.id !== group.id)

  const mutation = usePrivilegedMutation<void, { id: string }>({
    mutationFn: () => consoleApi.setGroupParent(group.id, parentId ?? ''),
    invalidateKeys: () => [consoleKeys.groups(activeTenant)],
    successMessage: t('console:granular.groupSubjects.parentSet'),
    onDone: onClose,
  })

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {t('console:granular.groupSubjects.setParentTitle')}
        </DialogTitle>
        <DialogDescription>
          {t('console:granular.groupSubjects.setParentBody', {
            child: group.display_name,
            parent:
              candidates.find((g) => g.id === parentId)?.display_name ?? '…',
          })}
        </DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-4">
        <Field
          label={t('console:granular.groupSubjects.colParent')}
          htmlFor="gh-parent"
        >
          <Combobox
            id="gh-parent"
            options={candidates.map((g) => ({
              value: g.id,
              label: g.display_name || g.id,
              keywords: [g.display_name, g.external_id].filter(
                Boolean,
              ) as string[],
            }))}
            value={parentId}
            onChange={setParentId}
            placeholder={t('console:granular.groupSubjects.selectParent')}
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
          disabled={!parentId || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:granular.groupSubjects.setParent')}
        </Button>
      </DialogFooter>
    </>
  )
}

/** MapRoleForm maps a directory group to a tenant role, or clears the mapping.
 *
 * Two things this form must not get wrong. First, BLAST RADIUS: mapping a role
 * grants it to every current member at once, so the count goes in front of the
 * operator before they commit — the group row already carries it. Second, the
 * ROLE CEILING: the server refuses a rank above the actor's own with a distinct
 * code, and that is NOT the same as lacking permission. Saying "not authorized"
 * would send an admin chasing a permission they already hold, when what they
 * actually need is someone more senior. Clearing narrows, so it has no ceiling.
 */
function MapRoleForm({
  group,
  pending,
  error,
  onSubmit,
  onClose,
}: {
  group: GroupDTO
  pending: boolean
  error: unknown
  onSubmit: (role: string) => void
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const [role, setRole] = useState(group.mapped_role ?? '')
  const ceiling = error instanceof ApiError && error.code === 'role_ceiling'

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {t('console:granular.groupSubjects.mapRoleTitle', {
            group: group.display_name,
          })}
        </DialogTitle>
        <DialogDescription>
          {t('console:granular.groupSubjects.mapRoleBody', {
            count: group.members,
          })}
        </DialogDescription>
      </DialogHeader>
      <Field
        label={t('console:granular.groupSubjects.role')}
        htmlFor="group-role"
        description={t('console:granular.groupSubjects.roleHint')}
      >
        <Select value={role} onValueChange={setRole}>
          <SelectTrigger id="group-role">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NO_ROLE}>
              {t('console:granular.groupSubjects.roleNone')}
            </SelectItem>
            {GROUP_ROLES.map((r) => (
              <SelectItem key={r} value={r}>
                {r}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      {ceiling ? (
        <p role="alert" className="text-sm text-danger">
          {t('console:granular.groupSubjects.roleCeiling')}
        </p>
      ) : (
        <FormError error={error} />
      )}
      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          disabled={pending}
          onClick={() => onSubmit(role === NO_ROLE ? '' : role)}
        >
          {t('console:granular.groupSubjects.saveRole')}
        </Button>
      </DialogFooter>
    </>
  )
}
