// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Archive } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { IntelPage } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { useCommunityBuild } from '@/lib/hooks/use-server-info'
import { RetentionTab } from './retention-view'
import './i18n'

/**
 * Reading retention classes, schedules and certificates is Community; authoring a schedule and
 * the hold-checked sweep are Business (docs/editions.md, "Records"), so a Community engine shows
 * them read-only and applies none; the shell's closed Business row says so (businessRows). The
 * Compliance page that also mounts this tab is Business. This page reads only the retention routes.
 */
export function RetentionView() {
  const { t } = useTranslation('compliance')
  const { can } = useAuth()
  const community = useCommunityBuild()
  return (
    <IntelPage icon={Archive} title={t('tabs.retention')}>
      <RetentionTab
        canAdmin={community === false && can('compliance:retention:admin')}
        canRead={can('compliance:retention:read')}
      />
    </IntelPage>
  )
}
