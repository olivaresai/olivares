// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// USAGE & COST, ONE CLICK FROM THE SIDEBAR (#3389): the official 5-hour and weekly windows
// each account's tool reports, the account nearest its limit first, then what today's
// sessions used. Both are reads the console already makes (the account list AI tools
// shows, and Now's recent sessions, on the same cache keys), so this page never says a
// number AI tools or Now does not. A read this principal may not make is not made, and a
// figure nobody reported is left out, never shown as 0.
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import { useBackgroundProviders } from '@/features/agent-tools/use-background-providers'
import { useProfileList } from '@/features/agent-tools/use-profiles'
import { AccountUsage } from '@/features/agentops/account-usage'
import { toolName } from '@/features/agentops/engine-tools'
import { ToolMark } from '@/features/agentops/tool-mark'
import { dayCost } from '@/features/home/now-sessions'
import { useRecentSessions } from '@/features/home/use-recent-sessions'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { useAuth } from '@/lib/auth/context'
import { formatMicroUsd, formatTokens } from '@/lib/format'
import {
  accountsNearestLimit,
  defaultLoginRows,
  type UsageRow,
} from './accounts'
import './i18n'

const ROW =
  'flex min-w-0 items-center gap-3 border-t border-line py-3 first:border-t-0'
const LINK =
  'inline-flex h-8 shrink-0 items-center rounded-ctl border border-line px-3 text-caption font-medium text-text outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus'

export function UsageView() {
  const { t } = useTranslation('usage')
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t('title')} description={t('subtitle')} />
      <Accounts />
      <Today />
    </div>
  )
}

function Accounts() {
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
  const rows = accountsNearestLimit([
    ...defaultLoginRows(providers.data?.providers ?? []),
    ...(accounts.data?.accounts ?? []),
  ])
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
        <p className="text-caption text-text-2">{t('accounts.empty')}</p>
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
    <li className={ROW} data-account={account.account_ref}>
      <ToolMark driver={account.driver} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <p className="truncate text-body font-medium">{name}</p>
        <AccountUsage usage={account.usage} showUnknown className="truncate" />
        <p className="truncate text-caption text-text-3">
          {[toolName(account.driver), account.identity]
            .filter(Boolean)
            .join(' · ')}
        </p>
      </div>
      {details ? (
        <Link
          to={details as never}
          className={LINK}
          aria-label={t('accounts.detailsFor', { account: name })}
        >
          {t('accounts.details')}
        </Link>
      ) : null}
    </li>
  )
}

function Today() {
  const { t } = useTranslation('usage')
  const { canSessions, canRuns, sessionsQ, runsQ, recentSessions, now, floor } =
    useRecentSessions()
  const { listed } = useViewAccess()
  const sessionsView = viewById('sessions')
  if (!canSessions && !canRuns) return null
  // As on Now: a figure over one read while the other failed would look complete.
  const failed =
    (canRuns && runsQ.isError) || (canSessions && sessionsQ.isError)
  const today = recentSessions ? dayCost(recentSessions, now) : null
  const facts: string[] = []
  if (today) {
    facts.push(t('today.sessions', { count: today.sessions }))
    if (today.inputTokens != null && today.outputTokens != null)
      facts.push(
        t('today.tokens', {
          input: formatTokens(today.inputTokens),
          output: formatTokens(today.outputTokens),
        }),
      )
    if (today.costMicroUsd != null)
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
            <p className="text-body tabular-nums" data-testid="usage-today">
              {floor(0) ? `${t('today.atLeast')} ` : null}
              {facts.join(' · ')}
            </p>
            {today.unpriced > 0 ? (
              <p className="text-caption text-text-3">
                {t('today.unpriced', { count: today.unpriced })}
              </p>
            ) : null}
            {today.tokensUnreported > 0 ? (
              <p className="text-caption text-text-3">
                {t('today.untokened', { count: today.tokensUnreported })}
              </p>
            ) : null}
          </div>
          {sessionsView && listed(sessionsView) ? (
            <Link to={sessionsView.path as never} className={LINK}>
              {t('today.viewSessions')}
            </Link>
          ) : null}
        </div>
      )}
    </section>
  )
}
