// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Orchestration / A2A (module IV) — the container. It wires the three reads (the
// privileged, self-audited communication graph; the delegation flows; the declared
// schedules + their ledger decisions) and composes the pure pieces inside <IntelPage>.
// It computes nothing about the topology — the engine derives roles/states/health;
// this presents. The coverage caveat is rendered PROMINENTLY so an honest absence
// ("no a2a connector") never reads as "no communication".
import { zodResolver } from '@hookform/resolvers/zod'
import { Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Plus, Workflow } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { useCommandStore } from '@/stores/command'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RevisionsSheet } from '@/features/shared/revisions-sheet'
import {
  AsyncSection,
  CaveatNotice,
  HashChip,
  IntelPage,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import { orchestrationApi, orchestrationKeys } from './api'
import {
  CommunicationGraph,
  DecisionList,
  FlowsTable,
  GraphLegend,
  SchedulesTable,
} from './components'
import type {
  DecisionOpStatus,
  DesiredStatus,
  FireOpStatus,
  FireScheduleResponse,
  FlowState,
  PatchScheduleInput,
  ScheduleDTO,
  SubjectKind,
  TriggerKind,
} from './types'
import './i18n'

const GRAPH_LIMIT = 500
const FLOW_STATES: (FlowState | 'all')[] = [
  'all',
  'active',
  'idle',
  'stalled',
  'completed',
]

const MIN_INTERVAL = 60
const MAX_INTERVAL = 31_622_400

/** Authoring validation mirrors schedules.go. cadence_spec remains opaque: only
 * presence is checked for cron/event; its syntax is never interpreted here. */
export const scheduleFormSchema = z
  .object({
    name: z.string().trim().min(1, { message: 'required' }),
    subject_kind: z.enum(['agent', 'swarm']),
    subject_ref: z.string().trim().min(1, { message: 'required' }),
    trigger_kind: z.enum(['cron', 'event', 'manual']),
    cadence_spec: z.string().trim(),
    expected_interval_seconds: z.coerce
      .number()
      .int({ message: 'intervalRange' })
      .min(0, { message: 'intervalRange' })
      .max(MAX_INTERVAL, { message: 'intervalRange' }),
    grace_factor: z.coerce
      .number()
      .int({ message: 'graceRange' })
      .min(1, { message: 'graceRange' })
      .max(10, { message: 'graceRange' })
      .default(2),
  })
  .superRefine((values, ctx) => {
    if (values.trigger_kind !== 'manual' && values.cadence_spec.length === 0) {
      ctx.addIssue({
        code: 'custom',
        path: ['cadence_spec'],
        message: 'required',
      })
    }
    if (
      // 0 = cadence-miss detection deliberately disabled — valid at create and
      // patch time (schedules.go accepts it; runCadenceScan skips interval<=0).
      values.trigger_kind === 'cron' &&
      values.expected_interval_seconds !== 0 &&
      values.expected_interval_seconds < MIN_INTERVAL
    ) {
      ctx.addIssue({
        code: 'custom',
        path: ['expected_interval_seconds'],
        message: 'intervalRange',
      })
    }
    if (
      values.trigger_kind !== 'cron' &&
      values.expected_interval_seconds !== 0
    ) {
      ctx.addIssue({
        code: 'custom',
        path: ['expected_interval_seconds'],
        message: 'intervalCronOnly',
      })
    }
  })

export type ScheduleFormInput = z.input<typeof scheduleFormSchema>
export type ScheduleForm = z.output<typeof scheduleFormSchema>

export function OrchestrationView() {
  const { t } = useTranslation('orchestration')
  const { activeTenant, can } = useAuth()
  // The graph read is privileged; mirror the server gate to keep the tab honest.
  const canGraph = can('orchestration:graph:read')

  return (
    <IntelPage
      icon={Workflow}
      title={t('title')}
      description={t('description')}
      notices={<SelfAuditNotice />}
    >
      <Tabs defaultValue="graph">
        <TabsList>
          <TabsTrigger value="graph">{t('tabs.graph')}</TabsTrigger>
          <TabsTrigger value="flows">{t('tabs.flows')}</TabsTrigger>
          <TabsTrigger value="schedules">{t('tabs.schedules')}</TabsTrigger>
        </TabsList>

        <TabsContent value="graph" className="flex flex-col gap-4">
          <GraphTab canGraph={canGraph} tenant={activeTenant} />
        </TabsContent>

        <TabsContent value="flows" className="flex flex-col gap-4">
          <FlowsTab tenant={activeTenant} />
        </TabsContent>

        <TabsContent value="schedules" className="flex flex-col gap-4">
          <SchedulesTab tenant={activeTenant} />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

// --- communication graph tab --------------------------------------------------

function GraphTab({
  canGraph,
  tenant,
}: {
  canGraph: boolean
  tenant: string | null
}) {
  const { t } = useTranslation('orchestration')
  const graphQ = useQuery({
    queryKey: orchestrationKeys.graph(tenant, { limit: GRAPH_LIMIT }),
    queryFn: () => orchestrationApi.graph({ limit: GRAPH_LIMIT }),
    enabled: canGraph,
  })

  return (
    <SectionCard
      title={t('graph.title')}
      description={t('graph.description')}
      actions={<GraphLegend />}
    >
      <AsyncSection query={graphQ} skeletonHeight={520}>
        {(graph) => (
          <div className="flex flex-col gap-3">
            {/* The honest-coverage caveats: absence is not zero. */}
            {graph.coverage.caveats.map((caveat, i) => (
              <CaveatNotice key={i}>{caveat}</CaveatNotice>
            ))}
            {graph.has_more ? (
              <CaveatNotice tone="warning">
                {t('graph.truncated', { n: GRAPH_LIMIT })}
              </CaveatNotice>
            ) : null}
            {graph.nodes.length === 0 ? (
              <EmptyState
                icon={<Workflow />}
                title={t('graph.empty.title')}
                description={t('graph.empty.description')}
              />
            ) : (
              <CommunicationGraph graph={graph} />
            )}
          </div>
        )}
      </AsyncSection>
    </SectionCard>
  )
}

// --- flows tab ----------------------------------------------------------------

function FlowsTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('orchestration')
  const [state, setState] = useState<FlowState | 'all'>('all')
  const stateParam = state === 'all' ? undefined : state

  const flowsQ = useQuery({
    queryKey: orchestrationKeys.flows(tenant, state),
    queryFn: () => orchestrationApi.flows(stateParam),
  })

  return (
    <SectionCard
      title={t('flows.title')}
      description={t('flows.description')}
      actions={
        <Select
          value={state}
          onValueChange={(v) => setState(v as FlowState | 'all')}
        >
          <SelectTrigger className="w-40" aria-label={t('flows.filter')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {FLOW_STATES.map((s) => (
              <SelectItem key={s} value={s}>
                {s === 'all'
                  ? t('flows.allStates')
                  : t(`flows.state.${s}`, { defaultValue: s })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
    >
      <AsyncSection query={flowsQ} skeletonHeight={200}>
        {(list) =>
          list.items.length === 0 ? (
            <EmptyState title={t('flows.empty')} />
          ) : (
            <FlowsTable flows={list.items} />
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}

// --- schedules tab ------------------------------------------------------------

function SchedulesTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation(['orchestration', 'common'])
  const { can } = useAuth()
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  // ⌘K palette action: "new schedule" navigated here — consume once.
  useEffect(() => {
    if (
      useCommandStore.getState().consumeAction('orchestration') ===
      'createSchedule'
    ) {
      setCreateOpen(true)
    }
  }, [])
  const [editSchedule, setEditSchedule] = useState<ScheduleDTO | null>(null)
  const [statusChange, setStatusChange] = useState<{
    schedule: ScheduleDTO
    desired_status: DesiredStatus
  } | null>(null)
  const [fireSchedule, setFireSchedule] = useState<ScheduleDTO | null>(null)
  const [historySchedule, setHistorySchedule] = useState<ScheduleDTO | null>(
    null,
  )
  const [fireDrafts, setFireDrafts] = useState<
    Record<string, FireScheduleResponse>
  >({})

  const canRead = can('orchestration:schedule:read')
  const canWrite = can('orchestration:schedule:write')
  const canFire = can('orchestration:schedule:admin')

  const schedulesQ = useQuery({
    queryKey: orchestrationKeys.schedules(tenant),
    queryFn: () => orchestrationApi.schedules(),
    enabled: canRead,
  })

  // ⛔ CENTINELA, NO UN ID VACÍO NI `null`: `null` ya significa «no has elegido nada» y
  //    reutilizarlo para «todo el estate» haría indistinguibles dos estados que la pantalla
  //    tiene que separar. No puede chocar con un id real: los ids de schedule son UUID.
  const LEDGER = '__estate__'
  const enLedger = selectedId === LEDGER
  const decisionsQ = useQuery({
    queryKey: orchestrationKeys.scheduleDecisions(tenant, selectedId ?? ''),
    queryFn: () => orchestrationApi.scheduleDecisions(selectedId as string),
    enabled: selectedId !== null && !enLedger,
  })

  // El techo real del motor es `maxLimit = 1000` (`sqlstore/generic.go`); pedir más no
  // trae más y no pedir nada trae 100 en silencio. Se pide el máximo y se DECLARA el
  // recorte cuando el motor contesta `has_more`.
  const LEDGER_PAGE = 1000
  // ⛔ AQUÍ SÍ HACE FALTA GUARDA, Y ES LA DIFERENCIA CON LA VISTA DE VOZ. La ruta
  //    `/decisions` exige `orchestration:schedule:read` (`modules/orchestration/api.go`)
  //    mientras la vista entera está registrada con `orchestration:graph:read`
  //    (`web/src/features/registry.tsx`). Con los roles de fábrica coinciden porque los dos
  //    son de tier read, pero un grant a medida puede separarlos — y entonces la pestaña
  //    pediría una ruta que su principal no puede leer.
  const puedeLeerSchedules = can('orchestration:schedule:read')
  const ledgerQ = useQuery({
    queryKey: orchestrationKeys.ledger(tenant, { limit: LEDGER_PAGE }),
    queryFn: () => orchestrationApi.decisions({ limit: LEDGER_PAGE }),
    enabled: enLedger && puedeLeerSchedules,
  })

  const statusMut = usePrivilegedMutation<
    { id: string; desired_status: DesiredStatus },
    ScheduleDTO
  >({
    mutationFn: ({ id, desired_status }) =>
      orchestrationApi.updateSchedule(id, { desired_status }),
    invalidateKeys: (_data, variables) => [
      orchestrationKeys.schedules(tenant),
      orchestrationKeys.schedule(tenant, variables.id),
    ],
    successMessage: (_data, variables) =>
      t(`schedules.status.changed.${variables.desired_status}`),
    onDone: () => setStatusChange(null),
  })

  const statusVerb =
    statusChange?.desired_status === 'paused'
      ? 'pause'
      : statusChange?.desired_status === 'active'
        ? 'resume'
        : 'retire'

  return (
    <>
      <SectionCard
        title={t('schedules.title')}
        description={t('schedules.description')}
        actions={
          canWrite ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setCreateOpen(true)}
            >
              <Plus />
              {t('schedules.new')}
            </Button>
          ) : null
        }
        noPadding
      >
        <div className="p-4">
          <AsyncSection query={schedulesQ} skeletonHeight={200}>
            {(list) =>
              list.items.length === 0 ? (
                <EmptyState
                  title={t('schedules.empty')}
                  description={t('schedules.emptyHint')}
                />
              ) : (
                <SchedulesTable
                  schedules={list.items}
                  canRead={canRead}
                  canWrite={canWrite}
                  canFire={canFire}
                  onEdit={setEditSchedule}
                  onStatusChange={(schedule, desired_status) =>
                    setStatusChange({ schedule, desired_status })
                  }
                  onFire={setFireSchedule}
                  onHistory={setHistorySchedule}
                />
              )
            }
          </AsyncSection>
        </div>
      </SectionCard>

      <SectionCard
        title={t('decisions.title')}
        // La descripción decía «for the selected schedule» también en modo estate, donde
        // no hay schedule seleccionado y hay filas que no cuelgan de ninguno.
        description={
          enLedger
            ? t('decisions.estateDescription')
            : t('decisions.description')
        }
        actions={
          <Select
            value={selectedId ?? ''}
            onValueChange={(v) => setSelectedId(v || null)}
          >
            <SelectTrigger className="w-56" aria-label={t('decisions.pick')}>
              <SelectValue placeholder={t('decisions.pick')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={LEDGER}>{t('decisions.estate')}</SelectItem>
              {(schedulesQ.data?.items ?? []).map((s) => (
                <SelectItem key={s.id} value={s.id}>
                  {s.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      >
        {selectedId === null ? (
          <EmptyState title={t('decisions.pickHint')} />
        ) : enLedger && !puedeLeerSchedules ? (
          <EmptyState title={t('decisions.forbidden')} />
        ) : enLedger ? (
          <AsyncSection query={ledgerQ} skeletonHeight={180}>
            {(list) => (
              <>
                <CaveatNotice className="mb-3">
                  {t('decisions.estateCaveat')}
                </CaveatNotice>
                {list.items.length === 0 ? (
                  <EmptyState title={t('decisions.empty')} />
                ) : (
                  <DecisionList decisions={list.items} showSubject />
                )}
                {/* ⛔ EL RECORTE SE DECLARA. El motor hace UNA llamada a `repo.List` y no
                    drena el cursor, así que con más decisiones que el techo la lista se ve
                    completa y no lo está — y en un ledger de gobierno «no hay más» es la
                    afirmación más cara que puede hacer una pantalla. */}
                {list.has_more ? (
                  <CaveatNotice className="mt-3">
                    {t('decisions.truncated', { count: LEDGER_PAGE })}
                  </CaveatNotice>
                ) : null}
              </>
            )}
          </AsyncSection>
        ) : (
          <AsyncSection query={decisionsQ} skeletonHeight={180}>
            {(list) =>
              list.items.length === 0 ? (
                <EmptyState title={t('decisions.empty')} />
              ) : (
                <DecisionList decisions={list.items} />
              )
            }
          </AsyncSection>
        )}
      </SectionCard>

      {canWrite ? (
        <ScheduleDialog open={createOpen} onOpenChange={setCreateOpen} />
      ) : null}

      {canWrite && editSchedule ? (
        <ScheduleDialog
          open
          onOpenChange={(open) => {
            if (!open) setEditSchedule(null)
          }}
          existing={editSchedule}
        />
      ) : null}

      {historySchedule ? (
        <RevisionsSheet
          open
          onOpenChange={(open) => {
            if (!open) setHistorySchedule(null)
          }}
          queryKey={orchestrationKeys.scheduleRevisions(
            tenant,
            historySchedule.id,
          )}
          listRevisions={(params) =>
            orchestrationApi.scheduleRevisions(historySchedule.id, params)
          }
          restoreRevision={(revisionId) =>
            orchestrationApi.restoreSchedule(historySchedule.id, revisionId)
          }
          invalidateKeys={[
            orchestrationKeys.schedules(tenant),
            orchestrationKeys.schedule(tenant, historySchedule.id),
          ]}
          canWrite={canWrite}
          labels={{
            title: t('history.title', { name: historySchedule.name }),
            description: t('history.description'),
            caption: t('history.scopeCaption'),
            empty: t('history.empty'),
            loading: t('common:states.loading'),
            loadMore: t('history.loadMore'),
            compareTitle: t('history.compareTitle'),
            selectTwo: t('history.selectTwo'),
            selectRevision: (operation, actor) =>
              t('history.selectRevision', { op: operation, actor }),
            originalLabel: t('history.originalLabel'),
            modifiedLabel: t('history.modifiedLabel'),
            restore: t('history.restore'),
            restoreTitle: t('history.restoreTitle'),
            restoreDescription: t('history.restoreDescription', {
              name: historySchedule.name,
            }),
            restoreConfirm: t('history.restoreConfirm'),
            restoreSuccess: t('history.restored'),
            operations: {
              create: t('history.operations.create'),
              update: t('history.operations.update'),
              delete: t('history.operations.delete'),
              restore: t('history.operations.restore'),
            },
          }}
        />
      ) : null}

      {statusChange ? (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setStatusChange(null)
          }}
          title={t(`schedules.status.${statusVerb}Title`, {
            name: statusChange.schedule.name,
          })}
          description={t(`schedules.status.${statusVerb}Description`, {
            name: statusChange.schedule.name,
          })}
          confirmLabel={t(`schedules.status.${statusVerb}Confirm`)}
          tone={statusVerb === 'retire' ? 'danger' : 'default'}
          confirmPhrase={
            statusVerb === 'retire' ? statusChange.schedule.name : undefined
          }
          pending={statusMut.isPending}
          onConfirm={() =>
            statusMut.mutate({
              id: statusChange.schedule.id,
              desired_status: statusChange.desired_status,
            })
          }
        />
      ) : null}

      {canFire && fireSchedule ? (
        <FireScheduleDialog
          schedule={fireSchedule}
          initialResult={fireDrafts[fireSchedule.id]}
          onResult={(result) =>
            setFireDrafts((current) => ({
              ...current,
              [fireSchedule.id]: result,
            }))
          }
          onClose={() => setFireSchedule(null)}
        />
      ) : null}
    </>
  )
}

// --- schedule create/edit dialog ---------------------------------------------

function ScheduleDialog({
  open,
  onOpenChange,
  existing,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  existing?: ScheduleDTO
}) {
  const { t } = useTranslation(['orchestration', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const isEdit = existing !== undefined

  const form = useForm<ScheduleFormInput, unknown, ScheduleForm>({
    resolver: zodResolver(scheduleFormSchema),
    defaultValues: {
      name: existing?.name ?? '',
      subject_kind: existing?.subject_kind ?? 'agent',
      subject_ref: existing?.subject_ref ?? '',
      trigger_kind: existing?.trigger_kind ?? 'cron',
      cadence_spec: existing?.cadence_spec ?? '',
      expected_interval_seconds:
        existing?.expected_interval_seconds ?? MIN_INTERVAL,
      grace_factor: existing?.grace_factor ?? 2,
    },
  })

  const createMut = useMutation({
    mutationFn: (values: ScheduleForm) =>
      orchestrationApi.createSchedule({
        name: values.name,
        subject_kind: values.subject_kind,
        subject_ref: values.subject_ref,
        trigger_kind: values.trigger_kind,
        cadence_spec: values.cadence_spec,
        expected_interval_seconds: values.expected_interval_seconds,
        grace_factor: values.grace_factor,
      }),
    onSuccess: () => {
      toast.success(t('schedules.form.created'))
      void qc.invalidateQueries({
        queryKey: orchestrationKeys.schedules(activeTenant),
      })
      form.reset()
      onOpenChange(false)
    },
    onError: (error: unknown) =>
      toast.error(t('schedules.form.createFailed'), {
        description: error instanceof Error ? error.message : undefined,
      }),
  })

  const updateMut = useMutation({
    mutationFn: (values: ScheduleForm) => {
      if (!existing) throw new Error('schedule id is required')
      // Only the fields the operator actually touched ride the PATCH: the
      // backend treats a present subject_ref/cadence_spec as a retarget (it
      // clears the sticky cadence-miss and voids a stale fire approval via the
      // plan_hash), so sending unchanged values would silently erase evidence
      // and pollute the audit trail.
      const dirty = form.formState.dirtyFields
      const body: PatchScheduleInput = {}
      if (dirty.subject_ref) body.subject_ref = values.subject_ref
      if (dirty.cadence_spec) body.cadence_spec = values.cadence_spec
      if (dirty.expected_interval_seconds)
        body.expected_interval_seconds = values.expected_interval_seconds
      if (dirty.grace_factor) body.grace_factor = values.grace_factor
      return orchestrationApi.updateSchedule(existing.id, body)
    },
    onSuccess: () => {
      toast.success(t('schedules.form.updated'))
      if (existing) {
        void qc.invalidateQueries({
          queryKey: orchestrationKeys.schedule(activeTenant, existing.id),
        })
      }
      void qc.invalidateQueries({
        queryKey: orchestrationKeys.schedules(activeTenant),
      })
      onOpenChange(false)
    },
    onError: (error: unknown) =>
      toast.error(t('schedules.form.updateFailed'), {
        description: error instanceof Error ? error.message : undefined,
      }),
  })

  const triggerKind = form.watch('trigger_kind')
  const pending = createMut.isPending || updateMut.isPending
  const nameError = form.formState.errors.name
    ? t('schedules.form.errors.required')
    : undefined
  const subjectError = form.formState.errors.subject_ref
    ? t('schedules.form.errors.required')
    : undefined
  const cadenceError = form.formState.errors.cadence_spec
    ? t('schedules.form.errors.required')
    : undefined
  const intervalError = form.formState.errors.expected_interval_seconds
    ? triggerKind === 'cron'
      ? t('schedules.form.errors.intervalRange', {
          min: MIN_INTERVAL,
          max: MAX_INTERVAL,
        })
      : t('schedules.form.errors.intervalCronOnly')
    : undefined
  const graceError = form.formState.errors.grace_factor
    ? t('schedules.form.errors.graceRange', { min: 1, max: 10 })
    : undefined

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (pending) return
        // Closing without saving (Escape/click-outside/Cancel) discards the
        // draft — a reopened "New schedule" must start blank.
        if (!next) form.reset()
        onOpenChange(next)
      }}
    >
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {isEdit
              ? t('schedules.form.editTitle')
              : t('schedules.form.createTitle')}
          </DialogTitle>
          <DialogDescription>
            {isEdit
              ? t('schedules.form.editDescription')
              : t('schedules.form.createDescription')}
          </DialogDescription>
        </DialogHeader>

        <form
          className="flex flex-col gap-3"
          onSubmit={form.handleSubmit((values) => {
            if (isEdit) updateMut.mutate(values)
            else createMut.mutate(values)
          })}
        >
          <Field
            label={t('schedules.form.name')}
            description={isEdit ? t('schedules.form.immutableHint') : undefined}
            required
            error={nameError}
          >
            {({ id, ...aria }) =>
              isEdit ? (
                <>
                  <Input
                    id={id}
                    disabled
                    value={form.watch('name')}
                    {...aria}
                  />
                  <input type="hidden" {...form.register('name')} />
                </>
              ) : (
                <Input id={id} {...aria} {...form.register('name')} />
              )
            }
          </Field>

          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              label={t('schedules.form.subjectKind')}
              description={
                isEdit ? t('schedules.form.immutableHint') : undefined
              }
              required
            >
              {({ id, ...aria }) => (
                <>
                  <input type="hidden" {...form.register('subject_kind')} />
                  <Select
                    value={form.watch('subject_kind')}
                    disabled={isEdit}
                    onValueChange={(value) =>
                      form.setValue('subject_kind', value as SubjectKind, {
                        shouldValidate: true,
                      })
                    }
                  >
                    <SelectTrigger id={id} {...aria}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(['agent', 'swarm'] as SubjectKind[]).map((kind) => (
                        <SelectItem key={kind} value={kind}>
                          {t(`schedules.subjectKind.${kind}`)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </>
              )}
            </Field>

            <Field
              label={t('schedules.form.triggerKind')}
              description={
                isEdit ? t('schedules.form.immutableHint') : undefined
              }
              required
            >
              {({ id, ...aria }) => (
                <>
                  <input type="hidden" {...form.register('trigger_kind')} />
                  <Select
                    value={triggerKind}
                    disabled={isEdit}
                    onValueChange={(value) => {
                      const trigger = value as TriggerKind
                      form.setValue('trigger_kind', trigger, {
                        shouldValidate: true,
                      })
                      form.setValue(
                        'expected_interval_seconds',
                        trigger === 'cron' ? MIN_INTERVAL : 0,
                        { shouldValidate: true },
                      )
                    }}
                  >
                    <SelectTrigger id={id} {...aria}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(['cron', 'event', 'manual'] as TriggerKind[]).map(
                        (kind) => (
                          <SelectItem key={kind} value={kind}>
                            {t(`schedules.trigger.${kind}`)}
                          </SelectItem>
                        ),
                      )}
                    </SelectContent>
                  </Select>
                </>
              )}
            </Field>
          </div>

          <Field
            label={t('schedules.form.subjectRef')}
            required
            error={subjectError}
          >
            {({ id, ...aria }) => (
              <Input id={id} mono {...aria} {...form.register('subject_ref')} />
            )}
          </Field>

          <Field
            label={t('schedules.form.cadenceSpec')}
            description={t(`schedules.form.cadenceHint.${triggerKind}`)}
            required={triggerKind !== 'manual'}
            error={cadenceError}
          >
            {({ id, ...aria }) => (
              <Textarea
                id={id}
                rows={2}
                className="font-mono"
                {...aria}
                {...form.register('cadence_spec')}
              />
            )}
          </Field>

          <div className="grid gap-3 sm:grid-cols-2">
            {triggerKind === 'cron' ? (
              <Field
                label={t('schedules.form.expectedInterval')}
                description={t('schedules.form.expectedIntervalHint')}
                required
                error={intervalError}
              >
                {({ id, ...aria }) => (
                  <Input
                    id={id}
                    type="number"
                    min={MIN_INTERVAL}
                    max={MAX_INTERVAL}
                    {...aria}
                    {...form.register('expected_interval_seconds')}
                  />
                )}
              </Field>
            ) : null}

            <Field
              label={t('schedules.form.graceFactor')}
              description={t('schedules.form.graceFactorHint')}
              required
              error={graceError}
            >
              {({ id, ...aria }) => (
                <Input
                  id={id}
                  type="number"
                  min={1}
                  max={10}
                  {...aria}
                  {...form.register('grace_factor')}
                />
              )}
            </Field>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              disabled={pending}
              onClick={() => {
                form.reset()
                onOpenChange(false)
              }}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={pending || (isEdit && !form.formState.isDirty)}
            >
              {isEdit ? t('schedules.form.update') : t('schedules.form.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- governed two-phase fire dialog ------------------------------------------

function FireScheduleDialog({
  schedule,
  initialResult,
  onResult,
  onClose,
}: {
  schedule: ScheduleDTO
  initialResult?: FireScheduleResponse
  onResult: (result: FireScheduleResponse) => void
  onClose: () => void
}) {
  const { t } = useTranslation(['orchestration', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [result, setResult] = useState<FireScheduleResponse | undefined>(
    initialResult,
  )
  const [approvalRef, setApprovalRef] = useState(
    initialResult?.approval_ref ?? '',
  )

  function preserveResult(next: FireScheduleResponse) {
    setResult(next)
    if (next.approval_ref) setApprovalRef(next.approval_ref)
    onResult(next)
  }

  function invalidateFireReads() {
    void qc.invalidateQueries({
      queryKey: orchestrationKeys.schedules(activeTenant),
    })
    void qc.invalidateQueries({
      queryKey: orchestrationKeys.schedule(activeTenant, schedule.id),
    })
    void qc.invalidateQueries({
      queryKey: orchestrationKeys.scheduleDecisions(activeTenant, schedule.id),
    })
  }

  const requestMut = useMutation({
    mutationFn: () => orchestrationApi.fireSchedule(schedule.id),
    onSuccess: (next) => {
      preserveResult(next)
      invalidateFireReads()
      toast.success(t('schedules.fire.requested'))
    },
    onError: (error: unknown) =>
      toast.error(t('schedules.fire.requestFailed'), {
        description: error instanceof Error ? error.message : undefined,
      }),
  })

  const executeMut = useMutation({
    mutationFn: () =>
      orchestrationApi.fireSchedule(schedule.id, {
        approval_ref: approvalRef.trim(),
      }),
    onSuccess: (next) => {
      preserveResult(next)
      invalidateFireReads()
      if (next.op_status === 'dispatched') {
        toast.success(t('schedules.fire.dispatched'))
      } else {
        toast.error(t('schedules.fire.notDispatched'))
      }
    },
    onError: async (error: unknown) => {
      invalidateFireReads()
      // The shared JSON client intentionally turns non-2xx responses into ApiError
      // and does not expose their body. A declined phase-2 fire is still appended to
      // the schedule ledger, so recover that authoritative receipt for the dialog.
      try {
        const ledger = await orchestrationApi.scheduleDecisions(schedule.id)
        const latestFire = ledger.items
          .filter((decision) => decision.op === 'fire')
          .sort((a, b) => b.occurred_at.localeCompare(a.occurred_at))[0]
        // ⛔ NO BASTA CON DESCARTAR `missed`, y antes sí bastaba. `DecisionOpStatus` se
        //    ensanchó con los cuatro estados de workflow, así que descartar uno solo ya no
        //    deja un `FireOpStatus`. Se comprueba contra el conjunto REAL en vez de forzar
        //    el tipo: un `cast` haría pasar un estado de workflow a un diálogo de fire y lo
        //    pintaría como si fuera el recibo de esta operación.
        if (latestFire && esEstadoDeFire(latestFire.op_status)) {
          preserveResult({
            op: latestFire.op,
            op_status: latestFire.op_status,
            plan_hash: latestFire.plan_hash,
            approval_ref: latestFire.approval_ref ?? approvalRef.trim(),
            gate_status: latestFire.gate_status,
            dispatch_ref: latestFire.dispatch_ref,
            detail: latestFire.result,
          })
        }
      } catch {
        // The mutation error remains the primary failure; the receipt refresh is
        // best-effort because permissions or connectivity may also block the GET.
      }
      toast.error(t('schedules.fire.executeFailed'), {
        description: error instanceof Error ? error.message : undefined,
      })
    },
  })

  const pending = requestMut.isPending || executeMut.isPending

  async function copyApprovalRef() {
    if (!approvalRef) return
    try {
      await navigator.clipboard.writeText(approvalRef)
      toast.success(t('schedules.fire.copied'))
    } catch {
      toast.error(t('schedules.fire.copyFailed'))
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {t('schedules.fire.title', { name: schedule.name })}
          </DialogTitle>
          <DialogDescription>
            {t('schedules.fire.description')}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-4">
          <section className="flex flex-col gap-2 rounded-lg border border-border p-3">
            <h3 className="text-sm font-medium">
              {t('schedules.fire.step1Title')}
            </h3>
            <p className="text-xs text-muted-foreground">
              {t('schedules.fire.step1Description')}
            </p>
            <Button
              type="button"
              variant="secondary"
              className="self-start"
              disabled={pending}
              onClick={() => requestMut.mutate()}
            >
              {t('schedules.fire.request')}
            </Button>

            {result?.approval_ref ? (
              <div className="flex flex-col gap-2 rounded-md bg-muted p-3">
                <div className="flex flex-wrap items-center gap-2">
                  {result.requires_approval ? (
                    <Badge variant="warning">
                      {t('schedules.fire.requiresApproval')}
                    </Badge>
                  ) : null}
                  <span className="font-mono text-xs">
                    {result.approval_ref}
                  </span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => void copyApprovalRef()}
                  >
                    <Copy />
                    {t('schedules.fire.copy')}
                  </Button>
                </div>
                <Link
                  to={'/permissions' as never}
                  className="text-sm text-accent-text underline-offset-4 hover:underline"
                >
                  {t('schedules.fire.governanceLink')}
                </Link>
              </div>
            ) : null}
          </section>

          <section className="flex flex-col gap-2 rounded-lg border border-border p-3">
            <h3 className="text-sm font-medium">
              {t('schedules.fire.step2Title')}
            </h3>
            <p className="text-xs text-muted-foreground">
              {t('schedules.fire.step2Description')}
            </p>
            <Field label={t('schedules.fire.approvalRef')} required>
              {({ id, ...aria }) => (
                <Input
                  id={id}
                  mono
                  value={approvalRef}
                  onChange={(event) => setApprovalRef(event.target.value)}
                  {...aria}
                />
              )}
            </Field>
            <Button
              type="button"
              variant="primary"
              className="self-start"
              disabled={approvalRef.trim() === '' || pending}
              onClick={() => executeMut.mutate()}
            >
              {t('schedules.fire.execute')}
            </Button>
          </section>

          {result ? (
            <section className="flex flex-col gap-2 rounded-lg border border-border p-3">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="text-sm font-medium">
                  {t('schedules.fire.receipt')}
                </h3>
                <Badge
                  variant={
                    result.op_status === 'dispatched'
                      ? 'success'
                      : result.op_status === 'requested'
                        ? 'info'
                        : 'warning'
                  }
                >
                  {t(`schedules.fire.opStatus.${result.op_status}`)}
                </Badge>
              </div>
              {result.plan_hash ? (
                <HashChip
                  hash={result.plan_hash}
                  label={t('schedules.fire.planHash')}
                />
              ) : null}
              <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
                <dt className="text-muted-foreground">
                  {t('schedules.fire.gateStatus')}
                </dt>
                <dd>{result.gate_status}</dd>
                {result.dispatch_ref ? (
                  <>
                    <dt className="text-muted-foreground">
                      {t('schedules.fire.dispatchRef')}
                    </dt>
                    <dd className="font-mono">{result.dispatch_ref}</dd>
                  </>
                ) : null}
                {result.detail ? (
                  <>
                    <dt className="text-muted-foreground">
                      {t('schedules.fire.detail')}
                    </dt>
                    <dd>{result.detail}</dd>
                  </>
                ) : null}
              </dl>
            </section>
          ) : null}
        </div>

        <DialogFooter>
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common:actions.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** Los estados que puede llevar una decisión `fire`. Se declara en vez de derivarse de un
 *  descarte: la unión de `DecisionOpStatus` incluye además los de workflow y los de
 *  cadencia, que nunca son el recibo de un fire. */
const ESTADOS_DE_FIRE = new Set<string>([
  'requested',
  'blocked',
  'dispatched',
  'declared_not_fired',
  'failed',
  'budget_blocked',
  'budget_throttled',
])

function esEstadoDeFire(s: DecisionOpStatus): s is FireOpStatus {
  return ESTADOS_DE_FIRE.has(s)
}
