// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deployments tab — the flat, cross-model inventory of local inference deployments.
// It is a TOP-LEVEL tab (not nested in the model drawer) because the backend offers no
// owned_ref filter and datasets/jobs already showed the drawer cannot own every
// lifecycle. Create/update run through the deny-closed admission gate: when the tenant
// enforces signed models, a deployment referencing an un-admitted or anchor-rotated
// version is refused with HTTP 422 — surfaced inline as a security decision (the form
// is retained), never a silent failure.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus, Trash2 } from 'lucide-react'
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
import { toast } from '@/components/ui/toaster'
import { EmptyState } from '@/components/ui/empty-state'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from '@/features/models/api'
import type {
  DeploymentRuntime,
  DeploymentStatus,
  DeploymentType,
  InferenceDeployment,
  InferenceDeploymentInput,
} from '@/features/models/types'

const RUNTIMES: DeploymentRuntime[] = ['vllm', 'ollama', 'llamacpp', 'other']
const STATUSES: DeploymentStatus[] = ['active', 'stopped']
// The console only ever WRITES these two — `unclassified` is a read-only migration state
// the operator resolves by picking one of these (D-08: an explicit discriminator, never
// "empty refs", decides whether signed-model admission applies).
const DEPLOYMENT_TYPES: DeploymentType[] = ['local', 'brokered']
const ALL = '__all__'

export function DeploymentsTab() {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant, can } = useAuth()
  const canWrite = can('models:registry:write')

  const [runtime, setRuntime] = useState<string>(ALL)
  const [status, setStatus] = useState<string>(ALL)
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<InferenceDeployment | null>(null)
  const [deleting, setDeleting] = useState<InferenceDeployment | null>(null)

  const filters = useMemo(() => {
    const f: Record<string, string> = {}
    if (runtime !== ALL) f.runtime = runtime
    if (status !== ALL) f.status = status
    return f
  }, [runtime, status])

  // ⛔ EL TECHO VA CON LOS FILTROS, no en vez de ellos: el motor aplica `runtime`/`status` antes
  //    de paginar, así que el recorte es de lo YA filtrado. Ocultar un despliegue activo cambia
  //    la lectura del runtime y la del gate de admisión.
  const params = { ...filters, limit: EVIDENCE_PAGE }
  const query = useQuery({
    queryKey: modelsKeys.deployments(activeTenant, params),
    queryFn: () => modelsApi.deployments(params),
  })

  const columns = useMemo<TableColumn<InferenceDeployment>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('deployments.columns.name'),
        cell: ({ row }) => (
          <span className="font-medium">{row.original.name}</span>
        ),
      },
      {
        accessorKey: 'runtime',
        header: t('deployments.columns.runtime'),
        cell: ({ row }) => (
          <Badge variant="neutral" className="font-mono text-[11px]">
            {row.original.runtime}
          </Badge>
        ),
      },
      {
        accessorKey: 'version_ref',
        header: t('deployments.columns.version'),
        cell: ({ row }) =>
          row.original.version_ref ? (
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.version_ref}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'governed',
        header: t('deployments.columns.governed'),
        cell: ({ row }) =>
          row.original.governed ? (
            <Badge variant="accent">{t('deployments.governed')}</Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'status',
        header: t('deployments.columns.status'),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
      ...(canWrite
        ? [
            {
              id: 'actions',
              header: '',
              cell: ({ row }: { row: { original: InferenceDeployment } }) => (
                <div className="flex justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t('common:actions.edit')}
                    onClick={() => {
                      setEditing(row.original)
                      setEditorOpen(true)
                    }}
                  >
                    <Pencil />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t('common:actions.delete')}
                    onClick={() => setDeleting(row.original)}
                  >
                    <Trash2 />
                  </Button>
                </div>
              ),
            },
          ]
        : []),
    ],
    [t, canWrite],
  )

  const deleteMutation = useMutation({
    mutationFn: (id: string) => modelsApi.deleteDeployment(id),
    onSuccess: async () => {
      await query.refetch()
      toast.success(t('deployments.deleted'))
      setDeleting(null)
    },
    onError: (err) => {
      report(err)
    },
  })

  return (
    <SectionCard
      title={t('deployments.title')}
      description={t('deployments.description')}
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
            {t('deployments.new')}
          </Button>
        ) : null
      }
      noPadding
    >
      {/* Fuera del bloque de datos: un refetch fallido no debe dejar el aviso flotando. */}
      <ListTruncationBadge
        query={query}
        label={t('deployments.truncated', {
          n: query.data?.items?.length ?? 0,
        })}
        hint={t('deployments.truncatedHint')}
        className="px-3 pt-3"
      />

      <DataTable<InferenceDeployment>
        label={t('deployments.title')}
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        searchable
        searchPlaceholder={t('deployments.search')}
        toolbar={
          <div className="flex items-center gap-2">
            <FilterSelect
              value={runtime}
              onChange={setRuntime}
              allLabel={t('deployments.filters.allRuntimes')}
              options={RUNTIMES}
            />
            <FilterSelect
              value={status}
              onChange={setStatus}
              allLabel={t('deployments.filters.allStatuses')}
              options={STATUSES}
            />
          </div>
        }
        empty={
          <EmptyState
            title={t('empty.deployments.title')}
            description={t('empty.deployments.description')}
          />
        }
      />

      {canWrite && (
        <DeploymentDialog
          key={editing?.id ?? 'new'}
          deployment={editing}
          open={editorOpen}
          onOpenChange={setEditorOpen}
          onSaved={() => void query.refetch()}
        />
      )}

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t('deployments.deleteTitle')}
        description={t('deployments.deleteBody', {
          name: deleting?.name ?? '',
        })}
        confirmLabel={t('common:actions.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </SectionCard>
  )
}

function FilterSelect({
  value,
  onChange,
  allLabel,
  options,
}: {
  value: string
  onChange: (v: string) => void
  allLabel: string
  options: string[]
}) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger className="h-8 w-[9.5rem] text-xs">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>{allLabel}</SelectItem>
        {options.map((o) => (
          <SelectItem key={o} value={o}>
            {o}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function DeploymentDialog({
  deployment,
  open,
  onOpenChange,
  onSaved,
}: {
  deployment: InferenceDeployment | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-xl overflow-y-auto">
        {open && (
          <DeploymentForm
            deployment={deployment}
            onClose={() => onOpenChange(false)}
            onSaved={onSaved}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DeploymentForm({
  deployment,
  onClose,
  onSaved,
}: {
  deployment: InferenceDeployment | null
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const editing = !!deployment

  const [name, setName] = useState(deployment?.name ?? '')
  const [runtime, setRuntime] = useState<DeploymentRuntime>(
    deployment?.runtime ?? 'vllm',
  )
  // No silent default: an unclassified/absent existing type resolves to `local` so the
  // operator must classify it (owned+version refs) before saving — never a gate-skip.
  const [deploymentType, setDeploymentType] = useState<DeploymentType>(
    deployment?.deployment_type === 'brokered' ? 'brokered' : 'local',
  )
  const [endpointRef, setEndpointRef] = useState(deployment?.endpoint_ref ?? '')
  const [ownedRef, setOwnedRef] = useState(deployment?.owned_ref ?? '')
  const [versionRef, setVersionRef] = useState(deployment?.version_ref ?? '')
  const [status, setStatus] = useState<DeploymentStatus>(
    deployment?.status ?? 'active',
  )
  const [governed, setGoverned] = useState(deployment?.governed ?? false)
  const [note, setNote] = useState(deployment?.note ?? '')
  // The deny-closed admission gate's HTTP 422 reason — a security decision shown inline,
  // with the form retained so the operator can adjust the version_ref and retry.
  const [denyReason, setDenyReason] = useState<string | null>(null)

  const isLocal = deploymentType === 'local'
  // A local deployment must name its owned model + version (admission is checked on both);
  // a brokered one must name its provider endpoint. Neither can be created "empty".
  const valid =
    name.trim() !== '' &&
    (isLocal
      ? ownedRef.trim() !== '' && versionRef.trim() !== ''
      : endpointRef.trim() !== '')

  const mutation = useMutation({
    mutationFn: (body: InferenceDeploymentInput) =>
      editing
        ? modelsApi.updateDeployment(deployment.id, body)
        : modelsApi.createDeployment(body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ['models', activeTenant, 'inference-deployments'],
      })
      toast.success(
        editing ? t('deployments.updated') : t('deployments.created'),
      )
      onSaved()
      onClose()
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 422) {
        // Admission gate refusal — keep the form, show the reason verbatim.
        setDenyReason(err.message)
        return
      }
      report(err)
    },
  })

  function submit() {
    if (!valid) return
    setDenyReason(null)
    // Send a discriminated body: a local deployment carries its owned+version refs (the
    // server checks membership + signed admission); a brokered one carries ONLY its
    // provider endpoint (the server rejects self-hosted refs on brokered).
    const base = {
      name: name.trim(),
      runtime,
      deployment_type: deploymentType,
      status,
      governed,
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    const body: InferenceDeploymentInput = isLocal
      ? {
          ...base,
          owned_ref: ownedRef.trim(),
          version_ref: versionRef.trim(),
          ...(endpointRef.trim() ? { endpoint_ref: endpointRef.trim() } : {}),
        }
      : { ...base, endpoint_ref: endpointRef.trim() }
    mutation.mutate(body)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {editing ? t('deployments.editTitle') : t('deployments.newTitle')}
        </DialogTitle>
        <DialogDescription>{t('deployments.formBody')}</DialogDescription>
      </DialogHeader>

      {denyReason && (
        <div className="rounded-md border border-danger bg-danger-soft px-3 py-2">
          <p className="text-[11px] font-medium uppercase tracking-wide text-danger">
            {t('deployments.deniedTitle')}
          </p>
          <p className="mt-0.5 whitespace-pre-wrap text-xs text-foreground">
            {denyReason}
          </p>
          <p className="mt-1 text-[11px] text-muted-foreground">
            {t('deployments.deniedHint')}
          </p>
        </div>
      )}

      <div className="flex flex-col gap-4">
        <Field label={t('deployments.form.name')} htmlFor="dep-name" required>
          <Input
            id="dep-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field
          label={t('deployments.form.deploymentType')}
          htmlFor="dep-type"
          description={t('deployments.form.deploymentTypeHint')}
          required
        >
          <Select
            value={deploymentType}
            onValueChange={(v) => setDeploymentType(v as DeploymentType)}
          >
            <SelectTrigger id="dep-type">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {DEPLOYMENT_TYPES.map((dt) => (
                <SelectItem key={dt} value={dt}>
                  {t(`deployments.types.${dt}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('deployments.form.runtime')} htmlFor="dep-runtime">
            <Select
              value={runtime}
              onValueChange={(v) => setRuntime(v as DeploymentRuntime)}
            >
              <SelectTrigger id="dep-runtime">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {RUNTIMES.map((r) => (
                  <SelectItem key={r} value={r}>
                    {r}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('deployments.form.status')} htmlFor="dep-status">
            <Select
              value={status}
              onValueChange={(v) => setStatus(v as DeploymentStatus)}
            >
              <SelectTrigger id="dep-status">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STATUSES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
        {isLocal && (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field
              label={t('deployments.form.ownedRef')}
              htmlFor="dep-owned"
              required
            >
              <Input
                id="dep-owned"
                value={ownedRef}
                onChange={(e) => setOwnedRef(e.target.value)}
                mono
              />
            </Field>
            <Field
              label={t('deployments.form.versionRef')}
              htmlFor="dep-version"
              required
            >
              <Input
                id="dep-version"
                value={versionRef}
                onChange={(e) => setVersionRef(e.target.value)}
                mono
              />
            </Field>
          </div>
        )}
        <Field
          label={
            isLocal
              ? t('deployments.form.endpointRef')
              : t('deployments.form.providerRef')
          }
          htmlFor="dep-endpoint"
          description={isLocal ? undefined : t('deployments.form.providerHint')}
          required={!isLocal}
        >
          <Input
            id="dep-endpoint"
            value={endpointRef}
            onChange={(e) => setEndpointRef(e.target.value)}
            mono
          />
        </Field>
        <Field
          label={t('deployments.form.governed')}
          htmlFor="dep-governed"
          description={t('deployments.form.governedHint')}
        >
          <Switch
            id="dep-governed"
            checked={governed}
            onCheckedChange={setGoverned}
          />
        </Field>
        <Field label={t('deployments.form.note')} htmlFor="dep-note">
          <Textarea
            id="dep-note"
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
