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

interface TenantOption {
  tenant: string
  label: string
  sub?: string
}

function shortId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 8)}…` : id
}

/**
 * Organization switcher. A superadmin can move across all provisioned orgs (by
 * name); a member switches among the tenants it belongs to. The active tenant is
 * propagated as X-Olivares-Tenant on every request. (The engine does not expose
 * org NAMES to non-superadmins — minimum data — so members see a short id + role.)
 */
export function TenantSwitcher() {
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
      <span className="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-sm text-muted-foreground">
        <Building2 className="size-4" />
        <span className="max-w-[12rem] truncate">{activeLabel}</span>
      </span>
    )
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="base" className="max-w-[14rem] gap-1.5">
          <Building2 className="size-4 text-muted-foreground" />
          <span className="truncate">{activeLabel}</span>
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
              <span className="truncate">{o.label}</span>
              {o.sub && (
                <span className="truncate font-mono text-xs text-muted-foreground">
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
