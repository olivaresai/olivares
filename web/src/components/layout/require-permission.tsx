// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ForbiddenState } from '@/components/ui/error-state'
import { useAuth } from '@/lib/auth/context'

/**
 * RequirePermission gates a route's content on the backend RBAC (mirrored client
 * side). With no permission it renders the content; otherwise, if the principal
 * lacks it, it shows the calm ForbiddenState (never a red error). This complements
 * nav hiding — a user can still deep-link to a path, so the route enforces too. The
 * backend remains the source of truth; this only avoids a guaranteed 403 round-trip.
 */
export function RequirePermission({
  permission,
  children,
}: {
  permission?: string
  children: ReactNode
}) {
  const { t } = useTranslation('errors')
  const { can } = useAuth()
  if (permission && !can(permission)) {
    return (
      <div className="flex min-h-[60vh] items-center justify-center">
        <ForbiddenState
          title={t('forbidden.title')}
          description={t('forbidden.description')}
        />
      </div>
    )
  }
  return <>{children}</>
}
