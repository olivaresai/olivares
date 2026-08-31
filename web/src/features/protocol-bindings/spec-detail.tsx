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
  getProtocolBindingSpec,
  isProtocolConflict,
  isProtocolUnknown,
  protocolBindingKeys,
  protocolErrorCode,
  protocolVerdictOfError,
  runProtocolBindingSpecTransition,
} from './api'
import {
  activationBlocker,
  canDisableSpec,
  type ActivationBlocker,
} from './model'
import {
  BrokenNotice,
  ProtocolVerdictBadge,
  SpecStateBadge,
  UnknownNotice,
} from './status'
import type {
  ProtocolBindingSpec,
  ProtocolBindingSpecOperation,
  ProtocolBindingSpecPlan,
  ProtocolSpecApplyOutcome,
} from './types'

export function ProtocolSpecDetailSheet({
  specId,
  onOpenChange,
  onChanged,
}: {
  specId: string | null
  onOpenChange: (open: boolean) => void
  onChanged: (tenant: string | null) => void
}) {
  const { t } = useTranslation('protocolBindings')
  const { activeTenant, can } = useAuth()
  const [transition, setTransition] = useState<{
    specId: string
    operation: 'activate' | 'disable'
    request: TenantRequestOptions
  } | null>(null)
  const query = useQuery({
    queryKey: protocolBindingKeys.spec(activeTenant, specId ?? ''),
    queryFn: ({ signal }) =>
      getProtocolBindingSpec(
        specId as string,
        { tenant: activeTenant },
        signal,
      ),
    enabled: !!specId,
  })

  return (
    <Sheet open={!!specId} onOpenChange={onOpenChange}>
      <SheetContent className="max-w-3xl overflow-y-auto">
        <SheetHeader>
          <SheetTitle>{t('detail.specTitle')}</SheetTitle>
          <SheetDescription>{t('detail.specDescription')}</SheetDescription>
        </SheetHeader>
        <AsyncSection query={query} skeletonHeight={240}>
          {({ spec, etag }) => {
            const blocker = activationBlocker(spec)
            return (
              <div className="space-y-5 py-2">
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="font-mono text-sm font-medium">
                    {spec.binding_key}
                  </h2>
                  <SpecStateBadge state={spec.state} />
                  <ProtocolVerdictBadge verdict={spec.validation.verdict} />
                  <Badge variant="outline">
                    {spec.protocol.toUpperCase()} {spec.protocol_version}
                  </Badge>
                </div>

                <SpecOverview spec={spec} etag={etag} />
                <MappingList spec={spec} />
                <LossList spec={spec} />

                {can('sessions:protocol-binding:admin') ? (
                  <section className="space-y-2 border-t border-border pt-4">
                    <h3 className="text-sm font-medium">
                      {t('detail.lifecycle')}
                    </h3>
                    {blocker && spec.state === 'draft' ? (
                      <p className="text-xs text-warning">
                        {t(`activation.blocker.${blocker}`)}
                      </p>
                    ) : null}
                    <div className="flex flex-wrap gap-2">
                      <Button
                        type="button"
                        size="sm"
                        disabled={blocker !== null}
                        onClick={() =>
                          setTransition({
                            specId: spec.id,
                            operation: 'activate',
                            request: { tenant: activeTenant },
                          })
                        }
                      >
                        {t('actions.activate')}
                      </Button>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        disabled={!canDisableSpec(spec)}
                        onClick={() =>
                          setTransition({
                            specId: spec.id,
                            operation: 'disable',
                            request: { tenant: activeTenant },
                          })
                        }
                      >
                        {t('actions.disable')}
                      </Button>
                    </div>
                  </section>
                ) : null}

                {transition ? (
                  <ProtocolSpecTransitionDialog
                    open
                    specId={transition.specId}
                    operation={transition.operation}
                    request={transition.request}
                    onOpenChange={(next) => {
                      if (!next) setTransition(null)
                    }}
                    onApplied={() => {
                      if (activeTenant === transition.request.tenant)
                        void query.refetch()
                      onChanged(transition.request.tenant)
                    }}
                  />
                ) : null}
              </div>
            )
          }}
        </AsyncSection>
      </SheetContent>
    </Sheet>
  )
}

function SpecOverview({
  spec,
  etag,
}: {
  spec: ProtocolBindingSpec
  etag: string | null
}) {
  const { t } = useTranslation('protocolBindings')
  const selector = JSON.stringify(spec.local_selector)
  return (
    <section className="space-y-3">
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">{t('fields.id')}</dt>
        <dd className="break-all font-mono text-xs">{spec.id}</dd>
        <dt className="text-muted-foreground">{t('fields.generation')}</dt>
        <dd>{spec.generation}</dd>
        <dt className="text-muted-foreground">{t('fields.direction')}</dt>
        <dd>{t(`direction.${spec.direction}`)}</dd>
        <dt className="text-muted-foreground">{t('fields.peerAuthority')}</dt>
        <dd className="break-all font-mono text-xs">{spec.peer_authority}</dd>
        <dt className="text-muted-foreground">{t('detail.localResource')}</dt>
        <dd className="break-all font-mono text-xs">
          {spec.local_kind} {selector}
        </dd>
        <dt className="text-muted-foreground">{t('detail.remoteResource')}</dt>
        <dd className="break-all font-mono text-xs">
          {spec.remote_resource_kind}:{spec.remote_resource_ref}
        </dd>
        <dt className="text-muted-foreground">
          {t('fields.permissionProfile')}
        </dt>
        <dd className="break-all font-mono text-xs">
          {spec.permission_profile_ref}
        </dd>
        <dt className="text-muted-foreground">{t('fields.currency')}</dt>
        <dd>
          <Badge variant="info">{t(`currency.${spec.currency_policy}`)}</Badge>
        </dd>
        <dt className="text-muted-foreground">{t('fields.etag')}</dt>
        <dd className="font-mono text-xs">{etag ?? '—'}</dd>
        <dt className="text-muted-foreground">{t('detail.validation')}</dt>
        <dd className="space-y-1">
          <ProtocolVerdictBadge verdict={spec.validation.verdict} />
          <p className="font-mono text-xs">{spec.validation.code}</p>
          <p className="text-xs text-muted-foreground">
            {spec.validation.observed_at ?? t('detail.notObserved')}
          </p>
        </dd>
        <dt className="text-muted-foreground">{t('fields.specHash')}</dt>
        <dd className="break-all font-mono text-xs">{spec.spec_hash || '—'}</dd>
        <dt className="text-muted-foreground">{t('fields.mappingHash')}</dt>
        <dd className="break-all font-mono text-xs">
          {spec.mapping_hash || '—'}
        </dd>
        <dt className="text-muted-foreground">{t('fields.lossesHash')}</dt>
        <dd className="break-all font-mono text-xs">
          {spec.losses_hash || '—'}
        </dd>
      </dl>
    </section>
  )
}

function MappingList({ spec }: { spec: ProtocolBindingSpec }) {
  const { t } = useTranslation('protocolBindings')
  return (
    <section>
      <h3 className="mb-2 text-sm font-medium">{t('detail.mapping')}</h3>
      <ul className="space-y-2">
        {spec.mapping.map((rule, index) => (
          <li
            key={`${rule.source}-${rule.target}-${index}`}
            className="rounded-md border border-border p-3 text-xs"
          >
            <p className="break-all font-mono">
              {rule.source} → {rule.target}
            </p>
            <p className="mt-1 text-muted-foreground">
              {t(`cardinality.${rule.cardinality}`)} ·{' '}
              {t(`transform.${rule.transform}`)}
            </p>
          </li>
        ))}
      </ul>
    </section>
  )
}

function LossList({ spec }: { spec: ProtocolBindingSpec }) {
  const { t } = useTranslation('protocolBindings')
  return (
    <section>
      <h3 className="mb-2 text-sm font-medium">{t('detail.losses')}</h3>
      {spec.known_losses.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t('detail.noLosses')}</p>
      ) : (
        <ul className="space-y-2">
          {spec.known_losses.map((loss, index) => (
            <li
              key={`${loss.field}-${index}`}
              className="rounded-md border border-border p-3 text-xs"
            >
              <div className="flex items-center justify-between gap-2">
                <span className="font-mono">{loss.field}</span>
                <Badge
                  variant={
                    loss.accepted && loss.acceptance_ref ? 'warning' : 'danger'
                  }
                >
                  {loss.accepted && loss.acceptance_ref
                    ? t('detail.accepted')
                    : t('detail.notAccepted')}
                </Badge>
              </div>
              <p className="mt-1 text-muted-foreground">{loss.reason_code}</p>
              {loss.acceptance_ref ? (
                <p className="mt-1 break-all font-mono">
                  {loss.acceptance_ref}
                </p>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

type TransitionPhase =
  | 'refreshing'
  | 'validating'
  | 'planning'
  | 'planned'
  | 'applying'
  | 'applied'
  | 'unknown'
  | 'broken'
  | 'conflict'
  | 'failed'

export function ProtocolSpecTransitionDialog({
  open,
  specId,
  operation,
  request,
  onOpenChange,
  onApplied,
}: {
  open: boolean
  specId: string
  operation: Exclude<ProtocolBindingSpecOperation, 'draft'>
  request: TenantRequestOptions
  onOpenChange: (open: boolean) => void
  onApplied: (outcome: ProtocolSpecApplyOutcome) => void
}) {
  const { t } = useTranslation('protocolBindings')
  const [phase, setPhase] = useState<TransitionPhase>('refreshing')
  const [plan, setPlan] = useState<ProtocolBindingSpecPlan | null>(null)
  const [etag, setEtag] = useState<string | null>(null)
  const [intention] = useState(() => ({
    request,
    key: crypto.randomUUID(),
  }))
  const [code, setCode] = useState<string | null>(null)
  const [blocker, setBlocker] = useState<ActivationBlocker | 'disable' | null>(
    null,
  )
  const [outcome, setOutcome] = useState<ProtocolSpecApplyOutcome | null>(null)

  const inspect = async () => {
    setPhase('refreshing')
    setPlan(null)
    setOutcome(null)
    setCode(null)
    setBlocker(null)
    try {
      const fresh = await getProtocolBindingSpec(specId, intention.request)
      setEtag(fresh.etag)
      const blocked =
        operation === 'activate'
          ? activationBlocker(fresh.spec)
          : canDisableSpec(fresh.spec)
            ? null
            : 'disable'
      if (blocked) {
        setBlocker(blocked)
        setPhase('broken')
        return
      }
      setPhase('validating')
      const validation = await runProtocolBindingSpecTransition(
        specId,
        operation,
        'validate',
        fresh.etag,
        intention.request,
      )
      if (validation.verdict === 'UNKNOWN') {
        setCode(validation.code)
        setPhase('unknown')
        return
      }
      if (validation.verdict !== 'CLEAN') {
        setCode(validation.code)
        setPhase('broken')
        return
      }
      setPhase('planning')
      const nextPlan = await runProtocolBindingSpecTransition(
        specId,
        operation,
        'plan',
        fresh.etag,
        intention.request,
      )
      setPlan(nextPlan)
      if (nextPlan.verdict === 'UNKNOWN') {
        setCode(nextPlan.code)
        setPhase('unknown')
      } else if (nextPlan.verdict !== 'CLEAN' || !nextPlan.plan_hash) {
        setCode(nextPlan.code)
        setPhase('broken')
      } else setPhase('planned')
    } catch (error) {
      setCode(protocolErrorCode(error))
      if (isProtocolConflict(error)) setPhase('conflict')
      else if (isProtocolUnknown(error)) setPhase('unknown')
      else if (protocolVerdictOfError(error) === 'BROKEN') setPhase('broken')
      else setPhase('failed')
    }
  }

  useEffect(() => {
    if (!open) return
    const start = window.setTimeout(() => void inspect(), 0)
    return () => window.clearTimeout(start)
    // A new open is one new operator intention. inspect is deliberately not a dep:
    // its identity changes every render but specId/operation are the actual inputs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, specId, operation])

  const apply = async () => {
    if (!plan?.plan_hash || !intention.key) return
    setPhase('applying')
    try {
      const result = await runProtocolBindingSpecTransition(
        specId,
        operation,
        'apply',
        etag,
        intention.request,
        { idempotencyKey: intention.key, planHash: plan.plan_hash },
      )
      setOutcome(result)
      const expectedState = operation === 'activate' ? 'active' : 'disabled'
      if (result.result.verdict === 'UNKNOWN') {
        setCode(result.result.code)
        setPhase('unknown')
      } else if (
        result.result.verdict !== 'CLEAN' ||
        result.result.spec?.state !== expectedState
      ) {
        setCode(result.result.code)
        setPhase('broken')
      } else {
        setPhase('applied')
        onApplied(result)
      }
    } catch (error) {
      setCode(protocolErrorCode(error))
      if (isProtocolConflict(error)) setPhase('conflict')
      else if (isProtocolUnknown(error)) setPhase('unknown')
      else if (protocolVerdictOfError(error) === 'BROKEN') setPhase('broken')
      else setPhase('failed')
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t(`transition.${operation}.title`)}</DialogTitle>
          <DialogDescription>
            {t(`transition.${operation}.description`)}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {phase === 'refreshing' ||
          phase === 'validating' ||
          phase === 'planning' ||
          phase === 'applying' ? (
            <p role="status" className="text-sm text-muted-foreground">
              {t(`transition.phase.${phase}`)}
            </p>
          ) : null}
          {plan ? (
            <TransitionPlan plan={plan} intentKey={intention.key} etag={etag} />
          ) : null}
          {phase === 'unknown' ? <UnknownNotice code={code} /> : null}
          {phase === 'broken' ? (
            <BrokenNotice code={code}>
              {blocker
                ? t(`activation.blocker.${blocker}`)
                : t('transition.broken')}
            </BrokenNotice>
          ) : null}
          {phase === 'failed' ? (
            <BrokenNotice code={code}>{t('transition.failed')}</BrokenNotice>
          ) : null}
          {phase === 'conflict' ? (
            <BrokenNotice code={code}>{t('transition.conflict')}</BrokenNotice>
          ) : null}
          {phase === 'applied' && outcome ? (
            <div
              role="status"
              className="rounded-md border border-success-line bg-success-soft p-3 text-sm text-success"
            >
              <p className="font-medium">
                {t(`transition.${operation}.applied`)}
              </p>
              {outcome.replayed ? (
                <p className="mt-1 text-xs">{t('outcome.replayed')}</p>
              ) : null}
            </div>
          ) : null}
        </div>
        <DialogFooter>
          {phase === 'planned' ? (
            <Button onClick={() => void apply()}>
              {t(`actions.${operation}`)}
            </Button>
          ) : null}
          {(phase === 'failed' || phase === 'unknown') && plan ? (
            <Button onClick={() => void apply()}>
              {t('actions.retrySameKey')}
            </Button>
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

function TransitionPlan({
  plan,
  intentKey,
  etag,
}: {
  plan: ProtocolBindingSpecPlan
  intentKey: string
  etag: string | null
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <section className="rounded-md border border-border p-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-medium">{t('transition.plan')}</h3>
        <ProtocolVerdictBadge verdict={plan.verdict} />
      </div>
      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">{t('fields.planHash')}</dt>
        <dd className="break-all font-mono">{plan.plan_hash}</dd>
        <dt className="text-muted-foreground">{t('detail.validation')}</dt>
        <dd className="space-y-1">
          <ProtocolVerdictBadge verdict={plan.validation.verdict} />
          <p className="font-mono">{plan.validation.code}</p>
          <p className="text-muted-foreground">
            {plan.validation.observed_at ?? t('detail.notObserved')}
          </p>
        </dd>
        <dt className="text-muted-foreground">{t('fields.specHash')}</dt>
        <dd className="break-all font-mono">{plan.spec_hash}</dd>
        <dt className="text-muted-foreground">{t('fields.priorActive')}</dt>
        <dd className="break-all font-mono">{plan.prior_active_id ?? '—'}</dd>
        <dt className="text-muted-foreground">{t('fields.etag')}</dt>
        <dd className="font-mono">{etag ?? '—'}</dd>
        <dt className="text-muted-foreground">{t('composer.intentKey')}</dt>
        <dd className="break-all font-mono">{intentKey}</dd>
      </dl>
    </section>
  )
}
