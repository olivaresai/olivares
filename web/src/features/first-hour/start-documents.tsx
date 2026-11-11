// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE DOCUMENTS A PERSON ATTACHES TO NEW WORK. They stay in the form until Start, which
// saves each one in the session's folder (the folder upload, one write each) before the
// session starts; its first message names the path each one got there.
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Paperclip, X } from 'lucide-react'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { PathText } from '@/components/ui/path-text'
import { toast } from '@/components/ui/toaster'
import type { LaunchAuthority } from '@/features/agentops/session-launch'
import type { WorkspaceDTO } from '@/features/agentops/types'
import {
  MAX_UPLOAD_BYTES,
  labelsToast,
  uploadOne,
} from '@/features/agentops/workspace-upload'
import { formatBytes } from '@/lib/format'
import i18n from '@/lib/i18n'
import './i18n'

export interface StartDocument {
  file: File
  /** The path the engine wrote it to, by the folder it was saved in. */
  saved?: Record<string, string>
}

/** The most one document may weigh in `folder`: its own limit for a file (the engine
 * refuses a write past it), never past what one write takes, which is also the limit
 * while the folder is not known. */
export function documentLimit(
  folder?: Pick<WorkspaceDTO, 'max_read_bytes'>,
): number {
  return Math.min(folder?.max_read_bytes || MAX_UPLOAD_BYTES, MAX_UPLOAD_BYTES)
}

export function StartDocuments({
  documents,
  onChange,
  saving,
  maxBytes,
  refusal,
}: {
  documents: StartDocument[]
  onChange: (next: StartDocument[]) => void
  /** The document being saved while Start saves them, else ''. */
  saving: string
  /** The most one document may weigh in the chosen folder (documentLimit). */
  maxBytes: number
  /** Why the chosen place takes no documents, else '': the control is not offered there,
   * and documents attached before it was chosen wait. */
  refusal: string
}) {
  const { t } = useTranslation('firstHour')
  const input = useRef<HTMLInputElement>(null)
  const [refused, setRefused] = useState<string[]>([])
  const [twice, setTwice] = useState<string[]>([])
  if (refusal && documents.length === 0)
    return <p className="text-caption text-text-3">{refusal}</p>
  const add = (files: File[]) => {
    // A file past the folder's limit is refused here, before anything is sent. A name
    // already attached would land on the same path: it is attached once, and said.
    const big = files.filter((f) => f.size > maxBytes)
    const again = files.filter((f) =>
      documents.some((d) => d.file.name === f.name),
    )
    setRefused(
      big.map((f) =>
        t('start.documentTooLarge', {
          name: f.name,
          size: formatBytes(maxBytes),
        }),
      ),
    )
    setTwice(
      again.map((f) => t('start.documentAttachedTwice', { name: f.name })),
    )
    const kept = files.filter((f) => !big.includes(f) && !again.includes(f))
    onChange([...documents, ...kept.map((file) => ({ file }))])
  }
  return (
    <fieldset className="flex flex-col gap-1.5">
      <legend className="text-body font-medium text-foreground">
        {t('start.documents')}
      </legend>
      {documents.length > 0 ? (
        <BoxedList role="list" aria-label={t('start.documents')}>
          {documents.map(({ file }) => (
            <BoxedListRow key={file.name} role="listitem">
              {/* The path it gets in the folder, shown and never used to build one
                  here: cut in its middle, so its extension stays. */}
              <PathText value={file.name} copy={false} className="flex-1" />
              <span className="shrink-0 text-caption text-text-3">
                {refusal
                  ? t('start.documentWaiting', { size: formatBytes(file.size) })
                  : formatBytes(file.size)}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={t('start.removeDocument', { name: file.name })}
                onClick={() =>
                  onChange(documents.filter((d) => d.file !== file))
                }
              >
                <X aria-hidden />
              </Button>
            </BoxedListRow>
          ))}
        </BoxedList>
      ) : null}
      {documents.length > 0 && !refusal ? (
        // What Start adds to the person's first message, shown before it is sent.
        <p className="text-caption text-text-3">
          {t('start.documentsAdded')}{' '}
          <span className="whitespace-pre-line text-text-2">
            {documentsMessage(
              '',
              documents.map((d) => d.file.name),
            )}
          </span>
        </p>
      ) : null}
      {refused.length > 0 ? (
        <p role="alert" className="text-caption text-danger">
          {refused.join(' ')}
        </p>
      ) : null}
      {twice.length > 0 ? (
        <p role="status" className="text-caption text-text-2">
          {twice.join(' ')}
        </p>
      ) : null}
      {saving ? (
        <p role="status" className="text-caption text-text-2">
          {saving}
        </p>
      ) : null}
      {refusal ? (
        <p className="text-caption text-text-3">{refusal}</p>
      ) : (
        <>
          <div>
            <Button
              type="button"
              variant="secondary"
              onClick={() => input.current?.click()}
            >
              <Paperclip aria-hidden />
              {t('start.attachDocument')}
            </Button>
          </div>
          <input
            ref={input}
            type="file"
            multiple
            hidden
            data-testid="attach-documents-input"
            onChange={(e) => {
              add(Array.from(e.target.files ?? []))
              e.target.value = ''
            }}
          />
        </>
      )}
    </fieldset>
  )
}

/** Saves each document not yet in folder `wref`, in order, under the start's
 * `authority`, telling `onSaving` which one ('' once done) and `onSaved` where it
 * landed, and returns every path. A start abandoned since (the dialog closed, another
 * sign-in or organization) sends nothing more. The first that cannot be saved stops the
 * rest with the sentence that says which, why, and which are already in the folder:
 * those keep their path, so the next Start sends only what is left. */
export async function saveDocuments({
  documents,
  wref,
  maxBytes,
  authority,
  onSaving,
  onSaved,
}: {
  documents: StartDocument[]
  wref: string
  maxBytes: number
  authority: LaunchAuthority
  onSaving: (text: string) => void
  onSaved: (file: File, path: string) => void
}): Promise<string[]> {
  const paths: string[] = []
  const labels = new Set<string>()
  try {
    for (const [i, { file, saved }] of documents.entries()) {
      const kept = saved?.[wref]
      if (kept) {
        paths.push(kept)
        continue
      }
      authority.dispatchGuard()
      onSaving(
        i18n.t('firstHour:start.savingDocument', {
          name: file.name,
          n: i + 1,
          total: documents.length,
        }),
      )
      const done = await uploadOne(
        wref,
        '',
        file,
        maxBytes,
        (key, opts) => i18n.t(`agentops:${key}`, opts),
        authority,
      ).catch((err: unknown) => ({
        refused: i18n.t('firstHour:start.documentFailed', {
          name: file.name,
          reason: err instanceof Error ? err.message : String(err),
        }),
      }))
      if ('refused' in done) throw new Error(done.refused)
      for (const hit of done.written.sensitivity ?? []) labels.add(hit.class)
      onSaved(file, done.written.path)
      paths.push(done.written.path)
    }
  } catch (err) {
    if (paths.length === 0) throw err
    const kept = i18n.t('firstHour:start.documentsKept', {
      paths: paths.join(', '),
    })
    const reason = err instanceof Error ? err.message : String(err)
    throw new Error(`${reason} ${kept}`)
  } finally {
    onSaving('')
  }
  const labeled = labelsToast([...labels], (key, opts) =>
    i18n.t(`agentops:${key}`, opts),
  )
  if (labeled) toast.warning(labeled)
  return paths
}

/** The first message: what the person wrote, then the documents by their path in the
 * folder, so the session knows where to read them. */
export function documentsMessage(prompt: string, paths: string[]): string {
  if (paths.length === 0) return prompt
  const list = [
    i18n.t('firstHour:start.documentsMessage'),
    ...paths.map((p) => `- ${p}`),
  ].join('\n')
  return prompt.trim() ? `${prompt}\n\n${list}` : list
}
