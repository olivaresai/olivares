// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import {
  Archive,
  Boxes,
  Pencil,
  Plus,
  ShieldOff,
  Trash2,
  UserPlus,
  Users,
} from 'lucide-react'
import { useMemo, useState } from 'react'
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
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
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
import { ListTruncationBadge } from '@/features/_intel'
import type { AgentDTO } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useWorkspaceFilter } from '@/lib/hooks/use-workspace-filter'
import {
  consoleApi,
  consoleKeys,
  type AgentGroupMemberDTO,
  type AgentGroupDTO,
  type WorkspaceDTO,
} from './api'
import { FormError } from './roles-shared'

const WORKSPACE_NONE = '__none__'
const GROUP_STATUSES = ['active', 'inactive'] as const
const AGENT_LIST_PARAMS = { limit: 200 }

export function ScopesTab() {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, activeRole, isSuperadmin, can } = useAuth()
  const { workspaceId, queryKey: wsKey } = useWorkspaceFilter()
  const canReadGroups = can('agent:read')
  const canManageGroups = can('agent:write')
  const isOwner = isSuperadmin || activeRole === 'owner'

  const [wsCreateOpen, setWsCreateOpen] = useState(false)
  const [groupCreateOpen, setGroupCreateOpen] = useState(false)
  const [editGroup, setEditGroup] = useState<AgentGroupDTO | null>(null)
  const [membersGroup, setMembersGroup] = useState<AgentGroupDTO | null>(null)
  const [renameWorkspace, setRenameWorkspace] = useState<WorkspaceDTO | null>(
    null,
  )
  const [archive, setArchive] = useState<WorkspaceDTO | null>(null)
  const [deleteGroup, setDeleteGroup] = useState<AgentGroupDTO | null>(null)

  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant),
    queryFn: () => consoleApi.listWorkspaces(),
    enabled: can('tenant:read'),
  })
  const groups = useQuery({
    queryKey: [...consoleKeys.agentGroups(activeTenant), wsKey],
    queryFn: () => consoleApi.listAgentGroups({ workspace_id: workspaceId }),
    enabled: canReadGroups,
  })

  const archiveMutation = usePrivilegedMutation<string, WorkspaceDTO>({
    mutationFn: (id) => consoleApi.updateWorkspace(id, { status: 'inactive' }),
    invalidateKeys: () => [consoleKeys.workspaces(activeTenant)],
    successMessage: t('console:workspaces.archived'),
    onDone: () => setArchive(null),
  })
  const deleteGroupMutation = usePrivilegedMutation<string, void>({
    mutationFn: (id) => consoleApi.deleteAgentGroup(id),
    invalidateKeys: () => [consoleKeys.agentGroups(activeTenant)],
    successMessage: t('console:groups.deleted'),
    onDone: () => setDeleteGroup(null),
  })

  const wsItems = workspaces.data?.items ?? []
  const groupItems = groups.data?.items ?? []
  const workspaceMap = useMemo(
    () => new Map(wsItems.map((workspace) => [workspace.id, workspace])),
    [wsItems],
  )

  return (
    <div className="flex flex-col gap-8 pt-4">
      <section className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-foreground">
              {t('console:workspaces.title')}
            </h2>
            <p className="max-w-2xl text-sm text-muted-foreground">
              {t('console:workspaces.caption')}
            </p>
          </div>
          {isOwner && (
            <Button onClick={() => setWsCreateOpen(true)}>
              <Plus />
              {t('console:workspaces.create')}
            </Button>
          )}
        </div>
        <ListTruncationBadge
          query={workspaces}
          label={t('intel:notices.listTruncated', {
            n: workspaces.data?.items?.length ?? 0,
          })}
          hint={t('intel:notices.listTruncatedHint')}
          className="px-0 pt-0 pb-3"
          filas={workspaces.data?.items?.length ?? 0}
        />
        {workspaces.isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : workspaces.isError ? (
          <ErrorState retry={() => void workspaces.refetch()} />
        ) : wsItems.length === 0 ? (
          <EmptyState title={t('console:workspaces.none')} />
        ) : (
          <div className="overflow-hidden rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">
                    {t('console:workspaces.name')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:workspaces.slug')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:workspaces.status')}
                  </th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {wsItems.map((ws) => (
                  <tr key={ws.id} className="border-t border-border">
                    <td className="px-3 py-2 font-medium text-foreground">
                      {ws.name}
                      {ws.is_default && (
                        <Badge variant="neutral" className="ml-2">
                          {t('console:workspaces.isDefault')}
                        </Badge>
                      )}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                      {ws.slug}
                    </td>
                    <td className="px-3 py-2">
                      <Badge
                        variant={ws.status === 'active' ? 'success' : 'neutral'}
                      >
                        {ws.status}
                      </Badge>
                    </td>
                    <td className="px-3 py-2 text-right">
                      {isOwner && (
                        <div className="flex flex-wrap justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setRenameWorkspace(ws)}
                          >
                            <Pencil />
                            {t('console:workspaces.rename')}
                          </Button>
                          {!ws.is_default && ws.status === 'active' && (
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setArchive(ws)}
                            >
                              <Archive />
                              {t('console:workspaces.archive')}
                            </Button>
                          )}
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-foreground">
              {t('console:groups.title')}
            </h2>
            <p className="max-w-2xl text-sm text-muted-foreground">
              {t('console:groups.caption')}
            </p>
          </div>
          {canManageGroups && (
            <Button onClick={() => setGroupCreateOpen(true)}>
              <Plus />
              {t('console:groups.create')}
            </Button>
          )}
        </div>
        {!canReadGroups ? (
          <ForbiddenState
            icon={<ShieldOff />}
            title={t('console:groups.readOnlyNotice')}
          />
        ) : groups.isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : groups.isError ? (
          <ErrorState retry={() => void groups.refetch()} />
        ) : groupItems.length === 0 ? (
          <EmptyState title={t('console:groups.none')} icon={<Boxes />} />
        ) : (
          <div className="overflow-hidden rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">
                    {t('console:groups.name')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:groups.slug')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:groups.workspace')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:groups.status')}
                  </th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {groupItems.map((g) => (
                  <tr key={g.id} className="border-t border-border">
                    <td className="px-3 py-2 font-medium text-foreground">
                      {g.name}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                      {g.slug}
                    </td>
                    <td className="px-3 py-2">
                      {workspaceLabel(t, workspaceMap, g.workspace_id)}
                    </td>
                    <td className="px-3 py-2">
                      <Badge
                        variant={g.status === 'active' ? 'success' : 'neutral'}
                      >
                        {t(`console:groups.statuses.${g.status}`, g.status)}
                      </Badge>
                    </td>
                    <td className="px-3 py-2 text-right">
                      <div className="flex flex-wrap justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setMembersGroup(g)}
                        >
                          <Users />
                          {t('console:groups.members')}
                        </Button>
                        {canManageGroups && (
                          <>
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setEditGroup(g)}
                            >
                              <Pencil />
                              {t('console:groups.edit')}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setDeleteGroup(g)}
                            >
                              <Trash2 />
                              {t('console:groups.delete')}
                            </Button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <Dialog open={wsCreateOpen} onOpenChange={setWsCreateOpen}>
        <DialogContent className="max-w-lg">
          {wsCreateOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <WorkspaceForm onClose={() => setWsCreateOpen(false)} />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={renameWorkspace !== null}
        onOpenChange={(open) => !open && setRenameWorkspace(null)}
      >
        <DialogContent className="max-w-lg">
          {renameWorkspace ? (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <WorkspaceRenameForm
                workspace={renameWorkspace}
                onClose={() => setRenameWorkspace(null)}
              />
            </RequireAssurance>
          ) : null}
        </DialogContent>
      </Dialog>

      <Dialog open={groupCreateOpen} onOpenChange={setGroupCreateOpen}>
        <DialogContent className="max-w-lg">
          {groupCreateOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <AgentGroupForm
                workspaces={wsItems}
                onClose={() => setGroupCreateOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editGroup !== null}
        onOpenChange={(open) => !open && setEditGroup(null)}
      >
        <DialogContent className="max-w-lg">
          {editGroup ? (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <AgentGroupForm
                group={editGroup}
                workspaces={wsItems}
                onClose={() => setEditGroup(null)}
              />
            </RequireAssurance>
          ) : null}
        </DialogContent>
      </Dialog>

      <Dialog
        open={membersGroup !== null}
        onOpenChange={(open) => !open && setMembersGroup(null)}
      >
        <DialogContent className="max-h-[88vh] max-w-2xl overflow-y-auto">
          {membersGroup ? (
            <AgentGroupMembersDialog
              group={membersGroup}
              onClose={() => setMembersGroup(null)}
            />
          ) : null}
        </DialogContent>
      </Dialog>

      {archive ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(o) => !o && setArchive(null)}
            title={t('console:workspaces.archiveTitle')}
            description={t('console:workspaces.archiveBody')}
            confirmLabel={t('console:workspaces.archive')}
            tone="danger"
            pending={archiveMutation.isPending}
            onConfirm={() => archiveMutation.mutate(archive.id)}
          />
        </RequireAssurance>
      ) : null}
      {deleteGroup ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(o) => !o && setDeleteGroup(null)}
            title={t('console:groups.deleteTitle')}
            description={t('console:groups.deleteBody')}
            confirmLabel={t('console:groups.delete')}
            tone="danger"
            pending={deleteGroupMutation.isPending}
            onConfirm={() => deleteGroupMutation.mutate(deleteGroup.id)}
          />
        </RequireAssurance>
      ) : null}
    </div>
  )
}

function WorkspaceRenameForm({
  workspace,
  onClose,
}: {
  workspace: WorkspaceDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [name, setName] = useState(workspace.name)
  const mutation = usePrivilegedMutation<void, WorkspaceDTO>({
    mutationFn: () =>
      consoleApi.updateWorkspace(workspace.id, { name: name.trim() }),
    invalidateKeys: () => [consoleKeys.workspaces(activeTenant)],
    successMessage: t('console:workspaces.renamed'),
    onDone: onClose,
  })
  const nextName = name.trim()
  const valid = nextName !== '' && nextName !== workspace.name
  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('console:workspaces.renameTitle')}</DialogTitle>
        <DialogDescription>
          {t('console:workspaces.renameBody')}
        </DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-4">
        <Field
          label={t('console:workspaces.name')}
          htmlFor="ws-rename-name"
          required
        >
          <Input
            id="ws-rename-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
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
          {t('console:workspaces.saveName')}
        </Button>
      </DialogFooter>
    </>
  )
}

function WorkspaceForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const mutation = usePrivilegedMutation<void, WorkspaceDTO>({
    mutationFn: () =>
      consoleApi.createWorkspace({ name: name.trim(), slug: slug.trim() }),
    invalidateKeys: () => [consoleKeys.workspaces(activeTenant)],
    successMessage: t('console:workspaces.created'),
    onDone: onClose,
  })
  const valid = name.trim() !== '' && /^[a-z0-9][a-z0-9-]*$/.test(slug.trim())
  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('console:workspaces.create')}</DialogTitle>
        <DialogDescription>{t('console:workspaces.caption')}</DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-4">
        <Field label={t('console:workspaces.name')} htmlFor="ws-name" required>
          <Input
            id="ws-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field
          label={t('console:workspaces.slug')}
          htmlFor="ws-slug"
          description={t('console:workspaces.slugHint')}
          required
        >
          <Input
            id="ws-slug"
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            mono
          />
        </Field>
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
          {t('console:workspaces.create')}
        </Button>
      </DialogFooter>
    </>
  )
}

function AgentGroupForm({
  group,
  workspaces,
  onClose,
}: {
  group?: AgentGroupDTO
  workspaces: WorkspaceDTO[]
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!group
  const [name, setName] = useState(group?.name ?? '')
  const [slug, setSlug] = useState(group?.slug ?? '')
  const [workspaceId, setWorkspaceId] = useState(
    group?.workspace_id ?? WORKSPACE_NONE,
  )
  const [description, setDescription] = useState(group?.description ?? '')
  const [status, setStatus] = useState<(typeof GROUP_STATUSES)[number]>(
    group?.status === 'inactive' ? 'inactive' : 'active',
  )
  const mutation = usePrivilegedMutation<void, AgentGroupDTO>({
    mutationFn: () =>
      isEdit && group
        ? consoleApi.updateAgentGroup(group.id, {
            name: name.trim(),
            description: description.trim(),
            status,
            // Always send the scope on edit: a chosen workspace re-scopes the group,
            // "none" sends "" which clears it back to tenant-wide (the backend treats
            // an absent field as "leave untouched", so the sentinel must become "").
            workspace_id: workspaceId === WORKSPACE_NONE ? '' : workspaceId,
          })
        : consoleApi.createAgentGroup({
            name: name.trim(),
            slug: slug.trim(),
            workspace_id:
              workspaceId === WORKSPACE_NONE ? undefined : workspaceId,
            description: description.trim() || undefined,
            status,
          }),
    invalidateKeys: () => [consoleKeys.agentGroups(activeTenant)],
    successMessage: isEdit
      ? t('console:groups.updated')
      : t('console:groups.created'),
    onDone: onClose,
  })
  const valid =
    name.trim() !== '' && (isEdit || /^[a-z0-9][a-z0-9-]*$/.test(slug.trim()))
  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:groups.editTitle')
            : t('console:groups.createTitle')}
        </DialogTitle>
        <DialogDescription>{t('console:groups.caption')}</DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-4">
        <Field label={t('console:groups.name')} htmlFor="ag-name" required>
          <Input
            id="ag-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field
          label={t('console:groups.slug')}
          htmlFor="ag-slug"
          description={isEdit ? t('console:groups.slugImmutable') : undefined}
          required={!isEdit}
        >
          <Input
            id="ag-slug"
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            disabled={isEdit}
            mono
          />
        </Field>
        <Field label={t('console:groups.workspace')} htmlFor="ag-workspace">
          <Select value={workspaceId} onValueChange={setWorkspaceId}>
            <SelectTrigger
              id="ag-workspace"
              aria-label={t('console:groups.workspace')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={WORKSPACE_NONE}>
                {t('console:groups.workspaceAny')}
              </SelectItem>
              {workspaces.map((workspace) => (
                <SelectItem key={workspace.id} value={workspace.id}>
                  {workspace.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t('console:groups.status')} htmlFor="ag-status">
          <Select
            value={status}
            onValueChange={(value) =>
              setStatus(value as (typeof GROUP_STATUSES)[number])
            }
          >
            <SelectTrigger
              id="ag-status"
              aria-label={t('console:groups.status')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {GROUP_STATUSES.map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`console:groups.statuses.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t('console:groups.description')} htmlFor="ag-description">
          <Textarea
            id="ag-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            rows={3}
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
          {isEdit ? t('console:groups.save') : t('console:groups.create')}
        </Button>
      </DialogFooter>
    </>
  )
}

function AgentGroupMembersDialog({
  group,
  onClose,
}: {
  group: AgentGroupDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('agent:write')
  const [agentID, setAgentID] = useState('')
  const [removeTarget, setRemoveTarget] = useState<AgentGroupMemberDTO | null>(
    null,
  )

  const members = useQuery({
    queryKey: consoleKeys.agentGroupMembers(activeTenant, group.id),
    queryFn: () => consoleApi.listAgentGroupMembers(group.id),
  })
  const agents = useQuery({
    queryKey: consoleKeys.agents(activeTenant, AGENT_LIST_PARAMS),
    queryFn: () => consoleApi.listAgents(AGENT_LIST_PARAMS),
  })
  const agentMap = useMemo(
    () => new Map((agents.data?.items ?? []).map((agent) => [agent.id, agent])),
    [agents.data?.items],
  )

  const addMutation = usePrivilegedMutation<string, AgentGroupMemberDTO>({
    mutationFn: (id) => consoleApi.addAgentGroupMember(group.id, id),
    invalidateKeys: () => [
      consoleKeys.agentGroupMembers(activeTenant, group.id),
      consoleKeys.agentGroups(activeTenant),
    ],
    successMessage: t('console:groups.memberAdded'),
    onDone: () => setAgentID(''),
  })
  const removeMutation = usePrivilegedMutation<AgentGroupMemberDTO, void>({
    mutationFn: (member) =>
      consoleApi.removeAgentGroupMember(group.id, member.agent_id),
    invalidateKeys: () => [
      consoleKeys.agentGroupMembers(activeTenant, group.id),
      consoleKeys.agentGroups(activeTenant),
    ],
    successMessage: t('console:groups.memberRemoved'),
    onDone: () => setRemoveTarget(null),
  })

  const rows = members.data?.items ?? []
  const addValid = agentID.trim() !== ''

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {t('console:groups.membersTitle', { name: group.name })}
        </DialogTitle>
        <DialogDescription>
          {t('console:groups.membersCaption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        {canWrite ? (
          <RequireAssurance minAal={AAL.HARDWARE} action="console">
            <div className="flex flex-col gap-2 rounded-lg border border-border bg-muted/20 p-3">
              <Field
                label={t('console:groups.agentId')}
                htmlFor="ag-member-agent-id"
                description={t('console:groups.agentIdHint')}
                required
              >
                <Input
                  id="ag-member-agent-id"
                  value={agentID}
                  onChange={(event) => setAgentID(event.target.value)}
                  mono
                />
              </Field>
              <div className="flex justify-end">
                <Button
                  variant="primary"
                  onClick={() => addMutation.mutate(agentID.trim())}
                  disabled={!addValid || addMutation.isPending}
                >
                  {addMutation.isPending ? (
                    <Spinner size="sm" aria-hidden />
                  ) : (
                    <UserPlus />
                  )}
                  {t('console:groups.addMember')}
                </Button>
              </div>
              <FormError error={addMutation.error} />
            </div>
          </RequireAssurance>
        ) : null}

        {members.isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : members.isError ? (
          <ErrorState retry={() => void members.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Users />}
            title={t('console:groups.membersNone')}
            description={t('console:groups.membersNoneHint')}
          />
        ) : (
          <div className="overflow-hidden rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">
                    {t('console:groups.memberAgent')}
                  </th>
                  <th className="px-3 py-2 font-medium">
                    {t('console:groups.memberId')}
                  </th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {rows.map((member) => (
                  <MemberRow
                    key={member.id}
                    member={member}
                    agent={agentMap.get(member.agent_id)}
                    canWrite={canWrite}
                    onRemove={setRemoveTarget}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <DialogFooter>
        <Button variant="secondary" onClick={onClose}>
          {t('common:actions.close')}
        </Button>
      </DialogFooter>

      {removeTarget ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(open) => !open && setRemoveTarget(null)}
            title={t('console:groups.removeMemberTitle')}
            description={t('console:groups.removeMemberBody', {
              id: removeTarget.agent_id,
            })}
            confirmLabel={t('console:groups.removeMember')}
            tone="danger"
            pending={removeMutation.isPending}
            onConfirm={() => removeMutation.mutate(removeTarget)}
          />
        </RequireAssurance>
      ) : null}
    </>
  )
}

function MemberRow({
  member,
  agent,
  canWrite,
  onRemove,
}: {
  member: AgentGroupMemberDTO
  agent?: AgentDTO
  canWrite: boolean
  onRemove: (member: AgentGroupMemberDTO) => void
}) {
  const { t } = useTranslation('console')
  return (
    <tr className="border-t border-border align-top">
      <td className="px-3 py-2">
        {agent ? (
          <div className="flex flex-col gap-1">
            <span className="font-medium text-foreground">{agent.name}</span>
            <span className="font-mono text-xs text-muted-foreground">
              {agent.kind}
            </span>
          </div>
        ) : (
          <span className="text-muted-foreground">
            {t('groups.memberUnknown')}
          </span>
        )}
      </td>
      <td className="px-3 py-2">
        <code className="break-all font-mono text-xs text-muted-foreground">
          {member.agent_id}
        </code>
      </td>
      <td className="px-3 py-2 text-right">
        {canWrite ? (
          <Button variant="ghost" size="sm" onClick={() => onRemove(member)}>
            <Trash2 />
            {t('groups.removeMember')}
          </Button>
        ) : null}
      </td>
    </tr>
  )
}

function workspaceLabel(
  t: (key: string, options?: Record<string, unknown>) => string,
  workspaces: Map<string, WorkspaceDTO>,
  workspaceId?: string,
) {
  if (!workspaceId) return t('console:groups.workspaceAny')
  const workspace = workspaces.get(workspaceId)
  if (!workspace) {
    return (
      <code className="break-all font-mono text-xs text-muted-foreground">
        {workspaceId}
      </code>
    )
  }
  return (
    <div className="flex flex-col gap-1">
      <span className="text-foreground">{workspace.name}</span>
      <span className="font-mono text-xs text-muted-foreground">
        {workspace.slug}
      </span>
    </div>
  )
}
