// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Owned models tab — the governed own-model registry (the aggregate root). The table
// lists owned models; a row opens a right-side drawer with the model overview, its
// VERSIONS (genuine children: create/delete + the canonical per-version Admit), and the
// evidence for the ONE selected version (never a verdict fetch per row). Deployments,
// datasets and fine-tune jobs are NOT nested here — they have independent lifecycles and
// live in their own top-level tabs.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
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
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from '@/features/models/api'
import type {
  ModelVersion,
  ModelVersionInput,
  OwnedModel,
  OwnedModelInput,
  OwnedModelKind,
  OwnedModelStatus,
  OwnedModelVisibility,
} from '@/features/models/types'
import { AdmitDialog, VersionEvidence } from './shared'
import { ModelDocuments } from './documents'

const KINDS: OwnedModelKind[] = ['hosted', 'fine_tuned', 'imported']
const VISIBILITIES: OwnedModelVisibility[] = ['private', 'internal']
const MODEL_STATUSES: OwnedModelStatus[] = ['active', 'deprecated', 'draft']
const VERSION_STATUSES = ['draft', 'active', 'deprecated']

export function OwnedModelsTab() {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant, can } = useAuth()
  const canWrite = can('models:registry:write')

  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<OwnedModel | null>(null)
  const [deleting, setDeleting] = useState<OwnedModel | null>(null)
  const [selected, setSelected] = useState<OwnedModel | null>(null)

  // ⛔ EL TECHO SE PIDE Y EL RECORTE SE DICE. `handleListOwnedModels` publica `has_more` y sin
  //    `limit` el repositorio genérico pagina a 100: el parque se leía «éstos son nuestros
  //    modelos», que es la frase con la que alguien concluye que uno no está registrado.
  const params = { limit: EVIDENCE_PAGE }
  const query = useQuery({
    queryKey: modelsKeys.ownedModels(activeTenant, params),
    queryFn: () => modelsApi.ownedModels(params),
  })

  const columns = useMemo<TableColumn<OwnedModel>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('owned.columns.name'),
        cell: ({ row }) => (
          <span className="font-medium">{row.original.name}</span>
        ),
      },
      {
        accessorKey: 'kind',
        header: t('owned.columns.kind'),
        cell: ({ row }) => (
          <Badge variant="neutral" className="font-mono text-[11px]">
            {row.original.kind}
          </Badge>
        ),
      },
      {
        accessorKey: 'provider_ref',
        header: t('owned.columns.provider'),
        cell: ({ row }) =>
          row.original.provider_ref ? (
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.provider_ref}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'visibility',
        header: t('owned.columns.visibility'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.visibility}
          </span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('owned.columns.status'),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
    ],
    [t],
  )

  const deleteMutation = useMutation({
    mutationFn: (id: string) => modelsApi.deleteOwnedModel(id),
    onSuccess: async () => {
      await query.refetch()
      toast.success(t('owned.deleted'))
      setDeleting(null)
      setSelected(null)
    },
    onError: (err) => {
      report(err)
    },
  })

  return (
    <SectionCard
      title={t('owned.title')}
      description={t('owned.description')}
      actions={
        canWrite ? (
          <Button
            variant="primary"
            size="sm"
            onClick={() => {
              setEditing(null)
              setEditorOpen(true)
            }}
          >
            <Plus />
            {t('owned.new')}
          </Button>
        ) : null
      }
      noPadding
    >
      {/* Fuera del bloque de datos: un refetch fallido no debe dejar el aviso flotando. */}
      <ListTruncationBadge
        query={query}
        label={t('owned.truncated', { n: query.data?.items?.length ?? 0 })}
        hint={t('owned.truncatedHint')}
        className="px-3 pt-3"
      />

      <DataTable<OwnedModel>
        label={t('owned.title')}
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        onRowClick={(r) => setSelected(r)}
        searchable
        searchPlaceholder={t('owned.search')}
        empty={
          <EmptyState
            title={t('empty.ownedModels.title')}
            description={t('empty.ownedModels.description')}
          />
        }
      />

      {canWrite && (
        <OwnedModelDialog
          key={editing?.id ?? 'new'}
          model={editing}
          open={editorOpen}
          onOpenChange={setEditorOpen}
          onSaved={() => void query.refetch()}
        />
      )}

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t('owned.deleteTitle')}
        description={t('owned.deleteBody', { name: deleting?.name ?? '' })}
        confirmLabel={t('common:actions.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />

      <OwnedModelDrawer
        model={selected}
        canWrite={canWrite}
        canAdmit={can('models:admission:write')}
        onClose={() => setSelected(null)}
        onEdit={(m) => {
          setEditing(m)
          setEditorOpen(true)
        }}
        onDelete={(m) => setDeleting(m)}
      />
    </SectionCard>
  )
}

function OwnedModelDrawer({
  model,
  canWrite,
  canAdmit,
  onClose,
  onEdit,
  onDelete,
}: {
  model: OwnedModel | null
  canWrite: boolean
  canAdmit: boolean
  onClose: () => void
  onEdit: (m: OwnedModel) => void
  onDelete: (m: OwnedModel) => void
}) {
  const { t } = useTranslation(['model-ops', 'common'])

  return (
    <Sheet open={!!model} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="flex w-full flex-col gap-0 sm:max-w-xl">
        {model && (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2">
                {model.name}
                <StatusBadge status={model.status} />
              </SheetTitle>
              <SheetDescription>{t('owned.drawer.subtitle')}</SheetDescription>
            </SheetHeader>
            <ScrollArea className="flex-1">
              <div className="flex flex-col gap-5 px-1 py-2">
                <div className="flex flex-col gap-3">
                  {canWrite && (
                    <div className="flex gap-2">
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => onEdit(model)}
                      >
                        {t('common:actions.edit')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onDelete(model)}
                      >
                        <Trash2 />
                        {t('common:actions.delete')}
                      </Button>
                    </div>
                  )}
                  <KvList>
                    <KvRow label={t('owned.columns.kind')} mono>
                      {model.kind}
                    </KvRow>
                    {model.base_ref && (
                      <KvRow label={t('owned.form.baseRef')} mono>
                        {model.base_ref}
                      </KvRow>
                    )}
                    {model.provider_ref && (
                      <KvRow label={t('owned.columns.provider')} mono>
                        {model.provider_ref}
                      </KvRow>
                    )}
                    <KvRow label={t('owned.columns.visibility')}>
                      {model.visibility}
                    </KvRow>
                    {model.owner_ref && (
                      <KvRow label={t('owned.form.ownerRef')} mono>
                        {model.owner_ref}
                      </KvRow>
                    )}
                    {model.note && (
                      <KvRow label={t('owned.form.note')} align="start">
                        {model.note}
                      </KvRow>
                    )}
                  </KvList>
                </div>
                <Separator />
                <VersionsSection
                  ownedRef={model.id}
                  canWrite={canWrite}
                  canAdmit={canAdmit}
                />
                <Separator />
                <ModelDocuments
                  ownedRef={model.id}
                  ownedName={model.name}
                  canWrite={canWrite}
                />
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

function VersionsSection({
  ownedRef,
  canWrite,
  canAdmit,
}: {
  ownedRef: string
  canWrite: boolean
  canAdmit: boolean
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const [createOpen, setCreateOpen] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState<ModelVersion | null>(
    null,
  )
  const [admitting, setAdmitting] = useState<ModelVersion | null>(null)
  const [deleting, setDeleting] = useState<ModelVersion | null>(null)

  // Mismo techo: una versión que no se vea bloquea la navegación hacia SU evidencia de
  // admisión, así que el silencio aquí se propaga al plano de evidencia entero.
  const versionParams = { owned_ref: ownedRef, limit: EVIDENCE_PAGE }
  const query = useQuery({
    queryKey: modelsKeys.modelVersions(activeTenant, ownedRef, versionParams),
    queryFn: () => modelsApi.modelVersions(versionParams),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => modelsApi.deleteModelVersion(id),
    onSuccess: async () => {
      await query.refetch()
      toast.success(t('versions.deleted'))
      setDeleting(null)
      setSelectedVersion(null)
    },
    onError: (err) => {
      report(err)
    },
  })

  const versions = query.data?.items ?? []

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('versions.title')}
        </h3>
        {canWrite && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setCreateOpen(true)}
          >
            <Plus />
            {t('versions.new')}
          </Button>
        )}
      </div>

      {/* Fuera del bloque de datos: un refetch fallido no debe dejar el aviso flotando. */}
      <ListTruncationBadge
        query={query}
        label={t('versions.truncated', { n: versions.length })}
        hint={t('versions.truncatedHint')}
      />

      {query.isLoading ? (
        <p className="text-xs text-muted-foreground">
          {t('common:states.loading')}
        </p>
      ) : query.error ? (
        // ⛔ UNA LECTURA FALLIDA NO ES «NO HAY VERSIONES». Sin esta rama, un 500 caía al estado
        //    vacío de abajo y el panel afirmaba una ausencia que nadie comprobó — y con dato
        //    viejo en caché quedaba una lista vieja y recortada sin marca, porque el aviso se
        //    oculta con `!error`. Es el mismo defecto que el contraste devolvió en el panel de
        //    documentos, en la misma feature y con la misma forma.
        <ErrorState
          title={t('versions.loadError')}
          retry={() => void query.refetch()}
        />
      ) : versions.length === 0 ? (
        <EmptyState
          title={t('versions.empty')}
          description={t('versions.emptyHint')}
        />
      ) : (
        <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
          {versions.map((v) => {
            const active = selectedVersion?.id === v.id
            return (
              <li key={v.id} className="flex flex-col">
                <div className="flex items-center justify-between gap-2 px-3 py-2">
                  <button
                    type="button"
                    onClick={() => setSelectedVersion(active ? null : v)}
                    className="flex flex-1 items-center gap-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    aria-expanded={active}
                  >
                    <span className="font-mono text-xs">{v.version}</span>
                    <StatusBadge status={v.status} />
                  </button>
                  <div className="flex items-center gap-1">
                    {canAdmit && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setAdmitting(v)}
                      >
                        <ShieldCheck />
                        {t('versions.admit')}
                      </Button>
                    )}
                    {canWrite && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t('common:actions.delete')}
                        onClick={() => setDeleting(v)}
                      >
                        <Trash2 />
                      </Button>
                    )}
                  </div>
                </div>
                {active && (
                  <div className="border-t border-border bg-muted/30 px-3 py-3">
                    <VersionEvidence versionRef={v.id} />
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}

      {canWrite && (
        <CreateVersionDialog
          ownedRef={ownedRef}
          open={createOpen}
          onOpenChange={setCreateOpen}
          onSaved={() => void query.refetch()}
        />
      )}

      {admitting && (
        <AdmitDialog
          versionId={admitting.id}
          versionLabel={admitting.version}
          open={!!admitting}
          onOpenChange={(o) => !o && setAdmitting(null)}
        />
      )}

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t('versions.deleteTitle')}
        description={t('versions.deleteBody', {
          version: deleting?.version ?? '',
        })}
        confirmLabel={t('common:actions.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </section>
  )
}

function OwnedModelDialog({
  model,
  open,
  onOpenChange,
  onSaved,
}: {
  model: OwnedModel | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-xl overflow-y-auto">
        {open && (
          <OwnedModelForm
            model={model}
            onClose={() => onOpenChange(false)}
            onSaved={onSaved}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function OwnedModelForm({
  model,
  onClose,
  onSaved,
}: {
  model: OwnedModel | null
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const editing = !!model

  const [name, setName] = useState(model?.name ?? '')
  const [kind, setKind] = useState<OwnedModelKind>(model?.kind ?? 'hosted')
  const [baseRef, setBaseRef] = useState(model?.base_ref ?? '')
  const [providerRef, setProviderRef] = useState(model?.provider_ref ?? '')
  const [visibility, setVisibility] = useState<OwnedModelVisibility>(
    model?.visibility ?? 'private',
  )
  const [status, setStatus] = useState<OwnedModelStatus>(
    model?.status ?? 'active',
  )
  const [ownerRef, setOwnerRef] = useState(model?.owner_ref ?? '')
  const [note, setNote] = useState(model?.note ?? '')

  const valid = name.trim() !== ''

  const mutation = useMutation({
    mutationFn: (body: OwnedModelInput) =>
      editing
        ? modelsApi.updateOwnedModel(model.id, body)
        : modelsApi.createOwnedModel(body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ['models', activeTenant, 'owned-models'],
      })
      toast.success(editing ? t('owned.updated') : t('owned.created'))
      onSaved()
      onClose()
    },
    onError: (err) => {
      report(err)
    },
  })

  function submit() {
    if (!valid) return
    const body: OwnedModelInput = {
      name: name.trim(),
      kind,
      visibility,
      status,
      ...(baseRef.trim() ? { base_ref: baseRef.trim() } : {}),
      ...(providerRef.trim() ? { provider_ref: providerRef.trim() } : {}),
      ...(ownerRef.trim() ? { owner_ref: ownerRef.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(body)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {editing ? t('owned.editTitle') : t('owned.newTitle')}
        </DialogTitle>
        <DialogDescription>{t('owned.formBody')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field label={t('owned.form.name')} htmlFor="om-name" required>
          <Input
            id="om-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('owned.form.kind')} htmlFor="om-kind">
            <Select
              value={kind}
              onValueChange={(v) => setKind(v as OwnedModelKind)}
            >
              <SelectTrigger id="om-kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {k}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('owned.form.status')} htmlFor="om-status">
            <Select
              value={status}
              onValueChange={(v) => setStatus(v as OwnedModelStatus)}
            >
              <SelectTrigger id="om-status">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {MODEL_STATUSES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('owned.form.baseRef')} htmlFor="om-base">
            <Input
              id="om-base"
              value={baseRef}
              onChange={(e) => setBaseRef(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('owned.form.providerRef')} htmlFor="om-provider">
            <Input
              id="om-provider"
              value={providerRef}
              onChange={(e) => setProviderRef(e.target.value)}
              mono
            />
          </Field>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('owned.form.visibility')} htmlFor="om-visibility">
            <Select
              value={visibility}
              onValueChange={(v) => setVisibility(v as OwnedModelVisibility)}
            >
              <SelectTrigger id="om-visibility">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {VISIBILITIES.map((v) => (
                  <SelectItem key={v} value={v}>
                    {v}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('owned.form.ownerRef')} htmlFor="om-owner">
            <Input
              id="om-owner"
              value={ownerRef}
              onChange={(e) => setOwnerRef(e.target.value)}
              mono
            />
          </Field>
        </div>
        <Field label={t('owned.form.note')} htmlFor="om-note">
          <Textarea
            id="om-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
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
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {editing ? t('common:actions.save') : t('common:actions.create')}
        </Button>
      </DialogFooter>
    </>
  )
}

function CreateVersionDialog({
  ownedRef,
  open,
  onOpenChange,
  onSaved,
}: {
  ownedRef: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
        {open && (
          <CreateVersionForm
            ownedRef={ownedRef}
            onClose={() => onOpenChange(false)}
            onSaved={onSaved}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function CreateVersionForm({
  ownedRef,
  onClose,
  onSaved,
}: {
  ownedRef: string
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [version, setVersion] = useState('')
  const [artifactRef, setArtifactRef] = useState('')
  const [status, setStatus] = useState('draft')
  const [parentRef, setParentRef] = useState('')
  const [sourceRef, setSourceRef] = useState('')
  const [note, setNote] = useState('')

  const valid = version.trim() !== ''

  const mutation = useMutation({
    mutationFn: (body: ModelVersionInput) => modelsApi.createModelVersion(body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: modelsKeys.modelVersions(activeTenant, ownedRef),
      })
      toast.success(t('versions.created'))
      onSaved()
      onClose()
    },
    onError: (err) => {
      report(err)
    },
  })

  function submit() {
    if (!valid) return
    const body: ModelVersionInput = {
      owned_ref: ownedRef,
      version: version.trim(),
      status,
      ...(artifactRef.trim() ? { artifact_ref: artifactRef.trim() } : {}),
      ...(parentRef.trim() ? { parent_ref: parentRef.trim() } : {}),
      ...(sourceRef.trim() ? { source_ref: sourceRef.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(body)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('versions.newTitle')}</DialogTitle>
        <DialogDescription>{t('versions.formBody')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field
            label={t('versions.form.version')}
            htmlFor="ver-version"
            required
          >
            <Input
              id="ver-version"
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('versions.form.status')} htmlFor="ver-status">
            <Select value={status} onValueChange={setStatus}>
              <SelectTrigger id="ver-status">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {VERSION_STATUSES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
        <Field label={t('versions.form.artifactRef')} htmlFor="ver-artifact">
          <Input
            id="ver-artifact"
            value={artifactRef}
            onChange={(e) => setArtifactRef(e.target.value)}
            mono
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('versions.form.parentRef')} htmlFor="ver-parent">
            <Input
              id="ver-parent"
              value={parentRef}
              onChange={(e) => setParentRef(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('versions.form.sourceRef')} htmlFor="ver-source">
            <Input
              id="ver-source"
              value={sourceRef}
              onChange={(e) => setSourceRef(e.target.value)}
              mono
            />
          </Field>
        </div>
        <Field label={t('versions.form.note')} htmlFor="ver-note">
          <Textarea
            id="ver-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
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
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('common:actions.create')}
        </Button>
      </DialogFooter>
    </>
  )
}
