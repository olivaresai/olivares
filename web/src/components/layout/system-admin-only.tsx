// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ShieldAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

/**
 * The one answer for a setting of the whole installation (license, SSO, secret store,
 * connectors, account limits, updates) shown to someone who is not a system administrator:
 * one sentence, one layout, and the role that can open it. The page around it says which
 * setting it is, so the sentence never repeats the name.
 */
export function SystemAdminOnly() {
  const { t } = useTranslation('errors')
  return (
    <div
      role="status"
      data-slot="system-admin-only"
      className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-body text-muted-foreground"
    >
      <ShieldAlert
        className="mt-0.5 size-4 shrink-0 text-warning"
        aria-hidden
      />
      {t('systemAdminOnly')}
    </div>
  )
}
