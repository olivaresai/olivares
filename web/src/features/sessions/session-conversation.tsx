// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NARRATIVE AS A CONVERSATION. Attach frames are mapped before they are
// painted: the default view is operator, assistant, tool, system, result. The
// raw line is one click away (inspect), never the row itself.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { TFunction } from 'i18next'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { agentOpsKeys } from '@/features/agentops/api'
import { MessageImages } from '@/features/agentops/attachment-ui'
import { useRunAttach } from '@/features/agentops/attach'
import { readsNativeRecord } from '@/features/agentops/provider-contract'
import { modelName } from '@/features/agentops/model-name'
import { useTurnInterrupt } from '@/features/agentops/turn-interrupt'
import type { AttachFrame, RunDTO } from '@/features/agentops/types'
import { CopyButton } from '@/components/ui/copy-button'
import { formatDuration, formatInt } from '@/lib/format'
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
import { systemFailed, systemText } from './conversation-text'
import {
  InspectButton,
  isRunning,
  KeyboardInspect,
  openOnWords,
  threadParts,
  WorkRow,
} from './conversation-work'
import { MarkdownText } from './markdown-text'
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
import './i18n'

const MAX_FRAMES = 5000
/** The side pane's Activity is read again at most this often while the tool writes. */
const ACTIVITY_REFRESH_MS = 1_500

/** How an ACP turn stopped, in words; an outcome this console does not know is shown as
 * the agent sent it. */
function stopText(t: TFunction, reason: string): string {
  switch (reason) {
    case 'cancelled':
      return t('conversation.stop.cancelled')
    case 'refusal':
      return t('conversation.stop.refusal')
    case 'max_tokens':
      return t('conversation.stop.maxTokens')
    case 'max_turn_requests':
      return t('conversation.stop.maxTurnRequests')
    default:
      return reason
  }
}

export function SessionConversation({
  run,
  selectedId,
  onInspect,
  onFolder,
  onModel,
}: {
  run: RunDTO
  selectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  /** The folder the tool's own first frame names, for the Context pane to say. */
  onFolder?: (cwd: string | null) => void
  /** The model the session's own turns run on now, for the header to say. */
  onModel?: (model: string | null) => void
}) {
  const { t } = useTranslation('sessions')
  const { t: tShared } = useTranslation('shared')
  const [frames, setFrames] = useState<AttachFrame[]>([])
  const [history, setHistory] = useState<string | null>(null)
  const [dropped, setDropped] = useState(0)
  const generation = useSessionStore((s) => s.credentialGeneration)
  const tenant = useTenantStore((s) => s.activeTenant)
  const ownerKey = `${run.run_ref}\n${tenant ?? ''}\n${generation}`
  const [owner, setOwner] = useState(ownerKey)
  // Clear before painting a different run, tenant or signed-in identity.
  if (owner !== ownerKey) {
    setOwner(ownerKey)
    setFrames([])
    setHistory(null)
    setDropped(0)
  }
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
      setFrames((prev) => {
        const next =
          prev.length >= MAX_FRAMES
            ? prev.slice(prev.length - MAX_FRAMES + 1)
            : prev
        return [...next, f]
      })
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
    sessionKey: `${run.state}:${run.transport}`,
    onFrame,
    onHistory: setHistory,
    onLag,
  })

  // Protocol bookkeeping (handshakes, replies, lifecycle notifications) is no row: it read
  // "Protocol frames: N" between every turn and told nothing. Every frame stays one command
  // away under "In a terminal" (`session follow -o json`); a protocol error is still shown.
  const items = useMemo(
    () =>
      mapConversationFrames(
        [...(history ? [history] : []), ...frames.map((f) => f.line)],
        [...(history ? [undefined] : []), ...frames.map((f) => f.at)],
        [...(history ? [undefined] : []), ...frames.map((f) => f.seq)],
      ).filter((item) => item.systemKind !== 'protocol' && !isQuiet(item)),
    [frames, history],
  )
  // The first message the console sent, until the tool shows it itself (HU2-27).
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
  const frameModel = conversationModel(items)
  useEffect(() => {
    onModel?.(frameModel ?? null)
    return () => onModel?.(null)
  }, [frameModel, onModel])

  useEffect(() => {
    if (!autoscroll) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [items, autoscroll])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
    setAutoscroll(atBottom)
  }, [])

  // The message index is read again when the person has sent another message, not on
  // every burst of output: each read is a scan of the tool's transcript.
  const sentCount = useMemo(
    () => items.filter((item) => item.kind === 'operator').length,
    [items],
  )
  // A note the tool has shown is forgotten: its own row tells it from now on (dev#2787).
  useEffect(() => {
    if (echoed.length) useSentTurns.getState().drop(runRef, echoed)
  }, [echoed, runRef])
  // A message sent from now on is echoed by a frame after the newest one shown here
  // (dev#2787). Nothing is said before a frame arrives: after a remount, the newest frame
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

  // The person's messages as a rail and a panel (#2129). A jump leaves the newest output
  // behind: following it again is the person scrolling back down.
  const stopFollowing = useCallback(() => setAutoscroll(false), [])
  const messages = useMessageNavigator({
    runRef,
    // A legacy run names no driver: the engine answers for it, as it always did.
    enabled: !run.provider_driver || readsNativeRecord(run),
    items,
    scrollRef,
    onJump: stopFollowing,
  })

  // THE THREAD AS IT READS (dev#3119): the calls between two replies fold into one row, a
  // subagent's calls into its Agent row, and the calls of the last row, while they run,
  // are one live line with their clock.
  const parts = useMemo(() => threadParts(items), [items])
  const live = isLiveRun(run) && !ended
  const last = parts[parts.length - 1]
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
      className="flex min-h-0 flex-1 flex-col gap-2"
      data-testid="session-conversation"
    >
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
          className="min-h-0 flex-1 overflow-auto focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset"
        >
          {/* ONE CENTERED COLUMN, 760 px: the transcript is read, not scanned, and a line
            longer than that is a line nobody follows. */}
          {/* While the rail shows, the column keeps clear of it: a message's Copy sits at
              its right edge on a narrow pane (dev#2788). */}
          <div
            data-slot="conversation-column"
            className={cn(
              'mx-auto w-full max-w-[760px] px-4 py-4',
              messages.available && messages.marks.length > 0 && 'pe-12',
            )}
          >
            {/* A LINK THAT DROPPED OR IS COMING UP SAYS SO, ONCE, QUIETLY, while the turns
              already read stay on the page. Without it a stale transcript reads as a
              finished one. */}
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
              <ol className="flex flex-col gap-1">
                {parts.map((part, index) =>
                  part.kind === 'work' ? (
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
                      key={part.item.id}
                      item={part.item}
                      runRef={run.run_ref}
                      selected={selectedId === part.item.id}
                      onInspect={onInspect}
                    />
                  ),
                )}
                {pending.map((turn, i) => (
                  <SentTurnRow
                    key={`sent-${i}`}
                    turn={turn}
                    ended={!isLiveRun(run) && run.state !== 'waiting_approval'}
                  />
                ))}
              </ol>
            )}
          </div>
        </div>
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

function ConversationRow({
  item,
  runRef,
  selected,
  onInspect,
}: {
  item: ConversationItem
  runRef: string
  selected: boolean
  onInspect?: (item: ConversationItem) => void
}) {
  const { t } = useTranslation('sessions')
  const inspect = () => onInspect?.(item)
  const openFromWords = openOnWords(inspect)

  if (item.kind === 'system') {
    return (
      <li>
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
            {systemText(t, item)}
          </span>
        </div>
        <KeyboardInspect onInspect={onInspect ? inspect : undefined} />
      </li>
    )
  }

  if (item.kind === 'result') {
    const facts = [
      item.stopReason ? stopText(t, item.stopReason) : null,
      item.inputTokens !== undefined || item.outputTokens !== undefined
        ? t('conversation.tokens', {
            input: formatInt(item.inputTokens ?? 0),
            output: formatInt(item.outputTokens ?? 0),
          })
        : null,
      item.costUsd !== undefined
        ? new Intl.NumberFormat(undefined, {
            style: 'currency',
            currency: 'USD',
            maximumFractionDigits: 4,
          }).format(item.costUsd)
        : null,
    ].filter(Boolean)
    // THE END OF A TURN IS A DIVIDER, not a bare "Result" button: "Finished in 8s" when
    // the tool said how long, "Result" when it did not ("Worked for" is the work row's,
    // dev#3119). It opens what the button showed inline: how the turn stopped, the model,
    // the tokens, the cost and the raw frame. A failed turn keeps its duration:
    // "Result · 8.0s", never a silent "Finished in". Every turn names the model that
    // produced it (dev#2786).
    const label = [
      item.durationMs === undefined
        ? t('conversation.result')
        : item.resultFailed
          ? `${t('conversation.result')} · ${formatDuration(item.durationMs)}`
          : t('conversation.finishedIn', {
              duration: formatDuration(item.durationMs),
            }),
      item.model && modelName(item.model),
    ]
      .filter(Boolean)
      .join(' · ')
    return (
      <li>
        <details
          data-testid="conversation-item"
          data-item-id={item.id}
          data-kind="result"
          className={cn('group my-2 rounded-ctl', selected && 'bg-active')}
        >
          <summary
            className={cn(
              'flex cursor-pointer list-none items-center gap-3 rounded-ctl px-2 py-1 outline-none',
              'text-overline font-normal text-text-3 hover:text-text-2',
              'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
              '[&::-webkit-details-marker]:hidden',
            )}
          >
            <span aria-hidden className="h-px flex-1 bg-line" />
            <span
              className={cn(
                'tabular-nums',
                item.resultFailed && 'font-medium text-danger',
              )}
              title={item.model}
            >
              {label}
            </span>
            <span aria-hidden className="h-px flex-1 bg-line" />
          </summary>
          <div className="flex flex-col items-center gap-1 px-2 pb-1 text-center text-caption text-text-2">
            {facts.length > 0 ? <p>{facts.join(' · ')}</p> : null}
            <InspectButton onClick={inspect} />
          </div>
        </details>
        {/* A failed result's text IS the failure (F1C: a failed start showed only
            "Result" while the run's stored reason and the CLI had the sentence). A
            successful result's text repeats the last reply, so it stays out. */}
        {item.resultFailed && item.text ? (
          <p
            data-slot="result-failure"
            className="whitespace-pre-wrap break-words px-2 pb-1 text-caption text-danger"
          >
            {item.text}
          </p>
        ) : null}
      </li>
    )
  }

  // THE PERSON'S TURN IS A QUIET BLOCK AND THE ASSISTANT'S IS PLAIN TEXT. No right-aligned
  // bubble and no "YOU" / "ASSISTANT" label above it: who spoke is what the block is, and
  // the assistant is the page itself.
  if (item.kind === 'operator')
    return (
      <li className="my-2">
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
          <span className="whitespace-pre-wrap break-words">
            {item.text || item.summary}
          </span>
        </div>
        <KeyboardInspect onInspect={onInspect ? inspect : undefined} />
        {/* The images the person attached, from the engine's copy: they come back with
            the message after a reload. */}
        {item.attachments?.length ? (
          <MessageImages runRef={runRef} attachments={item.attachments} />
        ) : null}
        {item.text ? (
          <MessageActions align="end" text={() => item.text ?? ''} />
        ) : null}
      </li>
    )

  if (item.kind === 'unknown')
    return (
      <li className="flex flex-col gap-0.5 py-1">
        <span className="text-overline uppercase text-text-3">
          {t('conversation.unknown')}
        </span>
        <div
          data-testid="conversation-item"
          data-item-id={item.id}
          data-kind="unknown"
          onClick={openFromWords}
          className={cn(
            'w-full rounded-md border border-line px-2.5 py-1.5 text-left text-body text-text',
            selected && 'bg-active',
          )}
        >
          <span className="whitespace-pre-wrap break-words">
            {item.text || item.summary}
          </span>
        </div>
        <KeyboardInspect onInspect={onInspect ? inspect : undefined} />
      </li>
    )

  // THE ASSISTANT'S WORDS ARE MARKDOWN (#1784): lists, code and bold are shown as such. A
  // reply holds blocks and links, which a button cannot, so the row is the reply itself.
  return (
    <li className="py-1">
      <div
        data-testid="conversation-item"
        data-item-id={item.id}
        data-kind={item.kind}
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
          text={() => plainText(parseMarkdown(item.text ?? ''))}
        />
      ) : null}
    </li>
  )
}

/** The person's message as the console sent it: there is no frame to inspect. A message
 * the engine refused says so, with the engine's reason, in the danger color; one held
 * for its launch's approval says it goes when the session runs; one the engine took says
 * so until the tool shows it (dev#2787). */
function SentTurnRow({ turn, ended }: { turn: SentTurn; ended: boolean }) {
  const { t } = useTranslation('sessions')
  return (
    <li className="my-2 flex flex-col gap-1" data-testid="conversation-sent">
      <p className="rounded-[12px] bg-muted px-4 py-3 text-body-l text-text">
        <span className="whitespace-pre-wrap break-words">{turn.text}</span>
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
      <MessageActions align="end" text={() => turn.text} />
    </li>
  )
}

/** Under a message: Copy, its words as the person reads them (dev#2788). A message without
 * words (only images, or a reply with no text) has none: its row shows a label, not words. */
function MessageActions({
  align,
  text,
}: {
  align: 'start' | 'end'
  text: () => string
}) {
  const { t } = useTranslation('common')
  return (
    <div
      className={cn(
        'mt-0.5 flex items-center gap-2',
        align === 'end' ? 'justify-end' : 'justify-start',
      )}
    >
      <CopyButton
        text={text}
        label={t('ui.copy.message')}
        copiedLabel={t('ui.copy.copied')}
        failedLabel={t('ui.copy.failed')}
      />
    </div>
  )
}
