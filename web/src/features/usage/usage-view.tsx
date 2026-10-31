// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// USAGE & COST, ONE CLICK FROM THE SIDEBAR (#3389): the official 5-hour and weekly windows
// each account's tool reports, the account nearest its limit first, then what today's
// sessions used. Both are reads the console already makes (the status bar's providers read
// and Now's recent sessions, on the same cache keys), so this page never says a number the
// bar or Now does not. A read this principal may not make is not made, and a figure nobody
// reported is left out, never shown as 0.
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import {
  USAGE_WINDOWS,
  useBackgroundProviders,
  windowTone,
} from '@/features/agent-tools/use-background-providers'
import { toolName } from '@/features/agentops/engine-tools'
import { dayCost } from '@/features/home/now-sessions'
import { useRecentSessions } from '@/features/home/use-recent-sessions'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { useAuth } from '@/lib/auth/context'
import {
  formatDateTime,
  formatMicroUsd,
  formatPercent,
  formatRelativeTime,
  formatTokens,
} from '@/lib/format'
import { type AccountUsage, accountsNearestLimit } from './accounts'
import '@/features/agent-tools/i18n'
import './i18n'

const ROW =
  'flex min-w-0 items-center gap-3 border-t border-line py-3 first:border-t-0'
const LINK =
  'inline-flex h-8 shrink-0 items-center rounded-ctl border border-line px-3 text-caption font-medium text-text outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus'

export function UsageView() {
  const { t } = useTranslation('usage')
  const { isSuperadmin } = useAuth()
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t('title')} description={t('subtitle')} />
      {isSuperadmin ? (
        <Accounts />
      ) : (
        <p className="text-caption text-text-2">{t('accounts.adminOnly')}</p>
      )}
      <Today />
    </div>
  )
}

function Accounts() {
  const { t } = useTranslation('usage')
  const providers = useBackgroundProviders()
  const { listed } = useViewAccess()
  const tools = viewById('agent-tools')
  const details = tools && listed(tools) ? tools.path : null
  const rows = accountsNearestLimit(providers.data?.providers ?? [])
  return (
    <section aria-labelledby="usage-accounts" className="flex flex-col gap-2">
      <h2 id="usage-accounts" className="text-body font-semibold">
        {t('accounts.heading')}
      </h2>
      {providers.isPending ? (
        <p role="status" className="text-caption text-text-2">
          {t('accounts.loading')}
        </p>
      ) : providers.isError ? (
        <p role="alert" className="text-caption text-bad">
          {t('accounts.error')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-caption text-text-2">{t('accounts.empty')}</p>
      ) : (
        <ul className="flex flex-col">
          {rows.map((row) => (
            <AccountRow key={row.instance} row={row} details={details} />
          ))}
        </ul>
      )}
    </section>
  )
}

function AccountRow({
  row,
  details,
}: {
  row: AccountUsage
  details: string | null
}) {
  const { t } = useTranslation(['usage', 'agentTools'])
  const login = t(
    row.default
      ? 'agentTools:providers.default'
      : 'agentTools:providers.olivares',
  )
  const name = [toolName(row.driver), row.plan].filter(Boolean).join(' · ')
  return (
    <li className={ROW} data-account={row.instance}>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <p className="truncate text-body font-medium">{name}</p>
        <p className="text-caption text-text-2 tabular-nums">
          {row.windows.map((w, i) => {
            const key = USAGE_WINDOWS[w.label as keyof typeof USAGE_WINDOWS]
            return (
              <span key={`${w.label}-${i}`}>
                {i > 0 ? ' · ' : null}
                <span className={windowTone(w.percent)}>
                  {formatPercent(Math.round(w.percent), { digits: 0 })}{' '}
                  {key ? t(`usage:windows.${key}`) : w.label}
                </span>
              </span>
            )
          })}
          {row.resetsAt ? (
            <>
              {' · '}
              <time
                dateTime={row.resetsAt}
                title={formatDateTime(row.resetsAt)}
              >
                {t('usage:accounts.resets', {
                  when: formatRelativeTime(row.resetsAt),
                })}
              </time>
            </>
          ) : null}
        </p>
        <p className="truncate text-caption text-text-3">
          {[login, row.email].filter(Boolean).join(' · ')}
          {row.stale
            ? ` · ${t('usage:accounts.stale', { at: formatDateTime(row.checkedAt) })}`
            : null}
        </p>
      </div>
      {details ? (
        <Link
          to={details as never}
          className={LINK}
          aria-label={t('usage:accounts.detailsFor', { account: name })}
        >
          {t('usage:accounts.details')}
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
