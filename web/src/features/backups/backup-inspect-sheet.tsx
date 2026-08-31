// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// BackupInspectSheet — the real Inspect action for a backup row (E4d; until
// then the Eye button was a no-op). Fetches GET /v1/console/dr/backups/{id} and
// renders the decoded manifest verbatim: engine, creation time, tenants and sealed
// key names. The web derives nothing (ARCHITECTURE.md SS8) — an operator reviews exactly
// what a restore of this bundle would bring back.
import { useQuery } from '@tanstack/react-query'
import { Archive } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { ErrorState } from '@/components/ui/error-state'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { formatDateTime } from '@/lib/format'
import { drApi, drKeys } from './api'
import './i18n'

/** Format raw bytes into a human-readable size (KB, MB, GB) — mirrors the list. */
function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const k = 1024
  const i = Math.min(
    Math.floor(Math.log(bytes) / Math.log(k)),
    units.length - 1,
  )
  const value = bytes / k ** i
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[i]}`
}

export function BackupInspectSheet({
  backupId,
  onClose,
}: {
  /** The backup to inspect, or null when the sheet is closed. */
  backupId: string | null
  onClose: () => void
}) {
  const { t } = useTranslation('backups')
  const open = backupId !== null

  const detailQ = useQuery({
    queryKey: drKeys.backup(backupId ?? ''),
    queryFn: () => drApi.getBackup(backupId as string),
    enabled: open,
  })
  const detail = detailQ.data

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <Archive className="size-4 text-accent-text" aria-hidden />
            <span className="truncate">{t('inspect.title')}</span>
          </SheetTitle>
          <SheetDescription>{t('inspect.description')}</SheetDescription>
        </SheetHeader>

        {detailQ.isLoading ? (
          <div className="flex justify-center py-10">
            <Spinner />
          </div>
        ) : detailQ.isError ? (
          <ErrorState
            title={t('inspect.loadFailed')}
            retry={() => void detailQ.refetch()}
          />
        ) : detail ? (
          <div className="flex flex-col gap-4 px-4 pb-4">
            <KvList>
              <KvRow label={t('inspect.filename')} mono>
                {detail.filename}
              </KvRow>
              <KvRow label={t('inspect.size')} mono>
                {formatBytes(detail.size_bytes)}
              </KvRow>
              <KvRow label={t('inspect.engine')}>
                <Badge variant="outline">{detail.manifest.engine}</Badge>
              </KvRow>
              <KvRow label={t('inspect.createdAt')}>
                {formatDateTime(detail.manifest.created_at)}
              </KvRow>
            </KvList>

            <div className="flex flex-col gap-1.5">
              <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                {t('inspect.tenants')}
              </span>
              {detail.manifest.tenants.length === 0 ? (
                <span className="text-sm text-muted-foreground">
                  {t('inspect.empty')}
                </span>
              ) : (
                <div className="flex flex-wrap gap-1">
                  {detail.manifest.tenants.map((tenant) => (
                    <Badge
                      key={tenant.tenant}
                      variant="outline"
                      className="font-mono"
                    >
                      {tenant.tenant}
                    </Badge>
                  ))}
                </div>
              )}
            </div>

            <div className="flex flex-col gap-1.5">
              <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                {t('inspect.keys')}
              </span>
              {detail.manifest.keys.length === 0 ? (
                <span className="text-sm text-muted-foreground">
                  {t('inspect.empty')}
                </span>
              ) : (
                <div className="flex flex-wrap gap-1">
                  {detail.manifest.keys.map((key) => (
                    <Badge
                      key={key.name}
                      variant="neutral"
                      className="font-mono"
                    >
                      {key.name}
                    </Badge>
                  ))}
                </div>
              )}
            </div>
          </div>
        ) : null}
      </SheetContent>
    </Sheet>
  )
}
