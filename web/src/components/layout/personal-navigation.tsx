// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link, useRouterState } from '@tanstack/react-router'
import { History, Star, X } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { usePersonalNavigation } from '@/features/navigation/personal-navigation'
import {
  personalLink,
  resolvePersonalLink,
  type PersonalLink,
} from '@/features/navigation/personal-navigation-store'
import { resolveLocation, viewLabel } from '@/features/navigation/model'
import { cn } from '@/lib/utils'

/** Explicit save only. Resolves the module, never captures the location's query or data. */
export function FavoriteButton() {
  const personal = usePersonalNavigation()
  const { t } = useTranslation('nav')
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const location = resolveLocation(pathname)
  const id =
    location.kind === 'view' || location.kind === 'home'
      ? location.view.id
      : location.kind === 'settings'
        ? 'settings'
        : null
  const link = id ? personalLink(id) : undefined
  if (!personal?.available || !link || !personal.visible(link)) return null
  const saved = personal.favorites.some((v) => v.id === link.id)
  const label = t(saved ? 'personal.remove' : 'personal.add', {
    name: viewLabel(t, link.id),
  })
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label={label}
      title={label}
      aria-pressed={saved}
      onClick={() => personal.setFavorite(link, !saved)}
    >
      <Star
        aria-hidden
        className={saved ? 'fill-accent-soft text-accent-text' : undefined}
      />
    </Button>
  )
}

/** Native links keep modified clicks, URL authority and the existing route guards. */
function PersonalLinks({
  links,
  onNavigate,
  kind,
  removable = false,
}: {
  links: readonly PersonalLink[]
  onNavigate?: () => void
  kind: 'favorites' | 'recents'
  removable?: boolean
}) {
  const personal = usePersonalNavigation()
  const { t } = useTranslation('nav')
  const list = useRef<HTMLUListElement>(null)
  return (
    <ul ref={list} className="space-y-0.5">
      {links.map((link, index) => {
        const target = resolvePersonalLink(link)
        if (!target || !personal?.visible(link)) return null
        const label = viewLabel(t, link.id)
        const Icon = target.icon
        return (
          <li key={link.id} className="flex min-w-0 items-center gap-1">
            <Link
              to={target.path as never}
              activeOptions={{ exact: true }}
              onClick={(event) => {
                if (!personal.visible(link)) {
                  event.preventDefault()
                  return
                }
                onNavigate?.()
              }}
              className="flex min-h-8 min-w-0 flex-1 items-center gap-2.5 rounded-md px-2.5 text-sm text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring data-[status=active]:bg-accent-soft data-[status=active]:text-foreground"
            >
              <Icon className="size-4 shrink-0" aria-hidden />
              <span className="truncate" title={label}>
                {label}
              </span>
            </Link>
            {removable && (
              <Button
                variant="ghost"
                size="icon"
                className="size-8 shrink-0"
                aria-label={t(
                  kind === 'favorites'
                    ? 'personal.remove'
                    : 'personal.recentRemove',
                  { name: label },
                )}
                onClick={() => {
                  // Move focus before removing the focused button; the last removal goes to
                  // the dialog's stable title, rather than dropping focus onto the document.
                  const rows = list.current?.querySelectorAll('li')
                  const next = rows?.[index + 1] ?? rows?.[index - 1]
                  const destination =
                    next?.querySelector('button') ??
                    list.current
                      ?.closest('[role="dialog"]')
                      ?.querySelector<HTMLElement>('[data-personal-title]')
                  destination?.focus()
                  if (kind === 'favorites') personal.setFavorite(link, false)
                  else personal.removeRecent(link.id)
                }}
              >
                <X className="size-3.5" aria-hidden />
              </Button>
            )}
          </li>
        )
      })}
    </ul>
  )
}

/** The same managers are reachable in desktop, mobile and the compact rail. */
export function PersonalNavigation(props: {
  collapsed?: boolean
  onNavigate?: () => void
}) {
  return (
    <>
      <PersonalSection {...props} kind="favorites" />
      <PersonalSection {...props} kind="recents" />
    </>
  )
}

function PersonalSection({
  kind,
  collapsed = false,
  onNavigate,
}: {
  collapsed?: boolean
  onNavigate?: () => void
  kind: 'favorites' | 'recents'
}) {
  const personal = usePersonalNavigation()
  const { t } = useTranslation('nav')
  const titleId = useId()
  const [open, setOpen] = useState(false)
  const heading = useRef<HTMLHeadingElement>(null)
  if (!personal?.available) return null
  const links = personal[kind]
  const recent = kind === 'recents'
  const title = t(recent ? 'personal.recents' : 'personal.favorites')
  const manage = t(recent ? 'personal.recentManage' : 'personal.manage')
  const empty = t(recent ? 'personal.recentEmpty' : 'personal.empty')
  const Icon = recent ? History : Star
  const openLabel = links.length
    ? t(recent ? 'personal.recentViewAll' : 'personal.viewAll', {
        count: links.length,
      })
    : manage
  return (
    <section
      aria-labelledby={titleId}
      className={cn(
        'min-w-0 py-1',
        !collapsed && 'border-b border-border pb-2',
      )}
    >
      <Dialog open={open} onOpenChange={setOpen}>
        <div
          className={cn(
            'flex items-center justify-between gap-1',
            !collapsed && 'px-2.5',
          )}
        >
          <h2
            id={titleId}
            className={
              collapsed
                ? 'sr-only'
                : 'text-xs font-medium text-muted-foreground'
            }
          >
            {title}
          </h2>
          <DialogTrigger asChild>
            {collapsed ? (
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-full"
                aria-label={manage}
                title={title}
              >
                <Icon className="size-4" aria-hidden />
              </Button>
            ) : (
              <button
                type="button"
                className="min-h-8 shrink-0 rounded px-1 text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
              >
                {openLabel}
              </button>
            )}
          </DialogTrigger>
        </div>
        {!collapsed &&
          (links.length ? (
            <PersonalLinks
              kind={kind}
              links={links.slice(0, recent ? 3 : 4)}
              onNavigate={onNavigate}
            />
          ) : (
            <p className="px-2.5 pb-1 text-xs leading-relaxed text-muted-foreground">
              {empty}
            </p>
          ))}
        <DialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-md grid-rows-[auto_minmax(0,1fr)_auto]">
          <DialogHeader>
            <DialogTitle
              ref={heading}
              tabIndex={-1}
              data-personal-title=""
              className="outline-none"
            >
              {title}
            </DialogTitle>
            <DialogDescription>
              {t(
                recent
                  ? 'personal.recentScope'
                  : personal.temporary
                    ? 'personal.temporary'
                    : 'personal.scope',
              )}
            </DialogDescription>
          </DialogHeader>
          <div className="-m-1 min-h-16 overflow-y-auto p-1">
            {links.length ? (
              <PersonalLinks
                kind={kind}
                links={links}
                removable
                onNavigate={() => {
                  setOpen(false)
                  onNavigate?.()
                }}
              />
            ) : (
              <p className="py-3 text-sm text-muted-foreground">{empty}</p>
            )}
          </div>
          <Button
            variant="secondary"
            onClick={() => {
              heading.current?.focus()
              if (recent) personal.clearRecents()
              else personal.clearFavorites()
            }}
          >
            {t(recent ? 'personal.recentClear' : 'personal.clear')}
          </Button>
        </DialogContent>
      </Dialog>
    </section>
  )
}
