// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PHONE BAR (redesign §3.2), below 761 px: Home · Sessions · New · AI tools · More.
// The three links are the sidebar's first three destinations, current by the same rule; New
// is the same action as the sidebar's button and the `N` key; More opens the area
// directory, where every other destination is. Targets are 44 × 44 px.
import { Link, useRouterState } from '@tanstack/react-router'
import { LayoutGrid, Plus } from 'lucide-react'
import { Fragment } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useViewAccess } from '@/features/navigation/authorization'
import { resolveLocation } from '@/features/navigation/model'
import { useNewSession } from './new-session'
import { currentDestination, phoneBarDestinations } from './shell-destinations'

const ITEM_CLASS = cn(
  'flex min-h-11 min-w-11 flex-col items-center justify-center gap-0.5 rounded-ctl px-1 text-[11px] leading-[14px] font-medium text-text-2 outline-none',
  'focus-visible:ring-2 focus-visible:ring-focus',
  'aria-[current=page]:text-text aria-[current=page]:[&_svg]:text-accent-text',
  '[&_svg]:size-5',
)

export function PhoneBar({ onMore }: { onMore?: () => void }) {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const newSession = useNewSession()
  const links = phoneBarDestinations(listed)
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const current = currentDestination(resolveLocation(pathname), search, listed)
  return (
    <nav
      aria-label={t('shell.phoneBarLabel')}
      className="grid h-16 grid-cols-5 items-center border-t border-line bg-frame px-1"
    >
      {links.map((d, i) => {
        const Icon = d.icon
        const link = (
          <Link
            to={d.path as never}
            search={d.search as never}
            activeOptions={{ exact: true }}
            aria-current={current === d.key ? 'page' : undefined}
            className={ITEM_CLASS}
          >
            <Icon aria-hidden />
            <span className="max-w-full truncate">
              {t(`shell.journeys.${d.key}`)}
            </span>
          </Link>
        )
        // New sits in the middle of the bar, after the second link.
        return i === 2 ? (
          <Fragment key={d.key}>
            <NewButton onClick={newSession} label={t('shell.newSession')} />
            {link}
          </Fragment>
        ) : (
          <Fragment key={d.key}>{link}</Fragment>
        )
      })}
      {links.length <= 2 ? (
        <NewButton onClick={newSession} label={t('shell.newSession')} />
      ) : null}
      <button type="button" onClick={onMore} className={ITEM_CLASS}>
        <LayoutGrid aria-hidden />
        <span aria-hidden>{t('shell.more')}</span>
        <span className="sr-only">{t('shell.moreLabel')}</span>
      </button>
    </nav>
  )
}

function NewButton({ onClick, label }: { onClick: () => void; label: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      className="mx-auto grid size-11 place-items-center rounded-[14px] border border-accent-border bg-accent text-on-accent outline-none focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-offset-2 focus-visible:ring-offset-frame"
    >
      <Plus aria-hidden className="size-5" />
    </button>
  )
}
