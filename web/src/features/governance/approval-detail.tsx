// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useQuery } from '@tanstack/react-query'
import { Check, Loader2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { withInlineCommands } from '@/components/ui/inline-commands'
import { KvList, KvRow } from '@/components/ui/kv'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusBadge } from '@/components/data/badges'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  isTypingTarget,
  modalIsOpen,
  navLeader,
  resolveBinding,
} from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { RelTimeLabel } from '@/features/shared'
import { governanceApi, governanceKeys } from './api'
import {
  ApprovalDetails,
  ApprovalPreview,
  approvalCommandUnknown,
} from './approval-preview'
import { DenyDialog, useDecision } from './decision-dialog'
import './i18n'
import { canDecideOnRequest } from './types'
import type { ApprovalDTO, DecisionDTO } from './types'

/** A tool-call approval (claude.tool, provider.tool, mcp.tool …): its reason
 * names the command or path under review. */
function isToolCall(subjectKind?: string): boolean {
  return subjectKind?.endsWith('.tool') ?? false
}

/** The approval the engine opens to re-enable a stopped estate (dual control). */
const REENABLE_ACTION = 'security.killswitch.reenable'

/** The region the queue's keys belong to: the queue and the open request. */
export const APPROVAL_QUEUE_REGION = 'data-approval-queue'

/** A bare key is the queue's only where the person is reading the queue: on the page
 * itself or inside the queue and its panel. Never in a field, a modal, or a control that
 * answers letters itself (a select's or a menu's typeahead), and never as the second half
 * of a `g` sequence. */
function queueOwnsKey(e: KeyboardEvent): boolean {
  if (e.defaultPrevented || e.repeat || e.isComposing) return false
  if (navLeader.armed()) return false
  const target = e.target
  if (isTypingTarget(target) || modalIsOpen()) return false
  return (
    target === document.body ||
    (target instanceof Element &&
      !!target.closest(`[${APPROVAL_QUEUE_REGION}]`) &&
      !target.closest('[role="combobox"],[role="listbox"],[role="menu"]'))
  )
}

/**
 * ApprovalDetailPanel is the open request beside the queue (#2548): what will run, the
 * rule that asked and what it reaches, who may not decide it, the decision, and the
 * immutable, append-only trail (read-only, never an edit or delete affordance).
 *
 * APPROVE ONCE IS ONE ACTION. The command, the rule and the reach are on screen above the
 * button, so a second confirmation would only repeat them; the engine records the one
 * decision in the ledger. Deny keeps its dialog for the note a refusal usually wants.
 * `A` and `D` do the same from the keyboard, outside a field and outside a modal.
 *
 * The list's row is shown at once; the request and its trail are then read and polled
 * every 12 s (no SSE), so a status change or a new decision shows without a refresh.
 */
export function ApprovalDetailPanel({
  approvalId,
  approval,
}: {
  approvalId: string
  /** The queue's row for it, shown while the engine's read arrives; absent for a linked
   * request the queue does not list (decided, expired, or under another filter). */
  approval?: ApprovalDTO
}) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can, principal } = useAuth()
  const canDecide = can('governance:approval:admin')
  const canWrite = can('governance:approval:write')

  const [denyOpen, setDenyOpen] = useState(false)
  const [confirmCancel, setConfirmCancel] = useState(false)

  const query = useQuery({
    queryKey: governanceKeys.approval(activeTenant, approvalId),
    queryFn: () => governanceApi.getApproval(approvalId),
    placeholderData: approval,
    refetchInterval: 12_000,
  })
  const detail = query.data

  const decisions = useQuery({
    queryKey: governanceKeys.decisions(activeTenant, approvalId),
    queryFn: () => governanceApi.listDecisions(approvalId),
    refetchInterval: 12_000,
  })

  const approve = useDecision(approvalId, 'approve')

  const cancel = usePrivilegedMutation({
    mutationFn: () => governanceApi.cancelApproval(approvalId),
    invalidateKeys: () => [
      governanceKeys.approvals(activeTenant),
      governanceKeys.approval(activeTenant, approvalId),
    ],
    successMessage: t('cancel.done'),
    onDone: () => setConfirmCancel(false),
  })

  const isPending = detail?.status === 'pending'
  // A request that needs more than one approver stays pending after this person's
  // approval; the engine refuses a second decision from them (409), so it is not offered.
  const decidedByMe =
    !!principal?.actor &&
    !!decisions.data?.items.some((d) => d.decider === principal.actor)
  // Separation of duties: hide Approve/Deny the engine would 403 (requester
  // cannot decide their own; a token has no stable user id).
  const mayDecide =
    isPending &&
    !decidedByMe &&
    canDecide &&
    canDecideOnRequest(detail?.requested_by, principal?.actor, principal?.kind)
  const { mutate: decide, isPending: deciding } = approve
  // One decision per action: a second click or key that lands before the first one's
  // render is dropped here, not sent and refused.
  const inFlight = useRef(false)
  useEffect(() => {
    if (!deciding) inFlight.current = false
  }, [deciding])
  const approveOnce = useCallback(() => {
    if (inFlight.current) return
    inFlight.current = true
    decide({ decision: 'approve' })
  }, [decide])

  useEffect(() => {
    if (!mayDecide) return
    const onKey = (e: KeyboardEvent) => {
      if (!queueOwnsKey(e)) return
      const command = resolveBinding(KEYBINDINGS, e, { approvalQueue: true })
      if (command === 'approvals.approve') {
        e.preventDefault()
        approveOnce()
      } else if (command === 'approvals.deny') {
        e.preventDefault()
        setDenyOpen(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [mayDecide, approveOnce])

  const requesterIsPerson =
    !!detail?.requested_by?.startsWith('user:') && !detail.launched_by

  return (
    <aside
      aria-label={t('preview.requestSummary')}
      className="flex min-w-0 flex-col gap-4 rounded-xl border border-line bg-surface p-4"
      data-slot="approval-panel"
    >
      {query.error instanceof ApiError && query.error.isStepUpRequired ? (
        // ⛔ ASSURANCE BEFORE ROLE, and here the engine really sends it:
        // `modules/governance/approvals.go:579` answers 403 with `code: step_up_required`.
        // `isForbidden` is ONLY the status (lib/api/errors.ts:59-61), so the approval's sheet
        // was replaced by "you are not authorized" exactly when what was needed was to
        // confirm the session.
        <StepUpRequiredState
          action="generic"
          onElevated={() => void query.refetch()}
        />
      ) : query.error instanceof ApiError && query.error.isForbidden ? (
        <ForbiddenState />
      ) : !detail && query.isPending ? (
        <div className="flex flex-col gap-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : query.error || !detail ? (
        <QueryErrorState error={query.error} retry={() => query.refetch()} />
      ) : (
        <>
          {!isPending || detail.escalated ? (
            <div className="flex flex-wrap items-center gap-1.5">
              <StatusBadge status={detail.status} />
              {detail.escalated && (
                <Badge variant="warning" title={t('approvals.escalatedHint')}>
                  {t('approvals.escalatedBadge')}
                </Badge>
              )}
            </div>
          ) : null}

          {/* What will run first, then why it needs a person and what it reaches. */}
          <ApprovalPreview approval={detail} />

          {isPending && decidedByMe ? (
            <p className="text-body text-text-2" role="status">
              {t('detail.decidedByYou')}
            </p>
          ) : isPending &&
            detail.action !== REENABLE_ACTION &&
            detail.requested_by === principal?.actor ? (
            <p className="text-body text-text-2" role="status">
              {t('detail.cannotDecideOwn')}
            </p>
          ) : isPending && requesterIsPerson ? (
            <p className="text-caption text-text-2">
              {t('detail.requesterCannotApprove')}
            </p>
          ) : null}

          {/* HU 047: the requester of an estate re-enable cannot decide it, and the panel
              offered nothing else. It says what is needed and where to do it. */}
          {isPending &&
          detail.action === REENABLE_ACTION &&
          !canDecideOnRequest(
            detail.requested_by,
            principal?.actor,
            principal?.kind,
          ) ? (
            <div
              className="text-body text-text-2"
              data-slot="reenable-needs-admins"
            >
              {/* The command in the sentence reads as a command (the inline chip with Copy),
                  not as text between backticks (Root, 047 review). */}
              {withInlineCommands(t('detail.reenableNeedsAdmins'))}{' '}
              <a
                href="/identity"
                className="text-accent-text underline underline-offset-2"
              >
                {t('detail.openIdentity')}
              </a>
            </div>
          ) : null}

          {isPending && (mayDecide || canWrite) ? (
            <div className="flex flex-wrap items-center gap-2">
              {mayDecide && (
                <Button
                  variant="primary"
                  size="sm"
                  disabled={deciding}
                  aria-keyshortcuts="A"
                  onClick={approveOnce}
                >
                  {/* Disabled only while its decision is sent, and it says so (dev#3268). */}
                  {deciding ? (
                    <Loader2 aria-hidden className="animate-spin" />
                  ) : (
                    <Check />
                  )}
                  {deciding
                    ? t('approvals.approving')
                    : t('approvals.approveOnce')}
                </Button>
              )}
              {mayDecide && (
                <Button
                  variant="destructive"
                  size="sm"
                  aria-keyshortcuts="D"
                  onClick={() => setDenyOpen(true)}
                >
                  {t('approvals.deny')}
                </Button>
              )}
              {canWrite && (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setConfirmCancel(true)}
                >
                  {t('approvals.cancel')}
                </Button>
              )}
            </div>
          ) : null}

          <ApprovalDetails approval={detail}>
            <Overview detail={detail} />
          </ApprovalDetails>

          <Separator />

          <section className="flex flex-col gap-2">
            <h3 className="text-body font-medium text-foreground">
              {t('detail.trail')}
            </h3>
            <p className="text-caption text-muted-foreground">
              {t('detail.trailCaption')}
            </p>
            <DecisionTrail
              items={decisions.data?.items ?? []}
              loading={decisions.isLoading}
              error={decisions.error}
              onRetry={() => decisions.refetch()}
            />
          </section>
        </>
      )}

      {/* Deny (HIGH risk, danger + note). */}
      {detail && (
        <DenyDialog
          open={denyOpen}
          onOpenChange={setDenyOpen}
          approval={detail}
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
    </aside>
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
      {detail.launched_by && (
        <KvRow label={t('detail.launchedBy')} mono>
          <span title={t('approvals.actorHint')}>{detail.launched_by}</span>
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
          {approvalCommandUnknown(detail) ? (
            // No command and no path in it (HU-R32): no command block, and it says so.
            <span className="flex flex-col gap-1">
              <span className="text-warning">
                {t('preview.commandUnknown')}
              </span>
              <span className="whitespace-pre-wrap break-all text-caption text-text-2">
                {detail.reason}
              </span>
            </span>
          ) : isToolCall(detail.subject_kind) ? (
            // A tool call's reason carries the command the reviewer approves:
            // shown whole, as code, wrapped at any character, scrolled when long.
            <pre
              data-slot="approval-command"
              className="max-h-80 overflow-y-auto whitespace-pre-wrap break-all rounded-md bg-muted px-2 py-1.5 font-mono text-caption text-foreground"
            >
              {detail.reason}
            </pre>
          ) : (
            detail.reason
          )}
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
  if (error) return <QueryErrorState error={error} retry={onRetry} />
  if (items.length === 0)
    return (
      <EmptyState
        description={t('detail.trailEmptyHint')}
        title={t('detail.trailEmpty')}
      />
    )

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
                <span className="text-caption text-muted-foreground">
                  <RelTimeLabel ts={d.decided_at} />
                </span>
              )}
            </div>
            <p
              className="mt-1 font-mono text-caption text-muted-foreground"
              title={t('approvals.actorHint')}
            >
              {d.decider}
            </p>
            {d.note && (
              <p className="mt-2 text-body text-foreground">{d.note}</p>
            )}
          </li>
        )
      })}
    </ol>
  )
}
