// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Building2, Check, ChevronsUpDown } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { systemApi } from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { shortId } from './tenant-label'

interface TenantOption {
  tenant: string
  label: string
  sub?: string
}

/**
 * Organization switcher. A superadmin can move across all provisioned orgs (by
 * name); a member switches among the tenants it belongs to. The active tenant is
 * propagated as X-Olivares-Tenant on every request. (The engine does not expose
 * org NAMES to non-superadmins — minimum data — so members see a short id + role.)
 */
export function TenantSwitcher({ className }: { className?: string } = {}) {
  const { t } = useTranslation(['auth', 'common'])
  const { grants, activeTenant, setActiveTenant, isSuperadmin } = useAuth()

  const orgs = useQuery({
    queryKey: queryKeys.orgs,
    queryFn: () => systemApi.listOrgs(),
    enabled: isSuperadmin,
    staleTime: 60_000,
  })

  const options: TenantOption[] =
    isSuperadmin && orgs.data
      ? orgs.data.items.map((o) => ({
          tenant: o.tenant_id,
          label: o.name,
          sub: o.slug,
        }))
      : grants.map((g) => ({
          tenant: g.tenant,
          label: shortId(g.tenant),
          sub: t(`auth:roles.${g.role}`, { defaultValue: String(g.role) }),
        }))

  // Nothing to show: a non-superadmin with no memberships.
  if (!isSuperadmin && grants.length === 0) return null

  const active = options.find((o) => o.tenant === activeTenant)
  const activeLabel =
    active?.label ??
    (activeTenant ? shortId(activeTenant) : t('auth:tenant.none'))

  // A single, fixed membership is a label, not a control.
  if (!isSuperadmin && options.length <= 1) {
    return (
      <span
        className={cn(
          'inline-flex h-8 min-w-0 items-center gap-1.5 rounded-md px-2 text-body text-muted-foreground',
          className,
        )}
        title={activeLabel}
      >
        <Building2 className="size-4 shrink-0" />
        <span className="max-w-[12rem] truncate" title={activeLabel}>
          {activeLabel}
        </span>
      </span>
    )
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        {/* `min-w-0 shrink` so the topbar can truncate this label instead of
            overflowing the bar at 1024/390 px; the full name stays the accessible
            name and is offered as a tooltip. */}
        <Button
          variant="ghost"
          size="base"
          className={cn('min-w-24 max-w-[14rem] shrink gap-1.5', className)}
          title={activeLabel}
        >
          <Building2 className="size-4 text-muted-foreground" />
          <span
            className="min-w-0 flex-1 truncate text-left"
            title={activeLabel}
          >
            {activeLabel}
          </span>
          <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-56">
        <DropdownMenuLabel>{t('auth:tenant.switch')}</DropdownMenuLabel>
        {options.map((o) => (
          <DropdownMenuItem
            key={o.tenant}
            onSelect={() => setActiveTenant(o.tenant)}
          >
            <span className="flex min-w-0 flex-col">
              <span className="truncate" title={o.label}>
                {o.label}
              </span>
              {o.sub && (
                <span
                  className="truncate font-mono text-caption text-muted-foreground"
                  title={o.sub}
                >
                  {o.sub}
                </span>
              )}
            </span>
            {o.tenant === activeTenant && (
              <Check className="ml-auto text-accent-text" />
            )}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
