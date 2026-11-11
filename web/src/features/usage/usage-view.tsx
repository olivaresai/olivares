// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// USAGE & COST, ONE CLICK FROM THE SIDEBAR: what the runs of a chosen window used, by
// tool and by profile (spend-section.tsx), then each account's share of the week and of
// five hours, the account nearest its limit first, its row opening every window its tool
// reports with when each resets (the one place window names are shown), then what today's
// sessions used. Each is a read the console already makes (the runs' totals Models › Usage
// shows, the account list AI tools shows, and Now's recent sessions, on the same cache
// keys), so this page never says a number another page does not. A tool or profile chosen
// narrows the spend and the limits alike. A read this principal may not make is not made,
// a figure nobody reported is left out, never shown as 0, and money, and the word for it,
// show only to a person Usage opens for. The Timeline tab holds the last-hour lanes that
// were on Now.
import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useBackgroundProviders } from '@/features/agent-tools/use-background-providers'
import { useProfileList } from '@/features/agent-tools/use-profiles'
import { AccountUsage, UsageWindows } from '@/features/agentops/account-usage'
import { toolName } from '@/features/agentops/engine-tools'
import { ToolMark } from '@/features/agentops/tool-mark'
import { currentAnswer } from '@/features/executive/derive'
import { usePendingApprovals } from '@/features/governance/use-pending-approvals'
import { EstateLanes } from '@/features/home/estate-lanes'
import '@/features/home/i18n'
import { dayCost } from '@/features/home/now-sessions'
import { onSwimlane, WINDOW_MS } from '@/features/home/swimlane-model'
import { useRecentSessions } from '@/features/home/use-recent-sessions'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { useUsagePath } from '@/features/sessions/session-meter'
import { useAuth } from '@/lib/auth/context'
import { formatMicroUsd, formatTokens } from '@/lib/format'
import { useTabParam } from '@/lib/hooks/use-tab-param'
import { cn } from '@/lib/utils'
import {
  accountsNearestLimit,
  defaultLoginRows,
  type UsageRow,
} from './accounts'
import { SpendSection, type SpendFilter } from './spend-section'
import './i18n'

const ROW =
  'flex min-w-0 items-center gap-3 border-t border-line py-3 first:border-t-0'

/** The tabs of Usage: the accounts and today (the lead), then the last hour (`?tab=timeline`). */
const USAGE_TABS = ['accounts', 'timeline'] as const

export function UsageView() {
  const { t } = useTranslation('usage')
  // The rows need the live sessions read; without it there is no Timeline to offer.
  const { canSessions } = useRecentSessions()
  const [tab, setTab] = useTabParam(
    canSessions ? USAGE_TABS : USAGE_TABS.slice(0, 1),
  )
  const [filter, setFilter] = useState<SpendFilter>({
    tool: null,
    profile: null,
  })
  // Money follows the capability: the one question the Sessions page asks.
  const money = useUsagePath() !== null
  const accounts = (
    <>
      <SpendSection money={money} filter={filter} onFilter={setFilter} />
      <Accounts filter={filter} />
      <Today money={money} />
    </>
  )
  return (
    <div className="flex flex-col gap-6">
      {/* The top bar already says Usage; the place's lead page does not say it twice. */}
      <PageHeader
        title={t('title')}
        titleHidden
        description={t(money ? 'subtitle' : 'subtitleNoCost')}
      />
      {canSessions ? (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="accounts">{t('tabs.accounts')}</TabsTrigger>
            <TabsTrigger value="timeline">{t('tabs.timeline')}</TabsTrigger>
          </TabsList>
          <TabsContent value="accounts" className="flex flex-col gap-6">
            {accounts}
          </TabsContent>
          <TabsContent value="timeline">
            <Timeline />
          </TabsContent>
        </Tabs>
      ) : (
        accounts
      )}
    </div>
  )
}

/** THE LAST HOUR, moved from Overview: one row per session, drag to the
 * Ledger, Show as a table, and past FLEET_SESSIONS a build's fleet panel (EstateLanes). It
 * reads the sessions Today reads and the approval queue's own read, on the same keys. */
function Timeline() {
  const { t } = useTranslation(['usage', 'home'])
  const { sessionsQ, recentSessions, now } = useRecentSessions()
  const pendingApprovals = usePendingApprovals()
  const approvals = currentAnswer(
    pendingApprovals.permitted,
    pendingApprovals.query,
  )
  if (sessionsQ.isError)
    return (
      <p role="alert" className="text-caption text-bad">
        {t('today.error')}
      </p>
    )
  if (!recentSessions)
    return (
      <p role="status" className="text-caption text-text-2">
        {t('today.loading')}
      </p>
    )
  if (!recentSessions.some((s) => onSwimlane(s, now - WINDOW_MS)))
    return (
      <p className="text-caption text-text-2">
        {t('home:swimlane.noActivity')}
      </p>
    )
  return (
    <EstateLanes
      sessions={recentSessions}
      approvals={approvals?.items ?? []}
      approvalsRead={!!approvals}
      approvalsMore={!!approvals?.has_more}
      now={now}
    />
  )
}

function Accounts({ filter }: { filter: SpendFilter }) {
  const { t } = useTranslation('usage')
  const { can } = useAuth()
  const accounts = useProfileList()
  // The server user's own login: a system read, so only a system administrator makes it.
  const providers = useBackgroundProviders()
  const { listed } = useViewAccess()
  // As on Today: a read this principal may not make is not made, and its section is not shown.
  if (!can('sessions:account:read')) return null
  const tools = viewById('agent-tools')
  const details = tools && listed(tools) ? tools.path : null
  // A tool or profile chosen above narrows the limits too.
  const chosen = (a: UsageRow) =>
    (!filter.tool || a.driver === filter.tool) &&
    (!filter.profile || a.account_ref === filter.profile)
  const all = [
    ...defaultLoginRows(providers.data?.providers ?? []),
    ...(accounts.data?.accounts ?? []),
  ]
  const rows = accountsNearestLimit(all.filter(chosen))
  const narrowed = !!(filter.tool || filter.profile)
  return (
    <section aria-labelledby="usage-accounts" className="flex flex-col gap-2">
      <h2 id="usage-accounts" className="text-body font-semibold">
        {t('accounts.heading')}
      </h2>
      {accounts.isPending ? (
        <p role="status" className="text-caption text-text-2">
          {t('accounts.loading')}
        </p>
      ) : accounts.isError || providers.isError ? (
        <p role="alert" className="text-caption text-bad">
          {t('accounts.error')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-caption text-text-2">
          {t(narrowed ? 'accounts.emptyChosen' : 'accounts.empty')}
        </p>
      ) : (
        <ul className="flex flex-col">
          {rows.map((row) => (
            <AccountRow key={row.account_ref} account={row} details={details} />
          ))}
        </ul>
      )}
    </section>
  )
}

function AccountRow({
  account,
  details,
}: {
  account: UsageRow
  details: string | null
}) {
  const { t } = useTranslation('usage')
  const name = account.display_name || account.name
  return (
    <li className={cn(ROW, 'items-start')} data-account={account.account_ref}>
      <ToolMark driver={account.driver} />
      <details className="group min-w-0 flex-1">
        <summary className="flex cursor-pointer list-none items-center gap-2 rounded-ctl outline-none focus-visible:ring-2 focus-visible:ring-focus">
          <span className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="truncate text-body font-medium">{name}</span>
            <AccountUsage
              usage={account.usage}
              showUnknown
              className="truncate"
            />
            <span className="truncate text-caption text-text-3">
              {[toolName(account.driver), account.identity]
                .filter(Boolean)
                .join(' · ')}
            </span>
          </span>
          <ChevronRight
            aria-hidden
            className="size-4 shrink-0 text-text-3 transition-transform group-open:rotate-90 motion-reduce:transition-none"
          />
        </summary>
        <UsageWindows usage={account.usage} className="mt-2" />
        {/* One trailing control per row, the chevron that opens it; the way to
            the account's tool waits inside. */}
        {details ? (
          <div className="mt-2">
            <Button asChild variant="secondary" size="sm">
              <Link
                to={details as never}
                aria-label={t('accounts.detailsFor', { account: name })}
              >
                {t('accounts.details')}
              </Link>
            </Button>
          </div>
        ) : null}
      </details>
    </li>
  )
}

function Today({ money }: { money: boolean }) {
  const { t } = useTranslation('usage')
  const {
    canSessions,
    canRuns,
    sessionsQ,
    runsQ,
    runsAnswer,
    recentSessions,
    now,
    floor,
  } = useRecentSessions()
  const { listed } = useViewAccess()
  const sessionsView = viewById('sessions')
  if (!canSessions && !canRuns) return null
  // As on Now: a figure over one read while the other failed would look complete.
  const failed =
    (canRuns && runsQ.isError) || (canSessions && sessionsQ.isError)
  const today = recentSessions ? dayCost(recentSessions, now) : null
  // Home's rule: without the runs, or on a page cut short, the day is not known whole, and a
  // sum that leaves sessions out is not the day's figure. Neither says anything.
  const exact = !!runsAnswer && !floor(0)
  const facts: string[] = []
  if (today && exact) {
    facts.push(t('today.sessions', { count: today.sessions }))
    if (
      today.tokensUnreported === 0 &&
      today.inputTokens != null &&
      today.outputTokens != null
    )
      facts.push(
        t('today.tokens', {
          input: formatTokens(today.inputTokens),
          output: formatTokens(today.outputTokens),
        }),
      )
    if (money && today.unpriced === 0 && today.costMicroUsd != null)
      facts.push(formatMicroUsd(today.costMicroUsd))
  }
  return (
    <section aria-labelledby="usage-today" className="flex flex-col gap-2">
      <h2 id="usage-today" className="text-body font-semibold">
        {t('today.heading')}
      </h2>
      {failed ? (
        <p role="alert" className="text-caption text-bad">
          {t('today.error')}
        </p>
      ) : !today ? (
        <p role="status" className="text-caption text-text-2">
          {t('today.loading')}
        </p>
      ) : (
        <div className={ROW}>
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            {facts.length > 0 ? (
              <p className="text-body tabular-nums" data-testid="usage-today">
                {facts.join(' · ')}
              </p>
            ) : null}
          </div>
          {sessionsView && listed(sessionsView) ? (
            <Button asChild variant="secondary" size="sm">
              <Link to={sessionsView.path as never}>
                {t('today.viewSessions')}
              </Link>
            </Button>
          ) : null}
        </div>
      )}
    </section>
  )
}
