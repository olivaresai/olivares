// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// GPAI supplier posture is one operator-attested record per provider. Claim booleans
// are rendered independently: absence is neutral, never a failed compliance check;
// `verified` is the operator's own review assertion, never an Olivares verdict.
import { useMemo, useState, type ReactNode } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus } from 'lucide-react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import {
  AsyncSection,
  IntelNotice,
  ListTruncationBadge,
  SectionCard,
} from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from './api'
import type { GpaiPosture, GpaiPostureInput } from './types'

type ClaimField =
  | 'cop_signatory'
  | 'technical_docs'
  | 'training_data_summary'
  | 'copyright_policy'
  | 'downstream_info'
  | 'systemic_risk'
  | 'safety_report'

interface ClaimDefinition {
  field: ClaimField
  descriptionKey?: string
  classification?: boolean
}

const CLAIM_FIELDS: readonly ClaimDefinition[] = [
  { field: 'cop_signatory' },
  { field: 'technical_docs' },
  { field: 'training_data_summary' },
  { field: 'copyright_policy' },
  { field: 'downstream_info' },
  {
    field: 'systemic_risk',
    descriptionKey: 'gpai.form.systemicRiskHint',
    classification: true,
  },
  {
    field: 'safety_report',
    descriptionKey: 'gpai.form.safetyReportHint',
  },
]

const gpaiFormBaseSchema = z.object({
  provider_ref: z.string(),
  cop_signatory: z.boolean(),
  technical_docs: z.boolean(),
  training_data_summary: z.boolean(),
  copyright_policy: z.boolean(),
  downstream_info: z.boolean(),
  systemic_risk: z.boolean(),
  safety_report: z.boolean(),
  verified: z.boolean(),
  verification_method: z.string(),
  note: z.string(),
})

type GpaiFormValues = z.infer<typeof gpaiFormBaseSchema>

export function GpaiTab() {
  const { t } = useTranslation('models')
  const { activeTenant, can } = useAuth()
  const canWrite = can('models:gpai:write')
  const [selected, setSelected] = useState<GpaiPosture | null>(null)
  const [editing, setEditing] = useState<GpaiPosture | null>(null)
  const [editorOpen, setEditorOpen] = useState(false)

  // ⛔ EL TECHO SE PIDE Y EL RECORTE SE DICE. `handleListGPAIPosture` publica `has_more`; sin
  //    `limit` la pantalla enseñaba las primeras cien posturas GPAI declaradas y se leía «esto es
  //    lo que hemos declarado», que en un registro de cumplimiento es la frase que se cita.
  const params = { limit: EVIDENCE_PAGE }
  const query = useQuery({
    queryKey: modelsKeys.gpaiPostures(activeTenant, undefined, params),
    queryFn: () => modelsApi.gpaiPostures(params),
  })

  const openEditor = (posture: GpaiPosture | null) => {
    setEditing(posture)
    setEditorOpen(true)
  }

  const columns = useMemo<TableColumn<GpaiPosture>[]>(() => {
    const claimColumns: TableColumn<GpaiPosture>[] = CLAIM_FIELDS.map(
      ({ field, classification }) => ({
        accessorKey: field,
        header: t(`gpai.claims.${field}`),
        cell: ({ row }) => (
          <ClaimBadge
            recorded={row.original[field]}
            classification={classification}
          />
        ),
      }),
    )

    return [
      {
        accessorKey: 'provider_ref',
        header: t('gpai.columns.provider'),
        cell: ({ row }) => (
          <span className="font-mono text-xs font-medium text-foreground">
            {row.original.provider_ref}
          </span>
        ),
      },
      ...claimColumns,
      {
        accessorKey: 'verified',
        header: t('gpai.columns.review'),
        cell: ({ row }) => <ReviewBadge reviewed={row.original.verified} />,
      },
      {
        accessorKey: 'attested_at',
        header: t('gpai.columns.attestedAt'),
        cell: ({ row }) => (
          <RelTimeLabel
            ts={row.original.attested_at}
            className="whitespace-nowrap text-xs text-muted-foreground"
          />
        ),
      },
      ...(canWrite
        ? [
            {
              id: 'actions',
              header: t('gpai.columns.actions'),
              enableSorting: false,
              cell: ({ row }: { row: { original: GpaiPosture } }) => (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t('gpai.actions.editProvider', {
                    provider: row.original.provider_ref,
                  })}
                  onClick={(event) => {
                    event.stopPropagation()
                    openEditor(row.original)
                  }}
                >
                  <Pencil />
                </Button>
              ),
            } satisfies TableColumn<GpaiPosture>,
          ]
        : []),
    ]
  }, [canWrite, t])

  return (
    <div className="flex flex-col gap-4">
      <IntelNotice tone="info">{t('gpai.notice')}</IntelNotice>

      <SectionCard
        title={t('gpai.title')}
        description={t('gpai.description')}
        actions={
          canWrite ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => openEditor(null)}
            >
              <Plus />
              {t('gpai.actions.attest')}
            </Button>
          ) : null
        }
      >
        <ListTruncationBadge
          query={query}
          label={t('gpai.truncated', {
            n: query.data?.items?.length ?? 0,
          })}
          hint={t('gpai.truncatedHint')}
        />
        <AsyncSection query={query} skeletonHeight={220}>
          {(list) => (
            <DataTable<GpaiPosture>
              label={t('gpai.title')}
              columns={columns}
              data={list.items}
              getRowId={(row) => row.id || row.provider_ref}
              onRowClick={setSelected}
              empty={
                <EmptyState
                  title={t('gpai.empty')}
                  description={t('gpai.emptyHint')}
                />
              }
            />
          )}
        </AsyncSection>
      </SectionCard>

      <GpaiDetailDialog
        posture={selected}
        canWrite={canWrite}
        onClose={() => setSelected(null)}
        onEdit={(posture) => {
          setSelected(null)
          openEditor(posture)
        }}
      />

      {canWrite ? (
        <GpaiPostureDialog
          key={editing?.id ?? 'new'}
          posture={editing}
          open={editorOpen}
          onOpenChange={(open) => {
            setEditorOpen(open)
            if (!open) setEditing(null)
          }}
        />
      ) : null}
    </div>
  )
}

function ClaimBadge({
  recorded,
  classification = false,
}: {
  recorded: boolean
  classification?: boolean
}) {
  const { t } = useTranslation('models')
  return (
    <Badge
      variant={recorded ? (classification ? 'outline' : 'accent') : 'neutral'}
      className="text-[11px]"
    >
      {recorded ? t('gpai.states.recorded') : t('gpai.states.notRecorded')}
    </Badge>
  )
}

function ReviewBadge({ reviewed }: { reviewed: boolean }) {
  const { t } = useTranslation('models')
  return (
    <Badge variant={reviewed ? 'accent' : 'neutral'}>
      {reviewed ? t('gpai.states.reviewed') : t('gpai.states.unreviewed')}
    </Badge>
  )
}

function GpaiDetailDialog({
  posture,
  canWrite,
  onClose,
  onEdit,
}: {
  posture: GpaiPosture | null
  canWrite: boolean
  onClose: () => void
  onEdit: (posture: GpaiPosture) => void
}) {
  const { t } = useTranslation('models')

  return (
    <Dialog open={posture !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {posture ? (
          <>
            <DialogHeader>
              <DialogTitle>
                {t('gpai.detail.title', { provider: posture.provider_ref })}
              </DialogTitle>
              <DialogDescription>
                {t('gpai.detail.description')}
              </DialogDescription>
            </DialogHeader>

            <IntelNotice tone="neutral">
              {t('gpai.verifiedMeaning')}
            </IntelNotice>

            <div className="flex flex-col gap-2">
              <h3 className="text-sm font-medium text-foreground">
                {t('gpai.detail.claims')}
              </h3>
              <dl className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {CLAIM_FIELDS.map(({ field, classification }) => (
                  <div
                    key={field}
                    className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2"
                  >
                    <dt className="text-xs text-foreground">
                      {t(`gpai.claims.${field}`)}
                    </dt>
                    <dd>
                      <ClaimBadge
                        recorded={posture[field]}
                        classification={classification}
                      />
                    </dd>
                  </div>
                ))}
              </dl>
            </div>

            <dl className="grid grid-cols-1 gap-3 rounded-md border border-border bg-muted p-3 text-sm sm:grid-cols-2">
              <DetailValue
                label={t('gpai.detail.reviewState')}
                value={<ReviewBadge reviewed={posture.verified} />}
              />
              <DetailValue
                label={t('gpai.detail.verificationMethod')}
                value={
                  posture.verification_method || t('gpai.detail.notProvided')
                }
              />
              <DetailValue
                label={t('gpai.detail.attestedBy')}
                value={posture.attested_by || t('gpai.detail.notProvided')}
              />
              <DetailValue
                label={t('gpai.detail.attestedAt')}
                value={<RelTimeLabel ts={posture.attested_at} />}
              />
              <DetailValue
                label={t('gpai.detail.note')}
                value={posture.note || t('gpai.detail.notProvided')}
                className="sm:col-span-2"
              />
            </dl>

            {canWrite ? (
              <DialogFooter>
                <Button variant="primary" onClick={() => onEdit(posture)}>
                  <Pencil />
                  {t('gpai.actions.edit')}
                </Button>
              </DialogFooter>
            ) : null}
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function DetailValue({
  label,
  value,
  className,
}: {
  label: string
  value: ReactNode
  className?: string
}) {
  return (
    <div className={className}>
      <dt className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        {label}
      </dt>
      <dd className="mt-1 whitespace-pre-wrap text-sm text-foreground">
        {value}
      </dd>
    </div>
  )
}

function GpaiPostureDialog({
  posture,
  open,
  onOpenChange,
}: {
  posture: GpaiPosture | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation(['models', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const editing = posture !== null
  const [serverError, setServerError] = useState<string | null>(null)

  const schema = useMemo(
    () =>
      gpaiFormBaseSchema.superRefine((values, context) => {
        if (!values.provider_ref.trim()) {
          context.addIssue({
            code: 'custom',
            path: ['provider_ref'],
            message: t('gpai.form.providerRequired'),
          })
        }
        if (values.verified && !values.verification_method.trim()) {
          context.addIssue({
            code: 'custom',
            path: ['verification_method'],
            message: t('gpai.form.verificationMethodRequired'),
          })
        }
      }),
    [t],
  )

  const form = useForm<GpaiFormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      provider_ref: posture?.provider_ref ?? '',
      cop_signatory: posture?.cop_signatory ?? false,
      technical_docs: posture?.technical_docs ?? false,
      training_data_summary: posture?.training_data_summary ?? false,
      copyright_policy: posture?.copyright_policy ?? false,
      downstream_info: posture?.downstream_info ?? false,
      systemic_risk: posture?.systemic_risk ?? false,
      safety_report: posture?.safety_report ?? false,
      verified: posture?.verified ?? false,
      verification_method: posture?.verification_method ?? '',
      note: posture?.note ?? '',
    },
  })
  const reviewed = useWatch({ control: form.control, name: 'verified' })

  const closeDialog = () => {
    form.reset()
    setServerError(null)
    onOpenChange(false)
  }

  const mutation = useMutation({
    mutationFn: (body: GpaiPostureInput) => modelsApi.attestGpaiPosture(body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: modelsKeys.gpaiPostures(activeTenant),
      })
      toast.success(editing ? t('gpai.form.updated') : t('gpai.form.created'))
      closeDialog()
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 400) {
        setServerError(error.message)
        return
      }
      report(error)
    },
  })

  const submit = (values: GpaiFormValues) => {
    setServerError(null)
    const verificationMethod = values.verification_method.trim()
    const note = values.note.trim()
    const body: GpaiPostureInput = {
      provider_ref: editing ? posture.provider_ref : values.provider_ref.trim(),
      cop_signatory: values.cop_signatory,
      technical_docs: values.technical_docs,
      training_data_summary: values.training_data_summary,
      copyright_policy: values.copyright_policy,
      downstream_info: values.downstream_info,
      systemic_risk: values.systemic_risk,
      safety_report: values.safety_report,
      verified: values.verified,
      ...(verificationMethod
        ? { verification_method: verificationMethod }
        : {}),
      ...(note ? { note } : {}),
    }
    mutation.mutate(body)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) closeDialog()
        else onOpenChange(true)
      }}
    >
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {editing ? t('gpai.form.editTitle') : t('gpai.form.createTitle')}
          </DialogTitle>
          <DialogDescription>{t('gpai.form.description')}</DialogDescription>
        </DialogHeader>

        <form
          className="flex flex-col gap-4"
          onSubmit={form.handleSubmit(submit)}
        >
          {serverError ? (
            <div
              role="alert"
              className="rounded-md border border-danger-line bg-danger-soft px-3 py-2"
            >
              <p className="text-xs font-medium text-danger">
                {t('gpai.form.requestRejected')}
              </p>
              <p className="mt-1 text-xs text-foreground">{serverError}</p>
            </div>
          ) : null}

          <Field
            label={t('gpai.form.providerRef')}
            description={editing ? t('gpai.form.providerLocked') : undefined}
            error={form.formState.errors.provider_ref?.message}
            required
          >
            {({ id, ...aria }) =>
              editing ? (
                <>
                  <Input
                    id={id}
                    value={posture.provider_ref}
                    disabled
                    {...aria}
                  />
                  <input type="hidden" {...form.register('provider_ref')} />
                </>
              ) : (
                <Input id={id} {...aria} {...form.register('provider_ref')} />
              )
            }
          </Field>

          <div className="flex flex-col gap-2">
            <div>
              <h3 className="text-sm font-medium text-foreground">
                {t('gpai.form.claimsTitle')}
              </h3>
              <p className="text-xs text-muted-foreground">
                {t('gpai.form.claimsDescription')}
              </p>
            </div>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {CLAIM_FIELDS.map(({ field, descriptionKey }) => (
                <Controller
                  key={field}
                  control={form.control}
                  name={field}
                  render={({ field: controlField }) => (
                    <Field
                      className="rounded-md border border-border px-3 py-2"
                      label={t(`gpai.claims.${field}`)}
                      description={
                        descriptionKey ? t(descriptionKey) : undefined
                      }
                    >
                      {({ id, ...aria }) => (
                        <Switch
                          id={id}
                          checked={controlField.value}
                          onCheckedChange={controlField.onChange}
                          {...aria}
                        />
                      )}
                    </Field>
                  )}
                />
              ))}
            </div>
          </div>

          <Controller
            control={form.control}
            name="verified"
            render={({ field }) => (
              <Field
                className="rounded-md border border-border px-3 py-2"
                label={t('gpai.form.verified')}
                description={t('gpai.verifiedMeaning')}
              >
                {({ id, ...aria }) => (
                  <Switch
                    id={id}
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    {...aria}
                  />
                )}
              </Field>
            )}
          />

          <Field
            label={t('gpai.form.verificationMethod')}
            description={t('gpai.form.verificationMethodHint')}
            error={form.formState.errors.verification_method?.message}
            required={reviewed}
          >
            {({ id, ...aria }) => (
              <Input
                id={id}
                {...aria}
                {...form.register('verification_method')}
              />
            )}
          </Field>

          <Field label={t('gpai.form.note')}>
            {({ id, ...aria }) => (
              <Textarea id={id} {...aria} {...form.register('note')} />
            )}
          </Field>

          <DialogFooter>
            <Button type="button" variant="ghost" onClick={closeDialog}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={mutation.isPending}
            >
              {mutation.isPending
                ? t('gpai.form.saving')
                : editing
                  ? t('gpai.form.save')
                  : t('gpai.actions.attest')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
