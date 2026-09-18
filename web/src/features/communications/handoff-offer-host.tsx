// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { useFreshRead } from '@/features/agentops/use-fresh-read'
import { useAuth } from '@/lib/auth/context'
import { offerHandoff, type HandoffOfferOutcome } from './api'
import { useCommunicationsScope, type CommunicationsScope } from './boundary'
import { freshly, type Fresh } from './fresh'
import { HandoffOfferDialog } from './handoff-offer-dialog'
import { useHandoffOperation } from './handoff-operation'
import { useIntentGuard, type HandoffOfferIntent } from './intent'
import type { Me } from './subject-picker'

/**
 * The narrow WorkItem projection this feature may see, and the whole of the
 * work → communications interface. It is a local view interface, not a wire
 * schema: work may import communications, communications never imports work.
 */
export interface HandoffWorkItemView {
  readonly item: {
    readonly id: string
    readonly workspace_id: string
    readonly title: string
    readonly status: string
    readonly owner_kind: string
    readonly owner_ref: string
    readonly owner_epoch: number
  }
  /** The server's own strong ETag. `null` when the read carried none — which is a
   * REFUSAL to offer, never a value to invent. */
  readonly etag: string | null
}

/** The fresh, uncached read the host performs through the work-side adapter. */
export type HandoffWorkItemReader = (
  itemId: string,
  signal: AbortSignal,
) => Promise<HandoffWorkItemView>

/**
 * What the work cockpit asks for. `invocation` distinguishes reopening the same
 * item as a new explicit editor action; an id alone cannot express "again".
 */
export interface HandoffOfferTarget {
  readonly itemId: string
  readonly invocation: number
}

/**
 * The communications-owned host of the offer, mounted by the work cockpit beside
 * its item sheet and keyed by the communications scope.
 *
 * It sits outside the item sheet because that sheet unmounts when the operator
 * closes it or opens another item, and a transmitted command must not die with a
 * panel. The item is re-read here rather than taken from the cockpit's cached
 * snapshot: the offer binds an `If-Match` and an `expected_owner_epoch` that have
 * to come from one read taken now, and the host verifies the returned id and
 * workspace and refuses to confirm without a server ETag.
 */
export function HandoffOfferHost({
  target,
  readItem,
  onTargetConsumed,
  getOpener,
  getFallbackFocus,
}: {
  target: HandoffOfferTarget | null
  readItem: HandoffWorkItemReader
  /** Called when the host has taken the target and the dialog is closed again, so
   * the cockpit can stop offering to reopen the same invocation. */
  onTargetConsumed?: () => void
  /** The control that opened the dialog, captured at the click. It usually lives
   * inside the item sheet, where a focusin observer cannot see it. */
  getOpener?: () => HTMLElement | null
  getFallbackFocus?: () => HTMLElement | null
}) {
  const scope = useCommunicationsScope()
  return (
    <Inner
      key={scope.key}
      scope={scope}
      target={target}
      readItem={readItem}
      onTargetConsumed={onTargetConsumed}
      getOpener={getOpener}
      getFallbackFocus={getFallbackFocus}
    />
  )
}

function Inner({
  scope,
  target,
  readItem,
  onTargetConsumed,
  getOpener,
  getFallbackFocus,
}: {
  scope: CommunicationsScope
  target: HandoffOfferTarget | null
  readItem: HandoffWorkItemReader
  onTargetConsumed?: () => void
  getOpener?: () => HTMLElement | null
  getFallbackFocus?: () => HTMLElement | null
}) {
  const { t } = useTranslation('communications')
  const { can, principal } = useAuth()
  const canOffer = can('sessions:message-send:write')
  // The Channel catalog is offered under ITS OWN read tier and nothing else. A
  // principal without it types the Channel id, because a directory permission is not
  // a prerequisite of a permitted write.
  const canChannelRead = can('sessions:channel:read')
  const canUserRead = can('user:read')
  const canAgentRead = can('agent:read')
  const me: Me = {
    userId: principal?.kind === 'user' ? principal.user_id : null,
    label: principal?.display_name ?? principal?.actor ?? '',
  }

  const [open, setOpen] = useState(false)
  /** The invocation the dialog is editing; the form is rebuilt by remounting on it. */
  const [editing, setEditing] = useState<HandoffOfferTarget | null>(null)
  /** A target that arrived while a transmitted command was still unresolved. */
  const [refusedTarget, setRefusedTarget] = useState(false)
  /** Stopping tracking of a command that may have been sent. Neutral, and cleared
   *  when the operator submits or abandons the next invocation. */
  const [stoppedAfterSend, setStoppedAfterSend] = useState(false)

  // The literal lives HERE, at the `useIntentGuard` call site, where the console
  // permission census can resolve it. The engine declares this exact string on
  // `POST /v1/m/sessions/handoffs`.
  const guard = useIntentGuard({
    allowed: canOffer,
    boundary: scope.key,
    permission: 'sessions:message-send:write',
  })
  const operation = useHandoffOperation<
    HandoffOfferIntent,
    HandoffOfferOutcome
  >({
    send: useCallback(
      (intent, options, signal) => offerHandoff(intent, options, signal),
      [],
    ),
    allowed: canOffer,
    guard,
  })

  const itemId = editing?.itemId ?? null
  const invocation = editing?.invocation ?? 0
  const read = useCallback(
    (signal: AbortSignal) => freshly(readItem(itemId as string, signal)),
    [itemId, readItem],
  )
  const fresh = useFreshRead<Fresh<HandoffWorkItemView>>({
    read,
    allowed: canOffer,
    boundary: scope.key,
  })
  const { start, stop } = fresh

  /**
   * One explicit invocation owns one reviewed intent. A new invocation discards an
   * unsent review so the displayed item and the submitted intent always agree; a
   * transmitted command that has not resolved is never replaced, and the operator
   * is shown that operation instead.
   *
   * Seeded with `null` rather than `target` so a host that mounts with a target
   * already set consumes it.
   */
  const [seenTarget, setSeenTarget] = useState<HandoffOfferTarget | null>(null)
  if (seenTarget !== target) {
    setSeenTarget(target)
    if (target) {
      if (operation.unresolved) {
        setRefusedTarget(true)
        setOpen(true)
      } else {
        operation.reset()
        setRefusedTarget(false)
        setStoppedAfterSend(false)
        setEditing(target)
        setOpen(true)
      }
    }
  }

  // Losing the write admission ends the local lifetime of this operation: no
  // editor, no reviewed intent and no receipt remain.
  const [seenAdmitted, setSeenAdmitted] = useState(canOffer)
  const [stranded, setStranded] = useState(false)
  if (seenAdmitted !== canOffer) {
    setSeenAdmitted(canOffer)
    if (!canOffer) {
      setStranded(editing !== null || operation.state.phase !== 'idle')
      setEditing(null)
      setOpen(false)
      setRefusedTarget(false)
      setStoppedAfterSend(false)
    } else {
      setStranded(false)
    }
  }

  // Keyed on the invocation as well as the item: reopening the same item is a new
  // explicit editor action and needs its own current validator and owner epoch.
  useEffect(() => {
    if (open && itemId) start()
    else stop()
  }, [open, itemId, invocation, start, stop])

  const close = () => {
    setOpen(false)
    setRefusedTarget(false)
    onTargetConsumed?.()
  }

  /**
   * Conflict recovery is a reread, not a relabel: the next review has to bind the
   * validator and owner epoch of a new observation, so the controller is reset and
   * the fresh read restarted together. Until it lands the editor has no snapshot
   * and cannot be reviewed.
   */
  const renew = useCallback(
    (mode: 'reset' | 'discard') => {
      const wasUnresolved =
        operation.unresolved || operation.priorUnresolvedAttempt
      if (mode === 'discard') operation.discard()
      else operation.reset()
      // Either ending act loses the local observation of an attempt that may have
      // reached the engine, so the neutral limit is stated for both.
      setStoppedAfterSend(wasUnresolved)
      // A new invocation: the editor remounts on this key, so the draft is
      // rebuilt, and the read effect is keyed on it, so the WorkItem is read
      // again. Nothing of the previous command's inputs survives into the next.
      setEditing((current) =>
        current
          ? { itemId: current.itemId, invocation: current.invocation + 1 }
          : current,
      )
    },
    [operation],
  )
  /** Conflict recovery is an observation, not a state-label reset. */
  const startOver = useCallback(() => renew('reset'), [renew])
  /** Explicit stop-tracking of a command that may have been sent. */
  const stopTracking = useCallback(() => renew('discard'), [renew])

  const state = fresh.state
  const answer = fresh.current && state.status === 'ready' ? state.data : null
  const view = answer && answer.ok ? answer.value : null
  const readFailure = answer && !answer.ok ? answer.failure : null

  // The retained operation is visible in the cockpit even with every panel closed:
  // an unresolved operation the operator cannot see is an operation they cannot
  // resolve. `confirmed` is offered too, so a receipt is not lost by a stray click.
  const retained =
    operation.state.phase === 'submitting' ||
    operation.state.phase === 'uncertain' ||
    operation.state.phase === 'confirmed'

  const strandedNotice = !canOffer && stranded

  return (
    <>
      {strandedNotice ? (
        <div
          role="status"
          className="mt-4 rounded-md border border-border bg-muted/30 px-3 py-2 text-caption"
          data-slot="handoff-offer-tracking-ended"
        >
          <p>{t('handoff.tracking.ended')}</p>
          {operation.state.phase === 'lost' && operation.state.sent ? (
            <p>{t('handoff.tracking.endedAfterSend')}</p>
          ) : null}
        </div>
      ) : null}
      {retained && !open ? (
        <div
          className="mt-4 flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-caption"
          data-slot="handoff-retained-operation"
        >
          <span>
            {operation.state.phase === 'confirmed'
              ? t('handoff.retained.confirmed')
              : operation.state.phase === 'uncertain'
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
      <HandoffOfferDialog
        // A new invocation rebuilds the draft; it is never inherited.
        key={editing ? `${editing.itemId}#${editing.invocation}` : 'none'}
        open={open}
        onOpenChange={(o) => (o ? setOpen(true) : close())}
        scope={scope}
        canOffer={canOffer}
        canChannelRead={canChannelRead}
        canUserRead={canUserRead}
        canAgentRead={canAgentRead}
        me={me}
        requestedItemId={itemId}
        view={view}
        readStatus={state.status}
        readFailure={readFailure}
        onReread={start}
        onStartOver={startOver}
        onStopTracking={stopTracking}
        stoppedAfterSend={stoppedAfterSend}
        operation={operation}
        targetRefused={refusedTarget}
        getOpener={getOpener}
        getFallbackFocus={getFallbackFocus}
      />
    </>
  )
}
