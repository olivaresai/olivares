// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
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
import { KvList, KvRow } from '@/components/ui/kv'
import { Spinner } from '@/components/ui/spinner'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import { formatDateTime } from '@/lib/format'
import { advanceCursor, type CursorAdvanceOutcome } from './api'
import { Mono } from './content-blocks'
import type { Failure } from './errors'
import { FailureNotice } from './failure-notice'
import type { CursorIntent, IntentGuard } from './intent'
import { useReturnFocus } from './return-focus'
import type { InboxItem } from './types'

/** What the operator prepared: ONE real, non-empty inbox page, its opaque
 * `cursor_target`, and the exact last Delivery the target was minted for. */
export interface CursorPreparation {
  /** 1-based page number in the chain the operator loaded. */
  pageNumber: number
  target: string
  deliveries: number
  last: InboxItem
}

/** The token the engine minted for the preparation. The token string itself is
 * shown NOWHERE — not in the dialog, not in the URL, not in a screenshot. */
export interface MintedCursor {
  cursor: string
  cursorId?: string
  version: number
  etag: string
}

export type CursorPhase =
  | 'preparing'
  | 'confirm'
  | 'advancing'
  | 'applied'
  | 'conflict'
  | 'ambiguous'
  | 'refused'
  | 'prepareFailed'

/**
 * InboxCursorDialog — «Mark seen up to here» for ONE inbox page. The GET has
 * already minted the cursor token when this opens in `confirm`; nothing has moved.
 * Only «Confirm» sends the PUT, with the body, the CURSOR ETag, the recipient and
 * the key that were frozen together into the intention. The result is either the
 * durable position the engine returned or an advance LIMITED BY A BARRIER, named
 * only with the reason and the Delivery id the engine sent; opening that Delivery
 * is a fresh read, never a second PUT. A 409/412/428 kills the intention; a lost
 * response is retried EXPLICITLY with the same object or left undetermined.
 */
export function InboxCursorDialog({
  open,
  phase,
  preparation,
  intent,
  recipient,
  workspaceName,
  outcome,
  failure,
  canDeliveryWrite,
  guard,
  onConfirm,
  onRetrySame,
  onDismiss,
  onOpenDelivery,
  onRefreshInbox,
  onAdvanced,
  onFailed,
}: {
  open: boolean
  phase: CursorPhase
  preparation: CursorPreparation | null
  intent: CursorIntent | null
  recipient: string
  workspaceName: string
  outcome: CursorAdvanceOutcome | null
  failure: Failure | null
  canDeliveryWrite: boolean
  guard: IntentGuard
  onConfirm: () => void
  onRetrySame: () => void
  onDismiss: () => void
  onOpenDelivery: (deliveryId: string) => void
  onRefreshInbox: () => void
  onAdvanced: (o: CursorAdvanceOutcome) => void
  onFailed: (err: unknown) => void
}) {
  const { t, i18n } = useTranslation('communications')
  const returnFocus = useReturnFocus(open)
  const advance = useMutation<CursorAdvanceOutcome, unknown, CursorIntent>({
    mutationFn: (i) => {
      const signal = guard.begin()
      if (!signal) throw new AuthorityLostError()
      return advanceCursor(
        i,
        { tenant: i.scope.tenant, guard: guard.check },
        signal,
      )
    },
    onSuccess: onAdvanced,
    onError: onFailed,
  })
  const busy = phase === 'advancing' || phase === 'preparing'
  const projection = outcome?.result.projection
  const barrier = projection?.barrier_delivery_id ?? null

  const send = () => {
    if (!intent || !canDeliveryWrite) return
    onConfirm()
    advance.mutate(intent)
  }
  const retry = () => {
    if (!intent || !canDeliveryWrite) return
    onRetrySame()
    // The SAME intention: same body, same If-Match, same key, same authority.
    advance.mutate(intent)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (o || busy ? undefined : onDismiss())}
    >
      {/* BOUNDED HEIGHT, SCROLLING BODY. `DialogContent` is a centred grid with no
          ceiling, so a tall child grows past the viewport in both directions and the
          footer becomes unreachable — measured in the browser (run 4): after a cursor
          receipt rendered, «Dismiss» was "outside of the viewport" and 465 click
          attempts could not scroll it into view. The ceiling and the scroll region
          live HERE, on this dialog, rather than in the shared component another
          delivery owns: three rows, and only the middle one scrolls, so the title
          and the actions are always on screen. */}
      <DialogContent
        className="grid max-h-[85vh] max-w-xl grid-rows-[auto_minmax(0,1fr)_auto]"
        data-slot="cursor-dialog"
        onCloseAutoFocus={returnFocus}
      >
        <DialogHeader>
          <DialogTitle>{t('cursor.title')}</DialogTitle>
          <DialogDescription>{t('cursor.description')}</DialogDescription>
        </DialogHeader>
        <div className="flex min-h-0 flex-col gap-3 overflow-y-auto">
          {preparation ? (
            <KvList>
              <KvRow label={t('cursor.fields.scope')}>
                {t('cursor.fields.scopeValue', {
                  page: preparation.pageNumber,
                  count: preparation.deliveries,
                })}
              </KvRow>
              <KvRow label={t('cursor.fields.workspace')}>
                {workspaceName}
              </KvRow>
              <KvRow label={t('cursor.fields.recipient')} mono>
                user:{recipient}
              </KvRow>
              <KvRow label={t('cursor.fields.lastSubject')} align="start">
                <span className="break-words">
                  {preparation.last.message.content.subject ||
                    t('content.empty')}
                </span>
              </KvRow>
              <KvRow label={t('cursor.fields.lastDelivery')} mono align="start">
                <span className="break-all" data-slot="cursor-last-delivery">
                  {preparation.last.delivery.id}
                </span>
              </KvRow>
              <KvRow label={t('cursor.fields.lastSeq')} mono>
                {preparation.last.delivery.delivery_seq}
              </KvRow>
            </KvList>
          ) : null}
          {phase === 'preparing' ? (
            <p
              role="status"
              className="inline-flex items-center gap-2 text-body"
            >
              <Spinner className="size-3.5" />
              {t('cursor.preparing')}
            </p>
          ) : null}
          {phase === 'prepareFailed' && failure ? (
            <div data-slot="cursor-prepare-failed">
              <FailureNotice
                failure={failure}
                title={t('cursor.prepareFailedTitle')}
              />
              <p className="mt-1 text-body text-muted-foreground">
                {t('cursor.prepareFailedBody')}
              </p>
            </div>
          ) : null}
          {(phase === 'confirm' ||
            phase === 'advancing' ||
            phase === 'ambiguous') &&
          intent ? (
            <div
              className="flex flex-col gap-2 rounded-md border border-border bg-muted p-3"
              data-slot="cursor-intent"
            >
              <p className="text-body font-medium">
                {t('cursor.confirm.title')}
              </p>
              <p className="text-body text-muted-foreground">
                {t('cursor.confirm.body')}
              </p>
              <KvList>
                <KvRow label={t('cursor.fields.cursorVersion')} mono>
                  {intent.version}
                </KvRow>
                <KvRow label={t('delivery.ack.ifMatch')} mono>
                  {intent.etag}
                </KvRow>
                <KvRow label={t('delivery.ack.key')} mono>
                  {intent.key}
                </KvRow>
              </KvList>
            </div>
          ) : null}
          {phase === 'conflict' && failure ? (
            <div data-slot="cursor-conflict">
              <FailureNotice
                failure={failure}
                title={t('cursor.conflictTitle')}
              />
              <p className="mt-1 text-body text-muted-foreground">
                {t('cursor.conflictBody')}
              </p>
            </div>
          ) : null}
          {phase === 'ambiguous' && failure ? (
            <div data-slot="cursor-ambiguous">
              <FailureNotice
                failure={failure}
                title={t('cursor.ambiguousTitle')}
              />
              <p className="mt-1 text-body text-muted-foreground">
                {t('cursor.ambiguousBody')}
              </p>
            </div>
          ) : null}
          {phase === 'refused' && failure ? (
            <div data-slot="cursor-refused">
              <FailureNotice
                failure={failure}
                title={t('cursor.refusedTitle')}
              />
              <p className="mt-1 text-body text-muted-foreground">
                {t('cursor.refusedBody')}
              </p>
            </div>
          ) : null}
          {phase === 'applied' && outcome ? (
            <section
              aria-label={t('receipt.title')}
              className="flex flex-col gap-2"
              data-slot="cursor-receipt"
            >
              <div
                role="status"
                className={
                  barrier
                    ? 'rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-body text-warning'
                    : outcome.result.replayed
                      ? 'rounded-md border border-info-line bg-info-soft px-3 py-2 text-body text-info'
                      : 'rounded-md border border-success-line bg-success-soft px-3 py-2 text-body text-success'
                }
              >
                <p className="font-medium">
                  {barrier
                    ? t('cursor.result.barrierTitle')
                    : outcome.result.replayed
                      ? t('cursor.result.replayedTitle')
                      : t('cursor.result.appliedTitle')}
                </p>
                <p>
                  {barrier
                    ? t('cursor.result.barrierBody')
                    : outcome.result.replayed
                      ? t('cursor.result.replayedBody')
                      : t('cursor.result.appliedBody')}
                </p>
              </div>
              <KvList>
                <KvRow label={t('receipt.commandId')} mono>
                  {outcome.result.command_id}
                </KvRow>
                <KvRow label={t('cursor.fields.cursorId')} mono>
                  {outcome.result.cursor_id}
                </KvRow>
                <KvRow label={t('receipt.version')} mono>
                  {outcome.result.version}
                </KvRow>
                <KvRow label={t('receipt.etag')} mono>
                  {outcome.etag ?? outcome.result.etag}
                </KvRow>
                <KvRow label={t('receipt.auditSeq')} mono>
                  {outcome.result.audit_seq}
                </KvRow>
                <KvRow label={t('cursor.fields.replayed')} mono>
                  {String(outcome.result.replayed)}
                </KvRow>
                {projection?.last_seen_seq !== undefined ? (
                  <KvRow label={t('cursor.fields.lastSeenSeq')} mono>
                    <span data-slot="cursor-last-seen-seq">
                      {projection.last_seen_seq}
                    </span>
                  </KvRow>
                ) : null}
                {barrier ? (
                  <>
                    <KvRow
                      label={t('cursor.fields.barrierDelivery')}
                      mono
                      align="start"
                    >
                      <span className="break-all" data-slot="cursor-barrier">
                        {barrier}
                      </span>
                    </KvRow>
                    <KvRow label={t('cursor.fields.barrierReason')}>
                      <Badge variant="warning">
                        <Mono>{projection?.barrier_reason ?? '—'}</Mono>
                      </Badge>
                    </KvRow>
                    <KvRow label={t('cursor.fields.barrierSince')}>
                      {formatDateTime(projection?.barrier_since, i18n.language)}
                    </KvRow>
                  </>
                ) : null}
              </KvList>
              <p className="text-caption text-muted-foreground">
                {t('cursor.result.notAck')}
              </p>
              {barrier ? (
                <p className="text-caption text-muted-foreground">
                  {t('cursor.result.barrierHint')}
                </p>
              ) : null}
            </section>
          ) : null}
        </div>
        <DialogFooter>
          {phase === 'confirm' ? (
            <>
              <Button type="button" variant="secondary" onClick={onDismiss}>
                {t('actions.cancel')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={send}
                disabled={!intent || !canDeliveryWrite}
              >
                {t('actions.confirmSeen')}
              </Button>
            </>
          ) : null}
          {phase === 'advancing' ? (
            <Button type="button" variant="primary" disabled>
              <Spinner className="size-3.5" />
              {t('actions.advancing')}
            </Button>
          ) : null}
          {phase === 'ambiguous' ? (
            <>
              <Button type="button" variant="secondary" onClick={onDismiss}>
                {t('actions.leaveUndetermined')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={retry}
                disabled={!intent || !canDeliveryWrite}
              >
                {t('actions.retrySameKey')}
              </Button>
            </>
          ) : null}
          {phase === 'conflict' ||
          phase === 'refused' ||
          phase === 'prepareFailed' ? (
            <>
              <Button type="button" variant="secondary" onClick={onDismiss}>
                {t('actions.dismiss')}
              </Button>
              <Button type="button" variant="primary" onClick={onRefreshInbox}>
                {t('actions.refreshInbox')}
              </Button>
            </>
          ) : null}
          {phase === 'applied' ? (
            <>
              {barrier ? (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => onOpenDelivery(barrier)}
                >
                  {t('actions.openBarrierDelivery')}
                </Button>
              ) : null}
              <Button type="button" variant="primary" onClick={onDismiss}>
                {t('actions.dismiss')}
              </Button>
            </>
          ) : null}
          {phase === 'preparing' ? (
            <Button type="button" variant="secondary" onClick={onDismiss}>
              {t('actions.cancel')}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
