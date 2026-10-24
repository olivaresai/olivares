// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate, useRouterState } from '@tanstack/react-router'
import {
  CircleHelp,
  KeyRound,
  LayoutGrid,
  LogOut,
  Palette,
  Settings,
  SquareTerminal,
  Star,
} from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { EditionBadge } from '@/features/console/edition-badge'
import { resolveLocation } from '@/features/navigation/model'
import { useAuth } from '@/lib/auth/context'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { useThemeStore, type Theme } from '@/stores/theme'
import { CommandLineDialog } from './command-line-dialog'
import { ChangePasswordDialog } from './change-password-dialog'
import { useAreasSheet } from './areas-sheet-store'
import { useFavoriteThisPage } from './personal-navigation'
import { THEME_OPTIONS } from './theme-toggle'

/**
 * Where Help sends the operator.
 *
 * ⛔ HISTORY, kept because it is the reason the switch below exists at all. Until 2026-08-19 this
 * was `https://docs.olivares.ai` and that name had been WITHDRAWN from DNS (NXDOMAIN, while the
 * same `getent` run resolved the apex), so all 38 help destinations failed before they were even a
 * 404. Repointing the base to olivares.ai/docs was measured before choosing: the 38 `helpHref`
 * values are Diataxis paths belonging to `docs-site/`, and the marketing site under
 * olivares.ai/docs has a different information architecture — **0 of the 38 existed there**. So
 * the link went to the docs index, the per-view paths stayed in the registry because they ARE the
 * mapping, and the comment named the condition to flip on: *the day docs-site ships at this base*.
 *
 * ⇒ THAT DAY WAS 2026-08-23, and the condition is MET as of 2026-08-27. docs-site is deployed as
 * the `olivares-docs` Worker and `docs.olivares.ai` serves it. Measured then, not assumed and not
 * taken from a second-hand report: the 37 non-root `helpHref` values of
 * `web/src/features/registry.tsx`, requested one by one against `https://docs.olivares.ai<path>/`,
 * answered **37/37 = 200, zero non-200**. The base moved back and the deep links went on.
 *
 * `registry-help.test.ts` is what keeps the 38 honest against the docs tree, and it is the thing
 * to read before touching either constant.
 */
const DOCS_BASE = 'https://docs.olivares.ai'

/**
 * Are the per-view docs pages published AT `DOCS_BASE`? Flipped to `true` on 2026-08-27 on the
 * measurement above (37/37 live). Flip it back the moment that stops being true — a precise link
 * that 404s is worse than a general one that works, which is the whole trade this pair encodes.
 */
const DEEP_LINKS_PUBLISHED = true

/** Contextual help: the current view's documentation page; a directory page opens
 * the console reference, which lists every screen; anything else the docs index. */
function helpHrefFor(pathname: string): string {
  const location = resolveLocation(pathname)
  const helpHref =
    location.kind === 'view' || location.kind === 'home'
      ? location.view.helpHref
      : location.kind === 'area'
        ? location.area.helpHref
        : undefined
  return DEEP_LINKS_PUBLISHED && helpHref && helpHref !== '/'
    ? `${DOCS_BASE}${helpHref}/`
    : DOCS_BASE
}

function initialsOf(name: string): string {
  const trimmed = name.replace(/^user:/, '').trim()
  return (trimmed.slice(0, 2) || '?').toUpperCase()
}

/** The account menu: identity, role, the edition and version, All areas (every page the top
 * bar does not name), this page in favorites, Settings, the theme, help for this page, the
 * command line, and sign-out (#3161 moved the version, the theme and favorites here from the
 * top bar; the engine's state went to the status bar). The engine does not return the
 * principal's email to itself (minimum data), so we show the display name and the principal
 * id (`actor`). */
export function UserMenu() {
  const { t } = useTranslation(['auth', 'nav', 'common'])
  const { principal, isSuperadmin, activeRole, logout } = useAuth()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const version = useServerInfo().data?.version
  const favorite = useFavoriteThisPage()
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)
  // All areas gives focus back to the account button, not to the menu item it closed.
  const trigger = useRef<HTMLButtonElement>(null)

  const name = principal?.display_name || principal?.actor || ''
  const initials = initialsOf(name)
  // Every signed-in person can reach the command line (Root, 09b), not only the wizard's
  // administrator.
  const [commandLine, setCommandLine] = useState(false)
  const [passwordChange, setPasswordChange] = useState(false)

  return (
    <>
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <Button
            ref={trigger}
            variant="ghost"
            size="icon"
            className="rounded-full"
            // The name starts with what the button shows (WCAG 2.5.3): a voice user
            // says the initials they see.
            aria-label={`${initials}, ${t('auth:account.title')}`}
            title={t('auth:account.title')}
          >
            <Avatar size="base">
              <AvatarFallback>{initials}</AvatarFallback>
            </Avatar>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-56">
          <div className="flex flex-col gap-1 px-2 py-1.5">
            {principal?.display_name && (
              <p className="truncate text-body font-medium text-foreground">
                {principal.display_name}
              </p>
            )}
            <p className="truncate font-mono text-caption text-muted-foreground">
              {principal?.actor}
            </p>
            <div className="mt-1 flex flex-wrap items-center gap-1">
              {/* ⛔ `warning`, NOT `accent`. The chip says which authority this session
                is acting with, which is a STATE — the semantic axis — and the accent
                is reserved for selection, the primary action and links. It keeps a
                colour of its own because the difference between operating as a
                superadmin and as anything else is worth seeing without reading. */}
              {isSuperadmin ? (
                <Badge variant="warning">{t('auth:roles.superadmin')}</Badge>
              ) : activeRole ? (
                <Badge variant="neutral">
                  {t(`auth:roles.${activeRole}`, { defaultValue: activeRole })}
                </Badge>
              ) : null}
              <EditionBadge />
              {version ? (
                <span className="text-caption text-muted-foreground tabular-nums">
                  {t('nav:shell.account.version', { version })}
                </span>
              ) : null}
            </div>
          </div>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onSelect={() => useAreasSheet.getState().show(trigger.current)}
          >
            <LayoutGrid />
            {t('nav:shell.allAreas')}
          </DropdownMenuItem>
          {favorite ? (
            <DropdownMenuItem onSelect={favorite.toggle}>
              <Star
                className={
                  favorite.saved
                    ? 'fill-accent-soft text-accent-text'
                    : undefined
                }
              />
              {favorite.label}
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem onSelect={() => void navigate({ to: '/settings' })}>
            <Settings />
            {t('nav:items.settings')}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuLabel className="flex items-center gap-2 font-normal text-muted-foreground">
            <Palette aria-hidden className="size-4" />
            {t('common:theme.label')}
          </DropdownMenuLabel>
          <DropdownMenuRadioGroup
            aria-label={t('common:theme.label')}
            value={theme}
            onValueChange={(value) => setTheme(value as Theme)}
          >
            {THEME_OPTIONS.map((o) => (
              <DropdownMenuRadioItem key={o.value} value={o.value}>
                {t(`common:${o.labelKey}`)}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
          <DropdownMenuSeparator />
          <DropdownMenuItem asChild>
            <a href={helpHrefFor(pathname)} target="_blank" rel="noreferrer">
              <CircleHelp />
              {t('common:actions.help')}
            </a>
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setCommandLine(true)}>
            <SquareTerminal />
            {t('auth:account.commandLine')}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setPasswordChange(true)}>
            <KeyRound />
            {t('auth:passwordChange.title')}
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => void logout()}
          >
            <LogOut />
            {t('auth:account.signOut')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <CommandLineDialog open={commandLine} onOpenChange={setCommandLine} />
      {passwordChange && (
        <ChangePasswordDialog onOpenChange={setPasswordChange} />
      )}
    </>
  )
}
