// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Datasets tab — the AIBOM lineage components. A dataset is a MINIMAL-DATA record:
// a name + an optional content reference + hash + a classification/governance label —
// never the dataset contents. It is TENANT-WIDE (not owned-scoped); `owned_ref` is an
// optional lineage pointer to an owned model, validated deny-closed on create (a
// non-existent ref is a 404, shown inline). `verified` is an OPERATOR CLAIM of provenance,
// NOT a cryptographic result — the UI labels it as such and never as system-proven.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { ListTruncationBadge, HashChip } from '@/features/_intel'
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { EmptyState } from '@/components/ui/empty-state'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import { SectionCard } from '@/features/_intel'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from '@/features/models/api'
import type {
  Dataset,
  DatasetClassification,
  DatasetInput,
} from '@/features/models/types'

const CLASSIFICATIONS: DatasetClassification[] = [
  'public',
  'internal',
  'confidential',
  'restricted',
  'pii',
  'other',
]

/** classification → badge tone. More sensitive classes read hotter; the mapping never
 *  loosens (pii/restricted are danger, confidential warning, internal accent). */
function classificationVariant(
  c: string,
): 'danger' | 'warning' | 'accent' | 'neutral' {
  if (c === 'pii' || c === 'restricted') return 'danger'
  if (c === 'confidential') return 'warning'
  if (c === 'internal') return 'accent'
  return 'neutral'
}

export function DatasetsTab() {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant, can } = useAuth()
  const canWrite = can('models:registry:write')

  const [createOpen, setCreateOpen] = useState(false)
  const [selected, setSelected] = useState<Dataset | null>(null)
  const [deleting, setDeleting] = useState<Dataset | null>(null)

  // The backend list filters only by owned_ref (a server param we don't surface here) —
  // there is NO classification filter. We deliberately DON'T offer a client-side
  // classification filter: it would filter only the loaded page and imply a completeness
  // the query can't guarantee. The searchable table covers name lookup instead.
  // ⛔ Y EL COMENTARIO DE ARRIBA YA SABÍA ESTO: dice que no se ofrece filtro de clasificación
  //    en cliente porque «filtraría sólo la página cargada e implicaría una completitud que la
  //    consulta no puede garantizar». Exacto — y la consulta tampoco lo garantizaba, porque sin
  //    `limit` traía 100 filas y tiraba el `has_more`. La conciencia estaba escrita; el techo no.
  const params = { limit: EVIDENCE_PAGE }
  const query = useQuery({
    queryKey: modelsKeys.datasets(activeTenant, undefined, params),
    queryFn: () => modelsApi.datasets(params),
  })

  const columns = useMemo<TableColumn<Dataset>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('datasets.columns.name'),
        cell: ({ row }) => (
          <span className="font-medium">{row.original.name}</span>
        ),
      },
      {
        accessorKey: 'classification',
        header: t('datasets.columns.classification'),
        cell: ({ row }) => (
          <Badge variant={classificationVariant(row.original.classification)}>
            {row.original.classification}
          </Badge>
        ),
      },
      {
        accessorKey: 'owned_ref',
        header: t('datasets.columns.ownedRef'),
        cell: ({ row }) =>
          row.original.owned_ref ? (
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.owned_ref}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'provenance',
        header: t('datasets.columns.provenance'),
        cell: ({ row }) =>
          row.original.verified ? (
            <Badge variant="outline">{t('datasets.claimed')}</Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'attested_at',
        header: t('datasets.columns.recorded'),
        cell: ({ row }) =>
          row.original.attested_at ? (
            <RelTimeLabel ts={row.original.attested_at} />
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
    ],
    [t],
  )

  const deleteMutation = useMutation({
    mutationFn: (id: string) => modelsApi.deleteDataset(id),
    onSuccess: async () => {
      await query.refetch()
      toast.success(t('datasets.deleted'))
      setDeleting(null)
      setSelected(null)
    },
    onError: (err) => {
      report(err)
    },
  })

  return (
    <SectionCard
      title={t('datasets.title')}
      description={t('datasets.description')}
      actions={
        canWrite ? (
          <Button
            variant="primary"
            size="sm"
            onClick={() => setCreateOpen(true)}
          >
            <Plus />
            {t('datasets.new')}
          </Button>
        ) : null
      }
      noPadding
    >
      {/* Fuera del bloque de datos: un refetch fallido no debe dejar el aviso flotando. */}
      <ListTruncationBadge
        query={query}
        label={t('datasets.truncated', { n: query.data?.items?.length ?? 0 })}
        hint={t('datasets.truncatedHint')}
        className="px-3 pt-3"
      />

      <DataTable<Dataset>
        label={t('datasets.title')}
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        onRowClick={(r) => setSelected(r)}
        searchable
        searchPlaceholder={t('datasets.search')}
        empty={
          <EmptyState
            title={t('empty.datasets.title')}
            description={t('empty.datasets.description')}
          />
        }
      />

      {canWrite && (
        <DatasetDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          onSaved={() => void query.refetch()}
        />
      )}

      <DatasetDrawer
        dataset={selected}
        canWrite={canWrite}
        onClose={() => setSelected(null)}
        onDelete={(d) => setDeleting(d)}
      />

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t('datasets.deleteTitle')}
        description={t('datasets.deleteBody', { name: deleting?.name ?? '' })}
        confirmLabel={t('common:actions.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </SectionCard>
  )
}

function DatasetDrawer({
  dataset,
  canWrite,
  onClose,
  onDelete,
}: {
  dataset: Dataset | null
  canWrite: boolean
  onClose: () => void
  onDelete: (d: Dataset) => void
}) {
  const { t } = useTranslation(['model-ops', 'common'])

  return (
    <Sheet open={!!dataset} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="flex w-full flex-col gap-0 sm:max-w-lg">
        {dataset && (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2">
                {dataset.name}
                <Badge variant={classificationVariant(dataset.classification)}>
                  {dataset.classification}
                </Badge>
              </SheetTitle>
              <SheetDescription>
                {t('datasets.drawerSubtitle')}
              </SheetDescription>
            </SheetHeader>
            <ScrollArea className="flex-1">
              <div className="flex flex-col gap-4 px-1 py-3">
                {canWrite && (
                  <div>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onDelete(dataset)}
                    >
                      <Trash2 />
                      {t('common:actions.delete')}
                    </Button>
                  </div>
                )}
                {/* Honesty: `verified` is an operator assertion, never a system proof. */}
                <div className="rounded-md border border-border bg-muted/50 px-3 py-2">
                  <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                    {t('datasets.provenanceLabel')}
                  </p>
                  <p className="mt-0.5 text-xs text-foreground">
                    {dataset.verified
                      ? t('datasets.provenanceClaimed')
                      : t('datasets.provenanceNotClaimed')}
                  </p>
                </div>
                <KvList>
                  {dataset.owned_ref && (
                    <KvRow label={t('datasets.columns.ownedRef')} mono>
                      {dataset.owned_ref}
                    </KvRow>
                  )}
                  {dataset.governance && (
                    <KvRow label={t('datasets.form.governance')}>
                      {dataset.governance}
                    </KvRow>
                  )}
                  {dataset.source_ref && (
                    <KvRow label={t('datasets.form.sourceRef')} mono>
                      <span className="block break-all">
                        {dataset.source_ref}
                      </span>
                    </KvRow>
                  )}
                  {dataset.content_hash && (
                    <KvRow label={t('datasets.form.contentHash')} align="start">
                      <HashChip
                        hash={`${dataset.content_alg ?? 'sha256'}:${dataset.content_hash}`}
                        head={14}
                        tail={10}
                      />
                    </KvRow>
                  )}
                  {dataset.attested_by && (
                    <KvRow label={t('datasets.attestedBy')} mono>
                      {dataset.attested_by}
                    </KvRow>
                  )}
                  {dataset.attested_at && (
                    <KvRow label={t('datasets.columns.recorded')}>
                      <RelTimeLabel ts={dataset.attested_at} />
                    </KvRow>
                  )}
                  {dataset.note && (
                    <KvRow label={t('datasets.form.note')} align="start">
                      {dataset.note}
                    </KvRow>
                  )}
                </KvList>
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

function DatasetDialog({
  open,
  onOpenChange,
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-xl overflow-y-auto">
        {open && (
          <DatasetForm onClose={() => onOpenChange(false)} onSaved={onSaved} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DatasetForm({
  onClose,
  onSaved,
}: {
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [name, setName] = useState('')
  const [ownedRef, setOwnedRef] = useState('')
  const [classification, setClassification] =
    useState<DatasetClassification>('other')
  const [governance, setGovernance] = useState('')
  const [sourceRef, setSourceRef] = useState('')
  const [contentHash, setContentHash] = useState('')
  const [contentAlg, setContentAlg] = useState('')
  const [verified, setVerified] = useState(false)
  const [note, setNote] = useState('')
  // A referential/validation refusal (400 name/classification, 404 owned_ref not found,
  // 409 duplicate name) is shown inline with the form retained — never a lost draft.
  const [formError, setFormError] = useState<string | null>(null)

  const valid = name.trim() !== ''

  const mutation = useMutation({
    mutationFn: (body: DatasetInput) => modelsApi.createDataset(body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ['models', activeTenant, 'datasets'],
      })
      toast.success(t('datasets.created'))
      onSaved()
      onClose()
    },
    onError: (err) => {
      // 400/404/409 son errores del FORMULARIO y se pintan en el formulario: van delante
      // porque son disjuntos del 403 y no compiten con él.
      if (
        err instanceof ApiError &&
        (err.status === 400 || err.status === 404 || err.status === 409)
      ) {
        setFormError(err.message)
        return
      }
      report(err)
    },
  })

  function submit() {
    if (!valid) return
    setFormError(null)
    const body: DatasetInput = {
      name: name.trim(),
      classification,
      verified,
      ...(ownedRef.trim() ? { owned_ref: ownedRef.trim() } : {}),
      ...(governance.trim() ? { governance: governance.trim() } : {}),
      ...(sourceRef.trim() ? { source_ref: sourceRef.trim() } : {}),
      ...(contentHash.trim() ? { content_hash: contentHash.trim() } : {}),
      ...(contentAlg.trim() ? { content_alg: contentAlg.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(body)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('datasets.newTitle')}</DialogTitle>
        <DialogDescription>{t('datasets.formBody')}</DialogDescription>
      </DialogHeader>

      {formError && (
        <div className="rounded-md border border-danger bg-danger-soft px-3 py-2">
          <p className="whitespace-pre-wrap text-xs text-foreground">
            {formError}
          </p>
        </div>
      )}

      <div className="flex flex-col gap-4">
        <Field label={t('datasets.form.name')} htmlFor="ds-name" required>
          <Input
            id="ds-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('datasets.form.classification')} htmlFor="ds-class">
            <Select
              value={classification}
              onValueChange={(v) =>
                setClassification(v as DatasetClassification)
              }
            >
              <SelectTrigger id="ds-class">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CLASSIFICATIONS.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('datasets.form.ownedRef')}
            htmlFor="ds-owned"
            description={t('datasets.form.ownedRefHint')}
          >
            <Input
              id="ds-owned"
              value={ownedRef}
              onChange={(e) => setOwnedRef(e.target.value)}
              mono
            />
          </Field>
        </div>
        <Field label={t('datasets.form.sourceRef')} htmlFor="ds-source">
          <Input
            id="ds-source"
            value={sourceRef}
            onChange={(e) => setSourceRef(e.target.value)}
            mono
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('datasets.form.contentHash')} htmlFor="ds-hash">
            <Input
              id="ds-hash"
              value={contentHash}
              onChange={(e) => setContentHash(e.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('datasets.form.contentAlg')}
            htmlFor="ds-alg"
            description={t('datasets.form.contentAlgHint')}
          >
            <Input
              id="ds-alg"
              value={contentAlg}
              onChange={(e) => setContentAlg(e.target.value)}
              mono
              placeholder="sha256"
            />
          </Field>
        </div>
        <Field label={t('datasets.form.governance')} htmlFor="ds-gov">
          <Input
            id="ds-gov"
            value={governance}
            onChange={(e) => setGovernance(e.target.value)}
          />
        </Field>
        <Field
          label={t('datasets.form.verified')}
          htmlFor="ds-verified"
          description={t('datasets.form.verifiedHint')}
        >
          <Switch
            id="ds-verified"
            checked={verified}
            onCheckedChange={setVerified}
          />
        </Field>
        <Field label={t('datasets.form.note')} htmlFor="ds-note">
          <Textarea
            id="ds-note"
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
