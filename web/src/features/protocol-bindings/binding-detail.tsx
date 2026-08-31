// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { AsyncSection } from '@/features/_intel'
import type { TenantRequestOptions } from '@/lib/api/client'
import { useAuth } from '@/lib/auth/context'
import {
  getProtocolBinding,
  isProtocolConflict,
  isProtocolUnknown,
  protocolBindingKeys,
  protocolErrorCode,
  protocolVerdictOfError,
  reconcileProtocolBinding,
} from './api'
import { BrokenNotice, ProtocolVerdictBadge, UnknownNotice } from './status'
import type {
  ProtocolBinding,
  ProtocolBindingAssessment,
  ProtocolBindingReconcilePlan,
  ProtocolReconcileOutcome,
} from './types'

export function ProtocolBindingDetailSheet({
  bindingId,
  onOpenChange,
  onChanged,
}: {
  bindingId: string | null
  onOpenChange: (open: boolean) => void
  onChanged: (tenant: string | null) => void
}) {
  const { t } = useTranslation('protocolBindings')
  const { activeTenant, can } = useAuth()
  const [reconcileOperation, setReconcileOperation] = useState<{
    bindingId: string
    request: TenantRequestOptions
  } | null>(null)
  const query = useQuery({
    queryKey: protocolBindingKeys.binding(activeTenant, bindingId ?? ''),
    queryFn: ({ signal }) =>
      getProtocolBinding(bindingId as string, { tenant: activeTenant }, signal),
    enabled: !!bindingId,
  })

  return (
    <Sheet open={!!bindingId} onOpenChange={onOpenChange}>
      <SheetContent className="max-w-3xl overflow-y-auto">
        <SheetHeader>
          <SheetTitle>{t('detail.bindingTitle')}</SheetTitle>
          <SheetDescription>{t('detail.bindingDescription')}</SheetDescription>
        </SheetHeader>
        <AsyncSection query={query} skeletonHeight={240}>
          {({ binding, etag }) => (
            <div className="space-y-5 py-2">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-sm font-medium">
                  {binding.id}
                </span>
                <ProtocolVerdictBadge verdict={binding.observation_verdict} />
                <Badge variant="outline">
                  {binding.protocol.toUpperCase()} {binding.protocol_version}
                </Badge>
                <Badge variant={binding.terminal ? 'neutral' : 'info'}>
                  {binding.terminal
                    ? t('binding.terminal')
                    : t('binding.nonTerminal')}
                </Badge>
              </div>
              <BindingOverview binding={binding} etag={etag} />
              <BindingEvidence binding={binding} />
              {can('sessions:protocol-binding:write') ? (
                <div className="border-t border-border pt-4">
                  <Button
                    type="button"
                    size="sm"
                    onClick={() =>
                      setReconcileOperation({
                        bindingId: binding.id,
                        request: { tenant: activeTenant },
                      })
                    }
                  >
                    {t('actions.reconcile')}
                  </Button>
                </div>
              ) : null}
              {reconcileOperation ? (
                <ProtocolBindingReconcileDialog
                  open
                  bindingId={reconcileOperation.bindingId}
                  request={reconcileOperation.request}
                  onOpenChange={(open) => {
                    if (!open) setReconcileOperation(null)
                  }}
                  onApplied={() => {
                    if (activeTenant === reconcileOperation.request.tenant)
                      void query.refetch()
                    onChanged(reconcileOperation.request.tenant)
                  }}
                />
              ) : null}
            </div>
          )}
        </AsyncSection>
      </SheetContent>
    </Sheet>
  )
}

function BindingOverview({
  binding,
  etag,
}: {
  binding: ProtocolBinding
  etag: string | null
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <section>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">{t('fields.bindingSpec')}</dt>
        <dd className="break-all font-mono text-xs">
          {binding.binding_spec_id} · {t('fields.generation')}{' '}
          {binding.binding_spec_generation}
        </dd>
        <dt className="text-muted-foreground">{t('fields.direction')}</dt>
        <dd>{t(`direction.${binding.direction}`)}</dd>
        <dt className="text-muted-foreground">{t('fields.peerAuthority')}</dt>
        <dd className="break-all font-mono text-xs">
          {binding.peer_authority}
        </dd>
        <dt className="text-muted-foreground">{t('detail.remoteResource')}</dt>
        <dd className="break-all font-mono text-xs">
          {binding.remote_resource_ref}
        </dd>
        <dt className="text-muted-foreground">{t('binding.external')}</dt>
        <dd className="break-all font-mono text-xs">
          {binding.external_kind}:{binding.external_id || '—'}
        </dd>
        <dt className="text-muted-foreground">{t('binding.states')}</dt>
        <dd className="font-mono text-xs">
          {binding.local_state || '—'} ↔ {binding.remote_state || '—'}{' '}
          {binding.remote_revision ? `@${binding.remote_revision}` : ''}
        </dd>
        <dt className="text-muted-foreground">{t('binding.owner')}</dt>
        <dd className="break-all font-mono text-xs">
          {binding.owner_kind || '—'}:{binding.owner_ref || '—'} · epoch{' '}
          {binding.owner_epoch ?? '—'} · fence {binding.lease_fence ?? '—'}
        </dd>
        <dt className="text-muted-foreground">{t('fields.etag')}</dt>
        <dd className="font-mono text-xs">{etag ?? '—'}</dd>
        <dt className="text-muted-foreground">{t('fields.specHash')}</dt>
        <dd className="break-all font-mono text-xs">
          {binding.pinned_spec_hash || '—'}
        </dd>
        <dt className="text-muted-foreground">{t('fields.mappingHash')}</dt>
        <dd className="break-all font-mono text-xs">
          {binding.pinned_mapping_hash || '—'}
        </dd>
        <dt className="text-muted-foreground">{t('fields.lossesHash')}</dt>
        <dd className="break-all font-mono text-xs">
          {binding.pinned_losses_hash || '—'}
        </dd>
      </dl>
    </section>
  )
}

function BindingEvidence({ binding }: { binding: ProtocolBinding }) {
  const { t } = useTranslation('protocolBindings')
  return (
    <section>
      <h3 className="mb-2 text-sm font-medium">{t('binding.evidenceTitle')}</h3>
      <ol className="space-y-2">
        <li className="rounded-md border border-border p-3 text-xs">
          <p className="font-medium">{t('binding.created')}</p>
          <time dateTime={binding.created_at} className="text-muted-foreground">
            {binding.created_at}
          </time>
        </li>
        <li className="rounded-md border border-border p-3 text-xs">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="font-medium">{t('binding.lastObservation')}</p>
            <ProtocolVerdictBadge verdict={binding.observation_verdict} />
          </div>
          <p className="mt-1 font-mono">{binding.observation_code || '—'}</p>
          <time
            dateTime={binding.last_observed_at}
            className="text-muted-foreground"
          >
            {binding.last_observed_at ?? t('detail.notObserved')}
          </time>
          {binding.detail_hash ? (
            <p className="mt-1 break-all font-mono text-muted-foreground">
              {binding.detail_hash}
            </p>
          ) : null}
        </li>
        <li className="rounded-md border border-border p-3 text-xs">
          <p className="font-medium">{t('binding.lastEvent')}</p>
          <p className="mt-1 break-all font-mono">
            #{binding.last_event_seq} · {binding.last_event_id || '—'}
          </p>
          <p className="mt-1 break-all font-mono text-muted-foreground">
            {binding.last_command_id || '—'}
          </p>
        </li>
      </ol>
    </section>
  )
}

type ReconcilePhase =
  | 'refreshing'
  | 'validating'
  | 'planning'
  | 'planned'
  | 'testing'
  | 'tested'
  | 'applying'
  | 'applied'
  | 'unknown'
  | 'broken'
  | 'conflict'
  | 'failed'

export function ProtocolBindingReconcileDialog({
  open,
  bindingId,
  request,
  onOpenChange,
  onApplied,
}: {
  open: boolean
  bindingId: string
  request: TenantRequestOptions
  onOpenChange: (open: boolean) => void
  onApplied: (outcome: ProtocolReconcileOutcome) => void
}) {
  const { t } = useTranslation('protocolBindings')
  const [phase, setPhase] = useState<ReconcilePhase>('refreshing')
  const [etag, setEtag] = useState<string | null>(null)
  const [intention] = useState(() => ({
    request,
    key: crypto.randomUUID(),
  }))
  const [plan, setPlan] = useState<ProtocolBindingReconcilePlan | null>(null)
  const [test, setTest] = useState<ProtocolBindingAssessment | null>(null)
  const [outcome, setOutcome] = useState<ProtocolReconcileOutcome | null>(null)
  const [code, setCode] = useState<string | null>(null)

  const classifyError = (error: unknown) => {
    setCode(protocolErrorCode(error))
    if (isProtocolConflict(error)) setPhase('conflict')
    else if (isProtocolUnknown(error)) setPhase('unknown')
    else if (protocolVerdictOfError(error) === 'ROTO') setPhase('broken')
    else setPhase('failed')
  }

  const inspect = async () => {
    setPhase('refreshing')
    setPlan(null)
    setTest(null)
    setOutcome(null)
    setCode(null)
    try {
      const fresh = await getProtocolBinding(bindingId, intention.request)
      setEtag(fresh.etag)
      setPhase('validating')
      const validation = await reconcileProtocolBinding(
        bindingId,
        'validate',
        fresh.etag,
        intention.request,
      )
      if (validation.verdict === 'NO_HE_PODIDO_MIRAR') {
        setCode(validation.code)
        setPhase('unknown')
        return
      }
      if (validation.verdict === 'ROTO') {
        setCode(validation.code)
        setPhase('broken')
        return
      }
      setPhase('planning')
      const nextPlan = await reconcileProtocolBinding(
        bindingId,
        'plan',
        fresh.etag,
        intention.request,
      )
      setPlan(nextPlan)
      if (nextPlan.verdict === 'NO_HE_PODIDO_MIRAR') {
        setCode(nextPlan.code)
        setPhase('unknown')
      } else if (nextPlan.verdict === 'ROTO' || !nextPlan.plan_hash) {
        setCode(nextPlan.code)
        setPhase('broken')
      } else setPhase('planned')
    } catch (error) {
      classifyError(error)
    }
  }

  useEffect(() => {
    if (!open) return
    const start = window.setTimeout(() => void inspect(), 0)
    return () => window.clearTimeout(start)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, bindingId])

  const runTest = async () => {
    if (!plan?.plan_hash) return
    setPhase('testing')
    try {
      const result = await reconcileProtocolBinding(
        bindingId,
        'test',
        etag,
        intention.request,
        { planHash: plan.plan_hash },
      )
      setTest(result)
      if (result.verdict === 'NO_HE_PODIDO_MIRAR') {
        setCode(result.code)
        setPhase('unknown')
      } else {
        // ROTO is actionable evidence. Apply records it; it is not rendered as clean.
        setPhase('tested')
      }
    } catch (error) {
      classifyError(error)
    }
  }

  const apply = async () => {
    if (
      !plan?.plan_hash ||
      !intention.key ||
      !test ||
      test.verdict === 'NO_HE_PODIDO_MIRAR'
    )
      return
    setPhase('applying')
    try {
      const result = await reconcileProtocolBinding(
        bindingId,
        'apply',
        etag,
        intention.request,
        {
          planHash: plan.plan_hash,
          idempotencyKey: intention.key,
        },
      )
      setOutcome(result)
      const assessment = result.assessment
      if (assessment.verdict === 'NO_HE_PODIDO_MIRAR') {
        setCode(assessment.code)
        setPhase('unknown')
      } else if (!assessment.resource || assessment.resource.id !== bindingId) {
        setCode(assessment.code || 'invalid_reconcile_resource')
        setPhase('unknown')
      } else {
        // Both LIMPIO and ROTO are observed outcomes. The badge preserves polarity.
        setPhase('applied')
        onApplied(result)
      }
    } catch (error) {
      classifyError(error)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-3xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('reconcile.title')}</DialogTitle>
          <DialogDescription>{t('reconcile.description')}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {[
            'refreshing',
            'validating',
            'planning',
            'testing',
            'applying',
          ].includes(phase) ? (
            <p role="status" className="text-sm text-muted-foreground">
              {t(`reconcile.phase.${phase}`)}
            </p>
          ) : null}
          {plan ? (
            <ReconcilePlanPanel
              plan={plan}
              etag={etag}
              intentKey={intention.key}
            />
          ) : null}
          {test ? (
            <AssessmentPanel
              title={t('reconcile.testResult')}
              assessment={test}
            />
          ) : null}
          {outcome ? (
            <AssessmentPanel
              title={t('reconcile.recordedResult')}
              assessment={outcome.assessment}
              replayed={outcome.replayed}
            />
          ) : null}
          {phase === 'unknown' ? (
            <UnknownNotice code={code}>{t('reconcile.unknown')}</UnknownNotice>
          ) : null}
          {phase === 'broken' ? (
            <BrokenNotice code={code}>{t('reconcile.broken')}</BrokenNotice>
          ) : null}
          {phase === 'failed' ? (
            <BrokenNotice code={code}>{t('reconcile.failed')}</BrokenNotice>
          ) : null}
          {phase === 'conflict' ? (
            <BrokenNotice code={code}>{t('reconcile.conflict')}</BrokenNotice>
          ) : null}
        </div>
        <DialogFooter>
          {phase === 'planned' ? (
            <Button onClick={() => void runTest()}>{t('actions.test')}</Button>
          ) : null}
          {phase === 'tested' ? (
            <Button onClick={() => void apply()}>
              {t('actions.recordObservation')}
            </Button>
          ) : null}
          {(phase === 'unknown' || phase === 'failed') && test && plan ? (
            test.verdict !== 'NO_HE_PODIDO_MIRAR' ? (
              <Button onClick={() => void apply()}>
                {t('actions.retrySameKey')}
              </Button>
            ) : null
          ) : null}
          {phase === 'conflict' ? (
            <Button onClick={() => void inspect()}>
              {t('actions.rereadReplan')}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ReconcilePlanPanel({
  plan,
  etag,
  intentKey,
}: {
  plan: ProtocolBindingReconcilePlan
  etag: string | null
  intentKey: string
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <section className="rounded-md border border-border p-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium">{t('reconcile.plan')}</h3>
        <ProtocolVerdictBadge verdict={plan.verdict} />
      </div>
      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">{t('fields.planHash')}</dt>
        <dd className="break-all font-mono">{plan.plan_hash}</dd>
        <dt className="text-muted-foreground">{t('fields.etag')}</dt>
        <dd className="font-mono">{etag ?? '—'}</dd>
        <dt className="text-muted-foreground">{t('composer.intentKey')}</dt>
        <dd className="break-all font-mono">{intentKey}</dd>
        <dt className="text-muted-foreground">{t('reconcile.rowEffects')}</dt>
        <dd>{plan.row_effects.join(', ') || '—'}</dd>
        <dt className="text-muted-foreground">
          {t('reconcile.externalCalls')}
        </dt>
        <dd>
          {plan.external_calls.join(', ') || t('reconcile.noExternalCalls')}
        </dd>
      </dl>
      <Checks checks={plan.checks} />
    </section>
  )
}

function AssessmentPanel({
  title,
  assessment,
  replayed = false,
}: {
  title: string
  assessment: ProtocolBindingAssessment
  replayed?: boolean
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <section className="rounded-md border border-border p-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium">{title}</h3>
        <ProtocolVerdictBadge verdict={assessment.verdict} />
      </div>
      <p className="mt-2 font-mono text-xs">{assessment.code}</p>
      <p className="mt-1 text-xs text-muted-foreground">
        {assessment.observed_at}
      </p>
      {replayed ? (
        <p className="mt-1 text-xs text-info">{t('outcome.replayed')}</p>
      ) : null}
      <Checks checks={assessment.checks} />
    </section>
  )
}

function Checks({ checks }: { checks: ProtocolBindingAssessment['checks'] }) {
  const { t } = useTranslation('protocolBindings')
  return (
    <ul className="mt-3 space-y-2" aria-label={t('reconcile.checks')}>
      {checks.map((check, index) => (
        <li
          key={`${check.name}-${index}`}
          className="flex flex-wrap items-center justify-between gap-2 rounded border border-border px-2 py-1.5 text-xs"
        >
          <span className="font-mono">{check.name}</span>
          <ProtocolVerdictBadge verdict={check.verdict} />
          {check.evidence_ref ? (
            <span className="w-full break-all font-mono text-muted-foreground">
              {check.evidence_ref}
            </span>
          ) : null}
        </li>
      ))}
    </ul>
  )
}
