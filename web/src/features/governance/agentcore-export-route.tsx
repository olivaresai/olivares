// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The routed page for the AgentCore Cedar export. It exists so the registry
// can `lazy(() => import(...))` a DEFAULT export while the view itself stays a
// named export the tests and any future embedder can import directly, and so the
// page chrome (title, recording notice) lives with the route rather than inside
// the view.
//
// Importing './i18n' here is not incidental: the registry loads this module as
// its own lazy chunk, and a chunk that renders the `governance` namespace without
// registering it paints raw key names on screen. That is what
// scripts/check-i18n-namespaces.mjs guards.
import { useTranslation } from 'react-i18next'
import { CloudUpload } from 'lucide-react'
import { PageHeader } from '@/components/ui/page-header'
import { RecordingNotice } from '@/features/recordings/recording-notice'
import { AgentCoreExportView } from './agentcore-export-view'
import './i18n'

export default function AgentCoreExportRoute() {
  const { t } = useTranslation(['governance', 'common'])
  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('agentcoreExport.pageTitle')}
        description={t('agentcoreExport.caption')}
        icon={CloudUpload}
      />
      <RecordingNotice namespace="governance" />
      <AgentCoreExportView />
    </div>
  )
}
