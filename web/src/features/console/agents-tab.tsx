// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import {
  Bot,
  MoreHorizontal,
  PauseCircle,
  Pencil,
  Plus,
  ShieldOff,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { PagePrimaryAction } from '@/components/ui/page-actions'
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
import { HIDDEN_ON_PHONE } from '@/lib/hooks/use-is-phone'
import {
  consoleApi,
  consoleKeys,
  type AgentInput,
  type AgentStatus,
  type WorkspaceDTO,
} from './api'
import { NamedRef } from '@/features/shared'
import { FormError } from './roles-shared'
import { FillingTable, TableRegion } from '@/components/data/filling-table'

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
      <ForbiddenState
        icon={<ShieldOff />}
        title={t('console:agents.readOnlyNotice')}
      />
    )
  }

  return (
    <div className="flex flex-col gap-2">
      {canWrite ? (
        <PagePrimaryAction>
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('console:agents.create')}
          </Button>
        </PagePrimaryAction>
      ) : null}
      <section className="flex flex-col gap-2">
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
          className="px-0 py-0"
          filas={rows.length}
        />
        {agents.isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : agents.isError ? (
          <ErrorState retry={() => void agents.refetch()} />
        ) : rows.length === 0 ? (
          /* ⛔ ZERO ROWS TAKES THE SAME REGION THE TABLE WOULD HAVE, and it used to take
             none — the one count where a dead half is CERTAIN was the one count that
             went round `FillingTable`, leaving a centred panel 220 px tall at the top of
             the frame — measured in Chromium at 1440×900, against the 772 px region the
             same screen gives its rows — and the rest empty and unbordered. `TableRegion`
             is that same piece, so the height is stated once for both row counts. */
          <TableRegion fill>
            <EmptyState
              className="flex-1"
              icon={<Bot />}
              title={t('console:agents.none')}
              description={t('console:agents.noneHint')}
            />
          </TableRegion>
        ) : (
          <AgentsTable
            agents={rows}
            workspaces={workspaceMap}
            canWrite={canWrite}
            onCreate={() => setCreateOpen(true)}
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
  onCreate,
  onEdit,
  onDeactivate,
  onDelete,
}: {
  agents: AgentDTO[]
  workspaces: Map<string, WorkspaceDTO>
  canWrite: boolean
  onCreate: () => void
  onEdit: (agent: AgentDTO) => void
  onDeactivate: (agent: AgentDTO) => void
  onDelete: (agent: AgentDTO) => void
}) {
  const { t } = useTranslation('console')
  return (
    <FillingTable
      fill
      oneLine
      colSpan={6}
      nextAction={
        canWrite ? (
          /* The same control as the profiles table's, for the same reason: this line
             opens the header's own dialog in place, so it is a button. The `#deploy-agent`
             fragment it used to carry named no element in the document. */
          <button
            type="button"
            data-testid="agents-next-action"
            className="text-accent-text underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            onClick={onCreate}
          >
            {t('agents.nextAction')}
          </button>
        ) : undefined
      }
    >
      <thead>
        <tr>
          <th>{t('agents.name')}</th>
          <th className={HIDDEN_ON_PHONE}>{t('agents.kind')}</th>
          <th>{t('agents.status')}</th>
          <th className={HIDDEN_ON_PHONE}>{t('agents.externalId')}</th>
          <th className={HIDDEN_ON_PHONE}>{t('agents.workspace')}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        {agents.map((agent) => (
          <tr key={agent.id}>
            <td title={agent.id}>
              <NamedRef
                className="font-medium text-foreground"
                name={agent.name}
                reference={agent.id}
                fallback={agent.name}
              />
            </td>
            <td
              className={`font-mono text-caption text-muted-foreground ${HIDDEN_ON_PHONE}`}
            >
              {agent.kind}
            </td>
            <td>
              <Badge
                variant={agent.status === 'active' ? 'success' : 'neutral'}
              >
                {t(`agents.statuses.${agent.status}`, agent.status)}
              </Badge>
            </td>
            <td
              className={`font-mono text-caption text-muted-foreground ${HIDDEN_ON_PHONE}`}
            >
              {agent.external_id || t('agents.notSet')}
            </td>
            <td className={HIDDEN_ON_PHONE}>
              {workspaceLabel(t, workspaces, agent.workspace_id)}
            </td>
            <td className="text-right">
              {canWrite ? (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={t('agents.rowMenu', { name: agent.name })}
                    >
                      <MoreHorizontal />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onSelect={() => onEdit(agent)}>
                      <Pencil />
                      {t('agents.edit')}
                    </DropdownMenuItem>
                    {agent.status !== 'inactive' ? (
                      <DropdownMenuItem onSelect={() => onDeactivate(agent)}>
                        <PauseCircle />
                        {t('agents.deactivate')}
                      </DropdownMenuItem>
                    ) : null}
                    <DropdownMenuItem
                      variant="destructive"
                      onSelect={() => onDelete(agent)}
                    >
                      <Trash2 />
                      {t('agents.delete')}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              ) : null}
            </td>
          </tr>
        ))}
      </tbody>
    </FillingTable>
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
        {isEdit && agent ? (
          <Field label={t('console:agents.id')} htmlFor="agent-id">
            <Input id="agent-id" value={agent.id} readOnly mono />
          </Field>
        ) : null}
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
      <NamedRef
        className="text-muted-foreground"
        name={t('agents.workspaceUnknown')}
        reference={workspaceId}
        fallback={t('agents.workspaceUnknown')}
        title={workspaceId}
      />
    )
  }
  return (
    <NamedRef
      className="text-foreground"
      name={workspace.name}
      reference={workspace.slug}
      fallback={workspace.name}
      title={workspace.slug}
    />
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
