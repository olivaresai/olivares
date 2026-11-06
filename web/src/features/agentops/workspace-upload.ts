// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { formatBytes } from '@/lib/format'
import { agentOpsApi } from './api'
import './i18n'

/** Why the folder takes no upload, or null when it does: the mount mode the engine enforces
 * (a read-only folder answers 403 to every write) and the caller's own write permission. */
export function uploadRefusal(
  mountMode: string,
  mayWrite: boolean,
  t: (key: string) => string,
): string | null {
  if (mountMode === 'ro') return t('browser.uploadReadOnly')
  if (!mayWrite) return t('browser.uploadNoAccess')
  return null
}

/** The most one write takes (the engine answers 413 past it, whatever the folder registered). */
export const MAX_UPLOAD_BYTES = 64 * 1024 * 1024

/** The toast line for DLP classes the engine found, or null when it found none. */
export function labelsToast(
  classes: string[],
  t: (key: string, opts: { classes: string }) => string,
): string | null {
  return classes.length > 0
    ? t('browser.labeled', { classes: classes.join(', ') })
    : null
}

async function exists(wref: string, path: string): Promise<boolean> {
  try {
    await agentOpsApi.statFile(wref, path)
    return true
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return false
    throw err
  }
}

/** Uploads files into `dir`, one request each. The engine overwrites on a write, and a drag
 * and drop is too easy to aim at the wrong name, so an existing entry is left alone and
 * named in the answer instead of replaced. */
export function useWorkspaceUpload({
  wref,
  dir,
  maxBytes,
  refusal,
  onDone,
}: {
  wref: string
  dir: string
  maxBytes: number
  refusal: string | null
  onDone: () => void
}) {
  const { t } = useTranslation('agentops')
  const mutation = useMutation({
    mutationFn: async (files: File[]) => {
      const sent: string[] = []
      const left: string[] = []
      const labels = new Set<string>()
      for (const file of files) {
        const target = dir ? `${dir}/${file.name}` : file.name
        if (file.size > maxBytes) {
          left.push(
            t('browser.uploadTooLarge', {
              name: file.name,
              size: formatBytes(maxBytes),
            }),
          )
        } else if (await exists(wref, target)) {
          left.push(t('browser.uploadExists', { name: file.name }))
        } else {
          const done = await agentOpsApi.writeFile(wref, target, file)
          for (const hit of done.sensitivity ?? []) labels.add(hit.class)
          sent.push(file.name)
        }
      }
      return { sent, left, labels: [...labels] }
    },
    onSuccess: ({ sent, left, labels }) => {
      if (sent.length > 0)
        toast.success(t('browser.uploaded', { n: sent.length }))
      const labeled = labelsToast(labels, t)
      if (labeled) toast.warning(labeled)
      if (left.length > 0) toast.error(left.join(' '))
    },
    onError: (err) =>
      toast.error(
        err instanceof ApiError ? err.message : t('browser.uploadFailed'),
      ),
    // A refused file can follow files that were written: the list shows what landed.
    onSettled: onDone,
  })
  const { mutate } = mutation
  const upload = (files: File[]) => {
    if (refusal) toast.error(refusal)
    else if (files.length > 0) mutate(files)
  }
  return { upload, pending: mutation.isPending }
}
