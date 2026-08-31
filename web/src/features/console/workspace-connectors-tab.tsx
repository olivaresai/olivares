// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { useQuery } from '@tanstack/react-query'
import { Link, Pencil, Plus, Trash2, Unlink } from 'lucide-react'
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
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useWorkspaceFilter } from '@/lib/hooks/use-workspace-filter'
import {
  consoleApi,
  consoleKeys,
  type ConnectorAssignmentDTO,
  type WorkspaceConnectorDTO,
  type WorkspaceDTO,
} from './api'
import { FormError } from './roles-shared'

//Workspace connector scoping UI:
// 1. Connector assignments: which global connectors are assigned to this workspace.
// 2. Workspace connectors: connectors defined and owned by the workspace admin.

/**
 * ⛔ LA REFERENCIA DE UN WORKSPACE SE PREGUNTA, NO SE BUSCA EN UNA PAGINA.
 *
 * Antes: `listWorkspaces().items.find((ws) => ws.id === workspaceId)?.slug ?? workspaceId`.
 * `listWorkspaces` sirve UNA pagina (el almacen generico da su `defaultLimit`), asi que en
 * cuanto el workspace no cabia, `find` devolvia `undefined` y el `??` caia al **id crudo**
 * como `workspace_ref`. El motor no reconoce ese valor, la consulta siguiente no casaba
 * nada, y la pantalla afirmaba **«no hay conectores»** — falso, y en la direccion
 * tranquilizadora.
 *
 * Y la guarda que habia —`enabled: !workspaceId || workspaces.isSuccess`— no lo cubria:
 * comprueba que la LISTA cargo, no que el workspace se ENCONTRARA. Es el exito de la
 * pregunta equivocada.
 *
 * `GET /v1/workspaces/{ref}` acepta id o slug y responde por el workspace concreto, asi que
 * el tamano de la pagina deja de importar. Y mientras no se resuelva, `resuelto` es falso:
 * la consulta dependiente NO corre con un filtro inventado.
 */
function useWorkspaceRef(
  tenant: string | null,
  workspaceId: string | undefined,
  canRead: boolean,
) {
  const q = useQuery({
    queryKey: consoleKeys.workspace(tenant, workspaceId ?? ''),
    queryFn: () => consoleApi.getWorkspaceByID(workspaceId!),
    enabled: canRead && !!workspaceId,
  })
  const ref = workspaceId ? q.data?.slug : undefined
  return {
    ref,
    // Sin `workspaceId` no hay nada que resolver: el filtro es «todos».
    resuelto: !workspaceId || q.data !== undefined,
    // ⛔ La clave NO puede caer a `__all__` mientras el resolver no haya contestado o haya
    //    fallado. `enabled: false` impide PEDIR, no impide LEER LA CACHÉ: una visita previa a
    //    la vista sin filtro deja ahí la lista de TODOS los workspaces, y esta vista la
    //    pintaría como si fuera la del suyo. Con clave propia no existe esa entrada.
    clave: !workspaceId ? '__all__' : (ref ?? '__sin-resolver__'),
    query: q,
  }
}

export function WorkspaceConnectorsTab() {
  const { activeTenant, can } = useAuth()
  const { workspaceId } = useWorkspaceFilter()

  return (
    <div className="space-y-8">
      <AssignmentSection
        tenant={activeTenant}
        workspaceId={workspaceId}
        canRead={can('sourcescope:assignment:read')}
        canWrite={can('sourcescope:assignment:write')}
      />
      <WsConnectorSection
        tenant={activeTenant}
        workspaceId={workspaceId}
        canRead={can('sourcescope:workspace_connector:read')}
        canWrite={can('sourcescope:workspace_connector:write')}
      />
    </div>
  )
}

// --- Section 1: Connector assignments ---------------------------------------

function AssignmentSection({
  tenant,
  workspaceId,
  canRead,
  canWrite,
}: {
  tenant: string | null
  workspaceId: string | undefined
  canRead: boolean
  canWrite: boolean
}) {
  const { t } = useTranslation(['console', 'common'])
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<ConnectorAssignmentDTO | null>(null)
  const [deleteTarget, setDeleteTarget] =
    useState<ConnectorAssignmentDTO | null>(null)

  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(tenant),
    queryFn: () => consoleApi.listWorkspaces(),
    enabled: canRead,
  })
  const {
    ref: workspaceRef,
    resuelto: refResuelto,
    clave: refClave,
    query: refQuery,
  } = useWorkspaceRef(tenant, workspaceId, canRead)
  const assignments = useQuery({
    queryKey: [...consoleKeys.connectorAssignments(tenant), refClave],
    queryFn: () =>
      consoleApi.listAssignments(
        workspaceRef ? { workspace_ref: workspaceRef } : undefined,
      ),
    enabled: canRead && refResuelto,
  })
  const sources = useQuery({
    queryKey: consoleKeys.sources(),
    queryFn: () => consoleApi.listSources(),
    enabled: canRead && canWrite,
  })
  const deleteMutation = usePrivilegedMutation<string, void>({
    mutationFn: (id) => consoleApi.deleteAssignment(id),
    invalidateKeys: () => [consoleKeys.connectorAssignments(tenant)],
    successMessage: t('console:connectors.assignmentDeleted'),
    onDone: () => setDeleteTarget(null),
  })

  if (!canRead) {
    return (
      <ForbiddenState title={t('console:connectors.assignmentsForbidden')} />
    )
  }
  if (workspaceId && workspaces.isLoading) return <Spinner />
  if (workspaceId && workspaces.isError) {
    return <ErrorState retry={() => void workspaces.refetch()} />
  }
  // ⛔ El resolver `workspaceId` → slug tiene su propio estado y no lo miraba NADIE: si
  //    `getWorkspaceByID` falla, la dependiente queda `enabled: false`, que no es `isLoading` ni
  //    `isError`, y la vista caía a EmptyState diciendo «no hay conectores» cuando lo cierto
  //    era «no he podido mirar». La ausencia no prueba nada; el fallo sí.
  if (workspaceId && refQuery.isLoading) return <Spinner />
  if (workspaceId && refQuery.isError) {
    return <ErrorState retry={() => void refQuery.refetch()} />
  }
  if (assignments.isLoading) return <Spinner />
  if (assignments.isError) {
    return <ErrorState retry={() => void assignments.refetch()} />
  }

  const items = assignments.data?.items ?? []
  const connectorNames =
    sources.data?.sources.map((source) => source.name) ?? []

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-base font-semibold text-foreground">
            {t('console:connectors.assignments')}
          </h3>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:connectors.assignmentsDesc')}
          </p>
        </div>
        {canWrite ? (
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('console:connectors.assignmentAdd')}
          </Button>
        ) : null}
      </div>

      {items.length === 0 ? (
        <EmptyState
          icon={<Link />}
          title={t('console:connectors.noAssignments')}
          description={t('console:connectors.noAssignmentsDesc')}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.connectorName')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.workspace')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.mode')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.statusLabel')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.note')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {items.map((assignment) => (
                <tr key={assignment.id} className="border-t border-border">
                  <td className="px-3 py-2 font-medium text-foreground">
                    {assignment.connector_name}
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="outline">{assignment.workspace_ref}</Badge>
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="neutral">
                      {assignment.mode === 'r'
                        ? t('console:connectors.modeRead')
                        : t('console:connectors.modeReadWrite')}
                    </Badge>
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant={assignment.enabled ? 'success' : 'neutral'}>
                      {assignment.enabled
                        ? t('common:status.enabled')
                        : t('common:status.disabled')}
                    </Badge>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {assignment.note || '—'}
                  </td>
                  <td className="px-3 py-2 text-right">
                    {canWrite ? (
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setEditing(assignment)}
                        >
                          <Pencil />
                          {t('console:connectors.assignmentEdit')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setDeleteTarget(assignment)}
                        >
                          <Trash2 />
                          {t('console:connectors.assignmentRemove')}
                        </Button>
                      </div>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {createOpen ? (
        <AssignmentDialog
          open
          tenant={tenant}
          workspaces={workspaces.data?.items ?? []}
          connectorNames={connectorNames}
          onClose={() => setCreateOpen(false)}
        />
      ) : null}
      {editing ? (
        <AssignmentDialog
          open
          tenant={tenant}
          workspaces={workspaces.data?.items ?? []}
          connectorNames={connectorNames}
          existing={editing}
          onClose={() => setEditing(null)}
        />
      ) : null}
      {deleteTarget ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(open) => {
              if (!open) setDeleteTarget(null)
            }}
            title={t('console:connectors.removeAssignment')}
            description={t('console:connectors.removeAssignmentDesc', {
              connector: deleteTarget.connector_name,
              workspace: deleteTarget.workspace_ref,
            })}
            confirmLabel={t('console:connectors.assignmentRemove')}
            pending={deleteMutation.isPending}
            tone="danger"
            onConfirm={() => {
              if (deleteTarget.id) deleteMutation.mutate(deleteTarget.id)
            }}
          />
        </RequireAssurance>
      ) : null}
    </section>
  )
}

function AssignmentDialog({
  open,
  tenant,
  workspaces,
  connectorNames,
  existing,
  onClose,
}: {
  open: boolean
  tenant: string | null
  workspaces: WorkspaceDTO[]
  connectorNames: string[]
  existing?: ConnectorAssignmentDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const isEdit = !!existing
  const connectorOptions = useMemo(
    () => unique([existing?.connector_name, ...connectorNames]),
    [connectorNames, existing?.connector_name],
  )
  const [connector, setConnector] = useState(existing?.connector_name ?? '')
  const [workspaceRef, setWorkspaceRef] = useState(
    existing?.workspace_ref ?? '',
  )
  const [mode, setMode] = useState(existing?.mode ?? 'rw')
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [note, setNote] = useState(existing?.note ?? '')

  if (open && existing && connector !== existing.connector_name) {
    setConnector(existing.connector_name)
    setWorkspaceRef(existing.workspace_ref)
    setMode(existing.mode ?? 'rw')
    setEnabled(existing.enabled)
    setNote(existing.note ?? '')
  }

  const mutation = usePrivilegedMutation<
    ConnectorAssignmentDTO,
    ConnectorAssignmentDTO
  >({
    mutationFn: (input) =>
      isEdit && existing?.id
        ? consoleApi.updateAssignment(existing.id, input)
        : consoleApi.createAssignment(input),
    invalidateKeys: () => [consoleKeys.connectorAssignments(tenant)],
    successMessage: isEdit
      ? t('console:connectors.assignmentUpdated')
      : t('console:connectors.assignmentCreated'),
    onDone: onClose,
  })
  const valid = connector.trim() !== '' && workspaceRef.trim() !== ''

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-w-lg">
        {open ? (
          <RequireAssurance minAal={AAL.HARDWARE} action="console">
            <DialogHeader>
              <DialogTitle>
                {isEdit
                  ? t('console:connectors.assignmentEditTitle')
                  : t('console:connectors.assignmentCreateTitle')}
              </DialogTitle>
              <DialogDescription>
                {t('console:connectors.assignConnectorDesc')}
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-4">
              <Field
                label={t('console:connectors.connectorName')}
                htmlFor="assignment-connector"
                required
              >
                <Select
                  value={connector}
                  onValueChange={setConnector}
                  disabled={isEdit}
                >
                  <SelectTrigger
                    id="assignment-connector"
                    aria-label={t('console:connectors.connectorName')}
                  >
                    <SelectValue placeholder={t('console:connectors.select')} />
                  </SelectTrigger>
                  <SelectContent>
                    {connectorOptions.map((name) => (
                      <SelectItem key={name} value={name}>
                        {name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t('console:connectors.workspace')}
                htmlFor="assignment-workspace"
                required
              >
                <Select
                  value={workspaceRef}
                  onValueChange={setWorkspaceRef}
                  disabled={isEdit}
                >
                  <SelectTrigger
                    id="assignment-workspace"
                    aria-label={t('console:connectors.workspace')}
                  >
                    <SelectValue placeholder={t('console:connectors.select')} />
                  </SelectTrigger>
                  <SelectContent>
                    {workspaces
                      .filter((workspace) => workspace.status === 'active')
                      .map((workspace) => (
                        <SelectItem key={workspace.slug} value={workspace.slug}>
                          {workspace.name} ({workspace.slug})
                        </SelectItem>
                      ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t('console:connectors.mode')}
                htmlFor="assignment-mode"
              >
                <Select value={mode} onValueChange={setMode}>
                  <SelectTrigger
                    id="assignment-mode"
                    aria-label={t('console:connectors.mode')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="rw">
                      {t('console:connectors.modeReadWrite')}
                    </SelectItem>
                    <SelectItem value="r">
                      {t('console:connectors.modeRead')}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t('console:connectors.enabled')}
                htmlFor="assignment-enabled"
              >
                <div className="flex items-center gap-2">
                  <Switch
                    id="assignment-enabled"
                    checked={enabled}
                    onCheckedChange={setEnabled}
                    aria-label={t('console:connectors.enabled')}
                  />
                  <span className="text-sm text-muted-foreground">
                    {enabled
                      ? t('common:status.enabled')
                      : t('common:status.disabled')}
                  </span>
                </div>
              </Field>
              <Field
                label={t('console:connectors.note')}
                htmlFor="assignment-note"
              >
                <Input
                  id="assignment-note"
                  value={note}
                  onChange={(event) => setNote(event.target.value)}
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
                disabled={!valid || mutation.isPending}
                onClick={() =>
                  mutation.mutate({
                    connector_name: connector.trim(),
                    workspace_ref: workspaceRef.trim(),
                    mode,
                    enabled,
                    note: note.trim() || undefined,
                  })
                }
              >
                {mutation.isPending ? <Spinner size="sm" aria-hidden /> : null}
                {t('common:actions.save')}
              </Button>
            </DialogFooter>
          </RequireAssurance>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

// --- Section 2: workspace-scoped connectors ---------------------------------

function WsConnectorSection({
  tenant,
  workspaceId,
  canRead,
  canWrite,
}: {
  tenant: string | null
  workspaceId: string | undefined
  canRead: boolean
  canWrite: boolean
}) {
  const { t } = useTranslation(['console', 'common'])
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<WorkspaceConnectorDTO | null>(null)
  const [deleteTarget, setDeleteTarget] =
    useState<WorkspaceConnectorDTO | null>(null)

  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(tenant),
    queryFn: () => consoleApi.listWorkspaces(),
    enabled: canRead,
  })
  const {
    ref: workspaceRef,
    resuelto: refResuelto,
    clave: refClave,
    query: refQuery,
  } = useWorkspaceRef(tenant, workspaceId, canRead)
  const connectors = useQuery({
    queryKey: [...consoleKeys.workspaceConnectors(tenant), refClave],
    queryFn: () =>
      consoleApi.listWsConnectors(
        workspaceRef ? { workspace_ref: workspaceRef } : undefined,
      ),
    enabled: canRead && refResuelto,
  })
  const deleteMutation = usePrivilegedMutation<string, void>({
    mutationFn: (id) => consoleApi.deleteWsConnector(id),
    invalidateKeys: () => [consoleKeys.workspaceConnectors(tenant)],
    successMessage: t('console:connectors.wsConnectorDeleted'),
    onDone: () => setDeleteTarget(null),
  })

  if (!canRead) {
    return (
      <ForbiddenState title={t('console:connectors.wsConnectorsForbidden')} />
    )
  }
  if (workspaceId && workspaces.isLoading) return <Spinner />
  if (workspaceId && workspaces.isError) {
    return <ErrorState retry={() => void workspaces.refetch()} />
  }
  // ⛔ El resolver `workspaceId` → slug tiene su propio estado y no lo miraba NADIE: si
  //    `getWorkspaceByID` falla, la dependiente queda `enabled: false`, que no es `isLoading` ni
  //    `isError`, y la vista caía a EmptyState diciendo «no hay conectores» cuando lo cierto
  //    era «no he podido mirar». La ausencia no prueba nada; el fallo sí.
  if (workspaceId && refQuery.isLoading) return <Spinner />
  if (workspaceId && refQuery.isError) {
    return <ErrorState retry={() => void refQuery.refetch()} />
  }
  if (connectors.isLoading) return <Spinner />
  if (connectors.isError) {
    return <ErrorState retry={() => void connectors.refetch()} />
  }

  const items = connectors.data?.items ?? []

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-base font-semibold text-foreground">
            {t('console:connectors.workspaceConnectors')}
          </h3>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:connectors.workspaceConnectorsDesc')}
          </p>
        </div>
        {canWrite ? (
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('console:connectors.createWsConnector')}
          </Button>
        ) : null}
      </div>

      {items.length === 0 ? (
        <EmptyState
          icon={<Unlink />}
          title={t('console:connectors.noWsConnectors')}
          description={t('console:connectors.noWsConnectorsDesc')}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.name')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.kind')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.workspace')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.statusLabel')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:connectors.note')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {items.map((connector) => (
                <tr key={connector.id} className="border-t border-border">
                  <td className="px-3 py-2 font-medium text-foreground">
                    {connector.name}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                    {connector.kind}
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="outline">{connector.workspace_ref}</Badge>
                  </td>
                  <td className="px-3 py-2">
                    <Badge
                      variant={
                        connector.enabled && connector.status === 'running'
                          ? 'success'
                          : 'neutral'
                      }
                    >
                      {connector.enabled
                        ? connector.status || 'pending'
                        : t('common:status.disabled')}
                    </Badge>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {connector.note || '—'}
                  </td>
                  <td className="px-3 py-2 text-right">
                    {canWrite ? (
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setEditing(connector)}
                        >
                          <Pencil />
                          {t('console:connectors.wsConnectorEdit')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setDeleteTarget(connector)}
                        >
                          <Trash2 />
                          {t('console:connectors.wsConnectorDelete')}
                        </Button>
                      </div>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {createOpen ? (
        <WsConnectorDialog
          open
          tenant={tenant}
          workspaces={workspaces.data?.items ?? []}
          onClose={() => setCreateOpen(false)}
        />
      ) : null}
      {editing ? (
        <WsConnectorDialog
          open
          tenant={tenant}
          workspaces={workspaces.data?.items ?? []}
          existing={editing}
          onClose={() => setEditing(null)}
        />
      ) : null}
      {deleteTarget ? (
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <ConfirmDialog
            open
            onOpenChange={(open) => {
              if (!open) setDeleteTarget(null)
            }}
            title={t('console:connectors.deleteWsConnector')}
            description={t('console:connectors.deleteWsConnectorDesc', {
              name: deleteTarget.name,
              workspace: deleteTarget.workspace_ref,
            })}
            confirmLabel={t('console:connectors.wsConnectorDelete')}
            pending={deleteMutation.isPending}
            tone="danger"
            onConfirm={() => {
              if (deleteTarget.id) deleteMutation.mutate(deleteTarget.id)
            }}
          />
        </RequireAssurance>
      ) : null}
    </section>
  )
}

function WsConnectorDialog({
  open,
  tenant,
  workspaces,
  existing,
  onClose,
}: {
  open: boolean
  tenant: string | null
  workspaces: WorkspaceDTO[]
  existing?: WorkspaceConnectorDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const isEdit = !!existing
  const [name, setName] = useState(existing?.name ?? '')
  const [kind, setKind] = useState(existing?.kind ?? '')
  const [workspaceRef, setWorkspaceRef] = useState(
    existing?.workspace_ref ?? '',
  )
  const [pollSeconds, setPollSeconds] = useState(
    String(existing?.poll_seconds ?? 0),
  )
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [note, setNote] = useState(existing?.note ?? '')
  const [configText, setConfigText] = useState(mapToLines(existing?.config))
  const [secretsText, setSecretsText] = useState(
    secretKeysToBlankLines(existing?.secrets),
  )

  if (open && existing && name !== existing.name) {
    setName(existing.name)
    setKind(existing.kind)
    setWorkspaceRef(existing.workspace_ref)
    setPollSeconds(String(existing.poll_seconds ?? 0))
    setEnabled(existing.enabled)
    setNote(existing.note ?? '')
    setConfigText(mapToLines(existing.config))
    setSecretsText(secretKeysToBlankLines(existing.secrets))
  }

  const connectorCatalog = useQuery({
    queryKey: consoleKeys.connectors(),
    queryFn: () => consoleApi.listConnectors(),
    enabled: open,
  })
  const catalogKinds = unique([
    existing?.kind,
    ...(connectorCatalog.data?.connectors.map((connector) => connector.kind) ??
      []),
  ])
  const mutation = usePrivilegedMutation<
    WorkspaceConnectorDTO,
    WorkspaceConnectorDTO
  >({
    mutationFn: (input) =>
      isEdit && existing?.id
        ? consoleApi.updateWsConnector(existing.id, input)
        : consoleApi.createWsConnector(input),
    invalidateKeys: () => [consoleKeys.workspaceConnectors(tenant)],
    successMessage: isEdit
      ? t('console:connectors.wsConnectorUpdated')
      : t('console:connectors.wsConnectorCreated'),
    onDone: onClose,
  })
  const poll = Number.parseInt(pollSeconds, 10)
  const valid =
    name.trim() !== '' &&
    kind.trim() !== '' &&
    workspaceRef.trim() !== '' &&
    Number.isFinite(poll) &&
    poll >= 0

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[88vh] max-w-2xl overflow-y-auto">
        {open ? (
          <RequireAssurance minAal={AAL.HARDWARE} action="console">
            <DialogHeader>
              <DialogTitle>
                {isEdit
                  ? t('console:connectors.editWsConnectorTitle')
                  : t('console:connectors.createWsConnector')}
              </DialogTitle>
              <DialogDescription>
                {t('console:connectors.createWsConnectorDesc')}
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-4">
              <div className="grid gap-3 md:grid-cols-2">
                <Field
                  label={t('console:connectors.name')}
                  htmlFor="ws-connector-name"
                  required
                >
                  <Input
                    id="ws-connector-name"
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    disabled={isEdit}
                    mono
                  />
                </Field>
                <Field
                  label={t('console:connectors.kind')}
                  htmlFor="ws-connector-kind"
                  required
                >
                  <Select
                    value={kind}
                    onValueChange={setKind}
                    disabled={isEdit}
                  >
                    <SelectTrigger
                      id="ws-connector-kind"
                      aria-label={t('console:connectors.kind')}
                    >
                      <SelectValue
                        placeholder={t('console:connectors.select')}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {catalogKinds.map((item) => (
                        <SelectItem key={item} value={item}>
                          {item}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              </div>
              <Field
                label={t('console:connectors.workspace')}
                htmlFor="ws-connector-workspace"
                required
              >
                <Select
                  value={workspaceRef}
                  onValueChange={setWorkspaceRef}
                  disabled={isEdit}
                >
                  <SelectTrigger
                    id="ws-connector-workspace"
                    aria-label={t('console:connectors.workspace')}
                  >
                    <SelectValue placeholder={t('console:connectors.select')} />
                  </SelectTrigger>
                  <SelectContent>
                    {workspaces
                      .filter((workspace) => workspace.status === 'active')
                      .map((workspace) => (
                        <SelectItem key={workspace.slug} value={workspace.slug}>
                          {workspace.name} ({workspace.slug})
                        </SelectItem>
                      ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t('console:connectors.pollSeconds')}
                htmlFor="ws-connector-poll"
                description={t('console:connectors.pollSecondsHint')}
              >
                <Input
                  id="ws-connector-poll"
                  type="number"
                  min={0}
                  value={pollSeconds}
                  onChange={(event) => setPollSeconds(event.target.value)}
                />
              </Field>
              <Field
                label={t('console:connectors.enabled')}
                htmlFor="ws-connector-enabled"
              >
                <div className="flex items-center gap-2">
                  <Switch
                    id="ws-connector-enabled"
                    checked={enabled}
                    onCheckedChange={setEnabled}
                    aria-label={t('console:connectors.enabled')}
                  />
                  <span className="text-sm text-muted-foreground">
                    {enabled
                      ? t('common:status.enabled')
                      : t('common:status.disabled')}
                  </span>
                </div>
              </Field>
              <Field
                label={t('console:connectors.config')}
                htmlFor="ws-connector-config"
                description={t('console:connectors.configHint')}
              >
                <Textarea
                  id="ws-connector-config"
                  value={configText}
                  onChange={(event) => setConfigText(event.target.value)}
                  rows={4}
                  mono
                />
              </Field>
              <Field
                label={t('console:connectors.secrets')}
                htmlFor="ws-connector-secrets"
                description={t('console:connectors.secretsHint')}
              >
                <Textarea
                  id="ws-connector-secrets"
                  value={secretsText}
                  onChange={(event) => setSecretsText(event.target.value)}
                  rows={3}
                  mono
                />
              </Field>
              <Field
                label={t('console:connectors.note')}
                htmlFor="ws-connector-note"
              >
                <Input
                  id="ws-connector-note"
                  value={note}
                  onChange={(event) => setNote(event.target.value)}
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
                disabled={!valid || mutation.isPending}
                onClick={() =>
                  mutation.mutate({
                    name: name.trim(),
                    kind: kind.trim(),
                    workspace_ref: workspaceRef.trim(),
                    config: parseKeyValueLines(configText),
                    secrets: parseKeyValueLines(secretsText),
                    poll_seconds: poll,
                    enabled,
                    note: note.trim() || undefined,
                  })
                }
              >
                {mutation.isPending ? <Spinner size="sm" aria-hidden /> : null}
                {t('common:actions.save')}
              </Button>
            </DialogFooter>
          </RequireAssurance>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function unique(values: Array<string | undefined>): string[] {
  return [...new Set(values.filter((value): value is string => !!value))]
}

function mapToLines(value?: Record<string, string>): string {
  return Object.entries(value ?? {})
    .map(([key, val]) => `${key}=${val}`)
    .join('\n')
}

function secretKeysToBlankLines(value?: Record<string, string>): string {
  return Object.keys(value ?? {})
    .map((key) => `${key}=`)
    .join('\n')
}

function parseKeyValueLines(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const rawLine of text.split('\n')) {
    const line = rawLine.trim()
    if (!line) continue
    const eq = line.indexOf('=')
    const key = (eq >= 0 ? line.slice(0, eq) : line).trim()
    if (!key) continue
    out[key] = eq >= 0 ? line.slice(eq + 1).trim() : ''
  }
  return Object.keys(out).length ? out : undefined
}
