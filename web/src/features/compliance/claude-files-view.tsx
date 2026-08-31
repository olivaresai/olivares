// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { FileWarning, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatBytes } from '@/lib/format'
import { DisclaimerNote, SectionCard } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { complianceApi, complianceKeys } from './api'
import type {
  ClaudeFileEraseResult,
  ClaudeFileEraseStatus,
  ClaudeFileRef,
} from './types'

/** El veredicto de un borrado NO se lee del código HTTP.
 *
 *  El motor mapea SIETE estados sobre SEIS códigos (`claudefiles.go`): 503 sirve para
 *  `not_wired` y para `error`, 403 para tres denegaciones distintas. Y un no-2xx no es un
 *  fallo de transporte: trae el documento de dominio, que el cliente conserva en
 *  `ApiError.body` justamente para esto. Sin esta función, una retención legal se le
 *  enseñaría al operador como «algo ha fallado». */
export function eraseOutcome(e: unknown): ClaudeFileEraseResult | null {
  if (e instanceof ApiError && e.body && typeof e.body === 'object') {
    const b = e.body as Partial<ClaudeFileEraseResult>
    if (typeof b.status === 'string' && typeof b.file_id === 'string') {
      return b as ClaudeFileEraseResult
    }
  }
  return null
}

/** Los estados que NO son «borrado», por si alguien añade uno: el mapa se recorre por
 *  clave, así que un estado nuevo sin entrada se ve como tal en vez de pintarse de verde. */
const TONO: Record<ClaudeFileEraseStatus, 'success' | 'warning' | 'danger'> = {
  deleted: 'success',
  pending: 'warning',
  held: 'warning',
  denied: 'danger',
  failed: 'danger',
  not_wired: 'danger',
  error: 'danger',
}

export function ClaudeFilesPanel({
  canAdmin,
  canRead,
}: {
  canAdmin: boolean
  canRead: boolean
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const [erasing, setErasing] = useState<ClaudeFileRef | null>(null)
  const [reason, setReason] = useState('')
  const [outcome, setOutcome] = useState<ClaudeFileEraseResult | null>(null)

  const q = useQuery({
    queryKey: complianceKeys.claudeFiles(activeTenant),
    queryFn: () => complianceApi.claudeFiles(),
    enabled: canRead,
  })

  const mutation = useMutation({
    mutationFn: (f: ClaudeFileRef) =>
      complianceApi.eraseClaudeFile(f.id, reason.trim() || undefined),
    onSuccess: async (res) => {
      setOutcome(res)
      await queryClient.invalidateQueries({
        queryKey: complianceKeys.claudeFiles(activeTenant),
      })
      if (res.status === 'deleted') toast.success(t('claudeFiles.deleted'))
    },
    onError: (err) => {
      // Un no-2xx con documento de dominio NO es un error: es el veredicto.
      const res = eraseOutcome(err)
      if (res) {
        setOutcome(res)
        return
      }
      toast.error(t('claudeFiles.eraseFailed'))
    },
  })

  if (!canRead) {
    return (
      <SectionCard title={t('claudeFiles.title')}>
        <EmptyState icon={<FileWarning />} title={t('claudeFiles.noAccess')} />
      </SectionCard>
    )
  }

  const inv = q.data
  const columns: TableColumn<ClaudeFileRef, unknown>[] = [
    {
      accessorKey: 'id',
      header: t('claudeFiles.col.id'),
      cell: ({ row }) => (
        <span className="font-mono text-xs break-all">{row.original.id}</span>
      ),
    },
    {
      accessorKey: 'mime_type',
      header: t('claudeFiles.col.mime'),
      cell: ({ row }) => row.original.mime_type || '—',
    },
    {
      accessorKey: 'size_bytes',
      header: t('claudeFiles.col.size'),
      cell: ({ row }) =>
        row.original.size_bytes ? formatBytes(row.original.size_bytes) : '—',
    },
    {
      accessorKey: 'created_at',
      header: t('claudeFiles.col.created'),
      cell: ({ row }) =>
        row.original.created_at ? (
          <RelTimeLabel ts={row.original.created_at} />
        ) : (
          '—'
        ),
    },
    {
      accessorKey: 'scope_id',
      header: t('claudeFiles.col.scope'),
      cell: ({ row }) => (
        <span className="font-mono text-xs">
          {row.original.scope_id || '—'}
        </span>
      ),
    },
    ...(canAdmin
      ? [
          {
            id: 'erase',
            header: '',
            cell: ({ row }: { row: { original: ClaudeFileRef } }) => (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setReason('')
                  setOutcome(null)
                  setErasing(row.original)
                }}
              >
                <Trash2 />
                {t('claudeFiles.erase')}
              </Button>
            ),
          } as TableColumn<ClaudeFileRef, unknown>,
        ]
      : []),
  ]

  return (
    <SectionCard title={t('claudeFiles.title')}>
      <div className="flex flex-col gap-3">
        {/* El aviso del MOTOR, literal. Es la razón de que esta pantalla exista: dice que el
            almacén no lleva metadatos de sujeto, así que el borrado por sujeto no puede
            seleccionar desde aquí. Sin él, un operador puede creer que ha ejercido un derecho
            de supresión que el producto no puede ejercer. */}
        <DisclaimerNote text={inv?.disclosure} />

        {q.isLoading && <Skeleton className="h-24 w-full" />}

        {inv && !inv.wired && (
          <div className="rounded-md border border-warning bg-warning-soft px-3 py-2">
            <p className="text-sm font-medium text-foreground">
              {t('claudeFiles.unwired.title')}
            </p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t('claudeFiles.unwired.body')}
            </p>
          </div>
        )}

        {inv && inv.wired && (
          <KvList>
            <KvRow label={t('claudeFiles.count')}>{inv.count}</KvRow>
            <KvRow label={t('claudeFiles.totalBytes')}>
              {formatBytes(inv.total_bytes)}
            </KvRow>
          </KvList>
        )}

        {inv?.wired && (
          <DataTable
            columns={columns}
            data={inv.files ?? []}
            isLoading={q.isLoading}
            error={q.error}
            onRetry={() => q.refetch()}
            getRowId={(r) => r.id}
            empty={
              <EmptyState
                icon={<FileWarning />}
                title={t('claudeFiles.empty.title')}
                description={t('claudeFiles.empty.body')}
              />
            }
          />
        )}

        {outcome && <EraseOutcome result={outcome} />}
      </div>

      <ConfirmDialog
        open={erasing !== null}
        onOpenChange={(o) => !o && setErasing(null)}
        title={t('claudeFiles.confirm.title')}
        description={t('claudeFiles.confirm.body')}
        confirmLabel={t('claudeFiles.erase')}
        /* Frase obligatoria: el id del fichero. Es la convención de la casa para lo
           irreversible, y aquí hace un segundo trabajo — teclear el id impide borrar la
           fila de al lado, que en una tabla de identificadores opacos es fácil. */
        confirmPhrase={erasing?.id}
        onConfirm={() => {
          const f = erasing
          setErasing(null)
          if (f) mutation.mutate(f)
        }}
      >
        <Field
          label={t('claudeFiles.reason')}
          htmlFor="cf-reason"
          description={t('claudeFiles.reasonHint')}
        >
          <Textarea
            id="cf-reason"
            rows={2}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </Field>
      </ConfirmDialog>
    </SectionCard>
  )
}

/** El resultado, con su estado NOMBRADO. Nunca «hecho» a secas: `held` y `pending` no han
 *  borrado nada, y `denied` es una decisión, no un fallo. */
function EraseOutcome({ result }: { result: ClaudeFileEraseResult }) {
  const { t } = useTranslation('compliance')
  return (
    <div className="rounded-md border border-border px-3 py-2">
      <div className="flex items-center gap-2">
        <Badge variant={TONO[result.status] ?? 'danger'}>
          {t(`claudeFiles.status.${result.status}`, {
            defaultValue: result.status,
          })}
        </Badge>
        <span className="font-mono text-xs break-all">{result.file_id}</span>
      </div>
      <KvList className="mt-2">
        {result.detail && (
          <KvRow label={t('claudeFiles.detail')}>{result.detail}</KvRow>
        )}
        {result.confirmation_id && (
          <KvRow label={t('claudeFiles.confirmationId')} mono>
            {result.confirmation_id}
          </KvRow>
        )}
        {result.approval_ref && (
          <KvRow label={t('claudeFiles.approvalRef')} mono>
            {result.approval_ref}
          </KvRow>
        )}
        {result.holds && result.holds.length > 0 && (
          <KvRow label={t('claudeFiles.holds')} align="start">
            <ul className="flex flex-col gap-0.5 text-xs">
              {result.holds.map((h, i) => (
                <li key={h.id ?? i}>{h.name || h.id || h.reason || '—'}</li>
              ))}
            </ul>
          </KvRow>
        )}
      </KvList>
    </div>
  )
}
