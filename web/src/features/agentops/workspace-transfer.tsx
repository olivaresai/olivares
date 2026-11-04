// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { Download, Upload } from 'lucide-react'
import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { DisabledReason } from '@/components/ui/disabled-reason'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { downloadBlob } from '@/lib/api/download'
import { ApiError } from '@/lib/api/errors'
import { formatBytes } from '@/lib/format'
import { agentOpsApi } from './api'
import './i18n'
import { labelsToast } from './workspace-upload'

/** The Upload button and, always visible, the size limit or the reason it is off. */
export function UploadButton({
  refusal,
  maxBytes,
  pending,
  onFiles,
}: {
  refusal: string | null
  maxBytes: number
  pending: boolean
  onFiles: (files: File[]) => void
}) {
  const { t } = useTranslation('agentops')
  const input = useRef<HTMLInputElement>(null)
  return (
    <>
      <DisabledReason
        disabled={refusal !== null}
        reason={refusal ?? t('browser.uploadReadOnly')}
        layout="block"
        className="basis-full"
      >
        <Button
          variant="ghost"
          size="sm"
          disabled={pending}
          onClick={() => input.current?.click()}
        >
          {pending ? (
            <Spinner className="size-3.5" />
          ) : (
            <Upload className="size-3.5" />
          )}
          {t('browser.upload')}
        </Button>
      </DisabledReason>
      {refusal === null && (
        <span className="text-caption text-muted-foreground">
          {t('browser.uploadLimit', { size: formatBytes(maxBytes) })}
        </span>
      )}
      <input
        ref={input}
        type="file"
        multiple
        hidden
        data-testid="upload-input"
        onChange={(e) => {
          onFiles(Array.from(e.target.files ?? []))
          e.target.value = ''
        }}
      />
    </>
  )
}

/** Downloads the named entries, folders included, as one zip the engine builds. */
export function ArchiveButton({
  wref,
  paths,
  label,
}: {
  wref: string
  paths: string[]
  label: string
}) {
  const { t } = useTranslation('agentops')
  const archive = useMutation({
    mutationFn: () => agentOpsApi.archiveFiles(wref, paths),
    onSuccess: ({ blob, filename, labels }) => {
      downloadBlob(blob, filename)
      const labeled = labelsToast(labels, t)
      if (labeled) toast.warning(labeled)
    },
    onError: (err) =>
      toast.error(
        err instanceof ApiError ? err.message : t('browser.downloadFailed'),
      ),
  })
  return (
    <Button
      variant="ghost"
      size="sm"
      disabled={archive.isPending}
      onClick={() => archive.mutate()}
    >
      {archive.isPending ? (
        <Spinner className="size-3.5" />
      ) : (
        <Download className="size-3.5" />
      )}
      {label}
    </Button>
  )
}
