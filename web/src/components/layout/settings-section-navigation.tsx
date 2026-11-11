// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { KeyRound, Search } from 'lucide-react'
import { viewById } from '@/features/navigation/model'
import { Link, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import { useViewAccess } from '@/features/navigation/authorization'
import {
  SETTINGS_SECTIONS,
  settingsSections,
} from '@/features/settings/sections'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

/** Settings uses the shell's one navigation column; bookmarked routes keep working. */
export function SettingsSectionNavigation({
  onNavigate,
  onSearch,
  folded = false,
}: {
  folded?: boolean
  onNavigate?: () => void
  /** Folded: the search is one press away, opening the column on its field. */
  onSearch?: () => void
}) {
  const { t } = useTranslation(['settings', 'nav'])
  const { isSuperadmin } = useAuth()
  const { navigable } = useViewAccess()
  const [url, patch] = useUrlState(['section', 'q', 'add', 'returnTo'])
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const offered = settingsSections(isSuperadmin, navigable)
  const active = SETTINGS_SECTIONS.some((s) => s.id === url.section)
    ? url.section
    : 'general'
  const groups = [
    {
      id: 'you',
      ids: ['general', 'newSessions', 'notifications', 'keyboard', 'vault'],
    },
    { id: 'work', ids: ['permissions'] },
    {
      id: 'installation',
      ids: [
        'signIn',
        'edition',
        'setup',
        'modules',
        'sessionIdle',
        'tracing',
        'signing',
        'about',
      ],
    },
  ]
  return (
    <nav
      aria-label={t('settings:nav.sections')}
      data-slot="settings-navigation"
      className="min-h-0 flex-1 overflow-y-auto p-2"
    >
      {folded ? (
        onSearch ? (
          <button
            type="button"
            onClick={onSearch}
            aria-label={t('settings:search')}
            title={t('settings:search')}
            className="mx-auto mb-3 grid size-10 place-items-center rounded-ctl text-text-2 hover:bg-hover hover:text-text active:bg-[var(--pressed)]"
          >
            <Search aria-hidden className="size-4" />
          </button>
        ) : null
      ) : (
        <Input
          id="settings-search"
          type="search"
          aria-label={t('settings:search')}
          placeholder={t('settings:search')}
          value={url.q ?? ''}
          onChange={(event) =>
            patch({
              q: event.target.value,
              add: undefined,
              returnTo: undefined,
            })
          }
          className="mb-3"
        />
      )}
      {groups.map((group) => (
        <section key={group.id} className="mb-3">
          <h2
            className={
              folded
                ? 'mx-auto my-2 w-6 border-t border-line text-[0px]'
                : 'px-2.5 pb-[5px] text-overline font-bold text-text-3'
            }
          >
            {t(`settings:groups.${group.id}`)}
          </h2>
          {group.ids.map((id) => {
            const vault = id === 'vault' ? viewById('vault') : undefined
            const section =
              vault && navigable(vault)
                ? { id, icon: KeyRound }
                : offered.find((s) => s.id === id)
            if (!section) return null
            const Icon = section.icon
            const current =
              id === 'vault'
                ? pathname === '/vault'
                : pathname === '/settings' && active === id
            const label =
              id === 'vault' ? t('nav:items.vault') : t(`settings:nav.${id}`)
            const className = cn(
              'flex min-h-[34px] w-full items-center gap-[11px] rounded-ctl px-2.5 text-left text-body text-text-2 hover:bg-hover hover:text-text active:bg-[var(--pressed)] focus-visible:-outline-offset-2',
              // Bold as well as filled: the fill is light on purpose.
              current && 'bg-active font-bold text-text',
              folded && 'mx-auto size-10 w-10 justify-center px-0',
            )
            const content = (
              <>
                <Icon aria-hidden className="size-4" />
                <span className={folded ? 'sr-only' : undefined}>{label}</span>
              </>
            )
            return id === 'vault' ? (
              <Link
                key={id}
                to={'/vault' as never}
                onClick={onNavigate}
                aria-current={current ? 'page' : undefined}
                title={folded ? label : undefined}
                className={className}
              >
                {content}
              </Link>
            ) : (
              <button
                key={id}
                type="button"
                onClick={() => {
                  patch(
                    {
                      section: id,
                      q: undefined,
                      add: undefined,
                      returnTo: undefined,
                    },
                    { history: 'push' },
                  )
                  onNavigate?.()
                }}
                aria-current={current ? 'page' : undefined}
                title={folded ? label : undefined}
                className={className}
              >
                {content}
              </button>
            )
          })}
        </section>
      ))}
    </nav>
  )
}
