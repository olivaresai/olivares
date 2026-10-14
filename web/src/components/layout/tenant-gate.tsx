// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { type ReactNode } from 'react'
import { useRouterState } from '@tanstack/react-router'
import { Building2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { Spinner } from '@/components/ui/spinner'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/lib/auth/context'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { grantName, shortId } from './tenant-label'

function MembershipGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation(['auth', 'common'])
  const { activeTenant, grants, setActiveTenant } = useAuth()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  if (activeTenant || pathname === '/settings') return <>{children}</>
  // AuthProvider selects a single grant; do not mount scoped queries before it does.
  if (grants.length === 1)
    return (
      <div role="status">
        <span className="sr-only">{t('common:states.loading')}</span>
        <Spinner aria-hidden />
      </div>
    )
  if (grants.length === 0)
    return (
      <EmptyState
        icon={<Building2 />}
        title={t('auth:tenant.none')}
        description={t('auth:tenant.noneHint')}
      />
    )
  return (
    <section aria-label={t('auth:tenant.switch')}>
      {grants.map((grant) => (
        <Button
          key={grant.tenant}
          onClick={() => setActiveTenant(grant.tenant)}
        >
          {grantName(grants, grant.tenant) ?? shortId(grant.tenant)}
        </Button>
      ))}
    </section>
  )
}

const OrganizationGate = PANEL_EXTENSIONS.tenantGate ?? MembershipGate
export function TenantGate({ children }: { children: ReactNode }) {
  return <OrganizationGate>{children}</OrganizationGate>
}
