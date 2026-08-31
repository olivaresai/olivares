// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Bot, PauseCircle, Pencil, Plus, ShieldOff, Trash2 } from 'lucide-react'
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
import {
  consoleApi,
  consoleKeys,
  type AgentInput,
  type AgentStatus,
  type WorkspaceDTO,
} from './api'
import { FormError } from './roles-shared'

/** ⛔ EL 200 ERA UN NÚMERO A MANO por debajo del techo real del motor: ni pedía lo que da, ni
 *  decía nada al quedarse corto. `maxLimit` es 1000 (`sqlstore/generic.go:29`). */
const AGENT_LIST_PARAMS = { limit: 1000 }
const WORKSPACE_NONE = '__none__'
const AGENT_STATUSES: AgentStatus[] = ['active', 'inactive', 'archived']

export function AgentsTab() {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const canRead = can('agent:read')
  const canWrite = can('agent:write')
  const canReadWorkspaces = can('tenant:read')
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<AgentDTO | null>(null)
  const [deactivateTarget, setDeactivateTarget] = useState<AgentDTO | null>(
    null,
  )
  const [deleteTarget, setDeleteTarget] = useState<AgentDTO | null>(null)

  const agents = useQuery({
    queryKey: consoleKeys.agents(activeTenant, AGENT_LIST_PARAMS),
    queryFn: () => consoleApi.listAgents(AGENT_LIST_PARAMS),
    enabled: canRead,
  })
  const workspaceParams = { limit: 1000 }
  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant, workspaceParams),
    queryFn: () => consoleApi.listWorkspaces(workspaceParams),
    enabled: canRead && canReadWorkspaces,
  })

  const deactivateMutation = usePrivilegedMutation<AgentDTO, AgentDTO>({
    mutationFn: (agent) =>
      consoleApi.updateAgent(agent.id, {
        name: agent.name,
        kind: agent.kind,
        external_id: agent.external_id,
        identity_id: agent.identity_id,
        workspace_id: agent.workspace_id,
        labels: agent.labels,
        metadata: agent.metadata,
        status: 'inactive',
      }),
    invalidateKeys: (_data, agent) => [
      consoleKeys.agents(activeTenant),
      consoleKeys.agent(activeTenant, agent.id),
    ],
    successMessage: t('console:agents.deactivated'),
    onDone: () => setDeactivateTarget(null),
  })
  const deleteMutation = usePrivilegedMutation<AgentDTO, void>({
    mutationFn: (agent) => consoleApi.deleteAgent(agent.id),
    invalidateKeys: (_data, agent) => [
      consoleKeys.agents(activeTenant),
      consoleKeys.agent(activeTenant, agent.id),
    ],
    successMessage: t('console:agents.deleted'),
    onDone: () => setDeleteTarget(null),
  })

  const workspaceMap = useMemo(
    () => new Map((workspaces.data?.items ?? []).map((ws) => [ws.id, ws])),
    [workspaces.data?.items],
  )
  const rows = agents.data?.items ?? []

  if (!canRead) {
    return (
      <div className="pt-4">
        <ForbiddenState
          icon={<ShieldOff />}
          title={t('console:agents.readOnlyNotice')}
        />
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4 pt-4">
      <section className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-foreground">
              {t('console:agents.title')}
            </h2>
            <p className="max-w-2xl text-sm text-muted-foreground">
              {t('console:agents.caption')}
            </p>
          </div>
          {canWrite ? (
            <Button onClick={() => setCreateOpen(true)}>
              <Plus />
              {t('console:agents.create')}
            </Button>
          ) : null}
        </div>

        {/* ⛔ EL AVISO ES DE WORKSPACES, NO DE AGENTES, y por eso vive fuera del condicional de
            la tabla. Esta consulta pide el techo del almacen pero su `has_more` no se leia: un
            workspace mas alla de la pagina no desaparece de la pantalla — sale como ID CRUDO en la
            columna, porque `workspaceMap` no puede resolver su nombre, y **no es elegible** en el
            alta ni en la edicion, porque el selector se alimenta de esta misma lista. Es decir: el
            recorte no oculta una fila, impide una OPERACION. */}
        {workspaces.data?.has_more === true && !workspaces.error ? (
          <div>
            <Badge
              variant="warning"
              title={t('console:agents.workspacesTruncatedHint')}
            >
              {t('console:agents.workspacesTruncated', {
                n: workspaces.data?.items?.length ?? 0,
              })}
            </Badge>
          </div>
        ) : null}
        {/* ⛔ UN SOLO AVISO POR LISTA, Y CON EL TEXTO ESPECIFICO. Aqui habia DOS: este y un
            `<Badge>` heredado arriba, que decian lo mismo con palabras distintas — asi que
            ningun `findByText` los delataba y la pantalla enseñaba el recorte dos veces. Lo
            conto el contraste (F-01) y lo reproduje: `getAllByText(/there are more/i)`
            daba 2.
            La cura NO es quedarse con el generico: `ListTruncationBadge` recibe el label, asi
            que se conserva el texto de siempre («Loaded N agents…») y se gana el componente
            comun, que es el que el trinquete sabe auditar. El aviso de WORKSPACES de arriba no
            se toca: no esta duplicado y dice algo distinto y mas grave — alli el recorte no
            oculta una fila, impide una OPERACION. */}
        <ListTruncationBadge
          query={agents}
          label={t('console:agents.truncated', { n: rows.length })}
          hint={t('console:agents.truncatedHint')}
          className="px-0 pt-0 pb-3"
          filas={rows.length}
        />
        {agents.isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : agents.isError ? (
          <ErrorState retry={() => void agents.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Bot />}
            title={t('console:agents.none')}
            description={t('console:agents.noneHint')}
          />
        ) : (
          <AgentsTable
            agents={rows}
            workspaces={workspaceMap}
            canWrite={canWrite}
            onEdit={setEditing}
            onDeactivate={setDeactivateTarget}
            onDelete={setDeleteTarget}
          />
        )}
      </section>

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[88vh] max-w-2xl overflow-y-auto">
          {createOpen ? (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <AgentForm
                workspaces={workspaces.data?.items ?? []}
                onClose={() => setCreateOpen(false)}
              />
            </RequireAssurance>
          ) : null}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(open) => !open && setEditing(null)}
      >
        <DialogContent className="max-h-[88vh] max-w-2xl overflow-y-auto">
          {editing ? (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <AgentForm
                agent={editing}
                workspaces={workspaces.data?.items ?? []}
                onClose={() => setEditing(null)}
              />
            </RequireAssurance>
          ) : null}
        </DialogContent>
      </Dialog>

      {deactivateTarget ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(open) => !open && setDeactivateTarget(null)}
            title={t('console:agents.deactivateTitle')}
            description={t('console:agents.deactivateBody', {
              name: deactivateTarget.name,
            })}
            confirmLabel={t('console:agents.deactivate')}
            pending={deactivateMutation.isPending}
            onConfirm={() => deactivateMutation.mutate(deactivateTarget)}
          />
        </RequireAssurance>
      ) : null}

      {deleteTarget ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(open) => !open && setDeleteTarget(null)}
            title={t('console:agents.deleteTitle')}
            description={t('console:agents.deleteBody', {
              name: deleteTarget.name,
            })}
            confirmLabel={t('console:agents.delete')}
            tone="danger"
            pending={deleteMutation.isPending}
            onConfirm={() => deleteMutation.mutate(deleteTarget)}
          />
        </RequireAssurance>
      ) : null}
    </div>
  )
}

function AgentsTable({
  agents,
  workspaces,
  canWrite,
  onEdit,
  onDeactivate,
  onDelete,
}: {
  agents: AgentDTO[]
  workspaces: Map<string, WorkspaceDTO>
  canWrite: boolean
  onEdit: (agent: AgentDTO) => void
  onDeactivate: (agent: AgentDTO) => void
  onDelete: (agent: AgentDTO) => void
}) {
  const { t } = useTranslation('console')
  return (
    <div className="overflow-hidden rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
          <tr>
            <th className="px-3 py-2 font-medium">{t('agents.name')}</th>
            <th className="px-3 py-2 font-medium">{t('agents.kind')}</th>
            <th className="px-3 py-2 font-medium">{t('agents.status')}</th>
            <th className="px-3 py-2 font-medium">{t('agents.externalId')}</th>
            <th className="px-3 py-2 font-medium">{t('agents.workspace')}</th>
            <th className="px-3 py-2" />
          </tr>
        </thead>
        <tbody>
          {agents.map((agent) => (
            <tr key={agent.id} className="border-t border-border align-top">
              <td className="px-3 py-2">
                <div className="flex flex-col gap-1">
                  <span className="font-medium text-foreground">
                    {agent.name}
                  </span>
                  <span className="font-mono text-xs text-muted-foreground">
                    {agent.id}
                  </span>
                </div>
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                {agent.kind}
              </td>
              <td className="px-3 py-2">
                <Badge
                  variant={agent.status === 'active' ? 'success' : 'neutral'}
                >
                  {t(`agents.statuses.${agent.status}`, agent.status)}
                </Badge>
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                {agent.external_id || t('agents.notSet')}
              </td>
              <td className="px-3 py-2">
                {workspaceLabel(t, workspaces, agent.workspace_id)}
              </td>
              <td className="px-3 py-2 text-right">
                {canWrite ? (
                  <div className="flex flex-wrap justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onEdit(agent)}
                    >
                      <Pencil />
                      {t('agents.edit')}
                    </Button>
                    {agent.status !== 'inactive' ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onDeactivate(agent)}
                      >
                        <PauseCircle />
                        {t('agents.deactivate')}
                      </Button>
                    ) : null}
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onDelete(agent)}
                    >
                      <Trash2 />
                      {t('agents.delete')}
                    </Button>
                  </div>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function AgentForm({
  agent,
  workspaces,
  onClose,
}: {
  agent?: AgentDTO
  workspaces: WorkspaceDTO[]
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!agent
  const [name, setName] = useState(agent?.name ?? '')
  const [kind, setKind] = useState(agent?.kind ?? '')
  const [externalId, setExternalId] = useState(agent?.external_id ?? '')
  const [identityId, setIdentityId] = useState(agent?.identity_id ?? '')
  const [workspaceId, setWorkspaceId] = useState(
    agent?.workspace_id ?? WORKSPACE_NONE,
  )
  const [status, setStatus] = useState<AgentStatus>(
    (agent?.status as AgentStatus | undefined) ?? 'active',
  )
  const [labels, setLabels] = useState(formatRecord(agent?.labels))
  const [metadata, setMetadata] = useState(formatRecord(agent?.metadata))
  const [jsonError, setJsonError] = useState<string | null>(null)

  const mutation = usePrivilegedMutation<void, AgentDTO>({
    mutationFn: () => {
      const parsedLabels = parseRecord(labels)
      const parsedMetadata = parseRecord(metadata)
      const input: AgentInput = {
        name: name.trim(),
        kind: kind.trim(),
        external_id: emptyToUndefined(externalId),
        identity_id: emptyToUndefined(identityId),
        workspace_id:
          workspaceId === WORKSPACE_NONE
            ? undefined
            : emptyToUndefined(workspaceId),
        status,
        labels: parsedLabels,
        metadata: parsedMetadata,
      }
      return isEdit && agent
        ? consoleApi.updateAgent(agent.id, input)
        : consoleApi.createAgent(input)
    },
    invalidateKeys: (data) => [
      consoleKeys.agents(activeTenant),
      consoleKeys.agent(activeTenant, data.id),
    ],
    successMessage: (data) =>
      isEdit
        ? t('console:agents.updated', { name: data.name })
        : t('console:agents.created', { name: data.name }),
    onDone: onClose,
  })

  const valid = name.trim() !== '' && kind.trim() !== ''
  const submit = () => {
    try {
      parseRecord(labels)
      parseRecord(metadata)
      setJsonError(null)
    } catch {
      setJsonError(t('console:agents.jsonObjectError'))
      return
    }
    mutation.mutate()
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:agents.editTitle')
            : t('console:agents.createTitle')}
        </DialogTitle>
        <DialogDescription>{t('console:agents.formCaption')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid gap-3 md:grid-cols-2">
          <Field label={t('console:agents.name')} htmlFor="agent-name" required>
            <Input
              id="agent-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          <Field label={t('console:agents.kind')} htmlFor="agent-kind" required>
            <Input
              id="agent-kind"
              value={kind}
              onChange={(event) => setKind(event.target.value)}
              mono
            />
          </Field>
        </div>
        <div className="grid gap-3 md:grid-cols-2">
          <Field label={t('console:agents.status')} htmlFor="agent-status">
            <Select
              value={status}
              onValueChange={(value) => setStatus(value as AgentStatus)}
            >
              <SelectTrigger
                id="agent-status"
                aria-label={t('console:agents.status')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {AGENT_STATUSES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {t(`console:agents.statuses.${value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('console:agents.workspace')}
            htmlFor="agent-workspace"
          >
            <Select value={workspaceId} onValueChange={setWorkspaceId}>
              <SelectTrigger
                id="agent-workspace"
                aria-label={t('console:agents.workspace')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={WORKSPACE_NONE}>
                  {t('console:agents.workspaceNone')}
                </SelectItem>
                {workspaces.map((workspace) => (
                  <SelectItem key={workspace.id} value={workspace.id}>
                    {workspace.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
        <div className="grid gap-3 md:grid-cols-2">
          <Field
            label={t('console:agents.externalId')}
            htmlFor="agent-external-id"
          >
            <Input
              id="agent-external-id"
              value={externalId}
              onChange={(event) => setExternalId(event.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('console:agents.identityId')}
            htmlFor="agent-identity-id"
          >
            <Input
              id="agent-identity-id"
              value={identityId}
              onChange={(event) => setIdentityId(event.target.value)}
              mono
            />
          </Field>
        </div>
        <Field
          label={t('console:agents.labels')}
          htmlFor="agent-labels"
          description={t('console:agents.jsonHint')}
          error={jsonError ?? undefined}
        >
          <Textarea
            id="agent-labels"
            value={labels}
            onChange={(event) => setLabels(event.target.value)}
            rows={4}
            mono
          />
        </Field>
        <Field
          label={t('console:agents.metadata')}
          htmlFor="agent-metadata"
          description={t('console:agents.jsonHint')}
        >
          <Textarea
            id="agent-metadata"
            value={metadata}
            onChange={(event) => setMetadata(event.target.value)}
            rows={4}
            mono
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
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending ? <Spinner size="sm" aria-hidden /> : null}
          {isEdit ? t('console:agents.save') : t('console:agents.create')}
        </Button>
      </DialogFooter>
    </>
  )
}

function workspaceLabel(
  t: (key: string, options?: Record<string, unknown>) => string,
  workspaces: Map<string, WorkspaceDTO>,
  workspaceId?: string,
) {
  if (!workspaceId) return t('agents.workspaceNone')
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

function emptyToUndefined(value: string): string | undefined {
  const trimmed = value.trim()
  return trimmed === '' ? undefined : trimmed
}

function formatRecord(value?: Record<string, unknown>): string {
  if (!value || Object.keys(value).length === 0) return ''
  return JSON.stringify(value, null, 2)
}

function parseRecord(value: string): Record<string, unknown> | undefined {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  const parsed = JSON.parse(trimmed) as unknown
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('expected object')
  }
  return parsed as Record<string, unknown>
}
