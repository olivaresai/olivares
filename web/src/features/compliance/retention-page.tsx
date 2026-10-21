// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Archive } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { IntelPage } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { RetentionTab } from './retention-view'
import './i18n'

/**
 * Retention schedules and the hold-checked sweep are Community (docs/editions.md, "Records"); the
 * Compliance page that also mounts this tab is Business. This page reads only the retention routes.
 */
export function RetentionView() {
  const { t } = useTranslation('compliance')
  const { can } = useAuth()
  return (
    <IntelPage icon={Archive} title={t('tabs.retention')}>
      <RetentionTab
        canAdmin={can('compliance:retention:admin')}
        canRead={can('compliance:retention:read')}
      />
    </IntelPage>
  )
}
