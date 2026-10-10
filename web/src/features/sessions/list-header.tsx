// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HEAD OF THE SESSIONS LIST, AND THE HEAD OF EVERY OTHER VIEW OF THIS PAGE.
//
// The list header is one 48 px row: the page's name, how many sessions there are, a `+`
// that starts one (the same action as the New session of the empty list) and a `⋯` that
// holds the three other views of this page: the table, the workspaces and the provider
// profiles. They were a strip of tabs on the title line; they are still the same views,
// reached through the same `tab` state, one step further from the work they are not.
//
// ⛔ THE LIST IS LIVE, AND SAYS NOTHING WHEN IT IS. There is no Refresh and no "Live"
//    chip: a chip that says Live next to a button that says Refresh disagree with each
//    other. A failed stream is the one thing worth a line, and it is a quiet one.
import { ArrowLeft, MoreHorizontal, Plus } from 'lucide-react'
import { useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import { IconTip } from './icon-tip'
import '@/features/shared/i18n'
import './i18n'

/** How many sessions there are: a figure, with the count in words on its hover. */
function SessionsCount({ count, label }: { count: number; label: string }) {
  if (count <= 0) return null
  return (
    <>
      <span
        data-testid="sessions-summary"
        title={label}
        aria-hidden="true"
        className="text-caption tabular-nums text-text-3"
      >
        {count}
      </span>
      <span className="sr-only">{label}</span>
    </>
  )
}

/** The views of the page other than the list, in the order the menu offers them. */
export type SessionsView = 'table' | 'workspaces' | 'profiles'

export function SessionsListHeader({
  count,
  countLabel,
  onNew,
  views,
  onView,
  streamFailed,
  focusMenu = false,
}: {
  /** Sessions in the list; nothing is printed at zero. */
  count: number
  /** The count in words ("12 sessions · 3 running"), for the hover and the reader. */
  countLabel: string
  /** Starts a session; absent when this person may not (or the empty list says it). */
  onNew?: (event: { currentTarget: HTMLElement }) => void
  /** The views of this page this person may open. */
  views: readonly SessionsView[]
  onView: (view: SessionsView) => void
  /** The live stream failed: one quiet line under the header. */
  streamFailed: boolean
  /**
   * Put the keyboard on the menu button as the header appears: a person who came back from
   * another view of this page lands where they left.
   */
  focusMenu?: boolean
}) {
  const { t } = useTranslation(['sessions', 'shared'])
  const menuRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (focusMenu) menuRef.current?.focus()
  }, [focusMenu])
  return (
    <div data-slot="list-header" className="shrink-0">
      <div className="flex h-12 items-center gap-2 px-3">
        <h1
          title={t('sessions:title')}
          className="min-w-0 truncate text-body font-semibold text-text"
        >
          {t('sessions:title')}
        </h1>
        <SessionsCount count={count} label={countLabel} />
        <span className="ml-auto flex items-center gap-0.5">
          {onNew ? (
            <IconTip label={t('sessions:launch')}>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t('sessions:launch')}
                data-testid="sessions-new"
                onClick={onNew}
              >
                <Plus />
              </Button>
            </IconTip>
          ) : null}
          {views.length > 0 ? (
            <DropdownMenu>
              <IconTip label={t('sessions:list.more')}>
                <DropdownMenuTrigger asChild>
                  <Button
                    ref={menuRef}
                    variant="ghost"
                    size="icon"
                    aria-label={t('sessions:list.more')}
                    data-testid="sessions-list-menu"
                  >
                    <MoreHorizontal />
                  </Button>
                </DropdownMenuTrigger>
              </IconTip>
              <DropdownMenuContent align="end">
                {views.map((view) => (
                  <DropdownMenuItem key={view} onSelect={() => onView(view)}>
                    {view === 'table'
                      ? t('sessions:list.showTable')
                      : t(`sessions:tabs.${view}`)}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </span>
      </div>
      {streamFailed ? (
        <p
          role="status"
          data-testid="sessions-stream-notice"
          className="px-3 pb-2 text-caption text-text-3"
        >
          {t('shared:live.error')}
        </p>
      ) : null}
    </div>
  )
}

/**
 * THE HEAD OF A VIEW THAT IS NOT THE LIST (table, workspaces, provider profiles): a way
 * back to the list, the view's name, and the one New session when the list does not
 * already carry it.
 */
export function SessionsViewHeader({
  title,
  count,
  countLabel,
  onBack,
  actions,
  className,
}: {
  title: string
  count: number
  countLabel: string
  onBack: () => void
  actions?: ReactNode
  className?: string
}) {
  const { t } = useTranslation('sessions')
  // The keyboard arrives on the way back, where the person came in by a menu.
  const backRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    backRef.current?.focus()
  }, [])
  return (
    <div
      data-slot="view-header"
      className={cn('flex h-12 shrink-0 items-center gap-2', className)}
    >
      <IconTip label={t('list.back')}>
        <Button
          ref={backRef}
          variant="ghost"
          size="icon"
          aria-label={t('list.back')}
          data-testid="sessions-view-back"
          onClick={onBack}
        >
          <ArrowLeft />
        </Button>
      </IconTip>
      <h1
        title={title}
        className="min-w-0 truncate text-body font-semibold text-text"
      >
        {title}
      </h1>
      <SessionsCount count={count} label={countLabel} />
      {actions ? (
        <span className="ml-auto flex items-center gap-2">{actions}</span>
      ) : null}
    </div>
  )
}
