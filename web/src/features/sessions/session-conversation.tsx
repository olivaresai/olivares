// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NARRATIVE AS A CONVERSATION. Attach frames are mapped before they are
// painted: the default view is operator, assistant, tool, system, result. The
// raw line is one click away (inspect), never the row itself.
import {
  memo,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { ArrowDown, Quote, GitFork } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useTranslation } from 'react-i18next'
import { agentOpsKeys } from '@/features/agentops/api'
import { MessageImages } from '@/features/agentops/attachment-ui'
import { readAttachPage, useRunAttach } from '@/features/agentops/attach'
import { readsNativeRecord } from '@/features/agentops/provider-contract'
import { useModelName } from '@/features/agentops/model-choices'
import { useTurnInterrupt } from '@/features/agentops/turn-interrupt'
import type { AttachFrame, RunDTO } from '@/features/agentops/types'
import { CopyButton } from '@/components/ui/copy-button'
import { Button } from '@/components/ui/button'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import { forkContext } from './session-context'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import {
  readSessionDraft,
  sessionDraftKey,
  writeSessionDraft,
} from '@/lib/session-drafts'
import { isTypingTarget } from '@/lib/keybindings/model'
import '@/features/shared/i18n'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import {
  conversationCwd,
  conversationModel,
  isQuiet,
  mapConversationFrames,
  type ConversationItem,
} from './conversation-frames'
import { ConversationApproval } from './conversation-approval'
import { ConversationCard, ConversationTaskList } from './conversation-cards'
import { systemFailed, systemText } from './conversation-text'
import {
  isRunning,
  KeyboardInspect,
  openOnWords,
  threadParts,
  type Part,
  TurnRow,
  WorkRow,
} from './conversation-work'
import { MarkdownText } from './markdown-text'
import { samePlain } from './same-plain'
import { parseMarkdown, plainText } from './markdown'
import { MessageBar, MessageRail } from './message-navigator'
import { useMessageNavigator } from './use-message-navigator'
import { hostIsolationUnavailable, isLiveRun } from './provenance'
import {
  settleTurns,
  useRunSentTurns,
  useSentTurns,
  wordsShown,
  type SentTurn,
} from './sent-turns'
import { useConversationTasks } from './use-conversation-tasks'
import { SelectionCite } from './selection-cite'
import './i18n'

/** Past this many frames read here, the oldest SETTLE_FRAMES join the folded base the stored conversation
 * starts: a frame then folds again only the frames after them, and no turn ever leaves the view. */
const MAX_FRAMES = 5000
const SETTLE_FRAMES = 2500
/** A session opens on its last this many frames and reads earlier ones this many at a time: the engine
 * buffers up to 4096 frames or 8 MiB, and replaying all of them at every open is what a page avoids. */
const PAGE_FRAMES = 500
/** The side pane's Activity is read again at most this often while the tool writes. */
const ACTIVITY_REFRESH_MS = 1_500
/** Past this many rows a conversation keeps in the page only the rows near what is on screen, with a spacer for
 * the rest above and below: a long session's thousands of rows stay out of the document. Below it
 * every row is in the page, as before, so the browser's own find reaches all of them. */
const VIRTUAL_FROM = 500
const ROW_ESTIMATE_PX = 64
const TAIL_ROWS = 40

export function SessionConversation(
  props: Parameters<typeof ConversationBody>[0],
) {
  const tenant = useTenantStore((s) => s.activeTenant)
  const generation = useSessionStore((s) => s.credentialGeneration)
  return (
    <ConversationBody
      key={`${props.run.run_ref}\n${tenant ?? ''}\n${generation}`}
      {...props}
    />
  )
}

function ConversationBody({
  run,
  selectedId,
  onInspect,
  onFolder,
  onUnknownFrames,
  onModel,
  composerId,
}: {
  run: RunDTO
  selectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  /** The folder the tool's own first frame names, for the Context pane to say. */
  onFolder?: (cwd: string | null) => void
  /** The raw frames this console does not read, for Details › Technical details. */
  onUnknownFrames?: (frames: readonly string[]) => void
  /** The model the session's own turns run on now, for the header to say. */
  onModel?: (model: string | null) => void
  /** The work surface's own field; a preview without a composer only offers copying. */
  composerId?: string
}) {
  const { t } = useTranslation('sessions')
  const modelName = useModelName(run.provider_profile_ref)
  const { t: tShared } = useTranslation('shared')
  // The frames read here: the settled ones, folded once into the base, and the newest, folded on every frame.
  const [read, setRead] = useState<{
    settled: AttachFrame[]
    frames: AttachFrame[]
  }>({ settled: [], frames: [] })
  const { settled, frames } = read
  const [delta, setDelta] = useState<AttachFrame | null>(null)
  // The stored conversation before the frames read here: every earlier turn, from the tool's own record.
  const [history, setHistory] = useState<string[]>([])
  const [dropped, setDropped] = useState(0)
  // Whether the engine buffers frames before the first one here, and a page being read or failed.
  const [earlier, setEarlier] = useState(false)
  const [paging, setPaging] = useState<'reading' | 'failed' | null>(null)
  const generation = useSessionStore((s) => s.credentialGeneration)
  const tenant = useTenantStore((s) => s.activeTenant)
  const { activeTenant, principal, can } = useAuth()
  const draftKey = sessionDraftKey(activeTenant, principal, run.run_ref)
  const canQuote = can('sessions:run:write')
  // One function while the composer, the draft and the identity stay: the rows are memoized, and a new function on
  // every frame would paint them all again.
  const quote = useMemo(
    () =>
      composerId && draftKey && canQuote
        ? (text: string) => {
            if (
              useSessionStore.getState().credentialGeneration !== generation ||
              useTenantStore.getState().activeTenant !== tenant
            )
              return
            const comment = readSessionDraft(draftKey)
            const cited = text
              .split(/\r?\n/)
              .map((line) => `> ${line}`)
              .join('\n')
            writeSessionDraft(
              draftKey,
              `${comment ? `${comment}\n\n` : ''}${cited}\n\n`,
            )
            // React paints the new draft before the caret moves. A different signed-in owner
            // or conversation must never receive this deferred focus.
            requestAnimationFrame(() => {
              const field = document.getElementById(composerId)
              if (
                !(field instanceof HTMLTextAreaElement) ||
                field.dataset.sessionDraftKey !== draftKey
              )
                return
              field.focus()
              field.setSelectionRange(field.value.length, field.value.length)
            })
          }
        : undefined,
    [composerId, draftKey, canQuote, generation, tenant],
  )
  const ownerKey = `${run.run_ref}\n${tenant ?? ''}\n${generation}`
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const [autoscroll, setAutoscroll] = useState(true)

  // Whatever the tool writes may change what it is doing: the Activity tab reads it again,
  // once per burst of frames, and only while that tab is open (an inactive query is only
  // marked stale).
  const queryClient = useQueryClient()
  const activityTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const runRef = run.run_ref
  useEffect(
    () => () => {
      if (activityTimer.current) clearTimeout(activityTimer.current)
      activityTimer.current = null
    },
    [runRef],
  )
  const onFrame = useCallback(
    (f: AttachFrame) => {
      setRead(({ settled, frames }) =>
        frames.length >= MAX_FRAMES
          ? {
              settled: [...settled, ...frames.slice(0, SETTLE_FRAMES)],
              frames: [...frames.slice(SETTLE_FRAMES), f],
            }
          : { settled, frames: [...frames, f] },
      )
      if (activityTimer.current) return
      activityTimer.current = setTimeout(() => {
        activityTimer.current = null
        void queryClient.invalidateQueries({
          queryKey: agentOpsKeys.runActivity(tenant, runRef),
        })
      }, ACTIVITY_REFRESH_MS)
    },
    [queryClient, tenant, runRef],
  )
  const onLag = useCallback((lag: { dropped: number }) => {
    setDropped((d) => d + lag.dropped)
  }, [])

  const isRemote = run.transport === 'remote-control'
  const { status, ended } = useRunAttach({
    runRef: run.run_ref,
    enabled: !isRemote,
    sessionKey: [run.state, run.transport, run.started_at].join(':'),
    onFrame,
    onDelta: setDelta,
    onHistory: setHistory,
    onLag,
    tail: PAGE_FRAMES,
    onPage: setEarlier,
  })

  // Protocol bookkeeping (handshakes, replies, lifecycle notifications) is no row: it read
  // "Protocol frames: N" between every turn and told nothing. Every frame stays one command
  // away under "In a terminal" (`session follow -o json`); a protocol error is still shown.
  // A long session's history is thousands of records: it is folded once, and each frame on top of it.
  // ponytail: a fold copies the item list per line (quadratic); fine to some ten thousand records, fold in
  // place when sessions longer than that open slowly.
  const stored = useMemo(() => mapConversationFrames(history), [history])
  const restored = useMemo(
    () =>
      mapConversationFrames(
        settled.map((f) => f.line),
        settled.map((f) => f.at),
        settled.map((f) => f.seq),
        stored,
      ),
    [settled, stored],
  )
  const items = useMemo(
    () =>
      mapConversationFrames(
        [...frames.map((f) => f.line), ...(delta ? [delta.line] : [])],
        frames.map((f) => f.at),
        frames.map((f) => f.seq),
        restored,
      ).filter((item) => item.systemKind !== 'protocol' && !isQuiet(item)),
    [frames, restored, delta],
  )
  const contextItems = useRef(items)
  useLayoutEffect(() => {
    contextItems.current = items
  }, [items])
  const fork = useCallback(
    (end: string) => {
      if (
        useSessionStore.getState().credentialGeneration !== generation ||
        useTenantStore.getState().activeTenant !== tenant
      )
        return
      try {
        if (earlier || dropped) throw new Error('incomplete context')
        const message = forkContext(run.run_ref, contextItems.current, end)
        useNewSessionDialog.getState().openAdvanced({
          message,
          workspaceRef: run.workspace_ref,
          profileRef: run.provider_profile_ref,
          driver: run.provider_driver,
          model: run.model_ref,
          effort: run.effort,
          runOn: run.run_on,
        })
      } catch {
        toast.error(t('context.readFailed'))
      }
    },
    [run, generation, tenant, earlier, dropped, t],
  )
  // The first message the console sent, until the tool shows it itself.
  const sent = useRunSentTurns(run.run_ref)
  const { pending, echoed } = useMemo(
    () => (sent ? settleTurns(items, sent) : { pending: [], echoed: [] }),
    [items, sent],
  )
  const frameCwd = conversationCwd(items)
  useEffect(() => {
    onFolder?.(frameCwd ?? null)
    return () => onFolder?.(null)
  }, [frameCwd, onFolder])
  // A frame this console does not read is no row: its raw text goes to Details › Technical
  // details, told once per change and not once per frame.
  const unknownKey = useMemo(
    () =>
      JSON.stringify(
        items
          .filter((item) => item.kind === 'unknown')
          .map((item) => item.raw.join('\n')),
      ),
    [items],
  )
  const unknownFrames = useMemo(
    () => JSON.parse(unknownKey) as string[],
    [unknownKey],
  )
  useEffect(() => {
    onUnknownFrames?.(unknownFrames)
    return () => onUnknownFrames?.([])
  }, [unknownFrames, onUnknownFrames])
  const frameModel = conversationModel(items)
  useEffect(() => {
    onModel?.(frameModel ?? null)
    return () => onModel?.(null)
  }, [frameModel, onModel])

  // Where the console last put or saw the view: a scroll event that finds it there was not
  // the person's (onScroll).
  const topRef = useRef(0)
  useEffect(() => {
    if (!autoscroll) return
    const el = scrollRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    topRef.current = el.scrollTop
  }, [items, autoscroll])
  // WHAT GROWS AFTER A FRAME is followed too: the request card that loads after the
  // transcript, a block opened at the end. Only while the person follows the end.
  const columnRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = scrollRef.current
    const column = columnRef.current
    if (!autoscroll || !el || !column || typeof ResizeObserver !== 'function')
      return
    const follow = new ResizeObserver(() => {
      el.scrollTop = el.scrollHeight
      topRef.current = el.scrollTop
    })
    follow.observe(column)
    return () => follow.disconnect()
  }, [autoscroll])

  // AN EARLIER PAGE goes above the rows already read, and the row the person was on stays where it was.
  const pageRef = useRef<AbortController | null>(null)
  const anchorRef = useRef<number | null>(null)
  useEffect(() => () => pageRef.current?.abort(), [ownerKey])
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (anchorRef.current === null || !el) return
    el.scrollTop = el.scrollHeight - anchorRef.current
    anchorRef.current = null
  }, [read])
  const firstSeq = (settled[0] ?? frames[0])?.seq
  const showEarlier = async () => {
    if (firstSeq === undefined) return
    pageRef.current?.abort()
    const controller = new AbortController()
    pageRef.current = controller
    setPaging('reading')
    setAutoscroll(false)
    try {
      const page = await readAttachPage({
        runRef,
        token: useSessionStore.getState().csrfToken,
        tenant: useTenantStore.getState().activeTenant,
        from: Math.max(1, firstSeq - PAGE_FRAMES),
        before: firstSeq,
        signal: controller.signal,
      })
      if (controller.signal.aborted) return
      const el = scrollRef.current
      anchorRef.current = el ? el.scrollHeight - el.scrollTop : null
      // Above every frame read here: a page is settled at once.
      setRead(({ settled, frames }) => ({
        settled: [
          ...page.frames.filter(
            (f) => f.seq < ((settled[0] ?? frames[0])?.seq ?? Infinity),
          ),
          ...settled,
        ],
        frames,
      }))
      setEarlier(page.earlier)
      setPaging(null)
    } catch {
      if (!controller.signal.aborted) setPaging('failed')
    } finally {
      if (pageRef.current === controller) pageRef.current = null
    }
  }

  // Back to the end, and following it again (T3's "Scroll to end").
  const jumpToLatest = useCallback(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
    setAutoscroll(true)
  }, [])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
    // The event of the console's own scroll to the end comes a frame later; a row measured
    // in that frame puts the end below the view without moving it. Only a view that moved
    // leaves the end, and the follow then catches the row up.
    const moved = el.scrollTop !== topRef.current
    topRef.current = el.scrollTop
    if (atBottom || moved) setAutoscroll(atBottom)
  }, [])

  // The message index is read again when the person has sent another message, not on
  // every burst of output: each read is a scan of the tool's transcript.
  const sentCount = useMemo(
    () => items.filter((item) => item.kind === 'operator').length,
    [items],
  )
  // A note the tool has shown is forgotten: its own row tells it from now on.
  useEffect(() => {
    if (echoed.length) useSentTurns.getState().drop(runRef, echoed)
  }, [echoed, runRef])
  // A message sent from now on is echoed by a frame after the newest one shown here
  //. Nothing is said before a frame arrives: after a remount, the newest frame
  // the session showed before is still the right place.
  const newestSeq = frames.length ? frames[frames.length - 1]!.seq : undefined
  const said = useMemo(() => wordsShown(items), [items])
  useEffect(() => {
    if (newestSeq !== undefined)
      useSentTurns.getState().see(runRef, newestSeq, said)
  }, [runRef, newestSeq, said])
  const sentSeen = useRef({ runRef, sentCount })
  useEffect(() => {
    const seen = sentSeen.current
    sentSeen.current = { runRef, sentCount }
    if (seen.runRef !== runRef || seen.sentCount === sentCount) return
    void queryClient.invalidateQueries({
      queryKey: agentOpsKeys.runMessages(tenant, runRef),
    })
  }, [runRef, sentCount, tenant, queryClient])

  // The person's messages as a rail and a panel. A jump leaves the newest output
  // behind: following it again is the person scrolling back down.
  const stopFollowing = useCallback(() => setAutoscroll(false), [])
  // The navigator reaches a row that is not in the page by asking for it first.
  const revealRef = useRef<(itemId: string) => Promise<void>>(async () => {})
  const messages = useMessageNavigator({
    runRef,
    // A legacy run names no driver: the engine answers for it, as it always did.
    enabled: !run.provider_driver || readsNativeRecord(run),
    items,
    scrollRef,
    onJump: stopFollowing,
    reveal: useCallback((itemId: string) => revealRef.current(itemId), []),
  })

  // THE THREAD AS IT READS: the calls between two replies fold into one row, a
  // subagent's calls into its Agent row, and the calls of the last row, while they run,
  // are one live line with their clock.
  const tasks = useConversationTasks(run, tenant)
  const authoritativeTodos = !tasks.isError && tasks.data?.transcript === 'read'
  const parts = useMemo(
    () =>
      threadParts(
        items.filter((item) => item.kind !== 'unknown'),
        authoritativeTodos,
      ),
    [items, authoritativeTodos],
  )
  const live = isLiveRun(run) && !ended
  const last = parts[parts.length - 1]
  const lastReplyId = parts.findLast(
    (part): part is Extract<Part, { kind: 'item' }> =>
      part.kind === 'item' && part.item.kind === 'assistant',
  )?.item.id
  const runningNow =
    live && last?.kind === 'work' ? last.items.filter(isRunning) : []
  const turn = useTurnInterrupt(run)
  // The pane this conversation is in, with its composer; the session sheet is a dialog,
  // whose Escape closes it, so there is none there and Escape interrupts nothing.
  const [pane, setPane] = useState<Element | null>(null)
  const rootRef = useCallback((node: HTMLDivElement | null) => {
    setPane(node?.closest('section[data-focused]') ?? null)
  }, [])
  const canInterrupt =
    pane !== null && runningNow.length > 0 && turn.allowed && !turn.pending
  // THE ROWS IN THE PAGE. Past VIRTUAL_FROM, only the rows near the screen render; a spacer stands for
  // the others, sized from the rows already measured, so the scroll height, following the end and the scrollbar
  // stay what the whole thread would give.
  const listRef = useRef<HTMLOListElement>(null)
  const virtual = parts.length > VIRTUAL_FROM
  const [listTop, setListTop] = useState(0)
  const virtualizer = useVirtualizer({
    count: virtual ? parts.length : 0,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_ESTIMATE_PX,
    getItemKey: (index) => partKey(parts[index]!),
    overscan: 20,
    gap: 4,
    scrollMargin: listTop,
    initialRect: { width: 0, height: 640 },
    // A row is never 0 px tall in a browser; a document without layout (a test) measures every one at 0, and
    // the estimate keeps the window to what a screen holds there too.
    measureElement: (el) =>
      Math.round(el.getBoundingClientRect().height) || ROW_ESTIMATE_PX,
  })
  const windowRows = virtualizer.getVirtualItems()
  // Until the scroller has a size (its first measure; a document without layout never has one), the window is the
  // end of the thread: what following the end shows.
  const tail = virtual && windowRows.length === 0
  // The default virtual range is contiguous. Keep its indices stable when a measurement
  // changes positions but not the rows: measuring again on that render feeds back into React.
  const firstShown = !virtual
    ? 0
    : tail
      ? Math.max(0, parts.length - TAIL_ROWS)
      : (windowRows[0]?.index ?? 0)
  const shownCount = !virtual
    ? parts.length
    : tail
      ? Math.min(parts.length, TAIL_ROWS)
      : windowRows.length
  const shown = useMemo(
    () => Array.from({ length: shownCount }, (_, index) => firstShown + index),
    [firstShown, shownCount],
  )
  const lastRow = windowRows[windowRows.length - 1]
  const padTop = windowRows[0]
    ? Math.max(0, windowRows[0].start - listTop - 4)
    : 0
  const padBottom = lastRow
    ? Math.max(0, virtualizer.getTotalSize() - (lastRow.end - listTop) - 4)
    : 0
  // Each rendered row is measured (and watched, for a block that opens): its root is the list item, after the top
  // spacer, in the order of `shown`.
  useLayoutEffect(() => {
    const list = listRef.current
    const scroller = scrollRef.current
    if (!virtual || !list || !scroller) return
    const top =
      list.getBoundingClientRect().top -
      scroller.getBoundingClientRect().top +
      scroller.scrollTop
    if (Math.abs(top - listTop) > 1) setListTop(top)
  }, [virtual, listTop, parts, earlier, paging, status, windowRows])
  useLayoutEffect(() => {
    const list = listRef.current
    if (!virtual || !list) return
    // A measure can update the window: never re-enter React's layout commit.
    const frame = requestAnimationFrame(() => {
      let k = 0
      for (const el of Array.from(list.children)) {
        if (!(el instanceof HTMLElement) || el.dataset.spacer !== undefined)
          continue
        const index = shown[k++]
        if (index === undefined) break
        el.dataset.index = String(index)
        virtualizer.measureElement(el)
      }
    })
    return () => cancelAnimationFrame(frame)
  }, [virtual, shown, virtualizer, parts])
  useLayoutEffect(() => {
    revealRef.current = async (itemId: string) => {
      if (!virtual) return
      const index = parts.findIndex((part) =>
        part.kind === 'work'
          ? part.items.some((item) => item.id === itemId)
          : part.item.id === itemId ||
            (part.kind === 'turn' &&
              part.items.some((item) => item.id === itemId)),
      )
      if (index < 0) return
      virtualizer.scrollToIndex(index, { align: 'center' })
      await new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      )
    }
  })
  const interruptRef = useRef(turn.interrupt)
  useEffect(() => {
    interruptRef.current = turn.interrupt
  })
  // ESCAPE INTERRUPTS THE TURN while a call runs, from a field of this pane (its composer),
  // where the pane's own Escape does nothing; anywhere else Escape keeps closing the side
  // pane. A dialog, a menu or the composer's own menu takes its Escape first.
  useEffect(() => {
    if (!canInterrupt || !pane) return
    const onKey = (e: KeyboardEvent) => {
      if (!interrupts(e) || !isTypingTarget(e.target)) return
      if (!(e.target instanceof Node) || !pane.contains(e.target)) return
      if (document.querySelector(OPEN_LAYER)) return
      e.preventDefault()
      interruptRef.current()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [canInterrupt, pane])

  return (
    <div
      ref={rootRef}
      className="group/conv relative flex min-h-0 flex-1 flex-col gap-2"
      data-testid="session-conversation"
    >
      <SelectionCite root={scrollRef} onCite={quote} />
      {dropped > 0 ? (
        <p className="text-caption text-warning">
          {t('conversation.lag', { count: dropped })}
        </p>
      ) : null}
      <MessageBar nav={messages} />
      <div className="relative flex min-h-0 flex-1 flex-col">
        <div
          ref={scrollRef}
          onScroll={onScroll}
          tabIndex={0}
          role="log"
          aria-label={t('conversation.log')}
          // A virtual list keeps the view in place itself when a row above it changes size;
          // the browser's scroll anchoring doing it too moved the view back and forth every
          // frame at some positions. A short list keeps the browser's.
          className={cn(
            'relative min-h-0 flex-1 overflow-auto focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
            virtual && '[overflow-anchor:none]',
          )}
        >
          {/* ONE CENTERED COLUMN, 760 px: the transcript is read, not scanned, and a line
            longer than that is a line nobody follows. */}
          {/* The rail of marks sits in the column's left padding: the column keeps its width. */}
          <div
            ref={columnRef}
            data-slot="conversation-column"
            className="mx-auto w-full max-w-[760px] px-4 py-4"
          >
            {/* A LINK THAT DROPPED OR IS COMING UP SAYS SO, ONCE, QUIETLY, while the turns
              already read stay on the page. Without it a stale transcript reads as a
              finished one. */}
            {earlier ? (
              <div className="mb-2 flex flex-col items-center gap-1">
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={paging === 'reading'}
                  onClick={() => void showEarlier()}
                >
                  {t('conversation.earlier')}
                </Button>
                {paging === 'failed' ? (
                  <p role="status" className="text-caption text-warning">
                    {t('conversation.earlierFailed')}
                  </p>
                ) : null}
              </div>
            ) : null}
            {status === 'error' || status === 'connecting' ? (
              <p
                role="status"
                data-testid="conversation-link"
                className="mb-2 text-center text-overline font-normal text-text-3"
              >
                {tShared(`live.${status}`)}
              </p>
            ) : null}
            {items.length === 0 &&
            pending.length === 0 &&
            !hostIsolationUnavailable(run) &&
            status !== 'error' &&
            status !== 'connecting' ? (
              <p className="py-2 text-caption text-muted-foreground">
                {status === 'open'
                  ? t('conversation.waiting')
                  : t('conversation.empty')}
              </p>
            ) : (
              <ol ref={listRef} className="flex flex-col gap-1">
                {padTop > 0 ? (
                  <li aria-hidden data-spacer="" style={{ height: padTop }} />
                ) : null}
                {shown.map((index) => {
                  const part = parts[index]!
                  return part.kind === 'card' ? (
                    <ConversationCard
                      key={part.item.id}
                      card={part.item.toolCard!}
                      run={run}
                      answerable={
                        (run.state === 'running' || run.state === 'idle') &&
                        !ended &&
                        can('sessions:run:write') &&
                        pending.length === 0 &&
                        !run.pending_approval_ref &&
                        !items
                          .slice(items.indexOf(part.item) + 1)
                          .some((later) => later.kind === 'operator')
                      }
                    />
                  ) : part.kind === 'turn' ? (
                    <TurnRow
                      key={part.item.id}
                      item={part.item}
                      items={part.items}
                      nested={part.nested}
                      runRef={run.run_ref}
                      selectedId={selectedId}
                      onInspect={onInspect}
                      onFork={
                        canQuote && can('sessions:run:output')
                          ? fork
                          : undefined
                      }
                      modelName={modelName}
                      live={live}
                    />
                  ) : part.kind === 'work' ? (
                    <WorkRow
                      key={`work-${part.items[0]!.id}`}
                      items={part.items}
                      nested={part.nested}
                      runRef={run.run_ref}
                      selectedId={selectedId}
                      onInspect={onInspect}
                      live={live}
                      running={index === parts.length - 1 ? runningNow : []}
                      canInterrupt={canInterrupt}
                    />
                  ) : (
                    <ConversationRow
                      modelName={modelName}
                      key={part.item.id}
                      item={part.item}
                      runRef={run.run_ref}
                      selected={selectedId === part.item.id}
                      onInspect={onInspect}
                      onFork={
                        canQuote && can('sessions:run:output')
                          ? fork
                          : undefined
                      }
                      onQuote={quote}
                      lastReply={part.item.id === lastReplyId}
                    />
                  )
                })}
                {padBottom > 0 ? (
                  <li
                    aria-hidden
                    data-spacer=""
                    style={{ height: padBottom }}
                  />
                ) : null}
                {pending.map((turn, i) => (
                  <SentTurnRow
                    key={`sent-${i}`}
                    turn={turn}
                    ended={!isLiveRun(run) && run.state !== 'waiting_approval'}
                  />
                ))}
              </ol>
            )}
            <DiscardedNotice />
            <ConversationTaskList key={ownerKey} run={run} query={tasks} />
            {/* The request the session waits on, after what led to it, answered here. */}
            <ConversationApproval run={run} />
          </div>
        </div>
        {autoscroll ? null : (
          <button
            type="button"
            onClick={jumpToLatest}
            className="absolute bottom-3 left-1/2 inline-flex -translate-x-1/2 items-center gap-1 rounded-full border border-line bg-surface px-3 py-1 text-caption text-text-2 shadow-sm outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
          >
            <ArrowDown aria-hidden className="size-3.5" />
            {t('conversation.jumpToLatest')}
          </button>
        )}
        <MessageRail nav={messages} />
      </div>
      {ended ? (
        <p className="text-center text-overline font-normal text-text-3">
          {t('conversation.ended')}
        </p>
      ) : null}
    </div>
  )
}

/** A dialog or a menu that is open takes the Escape key. */
const OPEN_LAYER =
  '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"], [role="menu"][data-state="open"]'

/** An Escape of its own: not one a control took, not one that ends an input method's
 * composition, not a held key repeating. */
function interrupts(e: KeyboardEvent): boolean {
  return (
    e.key === 'Escape' && !e.defaultPrevented && !e.isComposing && !e.repeat
  )
}

function ConversationRowView({
  modelName,
  item,
  runRef,
  selected,
  onInspect,
  onQuote,
  onFork,
  lastReply = false,
}: {
  modelName: (id: string) => string
  item: ConversationItem
  runRef: string
  selected: boolean
  onInspect?: (item: ConversationItem) => void
  onQuote?: (text: string) => void
  onFork?: (end: string) => void
  /** The reply the thread ends on keeps its actions shown; earlier ones reveal them. */
  lastReply?: boolean
}) {
  const { t } = useTranslation('sessions')
  const contentRef = useRef<HTMLDivElement>(null)
  const replyText = () => plainText(parseMarkdown(item.text ?? ''))
  const quoteReply = () => {
    const selection = window.getSelection()
    const selected =
      selection &&
      !selection.isCollapsed &&
      contentRef.current?.contains(selection.anchorNode) &&
      contentRef.current.contains(selection.focusNode)
        ? selection.toString()
        : ''
    onQuote?.(selected.trim() ? selected : replyText())
  }
  const inspect = () => onInspect?.(item)
  const openFromWords = openOnWords(inspect)

  if (item.kind === 'system') {
    return (
      <li>
        {onFork && item.systemKind === 'rate_limit' ? (
          <Button variant="ghost" size="sm" onClick={() => onFork(item.id)}>
            <GitFork className="size-3.5" aria-hidden />
            {t('context.fork')}
          </Button>
        ) : null}
        <div
          data-testid="conversation-item"
          data-item-id={item.id}
          data-kind="system"
          onClick={openFromWords}
          className={cn(
            'flex min-h-7 w-full items-center gap-2 rounded-ctl px-2 py-0.5 text-left',
            'text-caption',
            // The engine's notice reads as the terminal's warning does (`session follow`).
            systemFailed(item)
              ? 'text-danger'
              : item.systemKind === 'notice' ||
                  item.systemKind === 'model' ||
                  item.systemKind === 'rate_limit'
                ? 'text-warning'
                : 'text-muted-foreground',
            'hover:bg-hover',
            selected && 'bg-active text-text',
          )}
        >
          {/* A notice is the reason itself: it wraps instead of being cut on a phone. */}
          <span
            className={cn(
              'min-w-0',
              item.systemKind === 'notice' ||
                item.systemKind === 'model' ||
                item.systemKind === 'rate_limit'
                ? 'whitespace-normal'
                : 'truncate',
            )}
            title={
              item.systemKind === 'model'
                ? `${item.fromModel ?? ''} → ${item.model ?? ''}`
                : item.systemKind === 'init'
                  ? item.model
                  : undefined
            }
          >
            {systemText(t, item, undefined, modelName)}
          </span>
        </div>
        <KeyboardInspect onInspect={onInspect ? inspect : undefined} />
      </li>
    )
  }

  // THE PERSON'S TURN IS A QUIET BLOCK AND THE ASSISTANT'S IS PLAIN TEXT. No right-aligned
  // bubble and no "YOU" / "ASSISTANT" label above it: who spoke is what the block is, and
  // the assistant is the page itself.
  if (item.kind === 'operator')
    return (
      <li className="group/msg my-2">
        <div
          data-testid="conversation-item"
          data-item-id={item.id}
          data-kind="operator"
          onClick={openFromWords}
          className={cn(
            'w-full rounded-[12px] bg-muted px-4 py-3 text-left text-body-l text-text',
            selected && 'ring-1 ring-line-strong',
          )}
        >
          <UserMessage text={item.text || item.summary} />
        </div>
        <KeyboardInspect onInspect={onInspect ? inspect : undefined} />
        {/* The images the person attached, from the engine's copy: they come back with
            the message after a reload. */}
        {item.attachments?.length ? (
          <MessageImages runRef={runRef} attachments={item.attachments} />
        ) : null}
        {item.text ? (
          <MessageActions align="end" reveal text={() => item.text ?? ''} />
        ) : null}
      </li>
    )

  // THE ASSISTANT'S WORDS ARE MARKDOWN: lists, code and bold are shown as such. A
  // reply holds blocks and links, which a button cannot, so the row is the reply itself.
  return (
    <li className="group/msg py-1">
      <div
        data-testid="conversation-item"
        data-item-id={item.id}
        data-kind={item.kind}
        ref={contentRef}
        onClick={openFromWords}
        className={cn(
          'w-full rounded-ctl text-left text-body-l text-text [overflow-wrap:anywhere]',
          selected && 'bg-active',
        )}
      >
        <MarkdownText text={item.text || item.summary} />
        <KeyboardInspect onInspect={onInspect ? inspect : undefined} />
      </div>
      {item.text ? (
        <MessageActions
          align="start"
          reveal={!lastReply}
          text={replyText}
          onQuote={
            onQuote && item.kind === 'assistant' ? quoteReply : undefined
          }
        />
      ) : null}
    </li>
  )
}

/** A held first message that a session change dropped: says so, with none of its words,
 * until the reader dismisses it. */
function DiscardedNotice() {
  const { t } = useTranslation('sessions')
  const discarded = useSentTurns((s) => s.discarded)
  const dismiss = useSentTurns((s) => s.dismissDiscarded)
  if (!discarded) return null
  return (
    <div
      role="status"
      className="flex items-center gap-2 px-1 py-2 text-caption text-danger"
    >
      <span>{t('conversation.discardedOnSessionChange')}</span>
      <button type="button" className="underline" onClick={dismiss}>
        {t('conversation.dismissDiscarded')}
      </button>
    </div>
  )
}

/** The person's message as the console sent it: there is no frame to inspect. A message
 * the engine refused says so, with the engine's reason, in the danger color; one held
 * for its launch's approval says it goes when the session runs; one the engine took says
 * so until the tool shows it. */
function SentTurnRow({ turn, ended }: { turn: SentTurn; ended: boolean }) {
  const { t } = useTranslation('sessions')
  return (
    <li
      className="group/msg my-2 flex flex-col gap-1"
      data-testid="conversation-sent"
    >
      <p className="rounded-[12px] bg-muted px-4 py-3 text-body-l text-text">
        <UserMessage text={turn.text} />
      </p>
      {turn.refused !== undefined ? (
        <p className="px-1 text-caption text-danger">
          {t('conversation.notSent', { reason: turn.refused })}
        </p>
      ) : turn.waiting ? (
        <p className="px-1 text-caption text-muted-foreground">
          {t('conversation.waitingApproval')}
        </p>
      ) : (
        <p className="px-1 text-caption text-muted-foreground">
          {t(ended ? 'conversation.sentNotTaken' : 'conversation.sentPending')}
        </p>
      )}
      <MessageActions align="end" reveal text={() => turn.text} />
    </li>
  )
}

/** The same eight-line preview for a sent turn and its stored echo. */
function UserMessage({ text }: { text: string }) {
  const { t } = useTranslation('sessions')
  const id = useId()
  const ref = useRef<HTMLSpanElement>(null)
  const [expanded, setExpanded] = useState(false)
  const [long, setLong] = useState(false)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const measure = () => {
      const line = Number.parseFloat(getComputedStyle(el).lineHeight)
      setLong(el.scrollHeight > line * 8 + 1)
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [text])
  return (
    <>
      <span
        ref={ref}
        id={id}
        data-slot="user-message-text"
        className={cn(
          'block whitespace-pre-wrap break-words',
          !expanded && 'line-clamp-8',
        )}
      >
        {text}
      </span>
      {long ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          aria-expanded={expanded}
          aria-controls={id}
          onClick={() => setExpanded((value) => !value)}
        >
          {t(expanded ? 'conversation.showLess' : 'conversation.showAll')}
        </Button>
      ) : null}
    </>
  )
}

/** Copy the words as read; a writable reply can also be quoted into its composer.
 * A message without words has neither action. The transcript is text, not a column of
 * controls: a `reveal` row shows on hover or focus of its message, and always on touch. */
function MessageActions({
  align,
  text,
  onQuote,
  reveal = false,
}: {
  align: 'start' | 'end'
  text: () => string
  onQuote?: () => void
  reveal?: boolean
}) {
  const { t } = useTranslation('common')
  return (
    <div
      className={cn(
        'mt-0.5 flex items-center gap-2',
        align === 'end' ? 'justify-end' : 'justify-start',
        reveal &&
          'opacity-0 transition-opacity group-focus-within/msg:opacity-100 group-hover/msg:opacity-100 pointer-coarse:opacity-100',
      )}
    >
      <CopyButton
        text={text}
        label={t('ui.copy.message')}
        copiedLabel={t('ui.copy.copied')}
        failedLabel={t('ui.copy.failed')}
      />
      {onQuote ? (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label={t('ui.quoteReply')}
          title={t('ui.quoteReply')}
          onMouseDown={(event) => event.preventDefault()}
          onClick={onQuote}
        >
          <Quote aria-hidden="true" />
        </Button>
      ) : null}
    </div>
  )
}

/** A long session gets a frame at a time: only the row whose item changed paints again. */
const ConversationRow = memo(
  ConversationRowView,
  (a, b) =>
    a.runRef === b.runRef &&
    a.selected === b.selected &&
    a.onInspect === b.onInspect &&
    a.onQuote === b.onQuote &&
    a.onFork === b.onFork &&
    a.lastReply === b.lastReply &&
    samePlain(a.item, b.item),
)

/** A thread part's key, as the list keys it. */
function partKey(part: Part): string {
  return part.kind === 'work' ? `work-${part.items[0]!.id}` : part.item.id
}
