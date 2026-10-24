// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The message navigator's state for one session (#2129): the engine's index of what the
// person sent, and a jump that scrolls to the row of the conversation a message is, or
// says the message is older than what this view holds.
import { useQuery } from '@tanstack/react-query'
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from 'react'
import { useTranslation } from 'react-i18next'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunMessageDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import type { ConversationItem } from './conversation-frames'
import { itemForMessage, sampleMarks } from './message-rows'
import './i18n'

/** The marks a rail holds: 24 px targets, so a few dozen stand for the whole session. */
const RAIL_MARKS = 24
/** The newest messages the rail and the count are drawn from. */
const INDEX_PAGE = 200
const FLASH_MS = 1600
/** What a reached row wears for a moment; the classes exist elsewhere in the console. */
const FLASH_CLASSES = ['ring-2', 'ring-focus', 'rounded-ctl']

/** Why the navigator has nothing to offer when it should: said, not hidden. A tool the
 * engine reads no transcript of, and a transcript the tool has not written yet, are
 * ordinary and say nothing. */
export type MessageProblem = 'missing' | 'refused' | 'error'

export interface MessageNavigator {
  runRef: string
  /** The index has messages and the conversation shows some: the rail, the count and
   * the panel are offered. A count beside an empty conversation ("Messages 2" over "No
   * messages yet") counted what the person could not see (dev#3120). */
  available: boolean
  problem: MessageProblem | null
  total: number
  /** Records of the transcript the engine could not read. */
  unreadable: number
  marks: readonly RunMessageDTO[]
  open: boolean
  setOpen: (open: boolean) => void
  /** A message this view does not hold a row for, shown whole. */
  far: RunMessageDTO | null
  closeFar: () => void
  /** A jump that could not be made: shown, not only announced. */
  jumpFailed: boolean
  /** What a screen reader is told after a jump. */
  notice: string
  jump: (message: RunMessageDTO) => Promise<void>
}

/**
 * The navigator's state for one session. `items` are the rows the conversation holds;
 * `scrollRef` is the element they scroll in; `onJump` is called once a row is reached, so
 * the conversation stops following the newest output.
 */
export function useMessageNavigator({
  runRef,
  enabled = true,
  items,
  scrollRef,
  onJump,
}: {
  runRef: string
  /** False for a session whose tool keeps no transcript the engine reads: it would
   * answer 422, which the browser logs as a console error. Nothing is asked. */
  enabled?: boolean
  items: readonly ConversationItem[]
  scrollRef: RefObject<HTMLElement | null>
  onJump: () => void
}): MessageNavigator {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const itemsRef = useRef(items)
  const onJumpRef = useRef(onJump)
  const runRefNow = useRef(runRef)
  useEffect(() => {
    itemsRef.current = items
    onJumpRef.current = onJump
    runRefNow.current = runRef
  })
  const [open, setOpen] = useState(false)
  const [far, setFar] = useState<RunMessageDTO | null>(null)
  const [notice, setNotice] = useState('')
  const [jumpFailed, setJumpFailed] = useState(false)
  // Nothing of one session is carried to another: this view is not remounted when the
  // session on screen changes.
  const [owner, setOwner] = useState(runRef)
  if (owner !== runRef) {
    setOwner(runRef)
    setOpen(false)
    setFar(null)
    setNotice('')
    setJumpFailed(false)
  }
  // The same words twice are not announced twice by a status region; a trailing
  // no-break space alternates so a repeated jump is heard again.
  const echo = useRef(false)
  const announce = useCallback((words: string) => {
    echo.current = !echo.current
    setNotice(words + (echo.current ? ' ' : ''))
  }, [])

  // The newest page is the rail and the count; an older message is reached by the panel.
  // No placeholder: when the session changes, the other one's marks are not shown.
  const index = useQuery({
    queryKey: [...agentOpsKeys.runMessages(activeTenant, runRef), 'index'],
    queryFn: ({ signal }) =>
      agentOpsApi.messages(
        runRef,
        { order: 'desc', limit: INDEX_PAGE },
        { signal },
      ),
    enabled,
    refetchOnWindowFocus: false,
    retry: false,
  })
  const sent = index.data?.items
  const marks = useMemo(() => sampleMarks(sent ?? [], RAIL_MARKS), [sent])

  const status = index.error instanceof ApiError ? index.error.status : 0
  let problem: MessageProblem | null = null
  // A missing transcript matters only under messages the conversation shows: a session
  // stopped before its first turn has none yet, and its empty state says so (dev#3120).
  if (index.data?.transcript === 'missing')
    problem = items.length > 0 ? 'missing' : null
  else if (index.isError && status === 409) problem = 'refused'
  // 422: the engine does not read this transcript (too large to read, or a tool the
  // caller's `enabled` did not know). Nothing to say.
  else if (index.isError && status !== 422) problem = 'error'

  const flash = useRef<{ row: HTMLElement; timer: number } | null>(null)
  const clearFlash = useCallback(() => {
    if (!flash.current) return
    window.clearTimeout(flash.current.timer)
    flash.current.row.classList.remove(...FLASH_CLASSES)
    flash.current = null
  }, [])
  useEffect(() => clearFlash, [clearFlash])

  const jump = useCallback(
    async (message: RunMessageDTO) => {
      const asked = runRef
      setJumpFailed(false)
      try {
        // The whole message, and how many later ones repeat its words: which of several
        // equal rows is this one. Without it there is nothing to place, so a jump that
        // cannot read it fails and says so.
        const whole = await agentOpsApi.messages(asked, { id: message.id })
        const full = whole.items[0]
        if (!full || runRefNow.current !== asked) {
          if (!full) throw new Error('message not readable')
          return
        }
        const item = itemForMessage(itemsRef.current, full)
        const row = item
          ? Array.from(
              scrollRef.current?.querySelectorAll<HTMLElement>(
                '[data-item-id]',
              ) ?? [],
            ).find((el) => el.dataset.itemId === item.id)
          : undefined
        const target = row?.closest<HTMLElement>('li') ?? row
        if (!target) {
          setFar(full)
          announce(t('messages.farNotice', { n: formatInt(full.id) }))
          return
        }
        setFar(null)
        onJumpRef.current()
        clearFlash()
        const calm = window.matchMedia?.(
          '(prefers-reduced-motion: reduce)',
        ).matches
        target.scrollIntoView?.({
          block: 'center',
          behavior: calm ? 'auto' : 'smooth',
        })
        target.classList.add(...FLASH_CLASSES)
        target.setAttribute('tabindex', '-1')
        target.focus({ preventScroll: true })
        flash.current = {
          row: target,
          timer: window.setTimeout(clearFlash, FLASH_MS),
        }
        announce(t('messages.jumped', { n: formatInt(full.id) }))
      } catch {
        if (runRefNow.current !== asked) return
        setJumpFailed(true)
        announce(t('messages.error'))
      }
    },
    [runRef, scrollRef, clearFlash, announce, t],
  )

  return {
    runRef,
    available:
      index.data !== undefined && index.data.total > 0 && items.length > 0,
    problem,
    total: index.data?.total ?? 0,
    unreadable: index.data?.unreadable ?? 0,
    marks,
    open,
    setOpen,
    far,
    closeFar: () => setFar(null),
    jumpFailed,
    notice,
    jump,
  }
}
