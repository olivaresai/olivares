// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Activity,
  CirclePause,
  CirclePlay,
  Info,
  Pencil,
  Plus,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
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
import { toast } from '@/components/ui/toaster'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { RelTimeLabel, ppmToPercent } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { healthApi, healthKeys } from './api'
import { HealthStateBadge } from './health-state-badge'
import type {
  CreateCheckInput,
  DesiredStatus,
  StatusDTO,
  UpdateCheckInput,
} from './types'

const CHECK_LIMIT = 1000

type LifecycleStatus = 'active' | 'paused' | 'retired'

export function ChecksTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation(['health', 'common', 'errors'])
  const { can } = useAuth()
  const canWrite = can('health:check:write')
  const canAdmin = can('health:check:admin')
  const queryClient = useQueryClient()
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<StatusDTO | null>(null)
  const [deleting, setDeleting] = useState<StatusDTO | null>(null)

  const query = useQuery({
    queryKey: healthKeys.checks(tenant, { limit: CHECK_LIMIT }),
    queryFn: () => healthApi.checks({ limit: CHECK_LIMIT }),
  })

  const refreshChecks = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: healthKeys.checks(tenant) }),
      queryClient.invalidateQueries({ queryKey: healthKeys.status(tenant) }),
    ])
  }

  const lifecycleMutation = useMutation({
    mutationFn: ({
      check,
      desiredStatus,
    }: {
      check: StatusDTO
      desiredStatus: LifecycleStatus
    }) =>
      healthApi.updateCheck(check.id, {
        desired_status: desiredStatus,
        // The row displays this field, so send it explicitly. In particular, 0
        // must remain an intentional "clear target", not an omitted patch field.
        sla_target_ppm: check.sla_target_ppm,
      }),
    onSuccess: async () => {
      await refreshChecks()
      toast.success(t('health:checks.updated'))
    },
    onError: (error) => {
      toast.error(t('errors:generic'), {
        description:
          error instanceof Error ? error.message : t('health:checks.failed'),
      })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => healthApi.deleteCheck(id),
    onSuccess: async () => {
      await refreshChecks()
      setDeleting(null)
      toast.success(t('health:checks.deleted'))
    },
    onError: (error) => {
      toast.error(t('errors:generic'), {
        description:
          error instanceof Error ? error.message : t('health:checks.failed'),
      })
    },
  })

  const columns = useMemo<TableColumn<StatusDTO>[]>(
    () => [
      {
        id: 'subject',
        accessorKey: 'subject_ref',
        header: t('health:checks.cols.subject'),
        cell: ({ row }) => (
          <div className="min-w-0">
            <div className="truncate font-medium text-foreground">
              {row.original.name || row.original.subject_ref}
            </div>
            {row.original.name ? (
              <div
                className="truncate font-mono text-xs text-muted-foreground"
                title={row.original.subject_ref}
              >
                {row.original.subject_ref}
              </div>
            ) : null}
          </div>
        ),
      },
      {
        accessorKey: 'subject_kind',
        header: t('health:checks.cols.kind'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`health:subjectKind.${row.original.subject_kind}`, {
              defaultValue: row.original.subject_kind,
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'state',
        header: t('health:checks.cols.state'),
        cell: ({ row }) => <HealthStateBadge state={row.original.state} />,
      },
      {
        accessorKey: 'desired_status',
        header: t('health:checks.cols.intent'),
        cell: ({ row }) => <StatusBadge status={row.original.desired_status} />,
      },
      {
        accessorKey: 'expected_interval_seconds',
        header: t('health:checks.cols.cadence'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {t('health:checks.seconds', {
              count: row.original.expected_interval_seconds,
            })}
          </span>
        ),
      },
      {
        accessorKey: 'sla_target_ppm',
        header: t('health:checks.cols.sla'),
        cell: ({ row }) =>
          row.original.sla_target_ppm > 0 ? (
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              {ppmToPercent(row.original.sla_target_ppm)}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'last_checked_at',
        header: t('health:checks.cols.lastChecked'),
        cell: ({ row }) => <RelTimeLabel ts={row.original.last_checked_at} />,
      },
      ...(canWrite || canAdmin
        ? [
            {
              id: 'actions',
              header: t('health:checks.cols.actions'),
              enableSorting: false,
              enableGlobalFilter: false,
              cell: ({ row }: { row: { original: StatusDTO } }) => {
                const check = row.original
                const pending =
                  lifecycleMutation.isPending &&
                  lifecycleMutation.variables?.check.id === check.id
                return (
                  <div
                    className="flex flex-wrap justify-end gap-1"
                    onClick={(event) => event.stopPropagation()}
                  >
                    {canWrite ? (
                      <>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={t('health:checks.actions.editAria', {
                            subject: check.name || check.subject_ref,
                          })}
                          title={t('health:checks.actions.edit')}
                          onClick={() => setEditing(check)}
                        >
                          <Pencil aria-hidden />
                        </Button>
                        {check.desired_status === 'active' ? (
                          <LifecycleButton
                            check={check}
                            status="paused"
                            pending={pending}
                            onChange={(desiredStatus) =>
                              lifecycleMutation.mutate({
                                check,
                                desiredStatus,
                              })
                            }
                          />
                        ) : (
                          <LifecycleButton
                            check={check}
                            status="active"
                            pending={pending}
                            onChange={(desiredStatus) =>
                              lifecycleMutation.mutate({
                                check,
                                desiredStatus,
                              })
                            }
                          />
                        )}
                        {check.desired_status !== 'retired' ? (
                          <LifecycleButton
                            check={check}
                            status="retired"
                            pending={pending}
                            onChange={(desiredStatus) =>
                              lifecycleMutation.mutate({
                                check,
                                desiredStatus,
                              })
                            }
                          />
                        ) : null}
                      </>
                    ) : null}
                    {canAdmin ? (
                      <Button
                        type="button"
                        variant="destructive"
                        size="icon-sm"
                        aria-label={t('health:checks.actions.deleteAria', {
                          subject: check.name || check.subject_ref,
                        })}
                        title={t('health:checks.actions.delete')}
                        onClick={() => setDeleting(check)}
                      >
                        <Trash2 aria-hidden />
                      </Button>
                    ) : null}
                  </div>
                )
              },
            } satisfies TableColumn<StatusDTO>,
          ]
        : []),
    ],
    [canAdmin, canWrite, lifecycleMutation, t],
  )

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-1.5">
            <h2 className="text-base font-semibold text-foreground">
              {t('health:checks.title')}
            </h2>
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t('health:checks.reportTooltipLabel')}
                  >
                    <Info aria-hidden />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>
                  {t('health:checks.reportTooltip')}
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </div>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('health:checks.description')}
          </p>
        </div>
        {canWrite ? (
          <Button
            type="button"
            variant="primary"
            size="sm"
            onClick={() => setCreateOpen(true)}
          >
            <Plus aria-hidden />
            {t('health:checks.create')}
          </Button>
        ) : null}
      </div>

      <DataTable
        label={t('health:checks.title')}
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(check) => check.id}
        searchable
        searchPlaceholder={t('health:checks.searchPlaceholder')}
        stickyHeader
        empty={
          <EmptyState
            icon={<Activity />}
            title={t('health:checks.empty.title')}
            description={t('health:checks.empty.description')}
          />
        }
      />

      {canWrite && createOpen ? (
        <CheckDialog
          key="create-check"
          tenant={tenant}
          open
          onOpenChange={setCreateOpen}
        />
      ) : null}

      {canWrite && editing ? (
        <CheckDialog
          key={editing.id}
          tenant={tenant}
          open
          check={editing}
          onOpenChange={(open) => {
            if (!open) setEditing(null)
          }}
        />
      ) : null}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={t('health:checks.delete.title')}
        description={t('health:checks.delete.description', {
          subject: deleting?.name || deleting?.subject_ref || '',
        })}
        confirmLabel={t('health:checks.actions.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </div>
  )
}

function LifecycleButton({
  check,
  status,
  pending,
  onChange,
}: {
  check: StatusDTO
  status: LifecycleStatus
  pending: boolean
  onChange: (status: LifecycleStatus) => void
}) {
  const { t } = useTranslation('health')
  const subject = check.name || check.subject_ref
  const action =
    status === 'active' ? 'resume' : status === 'paused' ? 'pause' : 'retire'
  return (
    <Button
      type="button"
      variant={status === 'retired' ? 'destructive' : 'ghost'}
      size="icon-sm"
      disabled={pending}
      aria-label={t(`checks.actions.${action}Aria`, { subject })}
      title={t(`checks.actions.${action}`)}
      onClick={() => onChange(status)}
    >
      {pending ? (
        <Spinner size="sm" aria-hidden />
      ) : status === 'active' ? (
        <CirclePlay aria-hidden />
      ) : status === 'paused' ? (
        <CirclePause aria-hidden />
      ) : (
        <Trash2 aria-hidden />
      )}
    </Button>
  )
}

function CheckDialog({
  tenant,
  open,
  check,
  onOpenChange,
}: {
  tenant: string | null
  open: boolean
  check?: StatusDTO
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation(['health', 'common'])
  const queryClient = useQueryClient()
  const editing = check != null
  const [name, setName] = useState(check?.name ?? '')
  const [subjectKind, setSubjectKind] = useState<'agent' | 'mcp'>(
    check?.subject_kind === 'mcp' ? 'mcp' : 'agent',
  )
  const [subjectRef, setSubjectRef] = useState(check?.subject_ref ?? '')
  const [expectedInterval, setExpectedInterval] = useState(
    String(check?.expected_interval_seconds ?? 300),
  )
  const [graceFactor, setGraceFactor] = useState(
    String(check?.grace_factor ?? 2),
  )
  const [slaTarget, setSlaTarget] = useState(
    String(check?.sla_target_ppm ?? 999000),
  )
  const [desiredStatus, setDesiredStatus] = useState<LifecycleStatus>(
    asLifecycleStatus(check?.desired_status),
  )
  const [validationError, setValidationError] = useState<string | null>(null)

  const mutation = useMutation<
    StatusDTO,
    unknown,
    CreateCheckInput | UpdateCheckInput
  >({
    mutationFn: (input) =>
      editing
        ? healthApi.updateCheck(check.id, input as UpdateCheckInput)
        : healthApi.createCheck(input as CreateCheckInput),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: healthKeys.checks(tenant) }),
        queryClient.invalidateQueries({ queryKey: healthKeys.status(tenant) }),
      ])
      toast.success(
        editing ? t('health:checks.updated') : t('health:checks.created'),
      )
      onOpenChange(false)
    },
  })

  const duplicate =
    !editing &&
    mutation.error instanceof ApiError &&
    mutation.error.status === 409

  const submit = () => {
    setValidationError(null)
    mutation.reset()
    const interval = Number(expectedInterval)
    const grace = Number(graceFactor)
    const sla = Number(slaTarget)
    if (
      (!editing && !subjectRef.trim()) ||
      !Number.isInteger(interval) ||
      interval <= 0 ||
      !Number.isInteger(grace) ||
      grace <= 0 ||
      !Number.isInteger(sla) ||
      sla < 0 ||
      sla > 1_000_000
    ) {
      setValidationError(t('health:checks.form.validation'))
      return
    }

    if (editing) {
      mutation.mutate({
        name: name.trim(),
        expected_interval_seconds: interval,
        grace_factor: grace,
        // Always present: omitted means keep, while explicit 0 clears.
        sla_target_ppm: sla,
        desired_status: desiredStatus,
      })
      return
    }
    mutation.mutate({
      name: name.trim(),
      subject_kind: subjectKind,
      subject_ref: subjectRef.trim(),
      expected_interval_seconds: interval,
      grace_factor: grace,
      sla_target_ppm: sla,
      desired_status: desiredStatus,
    })
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!mutation.isPending) onOpenChange(next)
      }}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {editing
              ? t('health:checks.edit.title')
              : t('health:checks.createDialog.title')}
          </DialogTitle>
          <DialogDescription>
            {editing
              ? t('health:checks.edit.description')
              : t('health:checks.createDialog.description')}
          </DialogDescription>
        </DialogHeader>

        <form
          className="grid gap-3 sm:grid-cols-2"
          onSubmit={(event) => {
            event.preventDefault()
            submit()
          }}
        >
          {editing ? (
            <>
              <Field label={t('health:checks.form.subjectKind')}>
                <div className="flex h-8 items-center">
                  <Badge variant="outline">
                    {t(`health:subjectKind.${check.subject_kind}`)}
                  </Badge>
                </div>
              </Field>
              <Field label={t('health:checks.form.subjectRef')}>
                <div
                  className="flex min-h-8 items-center break-all font-mono text-xs text-muted-foreground"
                  aria-label={t('health:checks.form.subjectRef')}
                >
                  {check.subject_ref}
                </div>
              </Field>
            </>
          ) : (
            <>
              <Field label={t('health:checks.form.subjectKind')} required>
                {({ id, ...aria }) => (
                  <Select
                    value={subjectKind}
                    onValueChange={(value) =>
                      setSubjectKind(value as 'agent' | 'mcp')
                    }
                  >
                    <SelectTrigger
                      id={id}
                      aria-label={t('health:checks.form.subjectKind')}
                      {...aria}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="agent">
                        {t('health:subjectKind.agent')}
                      </SelectItem>
                      <SelectItem value="mcp">
                        {t('health:subjectKind.mcp')}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                )}
              </Field>
              <Field label={t('health:checks.form.subjectRef')} required>
                <Input
                  value={subjectRef}
                  onChange={(event) => setSubjectRef(event.target.value)}
                  autoComplete="off"
                  mono
                />
              </Field>
            </>
          )}

          <Field label={t('health:checks.form.name')}>
            <Input
              value={name}
              onChange={(event) => setName(event.target.value)}
              autoComplete="off"
            />
          </Field>
          <Field label={t('health:checks.form.expectedInterval')} required>
            <Input
              type="number"
              min={1}
              step={1}
              value={expectedInterval}
              onChange={(event) => setExpectedInterval(event.target.value)}
              mono
            />
          </Field>
          <Field label={t('health:checks.form.graceFactor')} required>
            <Input
              type="number"
              min={1}
              step={1}
              value={graceFactor}
              onChange={(event) => setGraceFactor(event.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('health:checks.form.slaTarget')}
            description={t('health:checks.form.slaTargetHint')}
            required
          >
            <Input
              type="number"
              min={0}
              max={1_000_000}
              step={1}
              value={slaTarget}
              onChange={(event) => setSlaTarget(event.target.value)}
              mono
            />
          </Field>
          <Field label={t('health:checks.form.desiredStatus')} required>
            {({ id, ...aria }) => (
              <Select
                value={desiredStatus}
                onValueChange={(value) =>
                  setDesiredStatus(value as LifecycleStatus)
                }
              >
                <SelectTrigger
                  id={id}
                  aria-label={t('health:checks.form.desiredStatus')}
                  {...aria}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="active">
                    {t('health:desired.active')}
                  </SelectItem>
                  <SelectItem value="paused">
                    {t('health:desired.paused')}
                  </SelectItem>
                  <SelectItem value="retired">
                    {t('health:desired.retired')}
                  </SelectItem>
                </SelectContent>
              </Select>
            )}
          </Field>

          {validationError ? (
            <p role="alert" className="text-sm text-danger sm:col-span-2">
              {validationError}
            </p>
          ) : duplicate ? (
            <p role="alert" className="text-sm text-danger sm:col-span-2">
              {t('health:checks.duplicate')}
            </p>
          ) : mutation.error ? (
            <p role="alert" className="text-sm text-danger sm:col-span-2">
              {mutation.error instanceof Error
                ? mutation.error.message
                : t('health:checks.failed')}
            </p>
          ) : null}

          <DialogFooter className="sm:col-span-2">
            <Button
              type="button"
              variant="ghost"
              disabled={mutation.isPending}
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={mutation.isPending}
            >
              {mutation.isPending ? <Spinner size="sm" aria-hidden /> : null}
              {editing ? t('common:actions.save') : t('health:checks.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function asLifecycleStatus(status: DesiredStatus | undefined): LifecycleStatus {
  if (status === 'paused') return 'paused'
  if (status === 'retired') return 'retired'
  return 'active'
}
