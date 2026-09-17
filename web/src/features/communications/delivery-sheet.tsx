// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { CheckCheck } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ForbiddenState } from '@/components/ui/error-state'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import { useFreshRead } from '@/features/agentops/use-fresh-read'
import { formatDateTime } from '@/lib/format'
import {
  ackDelivery,
  communicationsKeys,
  getDelivery,
  type AckOutcome,
} from './api'
import type { CommunicationsScope } from './boundary'
import { ContentView, FulfillmentView, Mono } from './content-blocks'
import { classifyFailure, type Failure } from './errors'
import { FailureNotice } from './failure-notice'
import { freshly, type Fresh } from './fresh'
import { buildAckIntent, useIntentGuard, type AckIntent } from './intent'
import type { ReadResult } from './types'

type AckPhase =
  | 'idle'
  | 'confirm'
  | 'acking'
  | 'applied'
  | 'replayed'
  | 'conflict'
  | 'ambiguous'
  | 'refused'

/**
 * DeliverySheet — one Delivery READ FRESH for its exact recipient, and the explicit
 * Ack. The Ack intention is built from the version of the read on screen
 * (`If-Match: "vN"`) with a key of its own; a 412, or the engine's bare 409 for a
 * stale or already-acknowledged delivery, is shown as a conflict and the
 * only way forward is to RE-READ and confirm again — the console never substitutes
 * the version. A late Ack is said to be late, never painted as fulfillment. Opening
 * the Message is a separate read under the message-read permission.
 */
export function DeliverySheet({
  open,
  onOpenChange,
  deliveryId,
  scope,
  canDeliveryRead,
  canDeliveryWrite,
  canMessageRead,
  onOpenMessage,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  deliveryId: string | null
  scope: CommunicationsScope
  canDeliveryRead: boolean
  canDeliveryWrite: boolean
  canMessageRead: boolean
  onOpenMessage: (messageId: string) => void
}) {
  const { t, i18n } = useTranslation('communications')
  const queryClient = useQueryClient()
  const tenant = scope.tenant
  const workspace = scope.workspace ?? ''
  const read = useCallback(
    (signal: AbortSignal) =>
      freshly(getDelivery(deliveryId ?? '', { tenant }, signal)),
    [deliveryId, tenant],
  )
  const fresh = useFreshRead<Fresh<ReadResult>>({
    read,
    allowed: canDeliveryRead,
    boundary: scope.key,
  })
  const { start, stop } = fresh
  useEffect(() => {
    if (open && deliveryId) start()
    else stop()
  }, [open, deliveryId, start, stop])

  const [phase, setPhase] = useState<AckPhase>('idle')
  const [intent, setIntent] = useState<AckIntent | null>(null)
  const [outcome, setOutcome] = useState<AckOutcome | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [lostCount, setLostCount] = useState(0)
  const guard = useIntentGuard({
    allowed: canDeliveryWrite,
    boundary: scope.key,
    permission: 'sessions:delivery:write',
  })

  if (!canDeliveryWrite && (phase === 'confirm' || phase === 'acking')) {
    setPhase('idle')
    setIntent(null)
    setLostCount((n) => n + 1)
  }
  useEffect(() => {
    if (lostCount > 0) toast.warning(t('authority.confirmationClosed'))
  }, [lostCount, t])
  // A new delivery in the same sheet is a new act: nothing of the previous Ack
  // survives (adjust-during-render, no extra paint of a stale receipt).
  const [seenDelivery, setSeenDelivery] = useState(deliveryId)
  if (seenDelivery !== deliveryId) {
    setSeenDelivery(deliveryId)
    setPhase('idle')
    setIntent(null)
    setOutcome(null)
    setFailure(null)
  }

  const ack = useMutation<AckOutcome, unknown, AckIntent>({
    mutationFn: (i) => {
      const signal = guard.begin()
      if (!signal) throw new AuthorityLostError()
      return ackDelivery(
        i,
        { tenant: i.scope.tenant, guard: guard.check },
        signal,
      )
    },
    onSuccess: (o) => {
      setOutcome(o)
      setPhase(o.result.replayed ? 'replayed' : 'applied')
      void queryClient.invalidateQueries({
        queryKey: communicationsKeys.workspaceScope(
          tenant,
          scope.epoch,
          workspace,
        ),
      })
      // The durable state, read again: the receipt is the act's, the read is the row's.
      start()
    },
    onError: (err) => {
      if (err instanceof AuthorityLostError) {
        setPhase('idle')
        setIntent(null)
        setLostCount((n) => n + 1)
        return
      }
      const f = classifyFailure(err)
      if (f.kind === 'aborted') {
        setPhase('confirm')
        return
      }
      setFailure(f)
      if (f.kind === 'version_mismatch' || f.kind === 'conflict') {
        // The intention dies with the version it was built on.
        setIntent(null)
        setPhase('conflict')
      } else if (f.kind === 'ambiguous') {
        setPhase('ambiguous')
      } else {
        setIntent(null)
        setPhase('refused')
      }
    },
  })

  const state = fresh.state
  const ready = fresh.current && state.status === 'ready' ? state.data : null
  const result = ready && ready.ok ? ready.value : null

  const beginAck = () => {
    if (!result || !canDeliveryWrite || !workspace) return
    setIntent(
      buildAckIntent(
        { tenant, workspace, boundary: scope.key },
        result.delivery.id,
        result.delivery.version,
      ),
    )
    setFailure(null)
    setOutcome(null)
    setPhase('confirm')
  }
  const confirmAck = () => {
    if (!intent || !canDeliveryWrite) return
    setPhase('acking')
    ack.mutate(intent)
  }
  const reread = () => {
    guard.end()
    setIntent(null)
    setFailure(null)
    setPhase('idle')
    start()
  }
  const cancelAck = () => {
    guard.end()
    setIntent(null)
    setPhase('idle')
  }

  return (
    <Sheet
      open={open}
      onOpenChange={(o) => (phase === 'acking' ? undefined : onOpenChange(o))}
    >
      <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{t('delivery.title')}</SheetTitle>
          <SheetDescription>{t('delivery.description')}</SheetDescription>
        </SheetHeader>
        {state.status === 'loading' || state.status === 'idle' ? (
          <div role="status" aria-busy="true">
            <span className="sr-only">{t('states.reading')}</span>
            <Skeleton className="h-40 w-full" />
          </div>
        ) : null}
        {state.status === 'forbidden' ? <ForbiddenState /> : null}
        {state.status === 'error' ? (
          <FailureNotice failure={{ kind: 'other', message: state.message }} />
        ) : null}
        {ready && !ready.ok ? <FailureNotice failure={ready.failure} /> : null}
        {result ? (
          <div className="flex flex-col gap-4" data-slot="delivery-detail">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="neutral">{result.delivery.state}</Badge>
              <Badge
                variant={
                  result.message.urgency === 'critical'
                    ? 'danger'
                    : result.message.urgency === 'high'
                      ? 'warning'
                      : 'outline'
                }
              >
                {t(`urgency.${result.message.urgency}`, {
                  defaultValue: result.message.urgency,
                })}
              </Badge>
              {result.delivery.required ? (
                <Badge variant="info">{t('delivery.fields.required')}</Badge>
              ) : null}
              <Badge variant="outline">
                <Mono>v{result.delivery.version}</Mono>
              </Badge>
            </div>
            <ContentView content={result.message.content} />
            <KvList>
              <KvRow label={t('delivery.fields.id')} mono>
                {result.delivery.id}
              </KvRow>
              <KvRow label={t('delivery.fields.messageId')} mono>
                {result.delivery.message_id}
              </KvRow>
              <KvRow label={t('delivery.fields.recipient')} mono>
                {result.delivery.recipient.kind}:{result.delivery.recipient.ref}
              </KvRow>
              <KvRow label={t('message.fields.sender')} mono>
                {result.message.sender.kind}:{result.message.sender.ref}
              </KvRow>
              <KvRow label={t('delivery.fields.seq')} mono>
                {result.delivery.delivery_seq}
              </KvRow>
              <KvRow label={t('delivery.fields.version')} mono>
                {result.delivery.version}
              </KvRow>
              <KvRow label={t('delivery.fields.available')}>
                {formatDateTime(result.delivery.available_at, i18n.language)}
              </KvRow>
              <KvRow label={t('delivery.fields.firstSeen')}>
                {formatDateTime(result.delivery.first_seen_at, i18n.language)}
              </KvRow>
              <KvRow label={t('delivery.fields.ackDue')}>
                {formatDateTime(result.delivery.ack_due_at, i18n.language)}
              </KvRow>
              <KvRow label={t('delivery.fields.expires')}>
                {formatDateTime(result.delivery.expires_at, i18n.language)}
              </KvRow>
              <KvRow label={t('delivery.fields.acknowledged')}>
                <span data-slot="acknowledged-at">
                  {formatDateTime(
                    result.delivery.acknowledged_at,
                    i18n.language,
                  )}
                </span>
              </KvRow>
            </KvList>
            <section aria-label={t('fulfillment.title')}>
              <p className="mb-1 text-sm font-medium">
                {t('fulfillment.title')}
              </p>
              <FulfillmentView fulfillment={result.fulfillment} />
            </section>

            {phase === 'confirm' || phase === 'acking' ? (
              intent ? (
                <div
                  className="flex flex-col gap-2 rounded-md border border-border bg-muted p-3"
                  data-slot="ack-intent"
                >
                  <p className="text-sm font-medium">
                    {t('delivery.ack.title')}
                  </p>
                  <p className="text-sm text-muted-foreground">
                    {t('delivery.ack.body', { etag: intent.etag })}
                  </p>
                  <KvList>
                    <KvRow label={t('delivery.ack.ifMatch')} mono>
                      {intent.etag}
                    </KvRow>
                    <KvRow label={t('delivery.ack.key')} mono>
                      {intent.key}
                    </KvRow>
                  </KvList>
                </div>
              ) : null
            ) : null}
            {phase === 'conflict' && failure ? (
              <div data-slot="ack-conflict">
                <FailureNotice
                  failure={failure}
                  title={t('delivery.ack.conflictTitle')}
                />
                <p className="mt-1 text-sm text-muted-foreground">
                  {t('delivery.ack.conflictBody')}
                </p>
              </div>
            ) : null}
            {phase === 'ambiguous' && failure ? (
              <div data-slot="ack-ambiguous">
                <FailureNotice
                  failure={failure}
                  title={t('delivery.ack.ambiguousTitle')}
                />
                <p className="mt-1 text-sm text-muted-foreground">
                  {t('delivery.ack.ambiguousBody')}
                </p>
              </div>
            ) : null}
            {phase === 'refused' && failure ? (
              <FailureNotice
                failure={failure}
                title={t('delivery.ack.refusedTitle')}
              />
            ) : null}
            {(phase === 'applied' || phase === 'replayed') && outcome ? (
              <section
                aria-label={t('receipt.title')}
                className="flex flex-col gap-2"
                data-slot="ack-receipt"
              >
                <div
                  role="status"
                  className={
                    outcome.result.replayed
                      ? 'rounded-md border border-info-line bg-info-soft px-3 py-2 text-sm text-info'
                      : 'rounded-md border border-success-line bg-success-soft px-3 py-2 text-sm text-success'
                  }
                >
                  <p className="font-medium">
                    {outcome.result.replayed
                      ? t('delivery.ack.replayedTitle')
                      : t('delivery.ack.appliedTitle')}
                  </p>
                  <p>
                    {outcome.result.replayed
                      ? t('delivery.ack.replayedBody')
                      : t('delivery.ack.appliedBody')}
                  </p>
                </div>
                {outcome.result.late ? (
                  <div
                    role="alert"
                    className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-sm text-warning"
                    data-slot="ack-late"
                  >
                    {t('delivery.ack.late')}
                  </div>
                ) : null}
                <KvList>
                  <KvRow label={t('receipt.ackId')} mono>
                    {outcome.result.ack_id}
                  </KvRow>
                  <KvRow label={t('receipt.commandId')} mono>
                    {outcome.result.command_id}
                  </KvRow>
                  <KvRow label={t('receipt.eventId')} mono>
                    {outcome.result.event_id}
                  </KvRow>
                  <KvRow label={t('receipt.state')} mono>
                    {outcome.result.state}
                  </KvRow>
                  <KvRow label={t('receipt.version')} mono>
                    {outcome.result.version}
                  </KvRow>
                  <KvRow label={t('receipt.etag')} mono>
                    {outcome.etag ?? outcome.result.etag}
                  </KvRow>
                  <KvRow label={t('receipt.late')} mono>
                    {String(outcome.result.late)}
                  </KvRow>
                  <KvRow label={t('receipt.auditSeq')} mono>
                    {outcome.result.audit_seq}
                  </KvRow>
                </KvList>
                <FulfillmentView fulfillment={outcome.result.fulfillment} />
              </section>
            ) : null}
            {!canDeliveryWrite ? (
              <p className="text-xs text-muted-foreground">
                {t('delivery.ack.noPermission')}
              </p>
            ) : null}
            {!canMessageRead ? (
              <p className="text-xs text-muted-foreground">
                {t('delivery.noMessageRead')}
              </p>
            ) : null}
          </div>
        ) : null}
        <SheetFooter>
          <Button
            type="button"
            variant="outline"
            onClick={reread}
            disabled={!canDeliveryRead || !deliveryId || phase === 'acking'}
          >
            {t('actions.reread')}
          </Button>
          {result && canMessageRead ? (
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenMessage(result.message.id)}
              title={t('delivery.openMessageHint')}
            >
              {t('actions.openMessage')}
            </Button>
          ) : null}
          {result &&
          canDeliveryWrite &&
          (phase === 'idle' ||
            phase === 'applied' ||
            phase === 'replayed' ||
            phase === 'refused') ? (
            <Button type="button" variant="primary" onClick={beginAck}>
              <CheckCheck className="size-4" aria-hidden="true" />
              {t('actions.ack')}
            </Button>
          ) : null}
          {phase === 'confirm' ? (
            <>
              <Button type="button" variant="secondary" onClick={cancelAck}>
                {t('actions.cancel')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={confirmAck}
                disabled={!canDeliveryWrite}
              >
                {t('actions.confirmAck')}
              </Button>
            </>
          ) : null}
          {phase === 'acking' ? (
            <Button type="button" variant="primary" disabled>
              <Spinner className="size-3.5" />
              {t('actions.acking')}
            </Button>
          ) : null}
          {phase === 'ambiguous' ? (
            <>
              <Button type="button" variant="secondary" onClick={cancelAck}>
                {t('actions.cancel')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={confirmAck}
                disabled={!canDeliveryWrite}
              >
                {t('actions.retrySameKey')}
              </Button>
            </>
          ) : null}
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
