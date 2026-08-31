// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Fine-tune jobs tab — RECORDS of fine-tuning jobs, not a training launcher. The control
// plane inventories a job's STATE and the model version it produced; it NEVER trains a
// model or holds weights, so the copy is deliberately "record"/"transition", never "run".
// `result_version_ref` is the validated reference to the produced model version;
// `base_ref`/`dataset_ref` are free-text references. The family has an update route
// (status/lineage transitions) but no delete. `started_at`/`ended_at` are RFC3339 — the
// engine SILENTLY drops an unparseable value (no 400), so the form uses datetime-local
// inputs and always emits ISO-8601, making a bad datetime impossible rather than a silent
// no-op.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { EmptyState } from '@/components/ui/empty-state'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from '@/features/models/api'
import type {
  FinetuneJob,
  FinetuneJobInput,
  FinetuneRuntime,
  FinetuneStatus,
} from '@/features/models/types'

const STATUSES: FinetuneStatus[] = [
  'queued',
  'running',
  'succeeded',
  'failed',
  'canceled',
]
const RUNTIMES: FinetuneRuntime[] = ['vllm', 'ollama', 'llamacpp', 'other']
const ALL = '__all__'
const NONE = '__none__'

/** Convert an RFC3339 instant to the value a datetime-local input expects (local time,
 *  minute precision). Returns '' for an empty/unparseable value. */
function toLocalInput(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(
    d.getHours(),
  )}:${pad(d.getMinutes())}`
}

/** Convert a datetime-local value to the engine's CANONICAL timestamp text: RFC3339 UTC
 *  with EXACTLY nine fractional digits and a literal 'Z' (core/model time.go `tsLayout`).
 *  `toISOString()` emits millisecond (3-digit) precision, which `model.ParseTimestamp`
 *  REJECTS and the handler then SILENTLY DROPS — so we pad to nine digits, making a bad
 *  datetime impossible rather than a silent no-op. '' → undefined (omit the field). */
function toCanonicalTs(local: string): string | undefined {
  if (!local.trim()) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  // e.g. "2026-07-21T10:30:00.000Z" → "2026-07-21T10:30:00.000000000Z"
  return d.toISOString().replace(/\.(\d{3})Z$/, (_m, frac) => `.${frac}000000Z`)
}

export function FinetuneTab() {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('models:registry:write')

  const [status, setStatus] = useState<string>(ALL)
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<FinetuneJob | null>(null)

  const filters = useMemo(() => {
    const f: Record<string, string> = {}
    if (status !== ALL) f.status = status
    return f
  }, [status])

  // El motor filtra por `status` antes de paginar; el techo va junto al filtro.
  const params = { ...filters, limit: EVIDENCE_PAGE }
  const query = useQuery({
    queryKey: modelsKeys.finetuneJobs(activeTenant, params),
    queryFn: () => modelsApi.finetuneJobs(params),
  })

  const columns = useMemo<TableColumn<FinetuneJob>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('finetune.columns.name'),
        cell: ({ row }) => (
          <span className="font-medium">{row.original.name}</span>
        ),
      },
      {
        accessorKey: 'runtime',
        header: t('finetune.columns.runtime'),
        cell: ({ row }) =>
          row.original.runtime ? (
            <Badge variant="neutral" className="font-mono text-[11px]">
              {row.original.runtime}
            </Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'status',
        header: t('finetune.columns.status'),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
      {
        accessorKey: 'result_version_ref',
        header: t('finetune.columns.resultVersion'),
        cell: ({ row }) =>
          row.original.result_version_ref ? (
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.result_version_ref}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'started_at',
        header: t('finetune.columns.started'),
        cell: ({ row }) =>
          row.original.started_at ? (
            <RelTimeLabel ts={row.original.started_at} />
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      ...(canWrite
        ? [
            {
              id: 'actions',
              header: '',
              cell: ({ row }: { row: { original: FinetuneJob } }) => (
                <div className="flex justify-end">
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
                </div>
              ),
            },
          ]
        : []),
    ],
    [t, canWrite],
  )

  return (
    <SectionCard
      title={t('finetune.title')}
      description={t('finetune.description')}
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
            {t('finetune.new')}
          </Button>
        ) : null
      }
      noPadding
    >
      {/* Fuera del bloque de datos: un refetch fallido no debe dejar el aviso flotando. */}
      <ListTruncationBadge
        query={query}
        label={t('finetune.truncated', { n: query.data?.items?.length ?? 0 })}
        hint={t('finetune.truncatedHint')}
        className="px-3 pt-3"
      />

      <DataTable<FinetuneJob>
        label={t('finetune.title')}
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        searchable
        searchPlaceholder={t('finetune.search')}
        toolbar={
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger className="h-8 w-[10rem] text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>
                {t('finetune.filters.allStatuses')}
              </SelectItem>
              {STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        empty={
          <EmptyState
            title={t('empty.finetune.title')}
            description={t('empty.finetune.description')}
          />
        }
      />

      {canWrite && (
        <FinetuneDialog
          key={editing?.id ?? 'new'}
          job={editing}
          open={editorOpen}
          onOpenChange={setEditorOpen}
          onSaved={() => void query.refetch()}
        />
      )}
    </SectionCard>
  )
}

function FinetuneDialog({
  job,
  open,
  onOpenChange,
  onSaved,
}: {
  job: FinetuneJob | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-xl overflow-y-auto">
        {open && (
          <FinetuneForm
            job={job}
            onClose={() => onOpenChange(false)}
            onSaved={onSaved}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function FinetuneForm({
  job,
  onClose,
  onSaved,
}: {
  job: FinetuneJob | null
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const editing = !!job

  const [name, setName] = useState(job?.name ?? '')
  const [baseRef, setBaseRef] = useState(job?.base_ref ?? '')
  const [datasetRef, setDatasetRef] = useState(job?.dataset_ref ?? '')
  const [runtime, setRuntime] = useState<string>(job?.runtime ?? NONE)
  const [status, setStatus] = useState<FinetuneStatus>(job?.status ?? 'queued')
  const [resultVersionRef, setResultVersionRef] = useState(
    job?.result_version_ref ?? '',
  )
  const [startedAt, setStartedAt] = useState(toLocalInput(job?.started_at))
  const [endedAt, setEndedAt] = useState(toLocalInput(job?.ended_at))
  const [note, setNote] = useState(job?.note ?? '')
  const [formError, setFormError] = useState<string | null>(null)

  const valid = name.trim() !== ''
  // A PUT cannot CLEAR a recorded timestamp — omitting it keeps the previous value. Warn
  // honestly rather than let a cleared field look like it took effect.
  const clearedTimestamp =
    editing &&
    ((!!job?.started_at && !startedAt.trim()) ||
      (!!job?.ended_at && !endedAt.trim()))

  const mutation = useMutation({
    mutationFn: (body: FinetuneJobInput) =>
      editing
        ? modelsApi.updateFinetuneJob(job.id, body)
        : modelsApi.createFinetuneJob(body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ['models', activeTenant, 'finetune-jobs'],
      })
      toast.success(editing ? t('finetune.updated') : t('finetune.created'))
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
    const body: FinetuneJobInput = {
      name: name.trim(),
      status,
      ...(runtime !== NONE ? { runtime: runtime as FinetuneRuntime } : {}),
      ...(baseRef.trim() ? { base_ref: baseRef.trim() } : {}),
      ...(datasetRef.trim() ? { dataset_ref: datasetRef.trim() } : {}),
      ...(resultVersionRef.trim()
        ? { result_version_ref: resultVersionRef.trim() }
        : {}),
      ...(toCanonicalTs(startedAt)
        ? { started_at: toCanonicalTs(startedAt) }
        : {}),
      ...(toCanonicalTs(endedAt) ? { ended_at: toCanonicalTs(endedAt) } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(body)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {editing ? t('finetune.editTitle') : t('finetune.newTitle')}
        </DialogTitle>
        <DialogDescription>{t('finetune.formBody')}</DialogDescription>
      </DialogHeader>

      <div className="rounded-md border border-border bg-muted/50 px-3 py-2">
        <p className="text-[11px] text-muted-foreground">
          {t('finetune.trackBanner')}
        </p>
      </div>

      {formError && (
        <div className="rounded-md border border-danger bg-danger-soft px-3 py-2">
          <p className="whitespace-pre-wrap text-xs text-foreground">
            {formError}
          </p>
        </div>
      )}

      <div className="flex flex-col gap-4">
        <Field label={t('finetune.form.name')} htmlFor="ft-name" required>
          <Input
            id="ft-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('finetune.form.status')} htmlFor="ft-status">
            <Select
              value={status}
              onValueChange={(v) => setStatus(v as FinetuneStatus)}
            >
              <SelectTrigger id="ft-status">
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
          <Field label={t('finetune.form.runtime')} htmlFor="ft-runtime">
            <Select value={runtime} onValueChange={setRuntime}>
              <SelectTrigger id="ft-runtime">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE}>
                  {t('finetune.form.runtimeNone')}
                </SelectItem>
                {RUNTIMES.map((rt) => (
                  <SelectItem key={rt} value={rt}>
                    {rt}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('finetune.form.baseRef')} htmlFor="ft-base">
            <Input
              id="ft-base"
              value={baseRef}
              onChange={(e) => setBaseRef(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('finetune.form.datasetRef')} htmlFor="ft-dataset">
            <Input
              id="ft-dataset"
              value={datasetRef}
              onChange={(e) => setDatasetRef(e.target.value)}
              mono
            />
          </Field>
        </div>
        <Field
          label={t('finetune.form.resultVersionRef')}
          htmlFor="ft-result"
          description={t('finetune.form.resultVersionRefHint')}
        >
          <Input
            id="ft-result"
            value={resultVersionRef}
            onChange={(e) => setResultVersionRef(e.target.value)}
            mono
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('finetune.form.startedAt')} htmlFor="ft-started">
            <Input
              id="ft-started"
              type="datetime-local"
              value={startedAt}
              onChange={(e) => setStartedAt(e.target.value)}
            />
          </Field>
          <Field label={t('finetune.form.endedAt')} htmlFor="ft-ended">
            <Input
              id="ft-ended"
              type="datetime-local"
              value={endedAt}
              onChange={(e) => setEndedAt(e.target.value)}
            />
          </Field>
        </div>
        {clearedTimestamp && (
          <p className="text-[11px] text-warning">
            {t('finetune.form.clearHint')}
          </p>
        )}
        <Field label={t('finetune.form.note')} htmlFor="ft-note">
          <Textarea
            id="ft-note"
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
