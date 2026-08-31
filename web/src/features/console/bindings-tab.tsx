// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import type { QueryKey } from '@tanstack/react-query'
import {
  Eye,
  Link2,
  Network,
  Pencil,
  Plus,
  ShieldAlert,
  ShieldCheck,
  ShieldOff,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
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
import type { AgentDTO } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  type BindingApplyResult,
  type BindingDeleteResult,
  type BindingEffect,
  type CredentialRefKind,
  type CustomRoleDTO,
  type GroupDTO,
  type PostureRequestDTO,
  type ConnectorAssignmentDTO,
  type RBACCatalogDTO,
  type ResourceNodeDTO,
  type SourceScopeActorKind,
  type SourceScopeBindingDTO,
  type SourceScopeSourceType,
  type SourceScopeTree,
} from './api'
import { ResourceTreePicker } from './resource-tree-picker'
import { FormError } from './roles-shared'

/**
 * El máximo que el repositorio genérico acepta (`maxLimit`, sqlstore/generic.go:29). UNA sola
 * constante para las SIETE listas de esta pantalla: dos copias del mismo techo envejecen aparte,
 * y aquí el techo no es una preferencia sino el contrato del store.
 */
const CONSOLE_PAGE = 1000

const SOURCE_TYPES: SourceScopeSourceType[] = [
  'mcp',
  'model',
  'provider',
  'knowledge',
  'data',
]

const SCOPE_TREES: SourceScopeTree[] = [
  'workspace',
  'agent_group',
  'folder',
  'session',
  'agent',
  'user',
  'user_group',
  'role',
]

const CRED_REF_KINDS: CredentialRefKind[] = [
  'env',
  'vault',
  'secret_manager',
  'file',
  'other',
]

const CREDENTIAL_PRECEDENCE: SourceScopeTree[] = [
  'session',
  'agent',
  'user',
  'user_group',
  'role',
  'agent_group',
  'folder',
  'workspace',
]

const STATUS_ALL = '__all__'

export function BindingsTab() {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const navigate = useNavigate()
  const canRead = can('sourcescope:binding:read')
  const canWrite = can('sourcescope:binding:write')
  const canReview = can('sourcescope:posture:admin')
  const [sourceType, setSourceType] = useState<SourceScopeSourceType>('mcp')
  const [sourceRef, setSourceRef] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<SourceScopeBindingDTO | null>(null)
  const [deleteTarget, setDeleteTarget] =
    useState<SourceScopeBindingDTO | null>(null)
  const [disableOpen, setDisableOpen] = useState(false)
  const [queued, setQueued] = useState<PostureRequestDTO | null>(null)

  const sourceReady = sourceRef.trim() !== ''
  const bindingParams = useMemo(
    () => ({
      source_type: sourceType,
      source_ref: sourceRef.trim(),
      // ⛔ EL TECHO VA CON LOS FILTROS: `handleListBindings` publica `has_more` y sin `limit` el
      //    repositorio genérico pagina a 100. Un binding es autoridad viva —qué identidad usa qué
      //    fuente—, así que una lista recortada se lee «esto es lo que hay atado».
      limit: CONSOLE_PAGE,
    }),
    [sourceRef, sourceType],
  )
  const bindings = useQuery({
    queryKey: consoleKeys.bindings(activeTenant, bindingParams),
    queryFn: () => consoleApi.listBindings(bindingParams),
    enabled: canRead && sourceReady,
  })

  if (!canRead) {
    return (
      <div className="pt-4">
        <ForbiddenState
          icon={<ShieldOff />}
          title={t('console:bindings.readOnlyNotice')}
        />
      </div>
    )
  }

  const rows = bindings.data?.items ?? []

  return (
    <div className="flex flex-col gap-8 pt-4">
      {bindings.data?.has_more && !bindings.error ? (
        <div>
          <Badge variant="warning" title={t('console:bindings.truncatedHint')}>
            {t('console:bindings.truncated', { n: rows.length })}
          </Badge>
        </div>
      ) : null}
      <section className="flex flex-col gap-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-foreground">
              {t('console:bindings.title')}
            </h2>
            <p className="max-w-3xl text-sm text-muted-foreground">
              {t('console:bindings.caption')}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            {canWrite && sourceReady ? (
              <>
                <Button onClick={() => setCreateOpen(true)}>
                  <Plus />
                  {t('console:bindings.create')}
                </Button>
                <Button
                  variant="destructive"
                  onClick={() => setDisableOpen(true)}
                >
                  <ShieldAlert />
                  {t('console:bindings.disableScoping')}
                </Button>
              </>
            ) : null}
          </div>
        </div>

        <div className="grid gap-3 md:grid-cols-[12rem_minmax(16rem,1fr)]">
          <Field
            label={t('console:bindings.sourceType')}
            htmlFor="ss-source-type"
          >
            <Select
              value={sourceType}
              onValueChange={(v) => {
                setSourceType(v as SourceScopeSourceType)
                setQueued(null)
              }}
            >
              <SelectTrigger
                id="ss-source-type"
                aria-label={t('console:bindings.sourceType')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SOURCE_TYPES.map((type) => (
                  <SelectItem key={type} value={type}>
                    {t(`console:bindings.sourceTypes.${type}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('console:bindings.sourceRef')}
            htmlFor="ss-source-ref"
            description={t('console:bindings.sourceRefHint')}
            required
          >
            <Input
              id="ss-source-ref"
              value={sourceRef}
              onChange={(event) => {
                setSourceRef(event.target.value)
                setQueued(null)
              }}
              mono
            />
          </Field>
        </div>

        {queued ? <QueuedNotice request={queued} /> : null}
      </section>

      <BindingsTable
        sourceReady={sourceReady}
        bindings={rows}
        loading={bindings.isLoading}
        isError={bindings.isError}
        refetch={() => void bindings.refetch()}
        canWrite={canWrite}
        onEdit={setEditing}
        onDelete={setDeleteTarget}
      />

      {sourceReady ? (
        <ResolutionPreview
          sourceType={sourceType}
          sourceRef={sourceRef.trim()}
          bindings={rows}
          canRead={canRead}
          onOpenAccessMap={() =>
            navigate({
              to: '/access-map',
              search: { focus: sourceRef.trim() },
            } as never)
          }
        />
      ) : null}

      <PostureQueue
        canReview={canReview}
        selectedSourceRef={sourceRef.trim()}
      />

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[88vh] max-w-2xl overflow-y-auto">
          {createOpen ? (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <BindingForm
                sourceType={sourceType}
                sourceRef={sourceRef.trim()}
                onQueued={setQueued}
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
              <BindingForm
                sourceType={sourceType}
                sourceRef={sourceRef.trim()}
                existing={editing}
                onQueued={setQueued}
                onClose={() => setEditing(null)}
              />
            </RequireAssurance>
          ) : null}
        </DialogContent>
      </Dialog>

      {deleteTarget ? (
        <BindingDeleteDialog
          binding={deleteTarget}
          onQueued={setQueued}
          onClose={() => setDeleteTarget(null)}
        />
      ) : null}

      {disableOpen ? (
        <DisableScopingDialog
          sourceType={sourceType}
          sourceRef={sourceRef.trim()}
          onQueued={setQueued}
          onClose={() => setDisableOpen(false)}
        />
      ) : null}
    </div>
  )
}

function BindingsTable({
  sourceReady,
  bindings,
  loading,
  isError,
  refetch,
  canWrite,
  onEdit,
  onDelete,
}: {
  sourceReady: boolean
  bindings: SourceScopeBindingDTO[]
  loading: boolean
  isError: boolean
  refetch: () => void
  canWrite: boolean
  onEdit: (binding: SourceScopeBindingDTO) => void
  onDelete: (binding: SourceScopeBindingDTO) => void
}) {
  const { t } = useTranslation('console')
  if (!sourceReady) {
    return (
      <EmptyState
        icon={<Link2 />}
        title={t('bindings.pickSource')}
        description={t('bindings.pickSourceHint')}
      />
    )
  }
  if (loading) {
    return (
      <div className="flex justify-center py-8">
        <Spinner />
      </div>
    )
  }
  if (isError) return <ErrorState retry={refetch} />
  if (bindings.length === 0) {
    return (
      <EmptyState
        icon={<ShieldCheck />}
        title={t('bindings.none')}
        description={t('bindings.noneHint')}
      />
    )
  }

  return (
    <section className="overflow-hidden rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
          <tr>
            <th className="px-3 py-2 font-medium">{t('bindings.scope')}</th>
            <th className="px-3 py-2 font-medium">{t('bindings.effect')}</th>
            <th className="px-3 py-2 font-medium">{t('bindings.enabled')}</th>
            <th className="px-3 py-2 font-medium">
              {t('bindings.credential')}
            </th>
            <th className="px-3 py-2 font-medium">{t('bindings.note')}</th>
            <th className="px-3 py-2" />
          </tr>
        </thead>
        <tbody>
          {bindings.map((binding) => (
            <tr key={binding.id} className="border-t border-border align-top">
              <td className="px-3 py-2">
                <div className="flex flex-col gap-1">
                  <Badge variant="outline">
                    {t(`bindings.scopeTrees.${binding.scope_tree}`)}
                  </Badge>
                  <code className="break-all font-mono text-xs text-foreground">
                    {binding.scope_ref || t('bindings.defaultWorkspace')}
                  </code>
                  {binding.folder_path ? (
                    <span className="break-all text-xs text-muted-foreground">
                      {binding.folder_path}
                    </span>
                  ) : null}
                </div>
              </td>
              <td className="px-3 py-2">
                <EffectBadge effect={binding.effect ?? 'allow'} />
              </td>
              <td className="px-3 py-2">
                <Badge variant={binding.enabled ? 'success' : 'neutral'}>
                  {binding.enabled
                    ? t('bindings.enabledYes')
                    : t('bindings.enabledNo')}
                </Badge>
              </td>
              <td className="px-3 py-2">
                {binding.cred_name ? (
                  <div className="flex flex-col gap-1">
                    <span className="font-mono text-xs text-foreground">
                      {binding.cred_name}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {binding.cred_hint || binding.cred_ref_kind}
                    </span>
                  </div>
                ) : (
                  <span className="text-muted-foreground">
                    {t('bindings.noCredential')}
                  </span>
                )}
              </td>
              <td className="max-w-xs px-3 py-2 text-muted-foreground">
                {binding.note || '—'}
              </td>
              <td className="px-3 py-2 text-right">
                {canWrite ? (
                  <div className="flex justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onEdit(binding)}
                    >
                      <Pencil />
                      {t('bindings.edit')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onDelete(binding)}
                    >
                      <Trash2 />
                      {t('bindings.delete')}
                    </Button>
                  </div>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  )
}

function BindingForm({
  sourceType,
  sourceRef,
  existing,
  onQueued,
  onClose,
}: {
  sourceType: SourceScopeSourceType
  sourceRef: string
  existing?: SourceScopeBindingDTO
  onQueued: (request: PostureRequestDTO) => void
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const isEdit = !!existing
  const [scopeTree, setScopeTree] = useState<SourceScopeTree>(
    existing?.scope_tree ?? 'workspace',
  )
  const [scopeRef, setScopeRef] = useState(existing?.scope_ref ?? '')
  const [selectedResource, setSelectedResource] =
    useState<ResourceNodeDTO | null>(null)
  const [effect, setEffect] = useState<BindingEffect>(
    existing?.effect ?? 'allow',
  )
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [credName, setCredName] = useState(existing?.cred_name ?? '')
  const [credRefKind, setCredRefKind] = useState<CredentialRefKind | ''>(
    existing?.cred_ref_kind ?? '',
  )
  const [credRef, setCredRef] = useState(existing?.cred_ref ?? '')
  const [credHint, setCredHint] = useState(existing?.cred_hint ?? '')
  const [note, setNote] = useState(existing?.note ?? '')

  // ⛔ ESTAS TRES NO PINTAN UNA TABLA: LLENAN UN SELECTOR, y ahí el recorte silencioso no oculta
  //    filas — impide ELEGIR. El operador que no encuentra su workspace, su grupo o su agente no
  //    ve una lista incompleta: ve una entidad que «no existe», y no tiene forma de saber que la
  //    página se acabó antes. Por eso piden el techo y, cuando el motor dice `has_more`, el
  //    selector lo DICE en vez de callarse.
  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant, { limit: CONSOLE_PAGE }),
    queryFn: () => consoleApi.listWorkspaces({ limit: CONSOLE_PAGE }),
    enabled: scopeTree === 'workspace' && can('tenant:read'),
  })
  const agentGroups = useQuery({
    queryKey: consoleKeys.agentGroups(activeTenant, { limit: CONSOLE_PAGE }),
    queryFn: () => consoleApi.listAgentGroups({ limit: CONSOLE_PAGE }),
    enabled: scopeTree === 'agent_group' && can('agent:read'),
  })
  const groups = useQuery({
    queryKey: consoleKeys.groups(activeTenant),
    queryFn: () => consoleApi.listGroups(),
    enabled: scopeTree === 'user_group',
  })
  const catalog = useQuery({
    queryKey: consoleKeys.rbacCatalog(),
    queryFn: () => consoleApi.rbacCatalog(),
    enabled: scopeTree === 'role',
    staleTime: 5 * 60_000,
  })
  const roles = useQuery({
    queryKey: consoleKeys.roles(activeTenant),
    queryFn: () => consoleApi.listRoles(),
    enabled: scopeTree === 'role',
  })
  // El 200 era un número puesto a mano por debajo del techo real: ni pedía todo lo que el motor
  // da, ni decía nada cuando se quedaba corto.
  const agentParams = useMemo(
    () => ({ tenant: activeTenant ?? undefined, limit: CONSOLE_PAGE }),
    [activeTenant],
  )
  const agents = useQuery({
    queryKey: consoleKeys.agents(activeTenant, agentParams),
    queryFn: () => consoleApi.listAgents(agentParams),
    enabled: scopeTree === 'agent' && can('agent:read'),
  })

  const roleOptions = useMemo(
    () => buildRoleOptions(catalog.data, roles.data?.items ?? [], t),
    [catalog.data, roles.data?.items, t],
  )
  const agentOptions = useMemo(
    () => buildAgentOptions(agents.data?.items ?? []),
    [agents.data?.items],
  )

  const credentialTouched =
    credName.trim() !== '' ||
    credRefKind !== '' ||
    credRef.trim() !== '' ||
    credHint.trim() !== ''
  const credentialComplete =
    !credentialTouched ||
    (credName.trim() !== '' && credRefKind !== '' && credRef.trim() !== '')
  const inlineCredential = containsInlineCredential(credRef)
  const scopeValid =
    scopeTree === 'workspace' ? scopeRef.trim() !== '' : scopeRef.trim() !== ''
  const valid =
    sourceRef !== '' &&
    scopeValid &&
    credentialComplete &&
    !inlineCredential &&
    credHint.trim().length <= 64 &&
    note.length <= 512

  const mutation = usePrivilegedMutation<void, BindingApplyResult>({
    mutationFn: () => {
      const body: SourceScopeBindingDTO = {
        source_type: sourceType,
        source_ref: sourceRef,
        scope_tree: scopeTree,
        scope_ref: scopeRef.trim() || undefined,
        effect,
        enabled,
        note: note.trim() || undefined,
        cred_name: credentialTouched ? credName.trim() : undefined,
        cred_ref_kind: credentialTouched
          ? (credRefKind as CredentialRefKind)
          : undefined,
        cred_ref: credentialTouched ? credRef.trim() : undefined,
        cred_hint: credentialTouched ? credHint.trim() || undefined : undefined,
      }
      return isEdit && existing.id
        ? consoleApi.updateBinding(existing.id, body)
        : consoleApi.createBinding(body)
    },
    invalidateKeys: (data) => {
      const keys: QueryKey[] = [
        consoleKeys.bindings(activeTenant),
        consoleKeys.postureRequests(activeTenant),
      ]
      if (data.kind === 'binding' && data.binding.id) {
        keys.push(consoleKeys.binding(activeTenant, data.binding.id))
      }
      if (data.kind === 'posture_request' && data.posture_request.id) {
        keys.push(
          consoleKeys.postureRequest(activeTenant, data.posture_request.id),
        )
      }
      return keys
    },
    successMessage: (data) =>
      data.kind === 'posture_request'
        ? t('console:bindings.queued')
        : isEdit
          ? t('console:bindings.updated')
          : t('console:bindings.created'),
    onDone: (data) => {
      if (data.kind === 'posture_request') onQueued(data.posture_request)
      onClose()
    },
  })

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:bindings.editTitle')
            : t('console:bindings.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {t('console:bindings.formCaption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="rounded-lg border border-border bg-muted/20 p-3 text-sm">
          <span className="font-medium text-foreground">
            {t('console:bindings.source')}
          </span>{' '}
          <code className="font-mono text-xs">
            {sourceType}:{sourceRef}
          </code>
        </div>

        <Field
          label={t('console:bindings.scopeTree')}
          htmlFor="binding-scope-tree"
        >
          <Select
            value={scopeTree}
            onValueChange={(value) => {
              setScopeTree(value as SourceScopeTree)
              setScopeRef('')
              setSelectedResource(null)
            }}
          >
            <SelectTrigger
              id="binding-scope-tree"
              aria-label={t('console:bindings.scopeTree')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SCOPE_TREES.map((tree) => (
                <SelectItem key={tree} value={tree}>
                  {t(`console:bindings.scopeTrees.${tree}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        {/* ⛔ UN SELECTOR RECORTADO NO OCULTA FILAS: IMPIDE ELEGIR. Y el operador no ve una
            lista corta, ve una entidad que «no existe». Se dice cuál de las tres se quedó a
            medias, porque cada una se llena de una consulta distinta. */}
        {workspaces.data?.has_more && !workspaces.error ? (
          <div>
            <Badge
              variant="warning"
              title={t('console:bindings.pickerTruncatedHint')}
            >
              {t('console:bindings.pickerTruncated.workspaces')}
            </Badge>
          </div>
        ) : null}
        {agentGroups.data?.has_more && !agentGroups.error ? (
          <div>
            <Badge
              variant="warning"
              title={t('console:bindings.pickerTruncatedHint')}
            >
              {t('console:bindings.pickerTruncated.agentGroups')}
            </Badge>
          </div>
        ) : null}
        {agents.data?.has_more && !agents.error ? (
          <div>
            <Badge
              variant="warning"
              title={t('console:bindings.pickerTruncatedHint')}
            >
              {t('console:bindings.pickerTruncated.agents')}
            </Badge>
          </div>
        ) : null}

        <ScopeRefField
          scopeTree={scopeTree}
          scopeRef={scopeRef}
          setScopeRef={setScopeRef}
          selectedResource={selectedResource}
          setSelectedResource={setSelectedResource}
          workspaces={workspaces.data?.items ?? []}
          agentGroups={agentGroups.data?.items ?? []}
          groups={groups.data?.groups ?? []}
          roles={roleOptions}
          agents={agentOptions}
          loading={
            workspaces.isLoading ||
            agentGroups.isLoading ||
            groups.isLoading ||
            catalog.isLoading ||
            roles.isLoading ||
            agents.isLoading
          }
        />

        <Field label={t('console:bindings.effect')}>
          <div className="inline-flex w-fit rounded-md border border-border bg-surface p-0.5">
            <Button
              type="button"
              size="sm"
              variant={effect === 'allow' ? 'primary' : 'ghost'}
              onClick={() => setEffect('allow')}
            >
              <ShieldCheck />
              {t('console:bindings.effects.allow')}
            </Button>
            <Button
              type="button"
              size="sm"
              variant={effect === 'forbid' ? 'destructive' : 'ghost'}
              onClick={() => setEffect('forbid')}
            >
              <ShieldOff />
              {t('console:bindings.effects.forbid')}
            </Button>
          </div>
        </Field>

        <Field label={t('console:bindings.enabled')} htmlFor="binding-enabled">
          <div className="flex items-center gap-2">
            <Switch
              id="binding-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
              aria-label={t('console:bindings.enabled')}
            />
            <span className="text-sm text-muted-foreground">
              {enabled
                ? t('console:bindings.enabledYes')
                : t('console:bindings.enabledNo')}
            </span>
          </div>
        </Field>

        <div className="grid gap-3 md:grid-cols-2">
          <Field
            label={t('console:bindings.credName')}
            htmlFor="binding-cred-name"
            description={t('console:bindings.credentialHint')}
          >
            <Input
              id="binding-cred-name"
              value={credName}
              onChange={(event) => setCredName(event.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('console:bindings.credRefKind')}
            htmlFor="binding-cred-kind"
          >
            <Select
              value={credRefKind || 'none'}
              onValueChange={(value) =>
                setCredRefKind(
                  value === 'none' ? '' : (value as CredentialRefKind),
                )
              }
            >
              <SelectTrigger
                id="binding-cred-kind"
                aria-label={t('console:bindings.credRefKind')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">
                  {t('console:bindings.credNone')}
                </SelectItem>
                {CRED_REF_KINDS.map((kind) => (
                  <SelectItem key={kind} value={kind}>
                    {t(`console:bindings.credKinds.${kind}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
        <Field
          label={t('console:bindings.credRef')}
          htmlFor="binding-cred-ref"
          error={
            inlineCredential
              ? t('console:bindings.inlineCredentialError')
              : credentialTouched && !credentialComplete
                ? t('console:bindings.credentialIncomplete')
                : undefined
          }
        >
          <Input
            id="binding-cred-ref"
            value={credRef}
            onChange={(event) => setCredRef(event.target.value)}
            mono
          />
        </Field>
        <Field
          label={t('console:bindings.credHint')}
          htmlFor="binding-cred-hint"
          description={t('console:bindings.credHintHelp')}
          error={
            credHint.trim().length > 64
              ? t('console:bindings.credHintTooLong')
              : undefined
          }
        >
          <Input
            id="binding-cred-hint"
            value={credHint}
            onChange={(event) => setCredHint(event.target.value)}
          />
        </Field>

        <Field
          label={t('console:bindings.note')}
          htmlFor="binding-note"
          error={
            note.length > 512 ? t('console:bindings.noteTooLong') : undefined
          }
        >
          <Textarea
            id="binding-note"
            value={note}
            onChange={(event) => setNote(event.target.value)}
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
          {mutation.isPending ? <Spinner size="sm" aria-hidden /> : null}
          {t('console:bindings.save')}
        </Button>
      </DialogFooter>
    </>
  )
}

function ScopeRefField({
  scopeTree,
  scopeRef,
  setScopeRef,
  selectedResource,
  setSelectedResource,
  workspaces,
  agentGroups,
  groups,
  roles,
  agents,
  loading,
}: {
  scopeTree: SourceScopeTree
  scopeRef: string
  setScopeRef: (value: string) => void
  selectedResource: ResourceNodeDTO | null
  setSelectedResource: (value: ResourceNodeDTO | null) => void
  workspaces: { slug: string; name: string }[]
  agentGroups: { slug: string; name: string }[]
  groups: GroupDTO[]
  roles: ComboboxOption[]
  agents: ComboboxOption[]
  loading: boolean
}) {
  const { t } = useTranslation('console')

  if (loading) return <Spinner size="sm" />

  if (scopeTree === 'workspace') {
    return (
      <Field
        label={t('bindings.ref.workspace')}
        htmlFor="binding-workspace"
        required
      >
        <Combobox
          id="binding-workspace"
          options={workspaces.map((workspace) => ({
            value: workspace.slug,
            label: workspace.name,
            keywords: [workspace.slug],
          }))}
          value={scopeRef || null}
          onChange={(value) => setScopeRef(value ?? '')}
          placeholder={t('bindings.selectRef')}
        />
      </Field>
    )
  }

  if (scopeTree === 'agent_group') {
    return (
      <Field
        label={t('bindings.ref.agent_group')}
        htmlFor="binding-agent-group"
        required
      >
        <Combobox
          id="binding-agent-group"
          options={agentGroups.map((group) => ({
            value: group.slug,
            label: group.name,
            keywords: [group.slug],
          }))}
          value={scopeRef || null}
          onChange={(value) => setScopeRef(value ?? '')}
          placeholder={t('bindings.selectRef')}
        />
      </Field>
    )
  }

  if (scopeTree === 'folder') {
    return (
      <Field
        label={t('bindings.ref.folder')}
        description={
          selectedResource?.path ||
          (scopeRef
            ? t('bindings.folderSelectedId', { id: scopeRef })
            : undefined)
        }
        required
      >
        <ResourceTreePicker
          value={scopeRef}
          onChange={(id, node) => {
            setScopeRef(id)
            setSelectedResource(node)
          }}
        />
      </Field>
    )
  }

  if (scopeTree === 'user_group') {
    return (
      <Field
        label={t('bindings.ref.user_group')}
        htmlFor="binding-user-group"
        required
      >
        <Combobox
          id="binding-user-group"
          options={groups.map((group) => ({
            value: group.id,
            label: group.display_name || group.id,
            keywords: [group.external_id, group.mapped_role].filter(Boolean),
          }))}
          value={scopeRef || null}
          onChange={(value) => setScopeRef(value ?? '')}
          placeholder={t('bindings.selectRef')}
        />
      </Field>
    )
  }

  if (scopeTree === 'role') {
    return (
      <Field label={t('bindings.ref.role')} htmlFor="binding-role" required>
        <Combobox
          id="binding-role"
          options={roles}
          value={scopeRef || null}
          onChange={(value) => setScopeRef(value ?? '')}
          placeholder={t('bindings.selectRef')}
        />
      </Field>
    )
  }

  if (scopeTree === 'agent') {
    return (
      <Field
        label={t('bindings.ref.agent')}
        htmlFor="binding-agent"
        description={t('bindings.ref.agentHint')}
        required
      >
        <Combobox
          id="binding-agent"
          options={agents}
          value={scopeRef || null}
          onChange={(value) => setScopeRef(value ?? '')}
          placeholder={t('bindings.selectRef')}
        />
      </Field>
    )
  }

  return (
    <Field
      label={t(`bindings.ref.${scopeTree}`)}
      htmlFor={`binding-${scopeTree}`}
      description={t(`bindings.ref.${scopeTree}Hint`)}
      required
    >
      <Input
        id={`binding-${scopeTree}`}
        value={scopeRef}
        onChange={(event) => setScopeRef(event.target.value)}
        mono
      />
    </Field>
  )
}

function BindingDeleteDialog({
  binding,
  onQueued,
  onClose,
}: {
  binding: SourceScopeBindingDTO
  onQueued: (request: PostureRequestDTO) => void
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const mutation = usePrivilegedMutation<void, BindingDeleteResult>({
    mutationFn: () => consoleApi.deleteBinding(binding.id ?? ''),
    invalidateKeys: (data) => {
      const keys: QueryKey[] = [
        consoleKeys.bindings(activeTenant),
        consoleKeys.postureRequests(activeTenant),
      ]
      if (data.kind === 'posture_request' && data.posture_request.id) {
        keys.push(
          consoleKeys.postureRequest(activeTenant, data.posture_request.id),
        )
      }
      return keys
    },
    successMessage: (data) =>
      data.kind === 'posture_request'
        ? t('console:bindings.queued')
        : t('console:bindings.deleted'),
    onDone: (data) => {
      if (data.kind === 'posture_request') onQueued(data.posture_request)
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-md">
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <DialogHeader>
            <DialogTitle>{t('console:bindings.deleteTitle')}</DialogTitle>
            <DialogDescription>
              {t('console:bindings.deleteBody')}
            </DialogDescription>
          </DialogHeader>
          <div className="rounded-lg border border-border bg-muted/20 p-3 text-sm">
            <code className="font-mono text-xs">
              {binding.scope_tree}:{binding.scope_ref}
            </code>
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
              variant="destructive-solid"
              onClick={() => mutation.mutate()}
              disabled={!binding.id || mutation.isPending}
            >
              {mutation.isPending ? <Spinner size="sm" aria-hidden /> : null}
              {t('console:bindings.delete')}
            </Button>
          </DialogFooter>
        </RequireAssurance>
      </DialogContent>
    </Dialog>
  )
}

function DisableScopingDialog({
  sourceType,
  sourceRef,
  onQueued,
  onClose,
}: {
  sourceType: SourceScopeSourceType
  sourceRef: string
  onQueued: (request: PostureRequestDTO) => void
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const mutation = usePrivilegedMutation<void, PostureRequestDTO>({
    mutationFn: () =>
      consoleApi.disableScoping({
        source_type: sourceType,
        source_ref: sourceRef,
      }),
    invalidateKeys: () => [
      consoleKeys.bindings(activeTenant),
      consoleKeys.postureRequests(activeTenant),
    ],
    successMessage: t('console:bindings.queued'),
    onDone: (request) => {
      onQueued(request)
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-md">
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <DialogHeader>
            <DialogTitle>{t('console:bindings.disableTitle')}</DialogTitle>
            <DialogDescription>
              {t('console:bindings.disableBody')}
            </DialogDescription>
          </DialogHeader>
          <div className="rounded-lg border border-warning-line bg-warning-soft p-3 text-sm text-warning">
            <code className="font-mono text-xs">
              {sourceType}:{sourceRef}
            </code>
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
              variant="destructive-solid"
              onClick={() => mutation.mutate()}
              disabled={mutation.isPending}
            >
              {mutation.isPending ? <Spinner size="sm" aria-hidden /> : null}
              {t('console:bindings.disableScoping')}
            </Button>
          </DialogFooter>
        </RequireAssurance>
      </DialogContent>
    </Dialog>
  )
}

function ResolutionPreview({
  sourceType,
  sourceRef,
  bindings,
  canRead,
  onOpenAccessMap,
}: {
  sourceType: SourceScopeSourceType
  sourceRef: string
  bindings: SourceScopeBindingDTO[]
  canRead: boolean
  onOpenAccessMap: () => void
}) {
  const { t } = useTranslation('console')
  const enabled = bindings.filter((binding) => binding.enabled)
  const forbids = enabled.filter(
    (binding) => (binding.effect ?? 'allow') === 'forbid',
  )
  const allows = enabled.filter(
    (binding) => (binding.effect ?? 'allow') !== 'forbid',
  )
  const confined = allows.length > 0
  const ordered = [...forbids, ...sortByCredentialPrecedence(allows)]

  return (
    <section className="flex flex-col gap-3 rounded-lg border border-border bg-muted/20 p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 text-base font-semibold text-foreground">
            <Eye className="size-4 text-accent-text" aria-hidden />
            {t('bindings.preview.title')}
          </h2>
          <p className="max-w-3xl text-sm text-muted-foreground">
            {confined
              ? t('bindings.preview.confined', { source: sourceRef })
              : forbids.length > 0
                ? t('bindings.preview.globalWithForbids', { source: sourceRef })
                : t('bindings.preview.global', { source: sourceRef })}
          </p>
        </div>
        <Button variant="secondary" size="sm" onClick={onOpenAccessMap}>
          <Network />
          {t('bindings.preview.openAccessMap')}
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">
        {t('bindings.preview.algebra')}
      </p>
      {ordered.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t('bindings.preview.noEnabled')}
        </p>
      ) : (
        <ol className="flex flex-col gap-2">
          {ordered.map((binding) => (
            <li
              key={binding.id ?? `${binding.scope_tree}:${binding.scope_ref}`}
              className="flex flex-wrap items-center gap-2 text-sm"
            >
              <EffectBadge effect={binding.effect ?? 'allow'} />
              <Badge variant="outline">
                {t(`bindings.scopeTrees.${binding.scope_tree}`)}
              </Badge>
              <code className="font-mono text-xs text-foreground">
                {binding.scope_ref || t('bindings.defaultWorkspace')}
              </code>
              {binding.cred_name ? (
                <span className="text-xs text-muted-foreground">
                  {t('bindings.preview.credential', {
                    name: binding.cred_name,
                    hint: binding.cred_hint || binding.cred_ref_kind || '',
                  })}
                </span>
              ) : null}
            </li>
          ))}
        </ol>
      )}
      <ActorVerdict
        sourceType={sourceType}
        sourceRef={sourceRef}
        canRead={canRead}
      />
    </section>
  )
}

/** El veredicto del MOTOR para un par (actor, fuente).
 *
 *  ⛔ ES OTRA PREGUNTA QUE LA DE ARRIBA, y por eso vive aquí y no la sustituye. El álgebra
 *  de arriba la calcula la CONSOLA desde la lista de enlaces y responde a nivel de FUENTE
 *  («está confinada o es global»). Ésta se la pregunta al resolvedor REAL y responde a
 *  nivel de PAR: qué le pasaría a ESTE actor. Una inferencia de cliente puede derivar del
 *  motor; la respuesta del motor no.
 *
 *  ⛔ Y SE ETIQUETA COMO LÍNEA BASE CUANDO EL MOTOR LO DICE. Corre el resolvedor con un
 *  principal a cero a propósito y devuelve `baseline: true` «so the console can label it
 *  honestly»: pintarlo como «lo que este actor obtiene» sería afirmar de más en una
 *  pantalla de autorización. */
function ActorVerdict({
  sourceType,
  sourceRef,
  canRead,
}: {
  sourceType: SourceScopeSourceType
  sourceRef: string
  canRead: boolean
}) {
  const { t } = useTranslation('console')
  const { activeTenant } = useAuth()
  const [actorKind, setActorKind] = useState<SourceScopeActorKind>('agent')
  const [actorRef, setActorRef] = useState('')
  const params = useMemo(
    () => ({
      source_type: sourceType,
      source_ref: sourceRef.trim(),
      actor_kind: actorKind,
      actor_ref: actorRef.trim(),
    }),
    [actorKind, actorRef, sourceRef, sourceType],
  )
  const ready = params.actor_ref !== '' && params.source_ref !== ''
  const verdict = useQuery({
    queryKey: consoleKeys.sourceScopeResolve(activeTenant, params),
    queryFn: () => consoleApi.resolvePreview(params),
    enabled: canRead && ready,
  })

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border p-3">
      <h3 className="text-sm font-medium text-foreground">
        {t('bindings.preview.actor.title')}
      </h3>
      <p className="text-xs text-muted-foreground">
        {t('bindings.preview.actor.hint')}
      </p>
      <div className="flex flex-wrap items-end gap-2">
        <Select
          value={actorKind}
          onValueChange={(v) => setActorKind(v as SourceScopeActorKind)}
        >
          <SelectTrigger
            className="w-40"
            aria-label={t('bindings.preview.actor.actorKind')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="agent">agent</SelectItem>
            <SelectItem value="session">session</SelectItem>
          </SelectContent>
        </Select>
        <Input
          value={actorRef}
          onChange={(e) => setActorRef(e.target.value)}
          className="w-64"
          aria-label={t('bindings.preview.actor.actorRef')}
          placeholder={t('bindings.preview.actor.actorRef')}
        />
      </div>
      {!ready ? (
        <p className="text-xs text-muted-foreground">
          {t('bindings.preview.actor.empty')}
        </p>
      ) : verdict.data ? (
        <div className="flex flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={verdict.data.allowed ? 'success' : 'danger'}>
              {verdict.data.allowed
                ? t('bindings.preview.actor.allowed')
                : t('bindings.preview.actor.denied')}
            </Badge>
            <Badge variant="outline">
              {verdict.data.bound
                ? t('bindings.preview.actor.bound')
                : t('bindings.preview.actor.unbound')}
            </Badge>
            <span className="text-sm text-foreground">
              {verdict.data.reason}
            </span>
          </div>
          {verdict.data.cred_name ? (
            <p className="text-xs text-muted-foreground">
              {t('bindings.preview.actor.cred')}: {verdict.data.cred_name}
              {verdict.data.cred_hint ? ` (${verdict.data.cred_hint})` : ''}
            </p>
          ) : null}
          {verdict.data.baseline ? (
            <p role="note" className="text-xs text-warning">
              {t('bindings.preview.actor.baseline')}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

/**
 * The posture a request would change, rendered as CURRENT → PROPOSED.
 *
 * ⛔ TRES ESTADOS, NUNCA DOS, y aqui la tercera respuesta no es ceremonia. `acl_aware` puede
 * llegar por dos caminos que significan cosas distintas: hay una fila guardada que lo dice, o no
 * hay ninguna y el motor aplica su defecto (`guardposture.go:37-39`). Fundirlos pintaria como
 * decision explicita lo que es una ausencia — en una pantalla cuyo unico trabajo es que el
 * aprobador sepa desde donde relaja.
 *
 * Y si la consulta no ha podido mirar, se dice: un aprobador que lee «acl_aware» cuando en
 * realidad no hemos cargado nada decide con una certeza que no tiene.
 */

/**
 * El SUJETO sobre el que se decide, resuelto — o dicho que no se pudo.
 *
 * ⛔ LA COLA PINTABA UN IDENTIFICADOR OPACO AL LADO DE UNA DECISIÓN DE SEGURIDAD. Puso el
 * PERFIL (de qué a qué), y quedaba la otra mitad: SOBRE QUÉ. Un `target_id` crudo no le dice al
 * aprobador si la asignación que va a tocar es de lectura o de escritura, ni a qué espacio va, ni
 * si está siquiera activa.
 *
 * ⛔ CUATRO RESPUESTAS, y las cuatro significan cosas distintas para quien decide:
 *
 *   resuelta ......... conector → espacio, modo y estado, que es lo accionable
 *   sin permiso ...... `sourcescope:assignment:read` NO es el permiso con el que se monta esta
 *                      cola (`sourcescope:posture:admin` + `binding:read`). Un revisor puede
 *                      legítimamente no tenerlo, y entonces la pantalla dice ESO, no «no existe».
 *   no encontrada .... el id no casa con ninguna asignación: borrada, o de otra clase. Es un
 *                      HECHO, y sólo se puede afirmar sobre un listado COMPLETO.
 *   no se sabe ....... la consulta no cargó, o vino paginada y la ausencia ya no prueba nada.
 *
 * ⛔ Y LA PAGINACIÓN ES LA MISMA TRAMPA QUE: `handleListAssignments` devuelve `has_more` sin
 * drenar el cursor (`modules/sourcescope/assignment.go:109`) sobre un repositorio que pagina a 100.
 * La asimetría vuelve a decidir el arreglo: paginar sólo puede fabricar AUSENCIAS falsas, jamás
 * una asignación falsa, así que `has_more` entra SÓLO en la rama del «no encontrada».
 */
/**
 * ⛔ LAS OPERACIONES CUYO `target_id` ES UNA ASIGNACIÓN, Y SÓLO ÉSAS.
 *
 * El motor NO tipa ese campo: `toPostureRequestDTO` copia el texto de `colPRTargetID` tal cual
 * (`modules/sourcescope/posture.go:95-102,571-583`), y cada operación mete lo suyo —
 * `update`/`delete` meten un id de BINDING y lo resuelven contra el repositorio de bindings
 * (`posture.go:934-952,993-1029`), mientras `assignment_update`/`assignment_delete` lo resuelven
 * contra el de asignaciones (`posture.go:1097-1152`).
 *
 * La primera versión de esta pantalla mandaba CUALQUIER `target_id` al índice de asignaciones. Un
 * objetivo de binding se habría pintado como «ninguna asignación coincide» —falso— y una colisión
 * de ids entre clases lo habría pintado como **la asignación equivocada**, que es peor que no
 * resolver. Lo cazó el contraste Codex sol max (hallazgo ALTO, B).
 */
const OPS_DE_ASIGNACION = new Set(['assignment_update', 'assignment_delete'])

function TargetAssignment({
  op,
  targetId,
  assignment,
  canResolve,
  unknown,
  incomplete,
}: {
  op: string
  targetId: string
  assignment?: ConnectorAssignmentDTO
  canResolve: boolean
  unknown: boolean
  /** El listado vino truncado: una AUSENCIA ya no prueba que no exista. */
  incomplete: boolean
}) {
  const { t } = useTranslation(['console'])
  const id = (
    <code className="rounded bg-muted px-1 py-0.5 font-mono text-[11px]">
      {targetId}
    </code>
  )
  // ⛔ EL QUINTO ESTADO VA EL PRIMERO DE TODOS: si esta operación no apunta a una asignación,
  //    nada de lo de abajo aplica, y decir «ninguna asignación coincide» sería afirmar sobre una
  //    clase de objeto que nadie ha buscado. Se enseña el id y se dice que este resolvedor no es
  //    el suyo.
  if (!OPS_DE_ASIGNACION.has(op)) {
    return (
      <span className="text-xs">
        {id}{' '}
        <span className="text-muted-foreground">
          {t('console:bindings.posture.target.notApplicable')}
        </span>
      </span>
    )
  }
  // ⛔ `canResolve` AQUÍ NO ES REDUNDANTE CON EL `enabled` DE LA CONSULTA, y creerlo fue el
  //    defecto: `enabled` evita una llamada NUEVA, pero el consumidor sigue observando la MISMA
  //    entrada de caché, cuya clave es `['console', tenant, 'sourcescope', 'assignments']` — sin
  //    principal ni permiso. Una respuesta cacheada por alguien CON el permiso se pintaría para
  //    alguien que ya no lo tiene. Es la misma clase que el contraste me devolvió horas antes en
  //    el panel de egreso, cometida otra vez aquí: **acotar el productor no acota al
  //    consumidor cuando algo entre medias está indexado más grueso que la frontera.**
  if (assignment && canResolve) {
    return (
      <span className="text-xs">
        {id}{' '}
        <span className="text-foreground">
          {assignment.connector_name} → {assignment.workspace_ref}
        </span>{' '}
        <Badge variant={assignment.mode === 'rw' ? 'warning' : 'neutral'}>
          {assignment.mode === 'rw'
            ? t('console:bindings.posture.target.rw')
            : t('console:bindings.posture.target.ro')}
        </Badge>
        {assignment.enabled ? null : (
          <Badge variant="neutral">
            {t('console:bindings.posture.target.disabled')}
          </Badge>
        )}
      </span>
    )
  }
  // ⛔ EL ORDEN DE ESTAS TRES IMPORTA. «Sin permiso» se sabe sin mirar el listado, así que va
  //    primero; si fuera después, un revisor sin permiso leería «no encontrada», que es una
  //    afirmación sobre el estado del mundo que nadie ha comprobado.
  const nota = !canResolve
    ? t('console:bindings.posture.target.forbidden')
    : unknown || incomplete
      ? t('console:bindings.posture.target.unknown')
      : t('console:bindings.posture.target.notFound')
  return (
    <span className="text-xs">
      {id} <span className="text-muted-foreground">{nota}</span>
    </span>
  )
}

function PostureChange({
  current,
  proposed,
  unknown,
  incomplete,
}: {
  current?: string
  proposed?: string
  unknown: boolean
  /** El listado vino paginado: una AUSENCIA ya no prueba que no haya fila. */
  incomplete: boolean
}) {
  const { t } = useTranslation(['console'])
  const label = (profile: string) =>
    profile === 'public_only'
      ? t('console:bindings.posture.profiles.publicOnly')
      : t('console:bindings.posture.profiles.aclAware')

  // ⛔ `incomplete` SOLO PESA CUANDO NO HAY FILA. Con fila, el valor es autoritativo aunque el
  //    listado estuviera truncado: la paginación omite, no inventa. Meterlo en la condición
  //    general ocultaría valores buenos y sería la sobrecorrección del defecto que arregla.
  if (unknown || (!current && incomplete)) {
    return (
      <span className="text-xs text-muted-foreground">
        {t('console:bindings.posture.profileUnknown')}
      </span>
    )
  }
  const currentLabel = current
    ? label(current)
    : t('console:bindings.posture.profileDefault', {
        profile: label('acl_aware'),
      })
  return (
    <div className="flex flex-col gap-0.5 text-xs">
      <span className="text-foreground">{currentLabel}</span>
      {proposed ? (
        <span
          className={
            proposed === 'public_only'
              ? 'font-medium text-warning'
              : 'text-muted-foreground'
          }
        >
          → {label(proposed)}
        </span>
      ) : null}
    </div>
  )
}

function PostureQueue({
  canReview,
  selectedSourceRef,
}: {
  canReview: boolean
  selectedSourceRef: string
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const [status, setStatus] = useState('pending')
  const [sourceFilter, setSourceFilter] = useState('')
  const params = useMemo(
    () => ({
      status: status === STATUS_ALL ? undefined : status,
      source_ref: sourceFilter.trim() || undefined,
      // ⛔ ES UNA COLA DE APROBACIÓN: «no hay peticiones pendientes» es la frase con la que alguien
      //    se va a casa. Sin `limit` salían las cien primeras por id y el resto no existía.
      limit: CONSOLE_PAGE,
    }),
    [sourceFilter, status],
  )
  const requests = useQuery({
    queryKey: consoleKeys.postureRequests(activeTenant, params),
    queryFn: () => consoleApi.listPostureRequests(params),
  })

  // ⛔ LA POSTURA ACTUAL, AL LADO DE LA PROPUESTA. Sin esto el aprobador decide con «qué fuente»
  //    y «por qué lo dice el proponente», y nada más: medido el 2026-08-20, `guard_profile`
  //    aparecia en todo `web/src` SOLO como tipo, nunca en un `.tsx`.
  //
  //    Un doble control cuyo aprobador no ve el estado que gobierna es su forma mas debil, y aqui
  //    la direccion importa: `public_only` RELAJA y `acl_aware` APRIETA
  //    (`modules/sourcescope/api.go:105-109`). Aprobar una relajacion sin saber desde que se relaja
  //    es aprobar a ciegas justo en el sentido que abre.
  const postures = useQuery({
    queryKey: consoleKeys.guardPostures(activeTenant),
    queryFn: () => consoleApi.listGuardPostures({ limit: CONSOLE_PAGE }),
  })
  // ⛔ UNA RESPUESTA CORRECTA PUEDE SER INCOMPLETA, y ésa es la tercera cosa que este panel tiene
  //    que saber. El handler hace UNA llamada a `repo.List` y devuelve `has_more` sin drenar el
  //    cursor (`modules/sourcescope/guardposture.go:112-121`). Con más overrides que la página, una
  //    fuente que SÍ tiene postura guardada se vería como una fuente sin fila.
  //
  //    Y la asimetría es lo que hace correcto el arreglo: la paginación sólo puede fabricar
  //    AUSENCIAS falsas, jamás un valor falso. Una fila presente es autoritativa venga en la página
  //    que venga; lo único que deja de ser fiable es concluir «no hay fila». Por eso `incomplete`
  //    entra en la rama del DEFECTO y no en la del valor.
  //
  //    Lo cazó el contraste Codex sol max (hallazgo ALTO), y verificado aquí antes de adoptarlo.
  const posturesIncomplete = postures.data?.has_more === true
  // ⛔ EL PERMISO NO ES EL DE ESTA COLA, y por eso la consulta se acota. `/assignments` exige
  //    `sourcescope:assignment:read` (`modules/sourcescope/api.go:25,131`), mientras que la cola
  //    se monta con `posture:admin` + `binding:read`. En la ruta hermana pedía el MISMO
  //    permiso y por eso allí no hacía falta; aquí difiere de verdad, así que un revisor puede
  //    legítimamente no poder resolver, y la pantalla tiene que decir eso y no «no existe».
  const canResolveTarget = can('sourcescope:assignment:read')
  const assignments = useQuery({
    queryKey: consoleKeys.assignments(activeTenant),
    queryFn: () => consoleApi.listAssignments({ limit: CONSOLE_PAGE }),
    enabled: canResolveTarget,
  })
  const assignmentsIncomplete = assignments.data?.has_more === true
  const assignmentById = useMemo(() => {
    const m = new Map<string, ConnectorAssignmentDTO>()
    for (const a of assignments.data?.items ?? []) {
      if (a.id) m.set(a.id, a)
    }
    return m
  }, [assignments.data])
  // ⛔ NO ES UN Map<string, string>: la AUSENCIA de fila significa algo distinto de «no se sabe».
  //    `defaultGuardPosture` (modules/sourcescope/guardposture.go:37-39) devuelve `acl_aware`, asi
  //    que una fuente sin fila esta EFECTIVAMENTE en el perfil que aprieta. Se distinguen los tres
  //    estados —guardado, por defecto, no he podido mirar— porque fundirlos es exactamente el
  //    defecto que esta pantalla viene a corregir.
  const postureByKey = useMemo(() => {
    const m = new Map<string, string>()
    for (const p of postures.data?.items ?? []) {
      m.set(`${p.source_type}:${p.source_ref}`, p.profile)
    }
    return m
  }, [postures.data])
  const decideMutation = usePrivilegedMutation<
    { id: string; decision: 'approve' | 'reject' },
    PostureRequestDTO
  >({
    mutationFn: ({ id, decision }) =>
      decision === 'approve'
        ? consoleApi.approvePostureRequest(id)
        : consoleApi.rejectPostureRequest(id),
    invalidateKeys: () => [
      consoleKeys.postureRequests(activeTenant),
      consoleKeys.bindings(activeTenant),
    ],
    successMessage: (_data, vars) =>
      vars.decision === 'approve'
        ? t('console:bindings.posture.approved')
        : t('console:bindings.posture.rejected'),
  })

  const items = requests.data?.items ?? []

  return (
    <section className="flex flex-col gap-3">
      {requests.data?.has_more && !requests.error ? (
        <div>
          <Badge
            variant="warning"
            title={t('console:bindings.posture.truncatedHint')}
          >
            {t('console:bindings.posture.truncated', { n: items.length })}
          </Badge>
        </div>
      ) : null}
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:bindings.posture.title')}
          </h2>
          <p className="max-w-3xl text-sm text-muted-foreground">
            {t('console:bindings.posture.caption')}
          </p>
        </div>
        <div className="grid gap-2 sm:grid-cols-[10rem_16rem]">
          <Field
            label={t('console:bindings.posture.status')}
            htmlFor="posture-status"
          >
            <Select value={status} onValueChange={setStatus}>
              <SelectTrigger
                id="posture-status"
                aria-label={t('console:bindings.posture.status')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="pending">
                  {t('console:bindings.posture.statuses.pending')}
                </SelectItem>
                <SelectItem value="approved">
                  {t('console:bindings.posture.statuses.approved')}
                </SelectItem>
                <SelectItem value="rejected">
                  {t('console:bindings.posture.statuses.rejected')}
                </SelectItem>
                <SelectItem value={STATUS_ALL}>
                  {t('console:bindings.posture.statuses.all')}
                </SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('console:bindings.posture.sourceFilter')}
            htmlFor="posture-source"
          >
            <Input
              id="posture-source"
              value={sourceFilter}
              onChange={(event) => setSourceFilter(event.target.value)}
              placeholder={
                selectedSourceRef ||
                t('console:bindings.posture.sourcePlaceholder')
              }
              mono
            />
          </Field>
        </div>
      </div>

      {requests.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : requests.isError ? (
        <ErrorState retry={() => void requests.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState
          icon={<ShieldCheck />}
          title={t('console:bindings.posture.none')}
          description={t('console:bindings.posture.noneHint')}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:bindings.posture.target')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:bindings.posture.profile')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:bindings.posture.reason')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:bindings.posture.proposer')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:bindings.posture.status')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {items.map((request) => (
                <tr
                  key={request.id}
                  className="border-t border-border align-top"
                >
                  <td className="px-3 py-2">
                    <div className="flex flex-col gap-1">
                      <code className="font-mono text-xs text-foreground">
                        {request.source_type}:{request.source_ref}
                      </code>
                      <span className="text-xs text-muted-foreground">
                        {request.op}
                      </span>
                      {request.target_id ? (
                        <TargetAssignment
                          op={request.op}
                          targetId={request.target_id}
                          assignment={assignmentById.get(request.target_id)}
                          canResolve={canResolveTarget}
                          unknown={assignments.isPending || assignments.isError}
                          incomplete={assignmentsIncomplete}
                        />
                      ) : null}
                    </div>
                  </td>
                  <td className="px-3 py-2">
                    <PostureChange
                      current={postureByKey.get(
                        `${request.source_type}:${request.source_ref}`,
                      )}
                      proposed={request.guard_profile}
                      unknown={postures.isPending || postures.isError}
                      incomplete={posturesIncomplete}
                    />
                  </td>
                  <td className="max-w-xs px-3 py-2 text-muted-foreground">
                    {request.reason || '—'}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-foreground">
                    {request.proposer || '—'}
                  </td>
                  <td className="px-3 py-2">
                    <Badge
                      variant={
                        request.status === 'pending'
                          ? 'warning'
                          : request.status === 'approved'
                            ? 'success'
                            : 'neutral'
                      }
                    >
                      {t(
                        `console:bindings.posture.statuses.${request.status}`,
                        {
                          defaultValue: request.status,
                        },
                      )}
                    </Badge>
                  </td>
                  <td className="px-3 py-2 text-right">
                    {canReview && request.status === 'pending' && request.id ? (
                      <RequireAssurance minAal={AAL.HARDWARE} action="console">
                        <div className="flex justify-end gap-1">
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() =>
                              decideMutation.mutate({
                                id: request.id!,
                                decision: 'reject',
                              })
                            }
                            disabled={decideMutation.isPending}
                          >
                            {t('console:bindings.posture.reject')}
                          </Button>
                          <Button
                            size="sm"
                            variant="primary"
                            onClick={() =>
                              decideMutation.mutate({
                                id: request.id!,
                                decision: 'approve',
                              })
                            }
                            disabled={decideMutation.isPending}
                          >
                            {t('console:bindings.posture.approve')}
                          </Button>
                        </div>
                      </RequireAssurance>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function QueuedNotice({ request }: { request: PostureRequestDTO }) {
  const { t } = useTranslation('console')
  return (
    <div
      role="status"
      className="rounded-lg border border-warning-line bg-warning-soft p-3 text-sm text-warning"
    >
      <div className="font-medium">{t('bindings.queuedTitle')}</div>
      <div>
        {t('bindings.queuedBody', {
          op: request.op,
          source: `${request.source_type}:${request.source_ref}`,
        })}
      </div>
    </div>
  )
}

function EffectBadge({ effect }: { effect: BindingEffect }) {
  const { t } = useTranslation('console')
  return (
    <Badge variant={effect === 'forbid' ? 'danger' : 'success'}>
      {t(`bindings.effects.${effect}`)}
    </Badge>
  )
}

function sortByCredentialPrecedence(bindings: SourceScopeBindingDTO[]) {
  const rank = new Map(
    CREDENTIAL_PRECEDENCE.map((tree, index) => [tree, index]),
  )
  return [...bindings].sort(
    (a, b) => (rank.get(a.scope_tree) ?? 99) - (rank.get(b.scope_tree) ?? 99),
  )
}

function buildRoleOptions(
  catalog: RBACCatalogDTO | undefined,
  roles: CustomRoleDTO[],
  t: (key: string, opts?: Record<string, unknown>) => string,
): ComboboxOption[] {
  const builtin = (catalog?.builtin_roles ?? []).map((role) => ({
    value: role,
    label: `${role} · ${t('console:bindings.roleBuiltin')}`,
    keywords: [role],
  }))
  const custom = roles.map((role) => ({
    value: role.name,
    label: `${role.display_name || role.name} · ${t('console:bindings.roleCustom')}`,
    keywords: [role.name],
  }))
  return [...builtin, ...custom]
}

function buildAgentOptions(agents: AgentDTO[]): ComboboxOption[] {
  return agents
    .filter((agent) => !!agent.external_id)
    .map((agent) => ({
      value: agent.external_id!,
      label: `${agent.name} · ${agent.external_id}`,
      keywords: [agent.id, agent.kind, agent.status],
    }))
}

function containsInlineCredential(value: string): boolean {
  const low = value.toLowerCase()
  if (low.includes('://')) {
    const rest = low.slice(low.indexOf('://') + 3)
    const at = rest.indexOf('@')
    if (at >= 0 && rest.slice(0, at).includes(':')) return true
  }
  return [
    'token=',
    'secret=',
    'password=',
    'passwd=',
    'api_key=',
    'apikey=',
    'access_key=',
    'client_secret=',
  ].some((keyword) => low.includes(keyword))
}
