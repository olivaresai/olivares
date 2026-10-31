// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A SESSION ROW'S ACTIONS MENU (#2795): rename, pin, archive, copy its link, id or
// transcript, delete. The rail opens it (`work-rail.tsx`: right-click, `⋯`, Shift+F10);
// this file says what is in it and does it, against the engine's own routes.
//
// ⛔ THE MENU NAMES ONLY WHAT WILL WORK. An item the person's role cannot do is left out,
//    and an item the session's state refuses (Delete on a running session) is shown
//    disabled with the reason, the way the engine would refuse it. A session Olivares
//    observes and did not start has no run, so it has nothing to rename, archive or
//    delete: only the copies.
//
// ⛔ THE DIALOGS OUTLIVE THE MENU. The menu's content unmounts when an item is chosen,
//    so Rename and Delete keep their state here, in the surface that owns the list.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Archive,
  ArchiveRestore,
  Link,
  Pencil,
  Pin,
  PinOff,
  ScrollText,
  Sparkles,
  SquareAsterisk,
  Trash2,
} from 'lucide-react'
import { useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from '@/components/ui/dropdown-menu'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { toolName } from '@/features/agentops/engine-tools'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  TITLE_STYLES,
  usePreferencesStore,
  type TitleStyle,
} from '@/stores/preferences'
import { sessionsKeys } from './api'
import {
  isDeletableRun,
  primaryRun,
  sessionReference,
  type Grants,
  type UnifiedSession,
} from './provenance'
import { addressOf, SESSION_PARAM } from './session-address'
import { DeleteRunDialog } from './run-actions'
import './i18n'

/** What each title style is called in the menu (`rowActions` keys). */
const TITLE_STYLE_LABEL = {
  short: 'rowActions.titleShort',
  sentence: 'rowActions.titleSentence',
  long: 'rowActions.titleLong',
} as const satisfies Record<TitleStyle, string>

/** A transcript page: whole texts, so a page is kept small. */
const TRANSCRIPT_PAGE = 50

/** The address that opens this session on the Sessions screen, as a whole URL. */
function sessionLink(session: UnifiedSession): string {
  const url = new URL('/sessions', window.location.origin)
  url.searchParams.set(SESSION_PARAM, addressOf(session))
  return url.toString()
}

/** What a copy reads: the text, how many transcript records it had to leave out, and
 * how many messages the engine cut at its 64 KiB bound. */
interface Copied {
  text: string
  unreadable: number
  clipped: number
}

/** A copy refused for a reason the person is told in words (a `rowActions` key). */
class CopyRefused extends Error {
  readonly key: string
  constructor(key: string) {
    super(key)
    this.key = key
  }
}

/**
 * The session's conversation as plain text, oldest first, every page of it: each
 * message under its sender's name and a blank line between messages.
 *
 * ⛔ NOTHING IS COPIED SHORTER THAN IT SAYS. The records the engine could not read are
 *    counted and said, and so are the messages it cut at 64 KiB; a message without its
 *    whole text (an engine that ignores `text=1`) refuses the copy instead of pasting
 *    first lines; and a transcript that stops being readable after the first page, or
 *    hands back the cursor it was asked for, refuses it too.
 */
async function transcriptText(
  run: RunDTO,
  you: string,
  assistant: string,
): Promise<Copied> {
  const parts: string[] = []
  let unreadable = 0
  let clipped = 0
  let cursor: string | undefined
  do {
    const page = await agentOpsApi.messages(run.run_ref, {
      role: 'all',
      order: 'asc',
      text: 1,
      limit: TRANSCRIPT_PAGE,
      cursor,
    })
    if (page.transcript !== 'read')
      throw new CopyRefused(
        cursor === undefined
          ? 'rowActions.noTranscript'
          : 'rowActions.transcriptChanged',
      )
    unreadable += page.unreadable ?? 0
    for (const m of page.items) {
      if (m.text === undefined && m.first_line !== '')
        throw new CopyRefused('rowActions.noWholeText')
      if (m.clipped) clipped++
      parts.push(`${m.role === 'user' ? you : assistant}:\n${m.text ?? ''}\n`)
    }
    // A page that points back at itself would be read forever.
    if (page.next !== undefined && page.next === cursor)
      throw new CopyRefused('rowActions.transcriptChanged')
    cursor = page.next
  } while (cursor)
  if (parts.length === 0) throw new CopyRefused('rowActions.noTranscript')
  return { text: parts.join('\n'), unreadable, clipped }
}

type MenuDialog = {
  kind: 'rename' | 'delete'
  run: RunDTO
  /** The row it was opened from, where focus goes back when it closes. */
  address: string
} | null

/** The pin, as the caller keeps it: the rail already owns the pinned set and its key. */
export interface MenuPin {
  pinned: boolean
  /** Null when there is nowhere to keep a pin (no run, or no tenant). */
  onTogglePin: (() => void) | null
}

/**
 * The menu of a session row: `items(session, pin)` for the open menu, and `dialogs`
 * rendered once beside the list.
 */
export function useSessionMenu(
  grants: Grants,
  /** Where focus goes when a dialog opened from a row's menu closes: that row. A dialog
   * with no trigger has nowhere to give focus back, and it would land on the page. */
  returnFocus?: (address: string) => void,
): {
  items: (session: UnifiedSession, pin: MenuPin) => ReactNode
  dialogs: ReactNode
} {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()
  const [dialog, setDialog] = useState<MenuDialog>(null)
  const titleStyle = usePreferencesStore((s) => s.titleStyle)
  const setTitleStyle = usePreferencesStore((s) => s.setTitleStyle)

  const invalidate = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) }),
      qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) }),
    ])
  // A ROW THE ACTION TOOK OUT OF THE LIST (archived, deleted) leaves it only when the list
  // is read again, after focus went back to it: once that read lands, focus that fell
  // to the page goes to the row, or to the rail when the row is gone.
  const settle = (address: string) =>
    void invalidate().then(() => {
      const at = document.activeElement
      if (!at || at === document.body) returnFocus?.(address)
    })
  const onErr = (err: unknown) =>
    toast.error(err instanceof ApiError ? err.message : t('card.actionFailed'))

  const rename = useMutation({
    mutationFn: ({ run, name }: { run: RunDTO; name: string }) =>
      agentOpsApi.rename(run.run_ref, name),
    onSuccess: () => {
      setDialog(null)
      void invalidate()
    },
    onError: onErr,
  })
  const retitle = useMutation({
    mutationFn: ({ run, style }: { run: RunDTO; style: TitleStyle }) =>
      agentOpsApi.generateTitle(run.run_ref, { style }),
    onSuccess: () => void invalidate(),
    onError: onErr,
  })
  const archive = useMutation({
    mutationFn: ({
      run,
      on,
      tenant,
    }: {
      run: RunDTO
      on: boolean
      address: string
      tenant: string | null
    }) =>
      on
        ? agentOpsApi.archive(run.run_ref, tenant)
        : agentOpsApi.unarchive(run.run_ref, tenant),
    onSuccess: (_, { run, on, address, tenant }) => {
      // AN ARCHIVE CAN BE UNDONE until the person closes its notice: no timer takes Undo
      // away from someone who is still reading, and a switch of organization meanwhile
      // does not send it to the wrong one.
      toast.success(
        t(on ? 'rowActions.archived' : 'rowActions.unarchived'),
        on
          ? {
              duration: Infinity,
              action: {
                label: t('rowActions.undo'),
                onClick: () =>
                  archive.mutate({ run, on: false, address, tenant }),
              },
            }
          : undefined,
      )
      settle(address)
    },
    onError: onErr,
  })

  const copy = (read: Promise<Copied>) => {
    // THE CLICK'S PERMISSION TO WRITE LASTS A MOMENT, and a transcript is read page by
    // page: Safari and Firefox refuse a write that comes after the reads. A ClipboardItem
    // given the read's promise is written inside the click and filled when it resolves.
    let written: Promise<Copied>
    try {
      written =
        typeof ClipboardItem !== 'undefined' && navigator.clipboard?.write
          ? navigator.clipboard
              .write([
                new ClipboardItem({
                  'text/plain': read.then(
                    (c) => new Blob([c.text], { type: 'text/plain' }),
                  ),
                }),
              ])
              .then(() => read)
          : read.then(async (c) => {
              await navigator.clipboard.writeText(c.text)
              return c
            })
    } catch (err: unknown) {
      // A browser that throws while building the write still gets its reason said.
      written = Promise.reject(err)
    }
    void written
      .then((c) => {
        toast.success(
          c.unreadable > 0
            ? t('rowActions.copiedPartial', { n: c.unreadable })
            : t('context.copied'),
        )
        if (c.clipped > 0) toast.warning(t('rowActions.clipped'))
      })
      // A refused read reaches here as the clipboard's own error: the read's reason is
      // the one to say. An insecure context or a denied permission is said too, because
      // the value is not on screen to select instead.
      .catch(async (err: unknown) => {
        const cause = await read.then(
          () => err,
          (readErr: unknown) => readErr,
        )
        toast.error(
          cause instanceof CopyRefused
            ? t(cause.key)
            : cause instanceof ApiError
              ? cause.message
              : t('rowActions.copyFailed'),
        )
      })
  }

  const text = (value: string): Promise<Copied> =>
    Promise.resolve({ text: value, unreadable: 0, clipped: 0 })

  const items = (session: UnifiedSession, pin: MenuPin) => {
    const run = primaryRun(session.runs)
    const deletable = run ? isDeletableRun(run) : false
    return (
      <>
        {run && grants.runWrite ? (
          <DropdownMenuItem
            onSelect={() =>
              setDialog({ kind: 'rename', run, address: addressOf(session) })
            }
          >
            <Pencil />
            {t('rowActions.rename')}
          </DropdownMenuItem>
        ) : null}
        {run && grants.runWrite && can('sessions:output:read') ? (
          // The title is cut from the conversation, so the engine asks for the output
          // permission as well as the write one; the item is left out without both.
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <Sparkles />
              {t('rowActions.regenerateTitle')}
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent>
              <DropdownMenuRadioGroup value={titleStyle}>
                {TITLE_STYLES.map((style) => (
                  <DropdownMenuRadioItem
                    key={style}
                    value={style}
                    // A radio group does not report choosing the style it already holds,
                    // and choosing it again is how a person asks for another go.
                    onSelect={() => {
                      setTitleStyle(style)
                      retitle.mutate({ run, style })
                    }}
                  >
                    {t(TITLE_STYLE_LABEL[style])}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        ) : null}
        {pin.onTogglePin ? (
          <DropdownMenuItem onSelect={pin.onTogglePin}>
            {pin.pinned ? <PinOff /> : <Pin />}
            {pin.pinned ? t('rail.unpin') : t('rail.pin')}
            <span className="ml-auto pl-3">
              <Kbd>p</Kbd>
            </span>
          </DropdownMenuItem>
        ) : null}
        {run && grants.runWrite ? (
          <DropdownMenuItem
            onSelect={() =>
              archive.mutate({
                run,
                on: !run.archived_at,
                address: addressOf(session),
                tenant: activeTenant,
              })
            }
          >
            {run.archived_at ? <ArchiveRestore /> : <Archive />}
            {run.archived_at
              ? t('rowActions.unarchive')
              : t('rowActions.archive')}
          </DropdownMenuItem>
        ) : null}
        {run && (grants.runWrite || pin.onTogglePin) ? (
          <DropdownMenuSeparator />
        ) : null}
        <DropdownMenuItem onSelect={() => copy(text(sessionLink(session)))}>
          <Link />
          {t('rowActions.copyLink')}
        </DropdownMenuItem>
        <DropdownMenuItem
          // The id the CLI and the API take: the run's. A session with no run has
          // only its observed reference.
          onSelect={() => copy(text(run?.run_ref ?? sessionReference(session)))}
        >
          <SquareAsterisk />
          {t('rowActions.copyId')}
        </DropdownMenuItem>
        {run && grants.runRead ? (
          <DropdownMenuItem
            onSelect={() =>
              copy(
                transcriptText(
                  run,
                  t('rowActions.you'),
                  run.provider_driver
                    ? toolName(run.provider_driver)
                    : t('rowActions.assistant'),
                ),
              )
            }
          >
            <ScrollText />
            {t('rowActions.copyTranscript')}
          </DropdownMenuItem>
        ) : null}
        {run && grants.runAdmin ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              disabled={!deletable}
              onSelect={() =>
                setDialog({ kind: 'delete', run, address: addressOf(session) })
              }
              className="text-bad"
            >
              <Trash2 />
              {t('rowActions.delete')}
              {deletable ? null : (
                <span className="ml-auto pl-3 text-caption text-text-3">
                  {t('rowActions.stopFirst')}
                </span>
              )}
            </DropdownMenuItem>
          </>
        ) : null}
      </>
    )
  }

  const onCloseAutoFocus = (e: Event) => {
    if (!dialog || !returnFocus) return
    e.preventDefault()
    returnFocus(dialog.address)
  }
  const dialogs = (
    <>
      {dialog?.kind === 'rename' ? (
        <RenameSessionDialog
          key={dialog.run.run_ref}
          current={dialog.run.name ?? ''}
          pending={rename.isPending}
          onOpenChange={(open) => !open && setDialog(null)}
          onCloseAutoFocus={onCloseAutoFocus}
          onSubmit={(name) => rename.mutate({ run: dialog.run, name })}
        />
      ) : null}
      {dialog?.kind === 'delete' ? (
        <DeleteRunDialog
          key={dialog.run.run_ref}
          run={dialog.run}
          open
          onOpenChange={(open) => !open && setDialog(null)}
          onCloseAutoFocus={onCloseAutoFocus}
          onDeleted={() => settle(dialog.address)}
        />
      ) : null}
    </>
  )

  return { items, dialogs }
}

/** The name, edited in place; empty gives the session back its derived name. */
function RenameSessionDialog({
  current,
  pending,
  onOpenChange,
  onCloseAutoFocus,
  onSubmit,
}: {
  current: string
  pending: boolean
  onOpenChange: (open: boolean) => void
  onCloseAutoFocus: (e: Event) => void
  onSubmit: (name: string) => void
}) {
  const { t } = useTranslation('sessions')
  const [name, setName] = useState(current)
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!pending) onSubmit(name.trim())
  }
  return (
    <Dialog open onOpenChange={(o) => (pending ? undefined : onOpenChange(o))}>
      <DialogContent className="max-w-md" onCloseAutoFocus={onCloseAutoFocus}>
        <DialogHeader>
          <DialogTitle>{t('rowActions.renameTitle')}</DialogTitle>
          <DialogDescription>
            {t('rowActions.renameDescription')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <Field label={t('rowActions.renameLabel')}>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
            />
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={pending}
            >
              {t('rowActions.cancel')}
            </Button>
            <Button type="submit" variant="primary" disabled={pending}>
              {pending && <Spinner className="size-3.5" />}
              {t('rowActions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
