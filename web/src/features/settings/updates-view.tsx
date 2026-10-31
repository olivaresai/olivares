// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Download } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import { useAuth } from '@/lib/auth/context'
import { UpdatesSettings } from './updates-settings'

export default function UpdatesView() {
  const { t } = useTranslation('settings')
  const { principal } = useAuth()
  return (
    <div className="flex flex-col gap-2">
      <PageHeader
        title={t('nav.updates')}
        description={t('updates.description')}
        icon={Download}
      />
      <UpdatesSettings key={principal?.user_id} />
    </div>
  )
}
