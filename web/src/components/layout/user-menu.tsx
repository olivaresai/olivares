// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate } from '@tanstack/react-router'
import { LogOut, Settings } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useAuth } from '@/lib/auth/context'

function initialsOf(name: string): string {
  const trimmed = name.replace(/^user:/, '').trim()
  return (trimmed.slice(0, 2) || '?').toUpperCase()
}

/** The account menu: identity, role, and the sign-out / settings actions. The
 * engine does not return the principal's email to itself (minimum data), so we show
 * the display name and the principal id (`actor`). */
export function UserMenu() {
  const { t } = useTranslation(['auth', 'nav'])
  const { principal, isSuperadmin, activeRole, logout } = useAuth()
  const navigate = useNavigate()

  const name = principal?.display_name || principal?.actor || ''

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="rounded-full"
          aria-label={t('auth:account.title')}
        >
          <Avatar size="base">
            <AvatarFallback>{initialsOf(name)}</AvatarFallback>
          </Avatar>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-56">
        <div className="flex flex-col gap-1 px-2 py-1.5">
          {principal?.display_name && (
            <p className="truncate text-sm font-medium text-foreground">
              {principal.display_name}
            </p>
          )}
          <p className="truncate font-mono text-xs text-muted-foreground">
            {principal?.actor}
          </p>
          <div className="mt-1 flex flex-wrap gap-1">
            {isSuperadmin ? (
              <Badge variant="accent">{t('auth:roles.superadmin')}</Badge>
            ) : activeRole ? (
              <Badge variant="neutral">
                {t(`auth:roles.${activeRole}`, { defaultValue: activeRole })}
              </Badge>
            ) : null}
          </div>
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void navigate({ to: '/settings' })}>
          <Settings />
          {t('nav:items.settings')}
        </DropdownMenuItem>
        <DropdownMenuItem variant="destructive" onSelect={() => void logout()}>
          <LogOut />
          {t('auth:account.signOut')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
