// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect } from 'react'
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
import { useFreshRead } from '@/features/agentops/use-fresh-read'
import { formatDateTime } from '@/lib/format'
import { getMessage } from './api'
import type { CommunicationsScope } from './boundary'
import { ContentView, FulfillmentView, Mono } from './content-blocks'
import { FailureNotice } from './failure-notice'
import { freshly, type Fresh } from './fresh'
import type { ReadResult } from './types'

/**
 * MessageSheet — `GET /messages/{id}` under the message-read permission, read
 * fresh for the exact user recipient. It carries the Delivery the read returns
 * but offers no Ack: acknowledging is the Delivery's act, with the Delivery's id.
 */
export function MessageSheet({
  open,
  onOpenChange,
  messageId,
  scope,
  canMessageRead,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  messageId: string | null
  scope: CommunicationsScope
  canMessageRead: boolean
}) {
  const { t, i18n } = useTranslation('communications')
  const tenant = scope.tenant
  const read = useCallback(
    (signal: AbortSignal) =>
      freshly(getMessage(messageId ?? '', { tenant }, signal)),
    [messageId, tenant],
  )
  const fresh = useFreshRead<Fresh<ReadResult>>({
    read,
    allowed: canMessageRead,
    boundary: scope.key,
  })
  const { start, stop } = fresh
  useEffect(() => {
    if (open && messageId) start()
    else stop()
  }, [open, messageId, start, stop])
  const state = fresh.state
  const ready = fresh.current && state.status === 'ready' ? state.data : null
  const result = ready && ready.ok ? ready.value : null

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{t('message.title')}</SheetTitle>
          <SheetDescription>{t('message.description')}</SheetDescription>
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
          <div className="flex flex-col gap-4" data-slot="message-detail">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="neutral">{result.message.state}</Badge>
              <Badge variant="outline">
                {t(`urgency.${result.message.urgency}`, {
                  defaultValue: result.message.urgency,
                })}
              </Badge>
              <Badge variant="outline">
                <Mono>v{result.message.version}</Mono>
              </Badge>
            </div>
            <ContentView content={result.message.content} />
            <KvList>
              <KvRow label={t('message.fields.id')} mono>
                {result.message.id}
              </KvRow>
              <KvRow label={t('message.fields.channel')} mono>
                {result.message.channel_id}
              </KvRow>
              <KvRow label={t('message.fields.thread')} mono>
                {result.message.thread_id}
              </KvRow>
              <KvRow label={t('message.fields.sender')} mono>
                {result.message.sender.kind}:{result.message.sender.ref}
              </KvRow>
              <KvRow label={t('message.fields.ackPolicy')} mono>
                {result.message.ack_policy}
              </KvRow>
              {result.message.ack_quorum !== undefined ? (
                <KvRow label={t('message.fields.ackQuorum')} mono>
                  {result.message.ack_quorum}
                </KvRow>
              ) : null}
              <KvRow label={t('message.fields.available')}>
                {formatDateTime(result.message.available_at, i18n.language)}
              </KvRow>
              <KvRow label={t('message.fields.published')}>
                {formatDateTime(result.message.published_at, i18n.language)}
              </KvRow>
              <KvRow label={t('message.fields.ackDue')}>
                {formatDateTime(result.message.ack_due_at, i18n.language)}
              </KvRow>
              <KvRow label={t('message.fields.expires')}>
                {formatDateTime(result.message.expires_at, i18n.language)}
              </KvRow>
              <KvRow label={t('message.fields.terminal')}>
                {formatDateTime(result.message.terminal_at, i18n.language)}
              </KvRow>
              <KvRow label={t('message.fields.terminalCode')} mono>
                {result.message.terminal_code || '—'}
              </KvRow>
              <KvRow label={t('delivery.fields.id')} mono>
                {result.delivery.id}
              </KvRow>
              <KvRow label={t('delivery.fields.state')} mono>
                {result.delivery.state}
              </KvRow>
              <KvRow label={t('delivery.fields.version')} mono>
                {result.delivery.version}
              </KvRow>
              <KvRow label={t('delivery.fields.acknowledged')}>
                {formatDateTime(result.delivery.acknowledged_at, i18n.language)}
              </KvRow>
            </KvList>
            <section aria-label={t('fulfillment.title')}>
              <p className="mb-1 text-sm font-medium">
                {t('fulfillment.title')}
              </p>
              <FulfillmentView fulfillment={result.fulfillment} />
            </section>
          </div>
        ) : null}
        <SheetFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => start()}
            disabled={!canMessageRead || !messageId}
          >
            {t('actions.reread')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
