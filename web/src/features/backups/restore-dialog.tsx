// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Multi-step restore dialog: (1) upload a .drbundle file, (2) review the manifest
// preview, (3) enter the decryption passphrase, (4) confirm with typed "RESTORE".
// Each step gates on the previous; the final apply is wrapped in ConfirmDialog with
// confirmPhrase="RESTORE" because restore is an estate-level destructive operation.
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { FileUp, ShieldAlert, UserCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import {
  useFailedActionReporter,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { formatDateTime } from '@/lib/format'
import { drApi, drKeys } from './api'
import { JobProgress } from './job-progress'
import type { Manifest, RestoreUploadResponse } from './types'

export interface RestoreDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function RestoreDialog({ open, onOpenChange }: RestoreDialogProps) {
  // El reporte vive en un solo sitio (use-privileged-mutation.ts:25-32): esta rama conserva
  // su manejo para lo suyo y DELEGA el reporte.
  const report = useFailedActionReporter()
  const { t } = useTranslation('backups')
  const [file, setFile] = useState<File | null>(null)
  const [upload, setUpload] = useState<RestoreUploadResponse | null>(null)
  const [passphrase, setPassphrase] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const [awaiting, setAwaiting] = useState<{
    requestId: string
    initiator: string
  } | null>(null)

  const uploadMutation = useMutation({
    mutationFn: (f: File) => drApi.uploadRestore(f),
    onSuccess: (res) => setUpload(res),
    onError: (err) => {
      // ⛔ ASEGURAMIENTO ANTES QUE ROL. `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que esta rama
      // acusaba al operador de un permiso que SÍ tiene y le escondía la salida.
      //
      // DEFENSA EN PROFUNDIDAD: los emisores medidos son cuatro familias —21 `requireAAL3` en
      // `core/api`, dos escrituras en `modules/governance`, el `requireStepUp` propio de
      // `modules/deploy` y los retornos de `core/auth/webauthn.go`— y esta ruta no está en ninguna
      // hoy. Se arregla porque el defecto es de FORMA y sobrevive al día en que el gate llegue.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(err)
        return
      }
      if (err instanceof ApiError && err.isForbidden) {
        toast.warning(t('restore.notAuthorized'))
        return
      }
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(
        t('restore.uploadFailed'),
        description ? { description } : undefined,
      )
    },
  })

  const applyMutation = usePrivilegedMutation({
    mutationFn: () =>
      drApi.applyRestore(upload!.upload_id, {
        passphrase: passphrase.trim(),
      }),
    invalidateKeys: [drKeys.backups(), drKeys.jobs(), drKeys.pending()],
    successMessage: t('restore.started'),
    onDone: (data) => {
      setConfirmOpen(false)
      // Dual-control: the restore is queued for a second approver, not started.
      if (data.awaiting_approval && data.request_id) {
        setAwaiting({
          requestId: data.request_id,
          initiator: data.initiator ?? '',
        })
        return
      }
      if (data.job_id) setJobId(data.job_id)
    },
  })

  const reset = () => {
    setFile(null)
    setUpload(null)
    setPassphrase('')
    setConfirmOpen(false)
    setJobId(null)
    setAwaiting(null)
  }

  const close = () => {
    onOpenChange(false)
    reset()
  }

  return (
    <>
      <Dialog open={open} onOpenChange={(o) => !o && close()}>
        <DialogContent className="max-h-[calc(100vh-2rem)] max-w-lg overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t('restore.title')}</DialogTitle>
            <DialogDescription>{t('restore.description')}</DialogDescription>
          </DialogHeader>

          {jobId ? (
            <JobProgress jobId={jobId} onFinished={() => {}} />
          ) : awaiting ? (
            <AwaitingApprovalStep requestId={awaiting.requestId} />
          ) : !upload ? (
            <UploadStep
              file={file}
              onFileChange={setFile}
              isPending={uploadMutation.isPending}
            />
          ) : (
            <ManifestStep
              upload={upload}
              passphrase={passphrase}
              onPassphraseChange={setPassphrase}
            />
          )}

          <DialogFooter>
            <Button
              variant="secondary"
              onClick={close}
              disabled={uploadMutation.isPending}
            >
              {jobId || awaiting ? t('actions.close') : t('actions.cancel')}
            </Button>
            {!jobId && !awaiting && !upload && (
              <Button
                variant="primary"
                onClick={() => file && uploadMutation.mutate(file)}
                disabled={uploadMutation.isPending || !file}
              >
                {uploadMutation.isPending && <Spinner size="sm" aria-hidden />}
                {t('actions.upload')}
              </Button>
            )}
            {!jobId && !awaiting && upload && (
              <Button
                variant="primary"
                onClick={() => setConfirmOpen(true)}
                disabled={!passphrase.trim()}
              >
                <ShieldAlert className="size-4" />
                {t('restore.apply')}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* The final confirmation gate — typed "RESTORE" required. */}
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('restore.confirmTitle')}
        description={t('restore.confirmDescription')}
        tone="danger"
        confirmPhrase={t('restore.confirmPhrase')}
        confirmLabel={t('restore.confirmLabel')}
        pending={applyMutation.isPending}
        onConfirm={() => applyMutation.mutate()}
      >
        {upload && (
          <div className="text-sm">
            {t('restore.confirmSummaryPrefix')}{' '}
            <span className="font-mono">{upload.filename}</span>{' '}
            {t('restore.confirmSummarySuffix', {
              count: upload.manifest.tenants.length,
            })}
          </div>
        )}
      </ConfirmDialog>
    </>
  )
}

/** Shown when dual-control is enabled: the restore is queued for a second approver. */
function AwaitingApprovalStep({ requestId }: { requestId: string }) {
  const { t } = useTranslation('backups')
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-amber-500/40 bg-amber-500/5 p-6 text-center">
      <UserCheck className="size-8 text-amber-500" />
      <p className="text-sm font-medium">{t('restore.awaitingTitle')}</p>
      <p className="text-sm text-muted-foreground">
        {t('restore.awaitingDescription')}
      </p>
      <p className="text-xs text-muted-foreground">
        {t('restore.requestId')}: <span className="font-mono">{requestId}</span>
      </p>
    </div>
  )
}

/** Step 1: file picker for the .drbundle. */
function UploadStep({
  file,
  onFileChange,
  isPending,
}: {
  file: File | null
  onFileChange: (f: File | null) => void
  isPending: boolean
}) {
  const { t } = useTranslation('backups')
  return (
    <div className="flex flex-col items-center gap-4 rounded-lg border border-dashed border-border p-6">
      <FileUp className="size-8 text-muted-foreground" />
      <p className="text-sm text-muted-foreground">
        {file ? file.name : t('restore.selectFile')}
      </p>
      <input
        type="file"
        accept=".drbundle"
        className="hidden"
        id="restore-file-input"
        onChange={(e) => onFileChange(e.target.files?.[0] ?? null)}
        disabled={isPending}
      />
      <Button
        variant="secondary"
        size="sm"
        onClick={() => document.getElementById('restore-file-input')?.click()}
        disabled={isPending}
      >
        {t('restore.chooseFile')}
      </Button>
    </div>
  )
}

/** Step 2: manifest preview + passphrase input. */
function ManifestStep({
  upload,
  passphrase,
  onPassphraseChange,
}: {
  upload: RestoreUploadResponse
  passphrase: string
  onPassphraseChange: (v: string) => void
}) {
  const { t } = useTranslation('backups')
  const { manifest } = upload

  return (
    <div className="flex flex-col gap-4">
      <ManifestPreview manifest={manifest} filename={upload.filename} />

      <Field
        label={t('restore.passphrase')}
        htmlFor="restore-passphrase"
        description={t('restore.passphraseDescription')}
      >
        <Input
          id="restore-passphrase"
          type="password"
          value={passphrase}
          onChange={(e) => onPassphraseChange(e.target.value)}
          autoComplete="off"
        />
      </Field>
    </div>
  )
}

/** Read-only manifest preview card. */
function ManifestPreview({
  manifest,
  filename,
}: {
  manifest: Manifest
  filename: string
}) {
  const { t } = useTranslation('backups')
  return (
    <div className="rounded-lg border border-border bg-muted/50 p-4">
      <h3 className="mb-3 text-sm font-medium">{t('restore.manifestTitle')}</h3>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
        <dt className="text-muted-foreground">{t('restore.manifest.file')}</dt>
        <dd className="font-mono text-xs">{filename}</dd>

        <dt className="text-muted-foreground">
          {t('restore.manifest.engine')}
        </dt>
        <dd>
          <Badge variant="outline">{manifest.engine}</Badge>
        </dd>

        <dt className="text-muted-foreground">
          {t('restore.manifest.created')}
        </dt>
        <dd>{formatDateTime(manifest.created_at)}</dd>

        <dt className="text-muted-foreground">
          {t('restore.manifest.tenants')}
        </dt>
        <dd>{manifest.tenants.length}</dd>

        {manifest.tenants.length > 0 && (
          <>
            <dt className="text-muted-foreground" />
            <dd className="flex flex-wrap gap-1">
              {manifest.tenants.map((tenant) => (
                <Badge
                  key={tenant.tenant}
                  variant="outline"
                  className="text-xs"
                >
                  {tenant.tenant}
                </Badge>
              ))}
            </dd>
          </>
        )}

        <dt className="text-muted-foreground">{t('restore.manifest.keys')}</dt>
        <dd>{manifest.keys.length}</dd>
      </dl>
    </div>
  )
}
