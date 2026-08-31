// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DataTable listing all backup snapshots with per-row actions: download, inspect
// (manifest detail sheet E4d), and delete. The web adds no logic — sizes,
// counts, and timestamps come from the engine verbatim (ARCHITECTURE.md SS8).
import { Download, Eye, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { formatDateTime } from '@/lib/format'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { drApi, drKeys } from './api'
import { BackupInspectSheet } from './backup-inspect-sheet'
import type { BackupListItem } from './types'

/** Format raw bytes into a human-readable size (KB, MB, GB). */
function formatBytes(
  bytes: number,
  t: (key: string, options?: Record<string, unknown>) => string,
): string {
  if (bytes === 0) return t('units.bytes', { value: '0' })
  const units = ['bytes', 'kilobytes', 'megabytes', 'gigabytes', 'terabytes']
  const k = 1024
  const i = Math.min(
    Math.floor(Math.log(bytes) / Math.log(k)),
    units.length - 1,
  )
  const value = bytes / k ** i
  return t(`units.${units[i]}`, {
    value: value < 10 ? value.toFixed(1) : Math.round(value),
  })
}

export interface BackupListProps {
  data: BackupListItem[]
  isLoading: boolean
  error?: unknown
  onRetry: () => void
}

export function BackupList({
  data,
  isLoading,
  error,
  onRetry,
}: BackupListProps) {
  const { t } = useTranslation('backups')
  const [deleteTarget, setDeleteTarget] = useState<BackupListItem | null>(null)
  const [inspectId, setInspectId] = useState<string | null>(null)

  const deleteMutation = usePrivilegedMutation({
    mutationFn: (id: string) => drApi.deleteBackup(id),
    invalidateKeys: [drKeys.backups()],
    successMessage: t('list.deleted'),
    onDone: () => setDeleteTarget(null),
  })

  const columns = useMemo<TableColumn<BackupListItem, unknown>[]>(
    () => [
      {
        accessorKey: 'created_at',
        header: t('list.columns.date'),
        cell: ({ row }) => (
          <span className="text-sm">
            {formatDateTime(row.original.created_at)}
          </span>
        ),
      },
      {
        accessorKey: 'filename',
        header: t('list.columns.filename'),
        cell: ({ row }) => (
          <span className="font-mono text-xs" title={row.original.filename}>
            {row.original.filename}
          </span>
        ),
      },
      {
        accessorKey: 'size_bytes',
        header: t('list.columns.size'),
        cell: ({ row }) => (
          <span className="tabular-nums text-sm">
            {formatBytes(row.original.size_bytes, t)}
          </span>
        ),
      },
      {
        accessorKey: 'engine',
        header: t('list.columns.engine'),
        cell: ({ row }) => (
          <Badge variant="outline">{row.original.engine}</Badge>
        ),
      },
      {
        accessorKey: 'tenant_count',
        header: t('list.columns.tenants'),
        cell: ({ row }) => (
          <span className="tabular-nums text-sm">
            {row.original.tenant_count}
          </span>
        ),
      },
      {
        accessorKey: 'notes',
        header: t('list.columns.notes'),
        cell: ({ row }) => (
          <span
            className="max-w-[200px] truncate text-xs text-muted-foreground"
            title={row.original.notes || undefined}
          >
            {row.original.notes || '—'}
          </span>
        ),
      },
      {
        id: 'actions',
        header: '',
        cell: ({ row }) => (
          <RowActions
            backup={row.original}
            onInspect={() => setInspectId(row.original.id)}
            onDelete={() => setDeleteTarget(row.original)}
          />
        ),
      },
    ],
    [t],
  )

  return (
    <>
      <DataTable
        columns={columns}
        data={data}
        isLoading={isLoading}
        error={error}
        onRetry={onRetry}
        getRowId={(r) => r.id}
        label={t('list.label')}
        searchable
        searchPlaceholder={t('list.searchPlaceholder')}
        empty={
          <EmptyState
            title={t('empty.backup.title')}
            description={t('empty.backup.description')}
          />
        }
      />

      {deleteTarget && (
        <ConfirmDialog
          open={!!deleteTarget}
          onOpenChange={(o) => !o && setDeleteTarget(null)}
          title={t('list.deleteTitle')}
          description={t('list.deleteDescription', {
            filename: deleteTarget.filename,
          })}
          tone="danger"
          confirmLabel={t('actions.delete')}
          pending={deleteMutation.isPending}
          onConfirm={() => deleteMutation.mutate(deleteTarget.id)}
        />
      )}

      <BackupInspectSheet
        backupId={inspectId}
        onClose={() => setInspectId(null)}
      />
    </>
  )
}

/** Per-row action buttons: download, inspect (manifest sheet), delete. */
function RowActions({
  backup,
  onInspect,
  onDelete,
}: {
  backup: BackupListItem
  onInspect: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation('backups')
  const handleDownload = () => {
    // Build an authenticated download by creating a temporary anchor. The route
    // requires auth headers so we fetch the blob with credentials and save it.
    const url = drApi.downloadUrl(backup.id)
    const token = useSessionStore.getState().token
    const tenant = useTenantStore.getState().activeTenant
    const headers = new Headers()
    if (token) headers.set('Authorization', `Bearer ${token}`)
    if (tenant) headers.set('X-Olivares-Tenant', tenant)

    void fetch(url, { method: 'GET', headers, credentials: 'same-origin' })
      .then((res) => {
        if (!res.ok)
          throw new Error(
            t('list.actions.downloadFailed', { status: res.status }),
          )
        return res.blob()
      })
      .then((blob) => {
        const href = URL.createObjectURL(blob)
        const a = document.createElement('a')
        a.href = href
        a.download = backup.filename
        document.body.appendChild(a)
        a.click()
        a.remove()
        URL.revokeObjectURL(href)
      })
  }

  return (
    <div className="flex items-center justify-end gap-1">
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('list.actions.downloadAria', {
          filename: backup.filename,
        })}
        title={t('list.actions.download')}
        onClick={(e) => {
          e.stopPropagation()
          handleDownload()
        }}
      >
        <Download className="size-4" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('list.actions.inspectAria', {
          filename: backup.filename,
        })}
        title={t('list.actions.inspect')}
        onClick={(e) => {
          e.stopPropagation()
          onInspect()
        }}
      >
        <Eye className="size-4" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('list.actions.deleteAria', {
          filename: backup.filename,
        })}
        title={t('actions.delete')}
        onClick={(e) => {
          e.stopPropagation()
          onDelete()
        }}
      >
        <Trash2 className="size-4" />
      </Button>
    </div>
  )
}
