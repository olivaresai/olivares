// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Check, X } from 'lucide-react'
import { type FocusEvent, useCallback, useEffect, useRef } from 'react'
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
import { getHandoffDetail } from './api'
import type { CommunicationsScope } from './boundary'
import { Mono } from './content-blocks'
import { FailureNotice } from './failure-notice'
import { freshly, type Fresh } from './fresh'
import { useReturnFocus } from './return-focus'
import type { HandoffDetail, HandoffTransition } from './types'

/**
 * One offer's protected context, read fresh for its exact recipient through its
 * carrier Delivery, and the two responses.
 *
 * The response precondition is `detail.handoff.etag`, the validator the engine
 * issued for this entity — not the carrier's `delivery_version`, which belongs to
 * the Ack route, and never an ETag rebuilt from an integer.
 *
 * The read is uncached: `useFreshRead` replaces its state on every cycle and
 * refuses a response delivered after the permission or the boundary moved. Nothing
 * protected reaches a query key, the URL or browser storage; the URL carries only
 * the carrier Delivery id.
 *
 * `offer_context` is the engine's own reading of whether the offer can still be
 * answered. An elapsed deadline is an elapsed window, not proof that a durable
 * expiration was recorded: this read never runs the reaper.
 */
export function HandoffSheet({
  open,
  onOpenChange,
  deliveryId,
  scope,
  canDeliveryRead,
  canRespond,
  onRespond,
  refreshSignal = 0,
  registerFallbackFocus,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  deliveryId: string | null
  scope: CommunicationsScope
  canDeliveryRead: boolean
  canRespond: boolean
  /**
   * Bumped by the room when a response resolved or conflicted. Invalidating a
   * cache key cannot refresh this read: it lives outside the query cache, so the
   * owner of the read is the only thing that can restart it.
   */
  refreshSignal?: number
  /** Publishes a still-visible control of this sheet for nested focus return. */
  registerFallbackFocus?: (get: () => HTMLElement | null) => void
  /** Hands the response to the room's own operation controller, which outlives this
   * sheet. The sheet never dispatches a mutation itself. */
  onRespond: (
    transition: HandoffTransition,
    target: {
      handoffId: string
      etag: string
      workItemId: string
      recipient: HandoffDetail['handoff']['to']
      deliveryId: string
    },
  ) => void
}) {
  const { t, i18n } = useTranslation('communications')
  const returnFocus = useReturnFocus(open)
  const rereadRef = useRef<HTMLButtonElement | null>(null)
  const titleRef = useRef<HTMLHeadingElement | null>(null)
  const tenant = scope.tenant
  const read = useCallback(
    (signal: AbortSignal) =>
      freshly(getHandoffDetail(deliveryId ?? '', { tenant }, signal)),
    [deliveryId, tenant],
  )
  const fresh = useFreshRead<Fresh<HandoffDetail>>({
    read,
    allowed: canDeliveryRead,
    boundary: scope.key,
  })
  const { start, stop } = fresh
  useEffect(() => {
    if (open && deliveryId) start()
    else stop()
  }, [open, deliveryId, start, stop])

  // A resolved or conflicting response changed the durable row. Restart this
  // sheet's own read so the panel shows the current state rather than the
  // observation the response was reviewed against.
  const seenRefresh = useRef(refreshSignal)
  useEffect(() => {
    if (seenRefresh.current === refreshSignal) return
    seenRefresh.current = refreshSignal
    if (open && deliveryId && canDeliveryRead) start()
  }, [refreshSignal, open, deliveryId, canDeliveryRead, start])

  const registerRef = useRef(registerFallbackFocus)
  useEffect(() => {
    registerRef.current = registerFallbackFocus
  })
  useEffect(() => {
    registerRef.current?.(() => rereadRef.current)
  }, [])

  // The panel is its own scroll container and Radix moves focus into it with
  // `preventScroll: true`, so a Tab wrap can leave the focused control outside the
  // visible area. Restore only that movement: shift the panel's own `scrollTop` by
  // the least amount that brings the focused element's real box back inside its
  // client bounds, and nothing when it is already inside. Never `scrollIntoView`,
  // which would also drag the page behind this fixed panel. Vertical only: the
  // panel's design forbids horizontal overflow. Capture phase with a `contains`
  // check, because React routes a portal's events through the React tree — a
  // nested dialog or a select popover reports here while living outside the panel.
  const keepFocusVisible = useCallback((event: FocusEvent<HTMLDivElement>) => {
    const panel = event.currentTarget
    const target = event.target
    if (!(target instanceof HTMLElement)) return
    if (!panel.contains(target)) return
    const panelRect = panel.getBoundingClientRect()
    const targetRect = target.getBoundingClientRect()
    if (panelRect.height === 0 || targetRect.height === 0) return
    const top = panelRect.top + panel.clientTop
    const bottom = top + panel.clientHeight
    if (targetRect.bottom > bottom)
      panel.scrollTop += targetRect.bottom - bottom
    else if (targetRect.top < top) panel.scrollTop -= top - targetRect.top
  }, [])

  // Initial focus is this sheet's own title, not Radix's first tabbable control.
  // The title is stable and precedes the asynchronously loaded body, so a read that
  // lands after mount cannot move the focused element; the footer's first action
  // can, and on a narrow viewport it then sits below the fold. The default is
  // prevented only when the mounted title actually takes focus, so Radix keeps its
  // own fallback. `preventScroll` because the title is already at the top.
  const focusTitle = useCallback((event: Event) => {
    const title = titleRef.current
    if (!title?.isConnected) return
    title.focus({ preventScroll: true })
    if (document.activeElement === title) event.preventDefault()
  }, [])

  const state = fresh.state
  const answer = fresh.current && state.status === 'ready' ? state.data : null
  const detail = answer && answer.ok ? answer.value : null
  const current = detail?.offer_context === 'current'
  const respondable = detail !== null && current && canRespond

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-xl"
        onCloseAutoFocus={returnFocus}
        onOpenAutoFocus={focusTitle}
        onFocusCapture={keepFocusVisible}
      >
        <SheetHeader>
          <SheetTitle ref={titleRef} tabIndex={-1}>
            {t('handoff.detail.title')}
          </SheetTitle>
          <SheetDescription>{t('handoff.detail.description')}</SheetDescription>
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
        {answer && !answer.ok ? (
          <FailureNotice failure={answer.failure} />
        ) : null}

        {detail ? (
          <div className="flex flex-col gap-4" data-slot="handoff-detail">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="neutral">
                {t(`handoff.state.${detail.handoff.state}`, {
                  defaultValue: detail.handoff.state,
                })}
              </Badge>
              <Badge
                variant={
                  detail.offer_context === 'current'
                    ? 'success'
                    : detail.offer_context === 'stale'
                      ? 'warning'
                      : 'neutral'
                }
                data-slot="handoff-offer-context"
              >
                {t(`handoff.context.${detail.offer_context}`)}
              </Badge>
              {detail.deadline_elapsed ? (
                <Badge variant="warning" data-slot="handoff-deadline-elapsed">
                  {t('handoff.detail.deadlineElapsed')}
                </Badge>
              ) : null}
            </div>

            {/* The protected body. Rendered inert, exactly like message content:
                text is text, a reference is a label, nothing is a link. */}
            <section
              aria-label={t('handoff.detail.contextTitle')}
              className="flex flex-col gap-3"
              data-slot="handoff-context"
            >
              <div>
                <p className="text-sm font-medium">
                  {t('handoff.detail.summary')}
                </p>
                <p className="whitespace-pre-wrap break-words text-sm">
                  {detail.content.summary}
                </p>
              </div>
              <div>
                <p className="text-sm font-medium">
                  {t('handoff.detail.nextAction')}
                </p>
                <p className="whitespace-pre-wrap break-words text-sm">
                  {detail.content.next_action}
                </p>
              </div>
              {detail.content.risk ? (
                <div>
                  <p className="text-sm font-medium">
                    {t('handoff.detail.risk')}
                  </p>
                  <p className="whitespace-pre-wrap break-words text-sm">
                    {detail.content.risk}
                  </p>
                </div>
              ) : null}
              {detail.content.artifact_refs &&
              detail.content.artifact_refs.length > 0 ? (
                <div>
                  <p className="text-sm font-medium">
                    {t('handoff.detail.artifacts')}
                  </p>
                  <ul className="flex flex-col gap-1">
                    {detail.content.artifact_refs.map((r, i) => (
                      <li
                        key={`${r.kind}:${r.ref}:${i}`}
                        data-slot="handoff-artifact-ref"
                        className="inline-flex max-w-full flex-wrap items-center gap-1 rounded-sm border border-border bg-muted px-1.5 py-0.5 text-xs"
                      >
                        <Mono>{r.kind}</Mono>
                        <Mono>{r.ref}</Mono>
                        {r.hash ? <Mono>{r.hash}</Mono> : null}
                      </li>
                    ))}
                  </ul>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t('handoff.detail.artifactsInert')}
                  </p>
                </div>
              ) : null}
            </section>

            {/* Administrative receipt fields, kept apart from the content above. */}
            <section
              aria-label={t('handoff.detail.adminTitle')}
              data-slot="handoff-admin"
            >
              <KvList>
                <KvRow label={t('handoff.detail.workItem')} mono>
                  {detail.work_item.id}
                </KvRow>
                <KvRow label={t('handoff.detail.handoffId')} mono>
                  {detail.handoff.id}
                </KvRow>
                <KvRow label={t('handoff.detail.from')} mono>
                  {detail.handoff.from.kind}:{detail.handoff.from.ref}
                </KvRow>
                <KvRow label={t('handoff.detail.to')} mono>
                  {detail.handoff.to.kind}:{detail.handoff.to.ref}
                </KvRow>
                <KvRow label={t('handoff.detail.deadline')}>
                  {formatDateTime(detail.handoff.ack_deadline, i18n.language)}
                </KvRow>
                <KvRow label={t('handoff.detail.created')}>
                  {formatDateTime(detail.handoff.created_at, i18n.language)}
                </KvRow>
                <KvRow label={t('handoff.detail.observed')}>
                  {formatDateTime(detail.observed_at, i18n.language)}
                </KvRow>
                <KvRow label={t('handoff.detail.precondition')} mono>
                  {detail.handoff.etag}
                </KvRow>
                <KvRow label={t('handoff.detail.carrierDelivery')} mono>
                  {detail.carrier.delivery_id}
                </KvRow>
                <KvRow label={t('handoff.detail.carrierVersion')} mono>
                  {detail.carrier.delivery_version}
                </KvRow>
                <KvRow label={t('handoff.detail.channel')} mono>
                  {detail.carrier.channel_id}
                </KvRow>
              </KvList>
              <p className="mt-1 text-xs text-muted-foreground">
                {t('handoff.detail.preconditionHint')}
              </p>
            </section>

            {detail.terminal_reason ? (
              <section
                aria-label={t('handoff.detail.terminalTitle')}
                className="rounded-md border border-border bg-muted px-3 py-2 text-sm"
                data-slot="handoff-terminal-reason"
              >
                <p className="font-medium">
                  {t('handoff.detail.terminalTitle')}
                </p>
                <p>
                  <Mono>{detail.terminal_reason.code}</Mono>
                </p>
                {detail.terminal_reason.text ? (
                  <p className="whitespace-pre-wrap break-words">
                    {detail.terminal_reason.text}
                  </p>
                ) : null}
              </section>
            ) : null}

            {!current ? (
              <div
                role="status"
                className="rounded-md border border-border bg-muted px-3 py-2 text-sm text-muted-foreground"
                data-slot="handoff-not-respondable"
              >
                <p className="font-medium">
                  {t(`handoff.detail.notCurrent.${detail.offer_context}`)}
                </p>
                <p>{t('handoff.detail.notCurrentBody')}</p>
              </div>
            ) : null}
            {current && !canRespond ? (
              <p className="text-xs text-muted-foreground">
                {t('handoff.detail.noRespondPermission')}
              </p>
            ) : null}
          </div>
        ) : null}

        <SheetFooter>
          <Button
            ref={rereadRef}
            type="button"
            variant="outline"
            onClick={start}
            disabled={!canDeliveryRead || !deliveryId}
          >
            {t('actions.reread')}
          </Button>
          {respondable && detail ? (
            <>
              <Button
                type="button"
                variant="secondary"
                onClick={() =>
                  onRespond('reject', {
                    handoffId: detail.handoff.id,
                    etag: detail.handoff.etag,
                    workItemId: detail.work_item.id,
                    recipient: detail.handoff.to,
                    deliveryId: detail.carrier.delivery_id,
                  })
                }
              >
                <X className="size-4" aria-hidden="true" />
                {t('handoff.actions.reject')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={() =>
                  onRespond('accept', {
                    handoffId: detail.handoff.id,
                    etag: detail.handoff.etag,
                    workItemId: detail.work_item.id,
                    recipient: detail.handoff.to,
                    deliveryId: detail.carrier.delivery_id,
                  })
                }
              >
                <Check className="size-4" aria-hidden="true" />
                {t('handoff.actions.accept')}
              </Button>
            </>
          ) : null}
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
