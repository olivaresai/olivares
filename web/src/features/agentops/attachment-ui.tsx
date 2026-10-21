// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The two faces of an attached image: the thumbnails under the field before a message is
// sent (with a way to take one off), and the thumbnails inside a sent message (each opens
// full size).
import { useQuery } from '@tanstack/react-query'
import { Paperclip, X } from 'lucide-react'
import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { useAuth } from '@/lib/auth/context'
import { agentOpsApi, agentOpsKeys } from './api'
import { ATTACH_ACCEPT, dataUrlOf, type DraftImage } from './attachments'
import './i18n'

/** The picker button. The file input is the platform's own; the button only opens it. */
export function AttachButton({
  disabled,
  onFiles,
}: {
  disabled?: boolean
  onFiles: (files: File[]) => void
}) {
  const { t } = useTranslation('agentops')
  const input = useRef<HTMLInputElement>(null)
  return (
    <>
      <input
        ref={input}
        type="file"
        multiple
        accept={ATTACH_ACCEPT}
        className="sr-only"
        tabIndex={-1}
        aria-hidden
        data-testid="attach-input"
        onChange={(e) => {
          onFiles(Array.from(e.target.files ?? []))
          // The same file can be chosen again.
          e.target.value = ''
        }}
      />
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="rounded-full"
        disabled={disabled}
        aria-label={t('attach.add')}
        title={t('attach.add')}
        data-testid="attach-button"
        onClick={() => input.current?.click()}
      >
        <Paperclip />
      </Button>
    </>
  )
}

/** The images of the message being written. */
export function AttachmentTray({
  images,
  uploading,
  onRemove,
}: {
  images: readonly DraftImage[]
  uploading: boolean
  onRemove: (key: string) => void
}) {
  const { t } = useTranslation('agentops')
  if (images.length === 0 && !uploading) return null
  return (
    <ul
      aria-label={t('attach.tray')}
      data-testid="attachment-tray"
      className="flex flex-wrap items-center gap-2 px-4 pt-3"
    >
      {images.map((image) => (
        <li key={image.key} className="relative">
          <img
            src={image.preview}
            alt={image.name}
            className="size-14 rounded-md border border-line object-cover"
          />
          <button
            type="button"
            aria-label={t('attach.remove', { name: image.name })}
            onClick={() => onRemove(image.key)}
            className="absolute -top-1.5 -right-1.5 grid size-5 place-items-center rounded-full border border-line bg-elevated text-text-2 outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
          >
            <X className="size-3" />
          </button>
        </li>
      ))}
      {uploading ? (
        <li role="status" className="text-caption text-text-3">
          {t('attach.uploading')}
        </li>
      ) : null}
    </ul>
  )
}

/** The images of a sent message, read from the engine. They are the engine's copy: after a
 * reload the message shows the same thumbnails, whatever the tool echoed. */
export function MessageImages({
  runRef,
  attachments,
}: {
  runRef: string
  attachments: readonly { id: string; mediaType: string }[]
}) {
  const { t } = useTranslation('agentops')
  return (
    <ul
      aria-label={t('attach.sent')}
      data-testid="message-images"
      className="mt-2 flex flex-wrap gap-2"
    >
      {attachments.map((attachment, i) => (
        <li key={attachment.id}>
          <MessageImage runRef={runRef} id={attachment.id} n={i + 1} />
        </li>
      ))}
    </ul>
  )
}

function MessageImage({
  runRef,
  id,
  n,
}: {
  runRef: string
  id: string
  n: number
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: agentOpsKeys.runAttachment(activeTenant, runRef, id),
    // The picture as a data URL: nothing to create or release, so a re-render or a
    // remount never leaves an address that was already freed.
    queryFn: async ({ signal }) =>
      dataUrlOf(await agentOpsApi.attachmentImage(runRef, id, { signal })),
    // The engine never changes a stored image.
    staleTime: Infinity,
    retry: false,
  })
  const url = query.data ?? null
  const label = t('attach.imageN', { n })
  if (query.isError)
    return (
      <span role="img" aria-label={label} className="text-caption text-text-3">
        {t('attach.unavailable')}
      </span>
    )
  if (!url)
    return <span className="block size-24 rounded-md bg-muted" aria-hidden />
  return (
    <Dialog>
      <DialogTrigger asChild>
        <button
          type="button"
          aria-label={t('attach.open', { name: label })}
          className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-focus"
        >
          <img
            src={url}
            alt={label}
            className="size-24 rounded-md border border-line object-cover"
          />
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-[min(90vw,64rem)]">
        <DialogTitle className="sr-only">{label}</DialogTitle>
        <DialogDescription className="sr-only">
          {t('attach.fullSize')}
        </DialogDescription>
        <img src={url} alt={label} className="max-h-[80vh] w-auto" />
      </DialogContent>
    </Dialog>
  )
}
