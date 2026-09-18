// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Send } from 'lucide-react'
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
import { getChannel } from './api'
import type { CommunicationsScope } from './boundary'
import { FailureNotice } from './failure-notice'
import { freshly, type Fresh } from './fresh'
import type { Channel, ChannelAccess } from './types'

export function AccessBadges({ access }: { access: ChannelAccess | null }) {
  const { t } = useTranslation('communications')
  if (!access) return <Badge variant="outline">{t('access.unknown')}</Badge>
  const bits = (['read', 'write', 'admin'] as const).filter((b) => access[b])
  if (bits.length === 0)
    return <Badge variant="outline">{t('access.none')}</Badge>
  return (
    <span className="inline-flex flex-wrap gap-1">
      {bits.map((b) => (
        <Badge key={b} variant="info">
          {t(`access.${b}`)}
        </Badge>
      ))}
    </span>
  )
}

/**
 * ChannelSheet — one Channel READ FRESH on open, with its ETag. The read carries no
 * grants and no local bits: when the sheet was opened from a catalog row, the row's
 * `my_access` is shown as what that page reported; opened by id, the bits are
 * "not reported by this read" and never fabricated. Composing is offered on the
 * core send permission and, when known, the local write bit; the engine decides
 * again on the send.
 */
export function ChannelSheet({
  open,
  onOpenChange,
  channelId,
  catalogAccess,
  scope,
  canChannelRead,
  canSend,
  onCompose,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelId: string | null
  catalogAccess: ChannelAccess | null
  scope: CommunicationsScope
  canChannelRead: boolean
  canSend: boolean
  onCompose: (channel: Channel) => void
}) {
  const { t, i18n } = useTranslation('communications')
  const tenant = scope.tenant
  const read = useCallback(
    (signal: AbortSignal) =>
      freshly(getChannel(channelId ?? '', { tenant }, signal)),
    [channelId, tenant],
  )
  const fresh = useFreshRead<Fresh<{ channel: Channel; etag: string | null }>>({
    read,
    allowed: canChannelRead,
    boundary: scope.key,
  })
  const { start, stop } = fresh
  useEffect(() => {
    if (open && channelId) start()
    else stop()
  }, [open, channelId, start, stop])

  const state = fresh.state
  const ready = fresh.current && state.status === 'ready' ? state.data : null
  const channel = ready && ready.ok ? ready.value.channel : null
  const composeAllowed =
    !!channel && canSend && (catalogAccess ? catalogAccess.write : true)
  const sendHint = !canSend
    ? t('channel.sendHint.noPermission')
    : catalogAccess
      ? catalogAccess.write
        ? t('channel.sendHint.canWrite')
        : t('channel.sendHint.noWrite')
      : t('channel.sendHint.unknown')

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{channel ? channel.name : t('channel.title')}</SheetTitle>
          <SheetDescription>{t('channel.description')}</SheetDescription>
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
        {channel && ready && ready.ok ? (
          <div className="flex flex-col gap-4" data-slot="channel-detail">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="outline">
                {t(`kind.${channel.kind}`, { defaultValue: channel.kind })}
              </Badge>
              <Badge variant="neutral">{channel.state}</Badge>
              <Badge variant="outline">
                {t(`protection.${channel.content_protection}`, {
                  defaultValue: channel.content_protection,
                })}
              </Badge>
              <span className="text-caption text-muted-foreground">
                {t('access.label')}:
              </span>
              <AccessBadges access={catalogAccess} />
            </div>
            <KvList>
              <KvRow label={t('channel.fields.id')} mono>
                {channel.id}
              </KvRow>
              <KvRow label={t('channel.fields.slug')} mono>
                {channel.slug}
              </KvRow>
              <KvRow label={t('channel.fields.descriptionField')} align="start">
                <span className="break-words">
                  {channel.description || '—'}
                </span>
              </KvRow>
              <KvRow label={t('channel.fields.sensitivity')}>
                {t(`sensitivity.${channel.sensitivity}`, {
                  defaultValue: channel.sensitivity,
                })}
              </KvRow>
              <KvRow label={t('channel.fields.protectionGeneration')} mono>
                {channel.protection_generation}
              </KvRow>
              <KvRow label={t('channel.fields.ackPolicy')}>
                {t(`ackPolicy.${channel.default_ack_policy}`, {
                  defaultValue: channel.default_ack_policy,
                })}
              </KvRow>
              <KvRow label={t('channel.fields.ackTimeout')} mono>
                {channel.default_ack_timeout_ms}
              </KvRow>
              <KvRow label={t('channel.fields.wake')}>
                {t(`wake.${channel.default_wake}`, {
                  defaultValue: channel.default_wake,
                })}
              </KvRow>
              <KvRow label={t('channel.fields.retention')} mono>
                {channel.retention_policy_ref || '—'}
              </KvRow>
              <KvRow label={t('channel.fields.maxFanout')} mono>
                {channel.max_fanout}
              </KvRow>
              <KvRow label={t('channel.fields.maxAutomationDepth')} mono>
                {channel.max_automation_depth}
              </KvRow>
              <KvRow label={t('channel.fields.aclRevision')} mono>
                {channel.acl_revision}
              </KvRow>
              <KvRow label={t('channel.fields.routeRevision')} mono>
                {channel.route_revision}
              </KvRow>
              <KvRow label={t('channel.fields.subscriptionRevision')} mono>
                {channel.subscription_revision}
              </KvRow>
              <KvRow label={t('channel.fields.version')} mono>
                {channel.version}
              </KvRow>
              <KvRow label={t('channel.fields.etag')} mono>
                {ready.value.etag ?? '—'}
              </KvRow>
              <KvRow label={t('channel.fields.workspace')} mono>
                {channel.workspace_id}
              </KvRow>
              <KvRow label={t('channel.fields.created')}>
                {formatDateTime(channel.created_at, i18n.language)}
              </KvRow>
              <KvRow label={t('channel.fields.updated')}>
                {formatDateTime(channel.updated_at, i18n.language)}
              </KvRow>
            </KvList>
            <p className="text-caption text-muted-foreground">
              {t('access.hint')}
            </p>
            <p className="text-caption text-muted-foreground">{sendHint}</p>
          </div>
        ) : null}
        <SheetFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => start()}
            disabled={!canChannelRead || !channelId}
          >
            {t('actions.reread')}
          </Button>
          {composeAllowed && channel ? (
            <Button
              type="button"
              variant="primary"
              onClick={() => onCompose(channel)}
            >
              <Send className="size-4" aria-hidden="true" />
              {t('actions.sendNotice')}
            </Button>
          ) : null}
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
