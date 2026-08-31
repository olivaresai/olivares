// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  History,
  Pencil,
  PlayCircle,
  ScanSearch,
  Trash2,
  Upload,
  XCircle,
} from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { SecretRef } from '@/components/data/secret-ref'
import { StatusBadge } from '@/components/data/badges'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  useFailedActionReporter,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { deployApi, deployKeys } from './api'
import { ChangeList, GateBadge } from './diff'
import { DefinitionEditorDialog } from './definition-editor'
import { RevisionsSheet } from './revisions'
import './i18n'
import type {
  DefinitionDTO,
  MutationResponse,
  PlanResponse,
  VerifyResponse,
} from './types'

function Section({
  title,
  caption,
  action,
  children,
}: {
  title: ReactNode
  caption?: ReactNode
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium text-foreground">{title}</h3>
        {action}
      </div>
      {caption && <p className="text-xs text-muted-foreground">{caption}</p>}
      {children}
    </section>
  )
}

export interface DefinitionDetailSheetProps {
  definitionId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function DefinitionDetailSheet({
  definitionId,
  open,
  onOpenChange,
}: DefinitionDetailSheetProps) {
  const { t } = useTranslation(['deploy', 'common'])
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const canWrite = can('deploy:deployment:write')
  const canAdmin = can('deploy:deployment:admin')

  const [editorOpen, setEditorOpen] = useState(false)
  const [revisionsOpen, setRevisionsOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmPlan, setConfirmPlan] = useState(false)
  const [confirmVerify, setConfirmVerify] = useState(false)
  const [confirmApply, setConfirmApply] = useState(false)
  const [confirmRetire, setConfirmRetire] = useState(false)

  // The dry-run plan / verify results to display inline.
  const [planResult, setPlanResult] = useState<PlanResponse | null>(null)
  const [verifyResult, setVerifyResult] = useState<VerifyResponse | null>(null)
  // The pending two-phase apply/retire (phase-1 returned requires_approval).
  const [applyPending, setApplyPending] = useState<MutationResponse | null>(
    null,
  )
  const [retirePending, setRetirePending] = useState<MutationResponse | null>(
    null,
  )

  // While a governed mutation is pending approval, poll the operations ledger for
  // this definition so the latest gate_status/status surfaces (~8s) (spec liveData).
  const pendingApproval = !!applyPending || !!retirePending
  const operationsPoll = useQuery({
    queryKey: deployKeys.operations(activeTenant, {
      definition_id: definitionId,
      live: true,
    }),
    queryFn: () =>
      deployApi.listOperations({ definition_id: definitionId!, limit: 1 }),
    enabled: open && !!definitionId && pendingApproval,
    refetchInterval: pendingApproval ? 8_000 : false,
  })
  const latestGate = operationsPoll.data?.items?.[0]?.gate_status

  const query = useQuery({
    queryKey: deployKeys.definition(activeTenant, definitionId ?? ''),
    queryFn: () => deployApi.getDefinition(definitionId!),
    enabled: open && !!definitionId,
    // Refetch detail periodically while shown live (picks up applied_version /
    // up_to_date / real.status after a completed apply/retire/verify).
    refetchInterval: open ? 20_000 : false,
  })
  const detail = query.data

  function invalidateAll() {
    void queryClient.invalidateQueries({
      queryKey: deployKeys.definition(activeTenant, definitionId ?? ''),
    })
    void queryClient.invalidateQueries({
      queryKey: deployKeys.definitions(activeTenant),
    })
    void queryClient.invalidateQueries({
      queryKey: deployKeys.operations(activeTenant),
    })
  }

  // Plan (dry-run) — privileged write, low risk. Stores the diff to display.
  const planMutation = useMutation<PlanResponse>({
    mutationFn: () => deployApi.plan(definitionId!),
    onSuccess: (data) => {
      setPlanResult(data)
      setConfirmPlan(false)
      toast.success(t('plan.done'))
      void queryClient.invalidateQueries({
        queryKey: deployKeys.operations(activeTenant),
      })
    },
    onError: (err) => reportError(err),
  })

  // Verify — privileged write, low risk. Stores drift to display.
  const verifyMutation = useMutation<VerifyResponse>({
    mutationFn: () => deployApi.verify(definitionId!),
    onSuccess: (data) => {
      setVerifyResult(data)
      setConfirmVerify(false)
      toast.success(t('verify.done'))
      invalidateAll()
    },
    onError: (err) => reportError(err),
  })

  // Apply — admin, HIGH, two-phase HITL.
  const applyMutation = useMutation<
    MutationResponse,
    unknown,
    string | undefined
  >({
    mutationFn: (approvalRef) =>
      deployApi.apply(
        definitionId!,
        approvalRef ? { approval_ref: approvalRef } : {},
      ),
    onSuccess: (data) => {
      if (data.requires_approval && data.status !== 'applied') {
        // Phase 1 (or still pending): surface the pending-approval state and poll.
        setApplyPending(data)
      } else if (data.status === 'blocked') {
        setApplyPending(data)
        toast.warning(t('apply.blocked'))
      } else if (data.status === 'noop') {
        setApplyPending(null)
        setConfirmApply(false)
        toast.success(t('apply.noop'))
        invalidateAll()
      } else {
        setApplyPending(null)
        setConfirmApply(false)
        toast.success(t('apply.applied'))
        invalidateAll()
      }
    },
    onError: (err) => reportError(err),
  })

  // Retire — admin, HIGH, two-phase HITL (mirrors apply).
  const retireMutation = useMutation<
    MutationResponse,
    unknown,
    string | undefined
  >({
    mutationFn: (approvalRef) =>
      deployApi.retire(
        definitionId!,
        approvalRef ? { approval_ref: approvalRef } : {},
      ),
    onSuccess: (data) => {
      if (data.requires_approval && data.status !== 'retired') {
        setRetirePending(data)
      } else if (data.status === 'blocked') {
        setRetirePending(data)
        toast.warning(t('retire.blocked'))
      } else {
        setRetirePending(null)
        setConfirmRetire(false)
        toast.success(t('retire.retired'))
        invalidateAll()
      }
    },
    onError: (err) => reportError(err),
  })

  // apply/retire are the one AAL3-gated pair reachable from a hand-rolled handler:
  // modules/deploy/helpers.go:74 refuses a human session below AAL3 with the
  // machine-readable `step_up_required` code. This local helper collapsed that
  // into "your role can't perform this action" and titled every other failure
  // with `common:states.empty` — an EMPTY-state string used as an error title.
  // Both are the shared policy's job now.
  const reportError = useFailedActionReporter('deploy')

  // Delete — write, danger (server enforces the 409-while-applied safety).
  const remove = usePrivilegedMutation({
    mutationFn: () => deployApi.deleteDefinition(definitionId!),
    invalidateKeys: () => [deployKeys.definitions(activeTenant)],
    successMessage: t('remove.done'),
    onDone: () => {
      setConfirmDelete(false)
      onOpenChange(false)
    },
  })

  const retired = detail?.desired_status === 'retired'

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{detail?.name ?? t('detail.title')}</SheetTitle>
          {detail && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <Badge variant="neutral">{detail.environment}</Badge>
              <Badge variant="outline">{detail.subject_kind}</Badge>
              <StatusBadge status={detail.desired_status} />
              <SyncBadge definition={detail} />
            </SheetDescription>
          )}
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {query.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-16 w-full" />
              ))}
            </div>
          ) : query.error instanceof ApiError &&
            query.error.isStepUpRequired ? (
            // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
            // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que leerlo
            // primero sustituía la pantalla por «no tienes autorización» —falso, y sin salida—.
            //
            // DEFENSA EN PROFUNDIDAD, y lo digo porque en esta campaña ya presenté dos veces como
            // «camino vivo» algo que no lo era: HOY esta ruta no emite el código. Los emisores medidos
            // son las dos escrituras de `modules/governance` y las 21 llamadas a `requireAAL3` de
            // `core/api`, todas cubiertas ya. Esto se arregla porque el defecto es de FORMA y sobrevive
            // al día en que el gate llegue aquí, no porque alguien lo esté sufriendo ahora.
            <StepUpRequiredState
              action="generic"
              onElevated={() => void query.refetch()}
            />
          ) : query.error instanceof ApiError && query.error.isForbidden ? (
            <ForbiddenState />
          ) : query.error || !detail ? (
            <ErrorState retry={() => query.refetch()} />
          ) : (
            <div className="flex flex-col gap-5">
              <LifecycleActions
                canWrite={canWrite}
                canAdmin={canAdmin}
                retired={retired}
                pendingApproval={pendingApproval}
                onPlan={() => setConfirmPlan(true)}
                onVerify={() => setConfirmVerify(true)}
                onApply={() => {
                  setApplyPending(null)
                  setConfirmApply(true)
                }}
                onRetire={() => {
                  setRetirePending(null)
                  setConfirmRetire(true)
                }}
                onEdit={() => setEditorOpen(true)}
                onRollback={() => setRevisionsOpen(true)}
                onDelete={() => setConfirmDelete(true)}
              />

              <p className="rounded-md border border-info-line bg-info-soft px-3 py-2 text-xs text-info">
                {t('detail.controlPlaneNote')}
              </p>

              {planResult && (
                <PlanResultPanel
                  result={planResult}
                  onClear={() => setPlanResult(null)}
                />
              )}
              {verifyResult && (
                <VerifyResultPanel
                  result={verifyResult}
                  onClear={() => setVerifyResult(null)}
                />
              )}

              <DetailBody detail={detail} latestGate={latestGate} />
            </div>
          )}
        </ScrollArea>
      </SheetContent>

      {/* Editor (edit existing definition). */}
      {detail && (
        <DefinitionEditorDialog
          open={editorOpen}
          onOpenChange={setEditorOpen}
          definition={detail}
        />
      )}

      {/* Versions & rollback. */}
      {detail && (
        <RevisionsSheet
          open={revisionsOpen}
          onOpenChange={setRevisionsOpen}
          definitionId={detail.id}
          name={detail.name}
          currentVersion={detail.current_version}
          canWrite={canWrite}
        />
      )}

      {/* Plan (low risk, default tone). */}
      <ConfirmDialog
        open={confirmPlan}
        onOpenChange={setConfirmPlan}
        title={t('plan.title')}
        description={t('plan.body')}
        confirmLabel={t('plan.confirm')}
        pending={planMutation.isPending}
        onConfirm={() => planMutation.mutate()}
      />

      {/* Verify (low risk, default tone). */}
      <ConfirmDialog
        open={confirmVerify}
        onOpenChange={setConfirmVerify}
        title={t('verify.title')}
        description={t('verify.body')}
        confirmLabel={t('verify.confirm')}
        pending={verifyMutation.isPending}
        onConfirm={() => verifyMutation.mutate()}
      />

      {/* Apply (HIGH, danger, two-phase HITL). */}
      <ConfirmDialog
        open={confirmApply}
        onOpenChange={(o) => {
          if (!o) {
            setConfirmApply(false)
            setApplyPending(null)
          }
        }}
        title={t('apply.title')}
        description={t('apply.body')}
        tone="danger"
        confirmPhrase="APPLY"
        confirmLabel={
          applyPending ? t('apply.confirmExecute') : t('apply.confirm')
        }
        pending={applyMutation.isPending}
        onConfirm={() => applyMutation.mutate(applyPending?.approval_ref)}
      >
        <GovernedBody
          detail={detail}
          pending={applyPending}
          latestGate={latestGate}
        />
      </ConfirmDialog>

      {/* Retire (HIGH, danger + typed phrase, two-phase HITL). */}
      <ConfirmDialog
        open={confirmRetire}
        onOpenChange={(o) => {
          if (!o) {
            setConfirmRetire(false)
            setRetirePending(null)
          }
        }}
        title={t('retire.title')}
        description={t('retire.body')}
        tone="danger"
        confirmPhrase={t('retire.phrase')}
        confirmLabel={
          retirePending ? t('retire.confirmExecute') : t('retire.confirm')
        }
        pending={retireMutation.isPending}
        onConfirm={() => retireMutation.mutate(retirePending?.approval_ref)}
      >
        <p className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-xs text-warning">
          {t('retire.edgesNotice')}
        </p>
        <GovernedBody
          detail={detail}
          pending={retirePending}
          latestGate={latestGate}
        />
      </ConfirmDialog>

      {/* Delete (write-tier, danger). */}
      {detail && (
        <ConfirmDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={t('remove.title')}
          description={t('remove.body', { name: detail.name })}
          tone="danger"
          confirmLabel={t('remove.confirm')}
          pending={remove.isPending}
          onConfirm={() => remove.mutate(undefined)}
        />
      )}
    </Sheet>
  )
}

/** The body shown inside the Apply / Retire confirm — the bound plan and, once
 * phase-1 returns, the pending-approval state plus the latest polled gate. */
function GovernedBody({
  detail,
  pending,
  latestGate,
}: {
  detail: DefinitionDTO | undefined
  pending: MutationResponse | null
  latestGate?: string
}) {
  const { t } = useTranslation('deploy')
  return (
    <div className="mt-1 flex flex-col gap-2">
      {/* The plan_hash the approval binds to (anti-TOCTOU). */}
      {(pending?.plan_hash ?? detail?.spec_hash) && (
        <p className="font-mono text-xs text-muted-foreground">
          {t('apply.planHashNotice', {
            hash: (pending?.plan_hash ?? detail?.spec_hash ?? '').slice(0, 12),
          })}
        </p>
      )}

      {pending?.changes && pending.changes.length > 0 && (
        <ChangeList changes={pending.changes} />
      )}

      {pending?.requires_approval && (
        <div className="rounded-md border border-info-line bg-info-soft px-3 py-2 text-xs text-info">
          <p className="font-medium">{t('apply.pendingTitle')}</p>
          <p className="mt-0.5">{t('apply.pendingBody')}</p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            {pending.approval_ref && (
              <span className="font-mono">
                {t('apply.approvalRef')}: {pending.approval_ref}
              </span>
            )}
            <GateBadge gate={latestGate ?? pending.gate_status} />
          </div>
        </div>
      )}

      {/* Gate status is reported for DISPLAY ONLY — never an authorization input. */}
      <p className="text-xs text-muted-foreground">{t('gate.displayNotice')}</p>
    </div>
  )
}

function LifecycleActions({
  canWrite,
  canAdmin,
  retired,
  pendingApproval,
  onPlan,
  onVerify,
  onApply,
  onRetire,
  onEdit,
  onRollback,
  onDelete,
}: {
  canWrite: boolean
  canAdmin: boolean
  retired: boolean
  /** A governed apply/retire is awaiting approval — lock spec-mutating actions so
   * the operator can't change the spec and invalidate the pending plan_hash. */
  pendingApproval: boolean
  onPlan: () => void
  onVerify: () => void
  onApply: () => void
  onRetire: () => void
  onEdit: () => void
  onRollback: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation('deploy')
  const lockTitle = pendingApproval ? t('apply.pendingTitle') : undefined
  return (
    <div className="flex flex-wrap gap-2">
      {canWrite && (
        <Button variant="secondary" size="sm" onClick={onPlan}>
          <ScanSearch />
          {t('detail.actions.plan')}
        </Button>
      )}
      {canWrite && (
        <Button variant="secondary" size="sm" onClick={onVerify}>
          <PlayCircle />
          {t('detail.actions.verify')}
        </Button>
      )}
      {/* Apply / Retire are admin-tier and HIGH risk. Disabled (not hidden) for
          non-admins, per the doc §6, so the affordance is visible-but-gated. */}
      <Button
        variant="primary"
        size="sm"
        onClick={onApply}
        disabled={!canAdmin || retired}
        title={!canAdmin ? t('common:privileged.notAuthorized') : undefined}
      >
        <Upload />
        {t('detail.actions.apply')}
      </Button>
      <Button
        variant="destructive"
        size="sm"
        onClick={onRetire}
        disabled={!canAdmin || retired}
        title={!canAdmin ? t('common:privileged.notAuthorized') : undefined}
      >
        <XCircle />
        {t('detail.actions.retire')}
      </Button>

      <span className="flex-1" />

      <Button
        variant="ghost"
        size="sm"
        onClick={onRollback}
        disabled={pendingApproval}
        title={lockTitle}
      >
        <History />
        {t('detail.actions.rollback')}
      </Button>
      {canWrite && (
        <Button
          variant="ghost"
          size="sm"
          onClick={onEdit}
          disabled={pendingApproval}
          title={lockTitle}
        >
          <Pencil />
          {t('detail.actions.edit')}
        </Button>
      )}
      {canWrite && (
        <Button
          variant="destructive"
          size="sm"
          onClick={onDelete}
          disabled={pendingApproval}
          title={lockTitle}
        >
          <Trash2 />
          {t('detail.actions.delete')}
        </Button>
      )}
    </div>
  )
}

function PlanResultPanel({
  result,
  onClear,
}: {
  result: PlanResponse
  onClear: () => void
}) {
  const { t } = useTranslation('deploy')
  return (
    <Section
      title={t('plan.resultTitle')}
      action={
        <Button variant="ghost" size="sm" onClick={onClear}>
          {t('common:actions.close')}
        </Button>
      }
    >
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <Badge variant="neutral">
          {t('plan.fromTo', {
            from: result.from_version,
            to: result.to_version,
          })}
        </Badge>
        <span className="font-mono">
          {t('plan.planHash')}: {result.plan_hash.slice(0, 12)}
        </span>
      </div>
      {result.up_to_date || result.changes.length === 0 ? (
        <p className="text-sm text-success">{t('plan.upToDate')}</p>
      ) : (
        <ChangeList changes={result.changes} />
      )}
    </Section>
  )
}

function VerifyResultPanel({
  result,
  onClear,
}: {
  result: VerifyResponse
  onClear: () => void
}) {
  const { t } = useTranslation('deploy')
  return (
    <Section
      title={t('verify.resultTitle')}
      action={
        <Button variant="ghost" size="sm" onClick={onClear}>
          {t('common:actions.close')}
        </Button>
      }
    >
      {result.in_sync || result.drift.length === 0 ? (
        <p className="text-sm text-success">{t('verify.inSync')}</p>
      ) : (
        <ChangeList changes={result.drift} />
      )}
    </Section>
  )
}

function SyncBadge({ definition }: { definition: DefinitionDTO }) {
  const { t } = useTranslation('deploy')
  if (definition.applied_version === 0) {
    return (
      <Badge variant="warning" title={t('sync.neverAppliedHint')}>
        {t('sync.neverApplied')}
      </Badge>
    )
  }
  if (!definition.up_to_date) {
    return (
      <Badge variant="warning" title={t('sync.pendingHint')}>
        {t('sync.pending')}
      </Badge>
    )
  }
  return <Badge variant="success">{t('sync.upToDate')}</Badge>
}

function DetailBody({
  detail,
  latestGate,
}: {
  detail: DefinitionDTO
  latestGate?: string
}) {
  const { t } = useTranslation('deploy')
  const spec = detail.spec
  void latestGate

  return (
    <>
      {/* Overview. */}
      <Section title={t('detail.overview')}>
        <KvList>
          <KvRow label={t('detail.subjectKind')}>{detail.subject_kind}</KvRow>
          <KvRow label={t('detail.subjectRef')} mono>
            {detail.subject_ref}
          </KvRow>
          <KvRow label={t('detail.target')} mono>
            {detail.target}
          </KvRow>
          {detail.runtime && (
            <KvRow label={t('detail.runtime')}>{detail.runtime}</KvRow>
          )}
          <KvRow label={t('detail.currentVersion')} mono>
            {detail.current_version}
          </KvRow>
          <KvRow label={t('detail.appliedVersion')} mono>
            {detail.applied_version}
          </KvRow>
          {detail.spec_hash && (
            <KvRow label={t('detail.specHash')} mono>
              {detail.spec_hash.slice(0, 16)}
            </KvRow>
          )}
          {detail.source_ref && (
            <KvRow label={t('detail.sourceRef')} mono align="start">
              {detail.source_ref}
            </KvRow>
          )}
        </KvList>
      </Section>

      {/* Real state (applied snapshot). */}
      <Separator />
      <Section
        title={t('detail.realState')}
        caption={t('detail.realStateHint')}
      >
        {detail.real ? (
          <KvList>
            <KvRow label={t('detail.realStatus')}>
              <StatusBadge status={detail.real.status} />
            </KvRow>
            {detail.real.version && (
              <KvRow label={t('detail.realVersion')} mono>
                {detail.real.version}
              </KvRow>
            )}
            {detail.real.deployed_at && (
              <KvRow label={t('detail.deployedAt')}>
                <RelTimeLabel ts={detail.real.deployed_at} />
              </KvRow>
            )}
          </KvList>
        ) : (
          <EmptyState
            title={t('detail.notApplied')}
            description={t('detail.notAppliedHint')}
          />
        )}
      </Section>

      {/* Desired spec. */}
      <Separator />
      <Section title={t('detail.desiredSpec')}>
        {!spec ? (
          <p className="text-sm text-muted-foreground">{t('detail.noSpec')}</p>
        ) : (
          <div className="flex flex-col gap-4">
            <KvList>
              {spec.image && (
                <KvRow label={t('detail.image')} mono align="start">
                  {spec.image}
                </KvRow>
              )}
              {spec.command && (
                <KvRow label={t('detail.command')} mono align="start">
                  {spec.command}
                </KvRow>
              )}
              {spec.replicas != null && (
                <KvRow label={t('detail.replicas')} mono>
                  {spec.replicas}
                </KvRow>
              )}
            </KvList>

            {spec.resources && Object.keys(spec.resources).length > 0 && (
              <div className="flex flex-col gap-1.5">
                <p className="text-xs font-medium text-foreground">
                  {t('detail.resources')}
                </p>
                <div className="flex flex-wrap gap-1.5">
                  {Object.entries(spec.resources).map(([key, value]) => (
                    <Badge key={key} variant="outline" className="font-mono">
                      {key}: {value}
                    </Badge>
                  ))}
                </div>
              </div>
            )}

            {/* Env references — secret REFERENCES only, never values. */}
            {spec.env_refs && spec.env_refs.length > 0 && (
              <div className="flex flex-col gap-1.5">
                <p className="text-xs font-medium text-foreground">
                  {t('detail.envRefs')}
                </p>
                <p className="text-xs text-muted-foreground">
                  {t('detail.envRefsHint')}
                </p>
                <ul className="flex flex-col gap-1.5">
                  {spec.env_refs.map((e) => (
                    <li
                      key={e.name}
                      className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs"
                    >
                      <span className="font-mono text-foreground">
                        {e.name}
                      </span>
                      <SecretRef name={e.secret_ref} />
                    </li>
                  ))}
                </ul>
              </div>
            )}

            {/* Wirings declared in the spec. */}
            {spec.wirings && spec.wirings.length > 0 && (
              <div className="flex flex-col gap-1.5">
                <p className="text-xs font-medium text-foreground">
                  {t('detail.wirings')}
                </p>
                <p className="text-xs text-muted-foreground">
                  {t('detail.wiringsHint')}
                </p>
                <ul className="flex flex-col gap-1.5">
                  {spec.wirings.map((w, i) => (
                    <li
                      key={`${w.resource_kind}:${w.resource_ref}:${i}`}
                      className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs"
                    >
                      <Badge variant="neutral">{w.resource_kind}</Badge>
                      <span className="font-mono text-foreground">
                        {w.resource_ref}
                      </span>
                      <Badge variant="outline">
                        {t(`spec.mode.${w.mode}`, { defaultValue: w.mode })}
                      </Badge>
                      {w.secret_ref && <SecretRef name={w.secret_ref} />}
                    </li>
                  ))}
                </ul>
              </div>
            )}

            {/* Identity intent. */}
            {spec.identity && (
              <div className="flex flex-col gap-1.5">
                <p className="text-xs font-medium text-foreground">
                  {t('detail.identity')}
                </p>
                <KvList>
                  {spec.identity.identity_ref && (
                    <KvRow label={t('detail.identityRef')} mono>
                      {spec.identity.identity_ref}
                    </KvRow>
                  )}
                  {spec.identity.mint && (
                    <KvRow label={t('detail.identityMint')}>
                      <span aria-hidden="true">✓</span>
                      <span className="sr-only">{t('common:states.yes')}</span>
                    </KvRow>
                  )}
                </KvList>
              </div>
            )}
          </div>
        )}
      </Section>
    </>
  )
}
