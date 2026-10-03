// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Bell } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { Spinner } from '@/components/ui/spinner'
import { RelTimeLabel } from '@/features/shared'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { auditApi } from '@/lib/api/endpoints'
import { useAuth } from '@/lib/auth/context'
import {
  actorOf,
  eventLink,
  hiddenInBell,
  readableAction,
  sentenceKey,
} from './bell-events'

// The bell reads GET /v1/audit/recent: the newest events, newest first, without the
// ledger's own audit.read events. That read is not itself recorded, so polling it once a
// minute does not fill the ledger. Until 26.10.1 the bell polled GET /v1/audit, and each
// poll appended an audit.read to the ledger it was showing.
const RECENT_LIMIT = 10
// Routine events (reads, sign-ins) are not shown, so ask for more than are shown.
const RECENT_READ = 30
const REFETCH_INTERVAL = 60_000
//E4e: the newest event timestamp the user has SEEN (opened the popover on),
// persisted per browser so the unread dot survives a reload instead of lighting
// up for history the user already reviewed. ISO-8601 strings compare lexically.
const LAST_SEEN_KEY = 'olivares.notifications.lastSeen'

function readLastSeen(): string {
  try {
    return localStorage.getItem(LAST_SEEN_KEY) ?? ''
  } catch {
    return ''
  }
}

function actionTone(action: string): string {
  if (
    action.includes('revoke') ||
    action.includes('delete') ||
    action.includes('disable')
  )
    return 'text-danger'
  if (action.includes('error') || action.includes('fail')) return 'text-danger'
  if (action.includes('create') || action.includes('enable'))
    return 'text-success'
  return 'text-foreground'
}

export function NotificationBell() {
  const { t } = useTranslation('common')
  const { activeTenant, principal, can } = useAuth()
  const [open, setOpen] = useState(false)
  const [lastSeen, setLastSeen] = useState<string>(readLastSeen)

  const { data, isLoading } = useQuery({
    queryKey: ['notifications', activeTenant],
    queryFn: () => auditApi.recent({ limit: RECENT_READ }),
    refetchInterval: REFETCH_INTERVAL,
    // The bell lives in the topbar, outside the routed TenantGate: the read is
    // tenant-scoped, and with no tenant selected the engine answers 400.
    enabled: !!activeTenant,
  })

  const events = (data?.items ?? [])
    .filter((e) => !hiddenInBell(e.action))
    .slice(0, RECENT_LIMIT)
  // People are named for a principal who may already read the members; otherwise "You" or
  // "A member". The bell shows no one's address to someone who could not see it elsewhere.
  const canReadMembers = can('user:read')
  const members = useQuery({
    queryKey: consoleKeys.members(activeTenant),
    queryFn: () => consoleApi.listMembers(),
    enabled: open && canReadMembers && !!activeTenant,
  })
  const actorName = (event: (typeof events)[number]) => {
    const who = actorOf(event, principal?.user_id)
    if (who.kind !== 'member') return t(`notifications.actors.${who.kind}`)
    const m = members.data?.items.find((x) => x.user_id === who.userId)
    return m?.display_name || m?.email || t('notifications.actors.member')
  }
  const sentence = (event: (typeof events)[number]) => {
    const key = sentenceKey(event.action)
    if (!key) return readableAction(event.action)
    const you = actorOf(event, principal?.user_id).kind === 'you'
    return t(`notifications.events.${key}`, {
      actor: actorName(event),
      context: you ? 'you' : undefined,
    })
  }
  const newest = events[0]?.occurred_at ?? ''
  const hasUnseen = newest !== '' && newest > lastSeen

  const handleOpenChange = (next: boolean) => {
    setOpen(next)
    // Opening = seeing: the newest timestamp on screen becomes the last one seen.
    if (next && hasUnseen) {
      setLastSeen(newest)
      try {
        localStorage.setItem(LAST_SEEN_KEY, newest)
      } catch {
        // localStorage unavailable — the in-memory state still clears the dot.
      }
    }
  }

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t('notifications.title')}
          className="relative"
        >
          <Bell className="size-4" />
          {hasUnseen && (
            <span className="absolute right-1 top-1 size-2 rounded-full bg-primary" />
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0" sideOffset={8}>
        <div className="border-b border-border px-3 py-2">
          <h3 className="text-body font-medium text-foreground">
            {t('notifications.title')}
          </h3>
        </div>
        {isLoading ? (
          <div className="flex justify-center py-6">
            <Spinner />
          </div>
        ) : events.length === 0 ? (
          <div className="py-6 text-center text-body text-muted-foreground">
            {t('notifications.empty')}
          </div>
        ) : (
          <div className="max-h-80 overflow-y-auto">
            {events.map((event) => {
              const link = eventLink(event)
              const body = (
                <div className="flex items-start justify-between gap-2">
                  <span
                    className={`min-w-0 text-caption font-medium ${actionTone(event.action)}`}
                  >
                    {sentence(event)}
                  </span>
                  <RelTimeLabel ts={event.occurred_at} />
                </div>
              )
              return link ? (
                <Link
                  key={event.id}
                  to={link.to as never}
                  search={link.search as never}
                  onClick={() => setOpen(false)}
                  className="block border-b border-border px-3 py-2 outline-none last:border-0 hover:bg-muted focus-visible:bg-muted"
                >
                  {body}
                </Link>
              ) : (
                <div
                  key={event.id}
                  className="border-b border-border px-3 py-2 last:border-0"
                >
                  {body}
                </div>
              )
            })}
          </div>
        )}
        {/*E4e: the bell is a preview — the audit ledger is the full record. */}
        <div className="border-t border-border p-1">
          <Button
            asChild
            variant="ghost"
            size="sm"
            className="w-full"
            onClick={() => setOpen(false)}
          >
            <Link to={'/audit' as never}>{t('notifications.viewAll')}</Link>
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}
