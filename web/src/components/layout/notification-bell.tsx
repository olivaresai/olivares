// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE BELL opens what needs the person first, then what happened. "Needs you" is read
// from the sources the shell already holds: the approval queue's pending read (the Rules
// badge's own cache, features/governance/use-pending-approvals.ts), of which only the
// requests the SERVER says this reader may decide (`may_decide`) are offered here, and the
// sessions list's Needs you group (use-session-rail.ts). The dot means something needs
// the person, never "something happened": it follows those reads, so only the server's
// answer clears it. A decision goes through the queue's own route and hook (`useDecision`,
// `DenyDialog`); a refused one changes nothing on screen.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Bell, Check, ChevronRight, Loader2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { Spinner } from '@/components/ui/spinner'
import { Checkbox } from '@/components/ui/checkbox'
import { RelTimeLabel } from '@/features/shared'
import { consoleApi, consoleKeys } from '@/features/console/api'
import {
  ApprovalRequestCell,
  FLAG_ARM_MS,
  oneClickApprove,
} from '@/features/governance/approval-preview'
import { DenyDialog, useDecision } from '@/features/governance/decision-dialog'
import '@/features/governance/i18n'
import type { ApprovalDTO } from '@/features/governance/types'
import { usePendingApprovals } from '@/features/governance/use-pending-approvals'
import { auditApi } from '@/lib/api/endpoints'
import { useAuth } from '@/lib/auth/context'
import { useStepUpStore } from '@/stores/step-up'
import {
  actorOf,
  eventLink,
  hiddenInBell,
  readableAction,
  sentenceKey,
} from './bell-events'
import type { RailRow } from './session-rail-model'
import { useSessionRail } from './use-session-rail'

// The bell reads GET /v1/audit/recent: the newest events, newest first, without the
// ledger's own audit.read events. That read is not itself recorded, so polling it once a
// minute while the bell is open does not fill the ledger. Until 26.10.1 the bell polled
// GET /v1/audit, and each poll appended an audit.read to the ledger it was showing.
const RECENT_LIMIT = 10
// Read enough rows to keep internal records from crowding out people's actions.
const RECENT_READ = 50
const REFETCH_INTERVAL = 60_000

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

/** One request this person may decide, decided in place. Keyed by its id AND its place:
 * a row the poll moves under the pointer mounts again and ignores Approve for
 * FLAG_ARM_MS, as Now's card does. A request the bell cannot show whole is opened
 * beside the queue instead (`oneClickApprove`). */
function ApprovalItem({
  approval,
  onDeny,
  onLeave,
}: {
  approval: ApprovalDTO
  onDeny: (approval: ApprovalDTO) => void
  onLeave: () => void
}) {
  const { t } = useTranslation('governance')
  const approve = useDecision(approval.id, 'approve')
  const armed = useRef(false)
  useEffect(() => {
    const timer = setTimeout(() => {
      armed.current = true
    }, FLAG_ARM_MS)
    return () => clearTimeout(timer)
  }, [])
  return (
    <li className="flex min-w-0 flex-col gap-2 border-b border-border px-3 py-2 last:border-0">
      <ApprovalRequestCell approval={approval} className="w-full max-w-full" />
      <div className="flex flex-wrap items-center gap-1.5">
        {oneClickApprove(approval) ? (
          // The bell opens over a page that keeps its own primary action: one orange control per view.
          <Button
            size="sm"
            variant="secondary"
            disabled={approve.isPending}
            onClick={() => {
              if (armed.current) approve.mutate({ decision: 'approve' })
            }}
          >
            {approve.isPending ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : (
              <Check aria-hidden />
            )}
            {approve.isPending
              ? t('approvals.approving')
              : t('approvals.approveOnce')}
          </Button>
        ) : null}
        <Button size="sm" variant="ghost" onClick={() => onDeny(approval)}>
          {t('approvals.deny')}
        </Button>
        <Button size="sm" variant="ghost" asChild>
          <Link
            to={'/permissions' as never}
            search={{ tab: 'approvals', approval: approval.id } as never}
            onClick={onLeave}
          >
            {t('preview.details')}
          </Link>
        </Button>
      </div>
    </li>
  )
}

/** A session or handoff of the sessions list's Needs you group: it opens where the
 * person answers it. */
function QuestionItem({ row, onLeave }: { row: RailRow; onLeave: () => void }) {
  const { t } = useTranslation('nav')
  const title =
    row.kind === 'handoff'
      ? t('shell.rail.handoffFrom', { from: row.from ?? '' })
      : (row.title ?? `${t('shell.rail.session')} ${row.reference}`)
  return (
    <li className="border-b border-border last:border-0">
      <Link
        to={row.to as never}
        search={row.search as never}
        onClick={onLeave}
        className="flex min-w-0 items-center gap-2 px-3 py-2 text-caption font-medium text-foreground outline-none hover:bg-muted focus-visible:bg-muted"
      >
        <span className="min-w-0 flex-1 truncate">{title}</span>
        <ChevronRight aria-hidden className="size-4 shrink-0" />
      </Link>
    </li>
  )
}

export function NotificationBell() {
  const { t } = useTranslation(['common', 'nav'])
  const { activeTenant, principal, can } = useAuth()
  const [open, setOpen] = useState(false)
  const [denying, setDenying] = useState<ApprovalDTO | null>(null)
  const [includeInternal, setIncludeInternal] = useState(false)
  const stepUp = useStepUpStore((s) => s.request)
  const boundary = JSON.stringify([
    activeTenant,
    principal?.kind,
    principal?.actor,
    principal?.user_id,
  ])
  // Another organization or person: what was open belonged to the one before.
  const [openFor, setOpenFor] = useState(boundary)
  if (openFor !== boundary) {
    setOpenFor(boundary)
    setOpen(false)
    setDenying(null)
  }

  // Needs you. Only the server's `may_decide` offers a decision: no role is derived here.
  const pending = usePendingApprovals().query.data
  const approvals = (pending?.items ?? []).filter((a) => a.may_decide === true)
  const offered = new Set(approvals.map((a) => a.id))
  // A session waiting on a request listed above is that request: it is not listed twice.
  const questions = (
    useSessionRail().groups.find((g) => g.id === 'needsYou')?.rows ?? []
  ).filter(
    (row) => !(row.to === '/permissions' && offered.has(row.search.approval)),
  )
  const needsYou = approvals.length + questions.length > 0
  const label = needsYou
    ? `${t('notifications.title')}, ${t('nav:shell.rail.state.need')}`
    : t('notifications.title')

  const { data, isLoading } = useQuery({
    queryKey: ['notifications', activeTenant, boundary],
    queryFn: () => auditApi.recent({ limit: RECENT_READ }),
    refetchInterval: REFETCH_INTERVAL,
    // Read while the bell is open: the dot no longer comes from the ledger. The bell lives
    // in the topbar, outside the routed TenantGate: the read is tenant-scoped, and with no
    // tenant selected the engine answers 400.
    enabled: open && !!activeTenant,
  })

  const events = (data?.items ?? [])
    .filter((e) => !hiddenInBell(e.action))
    // A record with no sentence would show only its name: it waits behind the checkbox.
    .filter((e) => includeInternal || sentenceKey(e.action) !== null)
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
  const leave = () => setOpen(false)

  return (
    <>
      <Popover
        open={open}
        onOpenChange={(next) => {
          // A decision asked for a fresh sign-in: its retry belongs to the row that asked,
          // so the bell stays open under the step-up dialog until it is done.
          if (!next && stepUp) return
          setOpen(next)
        }}
      >
        <PopoverTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            aria-label={label}
            title={label}
            className="relative"
          >
            <Bell className="size-4" />
            {needsYou && (
              <span className="absolute right-1 top-1 size-2 rounded-full bg-warn" />
            )}
          </Button>
        </PopoverTrigger>
        {/* One scroller for the whole popover: every request keeps its actions in reach. */}
        <PopoverContent
          align="end"
          className="max-h-[var(--radix-popover-content-available-height)] w-80 overflow-y-auto p-0"
          sideOffset={8}
        >
          {needsYou ? (
            <section
              aria-labelledby="bell-needs-you"
              className="border-b border-border"
            >
              <h3
                id="bell-needs-you"
                className="px-3 pt-2 text-body font-medium text-foreground"
              >
                {t('nav:shell.rail.groups.needsYou')}
              </h3>
              <ul>
                {approvals.map((approval, place) => (
                  <ApprovalItem
                    key={`${approval.id}:${place}`}
                    approval={approval}
                    onDeny={(a) => {
                      // The dialog outlives the popover, which closes under it.
                      setOpen(false)
                      setDenying(a)
                    }}
                    onLeave={leave}
                  />
                ))}
                {questions.map((row) => (
                  <QuestionItem key={row.key} row={row} onLeave={leave} />
                ))}
              </ul>
            </section>
          ) : null}
          <div className="border-b border-border px-3 py-2">
            <h3 className="text-body font-medium text-foreground">
              {t('notifications.title')}
            </h3>
            <label className="mt-2 flex cursor-pointer items-center gap-2 text-caption text-muted-foreground">
              <Checkbox
                checked={includeInternal}
                onCheckedChange={(checked) =>
                  setIncludeInternal(checked === true)
                }
              />
              {t('notifications.includeInternal')}
            </label>
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
                    onClick={leave}
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
          {/* The audit ledger holds the full record. */}
          <div className="border-t border-border p-1">
            <Button
              asChild
              variant="ghost"
              size="sm"
              className="w-full"
              onClick={leave}
            >
              <Link to={'/audit' as never}>{t('notifications.viewAll')}</Link>
            </Button>
          </div>
        </PopoverContent>
      </Popover>
      {denying ? (
        <DenyDialog
          open
          onOpenChange={(next) => {
            if (!next) setDenying(null)
          }}
          approval={denying}
        />
      ) : null}
    </>
  )
}
