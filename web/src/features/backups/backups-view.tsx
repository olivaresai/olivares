// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// BackupsView — the top-level DR management view, superadmin-only. Two tabs:
// "Backups" (list + create + restore) and "Schedule" (automated cadence config). The
// permission gate lives at the route level; this view assumes the caller is authorized.
import { useQuery } from '@tanstack/react-query'
import { DatabaseBackup, Plus, Upload } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { drApi, drKeys } from './api'
import { BackupList } from './backup-list'
import { BackupSchedule } from './backup-schedule'
import { BackupTriggerDialog } from './backup-trigger-dialog'
import { PendingRestores } from './pending-restores'
import { RestoreDialog } from './restore-dialog'
import './i18n'

export function BackupsView() {
  const { t } = useTranslation('backups')
  const [triggerOpen, setTriggerOpen] = useState(false)
  const [restoreOpen, setRestoreOpen] = useState(false)

  const backupsQ = useQuery({
    queryKey: drKeys.backups(),
    queryFn: () => drApi.listBackups(),
  })

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('title')}
        description={t('description')}
        icon={DatabaseBackup}
        actions={
          <div className="flex items-center gap-2">
            <Button variant="secondary" onClick={() => setRestoreOpen(true)}>
              <Upload className="size-4" />
              {t('actions.restore')}
            </Button>
            <Button variant="primary" onClick={() => setTriggerOpen(true)}>
              <Plus className="size-4" />
              {t('actions.createBackup')}
            </Button>
          </div>
        }
      />

      <Tabs defaultValue="backups">
        <TabsList>
          <TabsTrigger value="backups">{t('tabs.backups')}</TabsTrigger>
          <TabsTrigger value="schedule">{t('tabs.schedule')}</TabsTrigger>
        </TabsList>

        <TabsContent value="backups">
          <PendingRestores />
          <BackupList
            data={backupsQ.data?.items ?? []}
            isLoading={backupsQ.isLoading}
            error={backupsQ.error}
            onRetry={() => backupsQ.refetch()}
          />
        </TabsContent>

        <TabsContent value="schedule">
          <BackupSchedule />
        </TabsContent>
      </Tabs>

      <BackupTriggerDialog open={triggerOpen} onOpenChange={setTriggerOpen} />

      <RestoreDialog open={restoreOpen} onOpenChange={setRestoreOpen} />
    </div>
  )
}

export default BackupsView
