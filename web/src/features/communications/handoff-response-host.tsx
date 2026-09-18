// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { workItemQueryKeys } from '@/lib/api/work-query-keys'
import {
  communicationsKeys,
  respondToHandoff,
  type HandoffResponseOutcome,
} from './api'
import type { CommunicationsScope } from './boundary'
import { useHandoffOperation } from './handoff-operation'
import {
  HandoffRespondDialog,
  type HandoffRespondTarget,
} from './handoff-respond-dialog'
import {
  buildHandoffResponseIntent,
  useIntentGuard,
  type HandoffResponseIntent,
} from './intent'
import type { HandoffResponseInput } from './types'

/**
 * The room's owner of an accept or a reject.
 *
 * It is mounted beside the detail sheet, not inside it: closing the sheet clears
 * the protected read, and a transmitted mutation must not be cleared with it. The
 * operation lives here until it resolves, until the operator stops local tracking,
 * or until this context is torn down.
 *
 * Only the intent's administrative half survives a closed sheet — handoff id,
 * WorkItem reference, recipient, validator, key. The summary, next action and risk
 * die with the fresh read.
 */
export function HandoffResponseHost({
  scope,
  canRespond,
  canDeliveryRead,
  target,
  getOpener,
  getFallbackFocus,
  onTargetConsumed,
  onResolved,
  onRequestFreshDetail,
}: {
  scope: CommunicationsScope
  canRespond: boolean
  canDeliveryRead: boolean
  target: HandoffRespondTarget | null
  /** The sheet control that opened this dialog, captured at the click. */
  getOpener?: () => HTMLElement | null
  getFallbackFocus?: () => HTMLElement | null
  onTargetConsumed: () => void
  /**
   * A resolved or conflicting response changed durable state elsewhere. The room
   * rereads what it owns; a receipt is this command's result, not an observation.
   */
  onResolved: (outcome: { workItemId: string; settled: boolean }) => void
  /**
   * Ask the open Delivery detail's own read owner for a fresh read. It is a
   * request, not a claim: it never asserts that a command settled, and the read
   * keeps its own scope, admission and late-result rejection. The host does not
   * read the Delivery itself — that would duplicate an HTTP read in this boundary.
   */
  onRequestFreshDetail?: () => void
}) {
  const { t } = useTranslation('communications')
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [held, setHeld] = useState<HandoffRespondTarget | null>(null)
  /** A target that arrived while a transmitted command was still unresolved. */
  const [refusedTarget, setRefusedTarget] = useState(false)
  /** Stopping tracking of a command that may have been sent. Neutral, and shown
   *  until the operator starts the next invocation. */
  const [stoppedAfterSend, setStoppedAfterSend] = useState(false)

  // Both permissions, as literals at this call site so the console-permission
  // census can resolve them. The act is a handoff-response write; its confirmation
  // presents protected detail that only delivery:read admits, so losing the read
  // must stop the dispatch as well as the rendering.
  const guard = useIntentGuard({
    allowed: canRespond && canDeliveryRead,
    boundary: scope.key,
    permission: 'sessions:handoff-response:write',
    alsoRequires: 'sessions:delivery:read',
  })
  const admitted = canRespond && canDeliveryRead
  const operation = useHandoffOperation<
    HandoffResponseIntent,
    HandoffResponseOutcome
  >({
    send: useCallback(
      (intent, options, signal) => respondToHandoff(intent, options, signal),
      [],
    ),
    allowed: admitted,
    guard,
  })

  /**
   * One explicit invocation owns one reviewed intent.
   *
   * A new target is a new invocation: any unsent review of the previous one is
   * discarded, so the displayed WorkItem, recipient and transition always describe
   * the intent that would be submitted. A transmitted command that has not
   * resolved is never replaced — the operator is shown that operation and has to
   * resolve it or stop tracking first.
   */
  const [seen, setSeen] = useState<HandoffRespondTarget | null>(null)
  if (seen !== target) {
    setSeen(target)
    if (target) {
      if (operation.unresolved) {
        setRefusedTarget(true)
        setOpen(true)
      } else {
        operation.reset()
        setRefusedTarget(false)
        setStoppedAfterSend(false)
        setHeld(target)
        setOpen(true)
      }
    }
  }

  // Protected state follows admission: with the read gone there is no target, no
  // reason draft (the dialog remounts on the key) and no receipt to show.
  const [seenAdmitted, setSeenAdmitted] = useState(admitted)
  const [stranded, setStranded] = useState(false)
  if (seenAdmitted !== admitted) {
    setSeenAdmitted(admitted)
    if (!admitted) {
      setStranded(held !== null || operation.state.phase !== 'idle')
      setHeld(null)
      setOpen(false)
      setRefusedTarget(false)
      setStoppedAfterSend(false)
    } else {
      setStranded(false)
    }
  }

  // Held in a ref so an inline callback from the room cannot make the effect below
  // re-run — and re-invalidate — on every render.
  const onResolvedRef = useRef(onResolved)
  useEffect(() => {
    onResolvedRef.current = onResolved
  })

  const phase = operation.state.phase
  const settledWorkItemId =
    operation.state.phase === 'confirmed'
      ? operation.state.intent.workItemId
      : null
  const conflicted = phase === 'conflict'
  const conflictWorkItemId = held?.workItemId ?? null

  useEffect(() => {
    if (!settledWorkItemId && !conflicted) return
    if (!scope.workspace) return
    void queryClient.invalidateQueries({
      queryKey: communicationsKeys.workspaceScope(
        scope.tenant,
        scope.epoch,
        scope.workspace,
      ),
    })
    // Only the captured tenant's WorkItem collection prefix and the affected item
    // detail. Sibling Work families and other tenants are untouched.
    const itemId = settledWorkItemId ?? conflictWorkItemId
    void queryClient.invalidateQueries({
      queryKey: workItemQueryKeys.collection(scope.tenant),
    })
    if (itemId) {
      void queryClient.invalidateQueries({
        queryKey: workItemQueryKeys.detail(scope.tenant, itemId),
      })
    }
    onResolvedRef.current({
      workItemId: itemId ?? '',
      settled: settledWorkItemId !== null,
    })
  }, [settledWorkItemId, conflicted, conflictWorkItemId, queryClient, scope])

  /**
   * End this invocation. The reviewed intent, the held target with its validator
   * and the reason draft all belong to it, so the controller is ended, the target
   * is dropped and the editor is closed — the draft dies with the unmounted
   * dialog. A later response must be invoked again from an admitted, freshly read
   * Delivery detail, so the open detail is asked to refresh.
   *
   * `mode` separates editing an unsent review from abandoning observation of a
   * command that may have been sent: `reset` is refused while unresolved, and
   * `discard` is the explicit stop-tracking act.
   */
  const endInvocation = useCallback(
    (mode: 'reset' | 'discard') => {
      const wasUnresolved =
        operation.unresolved || operation.priorUnresolvedAttempt
      if (mode === 'discard') operation.discard()
      else operation.reset()
      setHeld(null)
      setOpen(false)
      setRefusedTarget(false)
      // Either ending act loses the local observation of an attempt that may have
      // reached the engine, so the neutral limit is stated for both.
      setStoppedAfterSend(wasUnresolved)
      onTargetConsumed()
      onRequestFreshDetail?.()
    },
    [operation, onTargetConsumed, onRequestFreshDetail],
  )

  const buildIntent = useCallback(
    (chosen: HandoffRespondTarget, body: HandoffResponseInput) =>
      buildHandoffResponseIntent(
        {
          tenant: scope.tenant,
          workspace: scope.workspace ?? '',
          boundary: scope.key,
        },
        {
          handoffId: chosen.handoffId,
          etag: chosen.etag,
          workItemId: chosen.workItemId,
          recipient: chosen.recipient as HandoffResponseIntent['recipient'],
          deliveryId: chosen.deliveryId,
        },
        body,
      ),
    [scope],
  )

  const retained =
    phase === 'submitting' || phase === 'uncertain' || phase === 'confirmed'
  const strandedNotice = !admitted && stranded

  return (
    <>
      {strandedNotice ? (
        <div
          role="status"
          className="mt-4 rounded-md border border-border bg-muted/30 px-3 py-2 text-caption"
          data-slot="handoff-response-tracking-ended"
        >
          <p>{t('handoff.tracking.ended')}</p>
          {operation.state.phase === 'lost' && operation.state.sent ? (
            <p>{t('handoff.tracking.endedAfterSend')}</p>
          ) : null}
        </div>
      ) : null}
      {stoppedAfterSend ? (
        <div
          role="status"
          className="mt-4 rounded-md border border-border bg-muted/30 px-3 py-2 text-caption"
          data-slot="handoff-response-stopped"
        >
          <p>{t('handoff.tracking.stoppedAfterSend')}</p>
        </div>
      ) : null}
      {retained && !open ? (
        <div
          className="mt-4 flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-caption"
          data-slot="handoff-response-retained"
        >
          <span>
            {phase === 'confirmed'
              ? t('handoff.retained.responseConfirmed')
              : phase === 'uncertain'
                ? t('handoff.retained.uncertain')
                : t('handoff.retained.submitting')}
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setOpen(true)}
          >
            {t('handoff.retained.reopen')}
          </Button>
        </div>
      ) : null}
      {admitted ? (
        <HandoffRespondDialog
          // A new invocation rebuilds the editor; the reason draft is never
          // inherited from a previous target.
          key={
            held ? `${held.handoffId}#${held.transition}#${held.etag}` : 'none'
          }
          open={open}
          onOpenChange={(o) => {
            if (o) {
              setOpen(true)
              return
            }
            setOpen(false)
            setRefusedTarget(false)
            onTargetConsumed()
          }}
          target={held}
          operation={operation}
          canRespond={admitted}
          buildIntent={buildIntent}
          onEndInvocation={endInvocation}
          targetRefused={refusedTarget}
          getOpener={getOpener}
          getFallbackFocus={getFallbackFocus}
        />
      ) : null}
    </>
  )
}
