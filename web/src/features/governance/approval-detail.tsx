// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Check, X } from 'lucide-react'
import { useState } from 'react'
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
import { StatusBadge } from '@/components/data/badges'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { governanceApi, governanceKeys } from './api'
import { DecisionDialog } from './decision-dialog'
import './i18n'
import { canDecideOnRequest } from './types'
import type { ApprovalDTO, DecisionDTO, DecisionVerb } from './types'

export interface ApprovalDetailSheetProps {
  approvalId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * ApprovalDetailSheet is the drill-in / traceability-from-action-to-human view: the
 * full approval header plus the immutable, append-only decision trail (who decided
 * what, when, with what note). The trail is read-only — never an edit/delete
 * affordance. Approve / Reject (admin) and Cancel (requester-or-admin) live here too,
 * each gated by permission and routed through a confirm + privileged mutation.
 *
 * Poll-based freshness (no SSE): while open, the request + its trail refetch on a 12s
 * interval so a status change or a new decision shows without a manual refresh.
 */
export function ApprovalDetailSheet({
  approvalId,
  open,
  onOpenChange,
}: ApprovalDetailSheetProps) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can, principal } = useAuth()
  const canDecide = can('governance:approval:admin')
  const canWrite = can('governance:approval:write')

  const [decisionVerb, setDecisionVerb] = useState<DecisionVerb | null>(null)
  const [decisionOpen, setDecisionOpen] = useState(false)
  const [confirmCancel, setConfirmCancel] = useState(false)

  const query = useQuery({
    queryKey: governanceKeys.approval(activeTenant, approvalId ?? ''),
    queryFn: () => governanceApi.getApproval(approvalId!),
    enabled: open && !!approvalId,
    refetchInterval: open ? 12_000 : false,
  })
  const detail = query.data

  const decisions = useQuery({
    queryKey: governanceKeys.decisions(activeTenant, approvalId ?? ''),
    queryFn: () => governanceApi.listDecisions(approvalId!),
    enabled: open && !!approvalId,
    refetchInterval: open ? 12_000 : false,
  })

  const cancel = usePrivilegedMutation({
    mutationFn: () => governanceApi.cancelApproval(approvalId!),
    invalidateKeys: () => [
      governanceKeys.approvals(activeTenant),
      governanceKeys.approval(activeTenant, approvalId ?? ''),
    ],
    successMessage: t('cancel.done'),
    onDone: () => setConfirmCancel(false),
  })

  const isPending = detail?.status === 'pending'
  // Separation of duties: hide Approve/Reject the engine would 403 (requester
  // cannot decide their own; a token has no stable user id).
  const mayDecide =
    canDecide &&
    canDecideOnRequest(detail?.requested_by, principal?.actor, principal?.kind)

  function openDecision(verb: DecisionVerb) {
    setDecisionVerb(verb)
    setDecisionOpen(true)
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{detail?.action ?? t('detail.title')}</SheetTitle>
          {detail && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <StatusBadge status={detail.status} />
              {detail.escalated && (
                <Badge variant="warning" title={t('approvals.escalatedHint')}>
                  {t('approvals.escalatedBadge')}
                </Badge>
              )}
            </SheetDescription>
          )}
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {query.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-16 w-full" />
              ))}
            </div>
          ) : query.error instanceof ApiError &&
            query.error.isStepUpRequired ? (
            // ⛔ ASEGURAMIENTO ANTES QUE ROL, y aquí el motor lo emite de verdad:
            // `modules/governance/approvals.go:579` contesta 403 con `code: step_up_required`.
            // `isForbidden` es SÓLO el status (lib/api/errors.ts:59-61), así que la hoja de la
            // aprobación se sustituía por «no tienes autorización» justo cuando lo que hacía
            // falta era confirmar la sesión.
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
              <Overview detail={detail} />

              <Separator />

              <section className="flex flex-col gap-2">
                <h3 className="text-sm font-medium text-foreground">
                  {t('detail.trail')}
                </h3>
                <p className="text-xs text-muted-foreground">
                  {t('detail.trailCaption')}
                </p>
                <DecisionTrail
                  items={decisions.data?.items ?? []}
                  loading={decisions.isLoading}
                  error={decisions.error}
                  onRetry={() => decisions.refetch()}
                />
              </section>
            </div>
          )}
        </ScrollArea>

        {/* Actions footer — gated by permission + pending status. */}
        {detail && isPending && (mayDecide || canWrite) && (
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border pt-3">
            {canWrite && (
              <Button
                variant="secondary"
                size="sm"
                onClick={() => setConfirmCancel(true)}
              >
                {t('approvals.cancel')}
              </Button>
            )}
            {mayDecide && (
              <Button
                variant="destructive"
                size="sm"
                onClick={() => openDecision('reject')}
              >
                <X />
                {t('approvals.reject')}
              </Button>
            )}
            {mayDecide && (
              <Button
                variant="primary"
                size="sm"
                onClick={() => openDecision('approve')}
              >
                <Check />
                {t('approvals.approve')}
              </Button>
            )}
          </div>
        )}
      </SheetContent>

      {/* Approve / reject decision (HIGH risk, danger + note). */}
      {detail && (
        <DecisionDialog
          open={decisionOpen}
          onOpenChange={setDecisionOpen}
          approvalId={detail.id}
          verb={decisionVerb}
        />
      )}

      {/* Cancel / withdraw (medium risk, danger). */}
      {detail && (
        <ConfirmDialog
          open={confirmCancel}
          onOpenChange={setConfirmCancel}
          title={t('cancel.title')}
          description={t('cancel.body')}
          tone="danger"
          confirmLabel={t('cancel.confirm')}
          pending={cancel.isPending}
          onConfirm={() => cancel.mutate(undefined)}
        />
      )}
    </Sheet>
  )
}

function Overview({ detail }: { detail: ApprovalDTO }) {
  const { t } = useTranslation('governance')
  return (
    <KvList>
      {detail.action && (
        <KvRow label={t('detail.action')} mono>
          {detail.action}
        </KvRow>
      )}
      {detail.subject_kind && (
        <KvRow label={t('detail.subjectKind')}>{detail.subject_kind}</KvRow>
      )}
      {detail.subject_ref && (
        <KvRow label={t('detail.subjectRef')} mono align="start">
          {detail.subject_ref}
        </KvRow>
      )}
      {detail.requested_by && (
        <KvRow label={t('detail.requestedBy')} mono>
          <span title={t('approvals.actorHint')}>{detail.requested_by}</span>
        </KvRow>
      )}
      <KvRow label={t('detail.threshold')} mono>
        {detail.required_approvals}
      </KvRow>
      <KvRow label={t('detail.approveCount')} mono>
        {detail.approve_count}
      </KvRow>
      <KvRow label={t('detail.rejectCount')} mono>
        {detail.reject_count}
      </KvRow>
      {detail.policy_ref && (
        <KvRow label={t('detail.policyRef')} mono>
          {detail.policy_ref}
        </KvRow>
      )}
      {detail.expires_at && (
        <KvRow label={t('detail.expiresAt')}>
          <RelTimeLabel ts={detail.expires_at} />
        </KvRow>
      )}
      {detail.escalate_at && (
        <KvRow label={t('detail.escalateAt')}>
          <RelTimeLabel ts={detail.escalate_at} />
        </KvRow>
      )}
      {detail.decided_at && (
        <KvRow label={t('detail.decidedAt')}>
          <RelTimeLabel ts={detail.decided_at} />
        </KvRow>
      )}
      {detail.reason && (
        <KvRow label={t('detail.reason')} align="start">
          {detail.reason}
        </KvRow>
      )}
    </KvList>
  )
}

function DecisionTrail({
  items,
  loading,
  error,
  onRetry,
}: {
  items: DecisionDTO[]
  loading: boolean
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation('governance')

  if (loading) {
    return (
      <div className="flex flex-col gap-2">
        {Array.from({ length: 2 }).map((_, i) => (
          <Skeleton key={i} className="h-14 w-full" />
        ))}
      </div>
    )
  }
  // Segunda decisión del fichero: el rastro de la aprobación se gatea aparte de la hoja.
  if (error instanceof ApiError && error.isStepUpRequired)
    return <StepUpRequiredState action="generic" onElevated={onRetry} />
  if (error instanceof ApiError && error.isForbidden) return <ForbiddenState />
  if (error) return <ErrorState retry={onRetry} />
  if (items.length === 0) return <EmptyState title={t('detail.trailEmpty')} />

  return (
    <ol className="flex flex-col gap-2">
      {items.map((d, i) => {
        const approved = d.decision === 'approve'
        return (
          <li
            key={`${d.decider}:${d.decided_at ?? i}`}
            className="rounded-lg border border-border bg-surface p-3"
          >
            <div className="flex items-center justify-between gap-2">
              <Badge variant={approved ? 'success' : 'danger'}>
                {approved
                  ? t('detail.decisionApprove')
                  : t('detail.decisionReject')}
              </Badge>
              {d.decided_at && (
                <span className="text-xs text-muted-foreground">
                  <RelTimeLabel ts={d.decided_at} />
                </span>
              )}
            </div>
            <p
              className="mt-1 font-mono text-xs text-muted-foreground"
              title={t('approvals.actorHint')}
            >
              {d.decider}
            </p>
            {d.note && <p className="mt-2 text-sm text-foreground">{d.note}</p>}
          </li>
        )
      })}
    </ol>
  )
}
