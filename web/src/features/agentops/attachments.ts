// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE IMAGES A PERSON ATTACHES TO A MESSAGE (#2158), from paste, drop or the picker to
// the ids the message is sent with.
//
// Each image is stored with the session the moment it is attached, not when the message
// is sent: whatever the engine refuses (a type, a size, a tool that takes no images) is
// said then, before anything is sent, and Send only ever carries images the engine holds.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { agentOpsApi } from './api'
import './i18n'

/** The image types the engine takes (run_attachment.go, attachmentExt). */
export const ATTACH_TYPES = [
  'image/png',
  'image/jpeg',
  'image/webp',
  'image/gif',
] as const

/** The picker's accept list. */
export const ATTACH_ACCEPT = ATTACH_TYPES.join(',')

/** The most images one message carries (maxTurnAttachments). */
export const MAX_ATTACHMENTS = 4

/** The largest image the tools take (maxAttachmentBytes). The engine decides; this only
 * spares a person the upload of a file it will refuse. */
export const MAX_ATTACHMENT_BYTES = 3_932_160

export interface DraftImage {
  /** Local, for the list. */
  key: string
  name: string
  /** The file itself as a data URL, for the thumbnail. */
  preview: string
  /** The engine's id once it holds the image; absent while it uploads. */
  id?: string
}

/** The images in a clipboard or drop payload, in order. A file that is not an image is
 * not an attachment and is left to the browser (a pasted file path stays text). */
export function imagesIn(data: DataTransfer | null): File[] {
  return Array.from(data?.files ?? []).filter((f) =>
    f.type.startsWith('image/'),
  )
}

/** The data URL of a file or blob: the image itself as an address, with no lifetime to manage. */
export function dataUrlOf(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error ?? new Error('read failed'))
    reader.onload = () => resolve(String(reader.result))
    reader.readAsDataURL(file)
  })
}

/**
 * The draft's images for one session. Switching session drops them (their ids belong
 * to the session that stored them: the composer is a new instance per session, or its
 * run changes); an upload that finishes after the switch is said to have not been added.
 */
export function useImageDraft(runRef: string) {
  const { t } = useTranslation('agentops')
  const [owned, setOwned] = useState<{ runRef: string; images: DraftImage[] }>({
    runRef,
    images: [],
  })
  const images = owned.runRef === runRef ? owned.images : []
  // Uploads in flight, by session: one session's wait is not another's. `add` reads the
  // count the moment it runs (state is a render behind); the screen reads the state.
  const inflight = useRef(new Map<string, number>())
  const [waiting, setWaiting] = useState<Record<string, number>>({})
  const uploading = (waiting[runRef] ?? 0) > 0
  const track = useCallback((run: string, delta: number) => {
    const n = (inflight.current.get(run) ?? 0) + delta
    inflight.current.set(run, n)
    setWaiting((w) => ({ ...w, [run]: n }))
  }, [])
  const latest = useRef(images)
  const currentRun = useRef(runRef)
  const counter = useRef(0)
  useEffect(() => {
    latest.current = images
    currentRun.current = runRef
  })
  // The composer is a new instance per session: an upload that outlives this one has
  // nowhere to be added.
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  const update = useCallback(
    (fn: (list: DraftImage[]) => DraftImage[]) =>
      setOwned((prev) => ({
        runRef,
        images: fn(prev.runRef === runRef ? prev.images : []),
      })),
    [runRef],
  )

  const add = useCallback(
    (files: File[]) => {
      for (const file of files) {
        const name = file.name || t('attach.pasted')
        const refuse = (reason: string) =>
          toast.error(t('attach.failed', { name, reason }))
        if (!(ATTACH_TYPES as readonly string[]).includes(file.type)) {
          refuse(t('attach.notImage'))
          continue
        }
        if (file.size > MAX_ATTACHMENT_BYTES) {
          refuse(
            t('attach.tooLarge', {
              size: (file.size / 2 ** 20).toFixed(1),
              limit: (MAX_ATTACHMENT_BYTES / 2 ** 20).toFixed(1),
            }),
          )
          continue
        }
        const inFlight = inflight.current.get(runRef) ?? 0
        if (latest.current.length + inFlight >= MAX_ATTACHMENTS) {
          refuse(t('attach.tooMany', { count: MAX_ATTACHMENTS }))
          continue
        }
        const key = `${runRef}:${counter.current++}`
        track(runRef, 1)
        void (async () => {
          try {
            // One read gives both the thumbnail address and the bytes to upload.
            const preview = await dataUrlOf(file)
            const stored = await agentOpsApi.uploadAttachment(runRef, {
              media_type: file.type,
              data: preview.slice(preview.indexOf(',') + 1),
            })
            if (!mounted.current || currentRun.current !== runRef) {
              // The person moved to another session meanwhile: this image belongs to
              // the first, and is not added to the second's message.
              refuse(t('attach.movedOn'))
              return
            }
            update((list) => [...list, { key, name, preview, id: stored.id }])
          } catch (error) {
            refuse(
              error instanceof ApiError ? error.message : t('attach.notStored'),
            )
          } finally {
            track(runRef, -1)
          }
        })()
      }
    },
    [runRef, t, track, update],
  )

  const remove = useCallback(
    (key: string) => update((list) => list.filter((i) => i.key !== key)),
    [update],
  )

  /** After a send: the images that went with the message leave the draft. One attached
   * while the message was being sent is a part of the next message and stays. */
  const discard = useCallback(
    (ids: readonly string[]) =>
      update((list) => list.filter((i) => !(i.id && ids.includes(i.id)))),
    [update],
  )

  return {
    images,
    ids: images.flatMap((i) => (i.id ? [i.id] : [])),
    uploading,
    add,
    remove,
    discard,
  }
}
