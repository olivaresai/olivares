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
import { auditApi } from '@/lib/api/endpoints'
import { useAuth } from '@/lib/auth/context'

// TWO constants, and their separation is the point. RECENT_LIMIT is what the popover
// SHOWS; SCAN_WINDOW is how many tail POSITIONS the engine is asked to examine to find
// them. They were one constant until F-04, and that is precisely what broke: the window
// is counted in sequence positions, the bell's own reads keep occupying them, so ten
// positions could hold ten reads and nothing else — a page that came back empty while
// real activity sat just below `from`.
//
// Widening the examination does not GUARANTEE ten events (a hundred positions can be a
// hundred reads); it buys a horizon roughly ten times deeper, and it is a quantified
// mitigation, not a closure. The guarantee needs a filtered reverse-tail operation in
// the engine, which is deliberately post-release.
const RECENT_LIMIT = 10
const SCAN_WINDOW = 100
const REFETCH_INTERVAL = 60_000
// F-01: reading the ledger is itself a ledger event. Every GET /v1/audit appends its
// own `audit.read`, so a bell that polls the TAIL fills that tail with its own
// looking — and re-lights the unread dot every interval for activity nobody
// performed. Excluding the family from the VIEW is the fix; the engine keeps sealing
// every read, because the other way to quiet the bell was to stop recording them,
// and that buys a quiet notification by destroying evidence.
const EXCLUDED_ACTIONS = ['audit.read']
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

function actionLabel(action: string): string {
  return action.replace(/[._]/g, ' ')
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
  const { activeTenant } = useAuth()
  const [open, setOpen] = useState(false)
  const [lastSeen, setLastSeen] = useState<string>(readLastSeen)

  //TWO calls, and the first one is not a round trip we can save.
  //
  // The ledger is keyset-paginated FORWARDS: `from` is a 1-based sequence and the
  // engine walks ORDER BY seq ASC (core/internal/store/sqlstore/audit.go Walk), so
  // a bare `list({ limit: N })` returns the N OLDEST events of the tenant — the
  // start of the chain, not its end. Asking for them and calling items[0] "newest"
  // is what this component did until: correct with one event, reversed from
  // the second, and — because lastSeen then froze on the genesis event's timestamp,
  // which never changes — the unread dot could never light again.
  //
  // There is no `order=desc` and no tail parameter to ask for instead. The tail is
  // addressable only through head_seq, and head_seq only comes back IN a response;
  // hence the probe. It costs one row.
  const { data: headData, isLoading: isLoadingHead } = useQuery({
    queryKey: ['notifications-head', activeTenant],
    queryFn: () => auditApi.list({ limit: 1 }),
    refetchInterval: REFETCH_INTERVAL,
    // The bell lives in the topbar, OUTSIDE the routed content TenantGate
    // guards, so it needs the precondition itself: /v1/audit is tenant-scoped
    // and with no tenant selected the engine answers 400 "tenant required" —
    // once a minute, forever (core/api/middleware.go resolveTenantValue).
    enabled: !!activeTenant,
  })
  const headSeq = headData?.head_seq ?? 0

  // headSeq is IN the key, so a new event moves the window and refetches the tail
  // by itself; polling this one too would only re-ask for a page that cannot have
  // changed. 0 is the empty ledger — nothing to fetch, and `from: 0` would be a
  // request for a sequence that does not exist.
  const { data, isLoading: isLoadingPage } = useQuery({
    queryKey: ['notifications', activeTenant, headSeq],
    queryFn: () =>
      auditApi.list({
        // `limit` is SCAN_WINDOW, not RECENT_LIMIT, and that is not a slip: the engine's
        // filtered scan walks FORWARDS and stops at `limit` MATCHES, so asking for ten
        // over a hundred positions would return the ten OLDEST matches in the window —
        // the original defect, one level down. Ask for the window, keep the newest.
        // Math.max keeps `from` at 1 on a ledger shorter than the window: sequence
        // numbers start at 1 and there is nothing below it.
        from: Math.max(1, headSeq - SCAN_WINDOW + 1),
        limit: SCAN_WINDOW,
        exclude_action: EXCLUDED_ACTIONS,
      }),
    enabled: !!activeTenant && headSeq > 0,
  })

  // Ascending, so the LAST element is the newest — the single line that made the
  // old `newest` a lie. The list is rendered reversed (newest first) below.
  const events = data?.items ?? []
  const newest = events[events.length - 1]?.occurred_at ?? ''
  const arrived = newest !== '' && newest > lastSeen

  // LATCHED, and the reason is a measured loss, not tidiness. The window is a bounded
  // number of SEQUENCE positions, and the bell's own reads keep occupying them, so an
  // event that lit the dot can still slide below `from` before the user ever opens —
  // SCAN_WINDOW pushes that horizon out, it does not remove it.
  // Computed fresh each render, `hasUnseen` would then go back to false — the dot
  // switching itself off for something nobody looked at, which is the one thing an
  // unread marker must never do. Once something has arrived unseen it stays lit until
  // the user OPENS, because opening is what "seen" means here.
  const [unseen, setUnseen] = useState(false)
  if (arrived && !unseen) setUnseen(true)
  const hasUnseen = unseen
  // Both queries, and the second only while it can actually run. A disabled query
  // is not "loading" (TanStack reports pending+idle), which is what keeps the two
  // resting states honest: with no tenant selected, and with an empty ledger, the
  // popover says "nothing yet" instead of spinning on an answer nobody asked for.
  const isLoading = isLoadingHead || (headSeq > 0 && isLoadingPage)

  const handleOpenChange = (next: boolean) => {
    setOpen(next)
    // Opening = seeing: the latch clears even when the page came back EMPTY, because
    // the user did look. Only a non-empty page has a timestamp worth persisting.
    if (next) setUnseen(false)
    if (next && newest !== '' && newest > lastSeen) {
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
          <h3 className="text-sm font-medium text-foreground">
            {t('notifications.title')}
          </h3>
        </div>
        {isLoading ? (
          <div className="flex justify-center py-6">
            <Spinner />
          </div>
        ) : events.length === 0 ? (
          <div className="py-6 text-center text-sm text-muted-foreground">
            {t('notifications.empty')}
          </div>
        ) : (
          <div className="max-h-80 overflow-y-auto">
            {/* The page arrives ascending by sequence; a notification list reads
                newest first. Copy before reversing — `events` is the query's own
                array and reverse() mutates in place. */}
            {[...events]
              .slice(-RECENT_LIMIT)
              .reverse()
              .map((event) => (
                <div
                  key={event.id}
                  className="border-b border-border px-3 py-2 last:border-0"
                >
                  <div className="flex items-start justify-between gap-2">
                    <span
                      className={`text-xs font-medium ${actionTone(event.action)}`}
                    >
                      {actionLabel(event.action)}
                    </span>
                    <RelTimeLabel ts={event.occurred_at} />
                  </div>
                  {event.target_kind && (
                    <div className="mt-0.5 truncate font-mono text-xs text-muted-foreground">
                      {event.target_kind}
                      {event.target_id ? ` ${event.target_id}` : ''}
                    </div>
                  )}
                  <div className="mt-0.5 text-xs text-muted-foreground">
                    {event.actor_kind}: {event.actor}
                  </div>
                </div>
              ))}
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
