// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NOTES ON A MARKDOWN PASSAGE OR A PREVIEW ELEMENT, SENT TO A SESSION. The person selects
// a passage of the rendered file (or picks an element of the session's preview), writes a
// note on it, collects as many as they like, and sends them as one turn to a live session,
// through the fence every conversation surface uses (sendTurn). Nothing is stored: the
// notes are the turn.
import { useMutation, useQuery } from '@tanstack/react-query'
import { MessageSquarePlus, Send } from 'lucide-react'
import { useId, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { isLiveRun } from '@/features/sessions/provenance'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { useTenantStore } from '@/stores/tenant'
import { agentOpsApi, agentOpsKeys } from './api'
import { toolName } from './engine-tools'
import { sendTurn } from './session-turn'
import { ToolMark } from './tool-mark'
import type { RunDTO, WorkspaceDTO } from './types'
import './i18n'

export interface PassageNote {
  passage: string
  note: string
}

const PASSAGE_MAX = 400

/** The turn the notes make: the file, then each passage quoted with its note under it. */
export function passageNotesText(
  path: string,
  notes: readonly PassageNote[],
): string {
  const quote = (passage: string) =>
    (passage.length > PASSAGE_MAX
      ? `${passage.slice(0, PASSAGE_MAX)}…`
      : passage
    )
      .split('\n')
      .map((line) => `> ${line}`)
      .join('\n')
  return [
    `Notes on ${path}:`,
    ...notes.map((n) => `${quote(n.passage)}\n\n${n.note.trim()}`),
  ].join('\n\n')
}

export function PassageNotes({
  workspace,
  path,
  container,
  to,
  picked,
  onPicked,
}: {
  /** A file's notes go to a live session of its workspace, the one the person chooses. */
  workspace?: WorkspaceDTO
  /** What the notes are on: the file's path, or the preview page's address. */
  path: string
  /** The rendered file: "Add a note" takes a passage selected inside it. */
  container?: RefObject<HTMLElement | null>
  /** A preview's notes go to the session it belongs to. */
  to?: RunDTO
  /** The element the person picked in the preview page; it opens the note form. */
  picked?: string | null
  /** The picked element was kept or dropped. */
  onPicked?: () => void
}) {
  const { t } = useTranslation('agentops')
  const { can } = useAuth()
  const allowed = can('sessions:run:write')
  const tenant = useTenantStore((s) => s.activeTenant)
  const [notes, setNotes] = useState<PassageNote[]>([])
  const [passage, setPassage] = useState<string | null>(null)
  const [note, setNote] = useState('')
  const [hint, setHint] = useState(false)
  const [target, setTarget] = useState('')
  const noteId = useId()
  const targetId = useId()
  const runs = useQuery({
    queryKey: agentOpsKeys.runs(tenant),
    queryFn: ({ signal }) =>
      agentOpsApi.listRuns(undefined, { tenant, signal }),
    enabled: allowed && !to,
  })
  const live = to
    ? [to]
    : (runs.data?.items ?? []).filter(
        (r) => r.workspace_ref === workspace?.workspace_ref && isLiveRun(r),
      )
  const chosen = live.find((r) => r.run_ref === target) ?? live[0]
  const send = useMutation({
    mutationFn: () => sendTurn(chosen!, passageNotesText(path, notes)),
    // Cleared only once the session took them: a refusal leaves them to send again.
    onSuccess: () => {
      setNotes([])
      toast.success(t('browser.notes.sent'))
    },
    onError: (err) =>
      toast.error(
        err instanceof ApiError ? err.message : t('browser.notes.failed'),
      ),
  })
  // The passage being noted: one selected in the file, or the element picked in a preview.
  const draft = passage ?? picked ?? null
  // A preview has nothing to show until an element is picked or a note is kept.
  if (!allowed || (!container && draft === null && notes.length === 0))
    return null

  // Read on press, before the press can move the selection.
  const take = () => {
    const selection = window.getSelection()
    const text = selection?.toString().trim() ?? ''
    const inside =
      !!selection?.anchorNode &&
      !!container?.current?.contains(selection.anchorNode)
    setHint(!(text && inside))
    if (text && inside) setPassage(text)
  }
  const closeDraft = () => {
    setPassage(null)
    setNote('')
    onPicked?.()
  }

  return (
    <section
      aria-label={t('browser.notes.title')}
      className="mt-3 flex flex-col gap-2 rounded-md border border-border p-3"
    >
      {container ? (
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onMouseDown={(e) => {
              e.preventDefault()
              take()
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') take()
            }}
          >
            <MessageSquarePlus aria-hidden className="size-4" />
            {t('browser.notes.add')}
          </Button>
          {notes.length > 0 ? (
            <span className="text-caption text-text-2">
              {t('browser.notes.count', { count: notes.length })}
            </span>
          ) : null}
        </div>
      ) : null}
      {hint ? (
        <p className="text-caption text-text-3">{t('browser.notes.select')}</p>
      ) : null}
      {draft !== null ? (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (!note.trim()) return
            setNotes((all) => [...all, { passage: draft, note }])
            closeDraft()
          }}
        >
          <blockquote className="border-s-2 border-line ps-2 text-caption text-text-2">
            {draft}
          </blockquote>
          <label htmlFor={noteId} className="text-caption text-text-2">
            {t('browser.notes.note')}
          </label>
          <Textarea
            id={noteId}
            rows={2}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          <div className="flex gap-2">
            <Button type="submit" size="sm" disabled={!note.trim()}>
              {t('browser.notes.keep')}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={closeDraft}
            >
              {t('browser.notes.cancel')}
            </Button>
          </div>
        </form>
      ) : null}
      {notes.length > 0 ? (
        <>
          <ol className="flex list-decimal flex-col gap-1 ps-5 text-caption text-text">
            {notes.map((n, i) => (
              <li key={i}>
                <span className="text-text-3">
                  “
                  {n.passage.length > 60
                    ? `${n.passage.slice(0, 60)}…`
                    : n.passage}
                  ”
                </span>{' '}
                {n.note}
              </li>
            ))}
          </ol>
          {chosen ? (
            <div className="flex flex-wrap items-end gap-2">
              {/* The chosen agent by its mark, as everywhere the console names a tool. */}
              <ToolMark
                driver={chosen.provider_driver ?? null}
                className="mb-1"
              />
              <label
                htmlFor={targetId}
                className="flex min-w-0 max-w-full flex-col gap-1 text-caption text-text-2"
              >
                {t('browser.notes.sendTo')}
                <select
                  id={targetId}
                  value={chosen.run_ref}
                  onChange={(e) => setTarget(e.target.value)}
                  className="min-w-0 max-w-full rounded-ctl border border-ctl-border bg-surface px-2 py-1 text-body text-text"
                >
                  {live.map((r) => (
                    <option key={r.run_ref} value={r.run_ref}>
                      {[
                        r.name || r.run_ref.slice(0, 8),
                        r.provider_driver ? toolName(r.provider_driver) : null,
                      ]
                        .filter(Boolean)
                        .join(' · ')}
                    </option>
                  ))}
                </select>
              </label>
              <Button
                type="button"
                size="sm"
                disabled={send.isPending}
                onClick={() => send.mutate()}
              >
                <Send aria-hidden className="size-4" />
                {t('browser.notes.send')}
              </Button>
            </div>
          ) : (
            <p className="text-caption text-text-3">
              {t('browser.notes.noSession')}
            </p>
          )}
        </>
      ) : null}
    </section>
  )
}
