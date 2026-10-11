// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NARRATIVE AS A CONVERSATION. Attach frames are mapped before they are
// painted: the default view is operator, assistant, tool, system, result. The
// raw line is one click away (inspect), never the row itself.
import { Check, Eye, Pencil, Terminal, Wrench } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'
import { useRunAttach } from '@/features/agentops/attach'
import type { AttachFrame, RunDTO } from '@/features/agentops/types'
import { formatDuration, formatInt } from '@/lib/format'
import '@/features/shared/i18n'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import {
  conversationCwd,
  mapConversationFrames,
  type ConversationItem,
} from './conversation-frames'
import { systemFailed, systemText } from './conversation-text'
import { hostIsolationUnavailable } from './provenance'
import { unechoedTurns, useRunSentTurns, type SentTurn } from './sent-turns'
import './i18n'

const MAX_FRAMES = 5000

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
}: {
  run: RunDTO
  selectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  /** The folder the tool's own first frame names, for the Context pane to say. */
  onFolder?: (cwd: string | null) => void
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

  const onFrame = useCallback((f: AttachFrame) => {
    setFrames((prev) => {
      const next =
        prev.length >= MAX_FRAMES
          ? prev.slice(prev.length - MAX_FRAMES + 1)
          : prev
      return [...next, f]
    })
  }, [])
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
      mapConversationFrames([
        ...(history ? [history] : []),
        ...frames.map((f) => f.line),
      ]).filter((item) => item.systemKind !== 'protocol'),
    [frames, history],
  )
  // The first message the console sent, until the tool shows it itself (HU2-27).
  const sent = useRunSentTurns(run.run_ref)
  const pending = useMemo(
    () => (sent ? unechoedTurns(items, sent) : []),
    [items, sent],
  )
  const frameCwd = conversationCwd(items)
  useEffect(() => {
    onFolder?.(frameCwd ?? null)
    return () => onFolder?.(null)
  }, [frameCwd, onFolder])

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

  return (
    <div
      className="flex min-h-0 flex-1 flex-col gap-2"
      data-testid="session-conversation"
    >
      {dropped > 0 ? (
        <p className="text-caption text-warning">
          {t('conversation.lag', { count: dropped })}
        </p>
      ) : null}
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
        <div className="mx-auto w-full max-w-[760px] px-4 py-4">
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
              {foldTools(items).map((part) =>
                part.kind === 'fold' ? (
                  <li key={`fold-${part.items[0]!.id}`}>
                    <details data-testid="conversation-tools-fold">
                      <summary className="flex min-h-8 cursor-pointer list-none items-center gap-2 rounded-ctl px-2 text-caption text-text-3 outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus [&::-webkit-details-marker]:hidden">
                        {t('conversation.toolsFolded', {
                          count: part.items.length,
                        })}
                      </summary>
                      <ol className="flex flex-col">
                        {part.items.map((item) => (
                          <ConversationRow
                            key={item.id}
                            item={item}
                            selected={selectedId === item.id}
                            onInspect={onInspect}
                          />
                        ))}
                      </ol>
                    </details>
                  </li>
                ) : (
                  <ConversationRow
                    key={part.item.id}
                    item={part.item}
                    selected={selectedId === part.item.id}
                    onInspect={onInspect}
                  />
                ),
              )}
              {pending.map((turn, i) => (
                <SentTurnRow key={`sent-${i}`} turn={turn} />
              ))}
            </ol>
          )}
        </div>
      </div>
      {ended ? (
        <p className="text-center text-overline font-normal text-text-3">
          {t('conversation.ended')}
        </p>
      ) : null}
    </div>
  )
}

/** Tool calls beyond the latest three in a row of them fold under "+N tool calls". */
const VISIBLE_TOOL_CALLS = 3

type Part =
  | { kind: 'item'; item: ConversationItem }
  | { kind: 'fold'; items: ConversationItem[] }

/**
 * The items as the thread paints them: a run of consecutive tool calls longer than
 * three keeps its latest three on the page, and the older ones fold into one line that
 * opens them. Nothing is dropped: every folded row is still in the document.
 */
function foldTools(items: readonly ConversationItem[]): Part[] {
  const parts: Part[] = []
  let at = 0
  while (at < items.length) {
    if (items[at]!.kind !== 'tool') {
      parts.push({ kind: 'item', item: items[at]! })
      at += 1
      continue
    }
    let end = at
    while (end < items.length && items[end]!.kind === 'tool') end += 1
    const run = items.slice(at, end)
    const folded = run.length - VISIBLE_TOOL_CALLS
    if (folded > 0) parts.push({ kind: 'fold', items: run.slice(0, folded) })
    for (const item of folded > 0 ? run.slice(folded) : run)
      parts.push({ kind: 'item', item })
    at = end
  }
  return parts
}

/** The glyph of a tool call: what it does to the folder, not which tool does it. */
function ToolGlyph({ name, command }: { name?: string; command?: string }) {
  const cls = 'size-3.5 shrink-0 text-text-3'
  if (command) return <Terminal aria-hidden className={cls} />
  switch ((name ?? '').toLowerCase()) {
    case 'read':
    case 'grep':
    case 'glob':
    case 'search':
      return <Eye aria-hidden className={cls} />
    case 'edit':
    case 'write':
    case 'multiedit':
      return <Pencil aria-hidden className={cls} />
    default:
      return <Wrench aria-hidden className={cls} />
  }
}

function ConversationRow({
  item,
  selected,
  onInspect,
}: {
  item: ConversationItem
  selected: boolean
  onInspect?: (item: ConversationItem) => void
}) {
  const { t } = useTranslation('sessions')
  const inspect = () => onInspect?.(item)

  if (item.kind === 'tool') {
    const target = item.toolCommand ?? item.toolArgsSummary
    return (
      <li>
        <details
          data-testid="conversation-item"
          data-kind="tool"
          className={cn('group rounded-ctl', selected && 'bg-active')}
        >
          {/* A TOOL CALL IS A 13 px ROW: the glyph, the verb, the target in the machine
              face, and at the right the check once the tool answered. */}
          <summary
            className={cn(
              'flex min-h-8 cursor-pointer list-none items-center gap-2 rounded-ctl px-2 py-1',
              'text-caption text-text outline-none hover:bg-hover',
              'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
              '[&::-webkit-details-marker]:hidden',
            )}
          >
            <ToolGlyph name={item.toolName} command={item.toolCommand} />
            <span className="shrink-0 font-medium">
              {item.toolName ?? t('conversation.tool')}
            </span>
            {target ? (
              <span
                className="min-w-0 flex-1 truncate font-mono text-text-2"
                title={item.toolCommand ?? target}
              >
                {target}
              </span>
            ) : (
              <span className="flex-1" />
            )}
            {item.toolFailed ? (
              <span className="shrink-0 text-danger">
                {t('conversation.toolFailed')}
              </span>
            ) : item.toolElapsedSeconds !== undefined ? (
              <span className="shrink-0 tabular-nums text-text-3">
                {formatDuration(item.toolElapsedSeconds * 1000)}
              </span>
            ) : item.toolResultSummary !== undefined ? (
              <Check aria-hidden className="size-3.5 shrink-0 text-text-3" />
            ) : null}
          </summary>
          <div className="space-y-1 px-8 pb-2 text-caption text-muted-foreground">
            {item.toolCommand ? (
              <>
                <pre
                  data-slot="tool-command"
                  className="m-0 whitespace-pre-wrap font-mono text-caption text-foreground [overflow-wrap:anywhere]"
                >
                  {item.toolCommand}
                </pre>
                {item.toolDescription ? <p>{item.toolDescription}</p> : null}
              </>
            ) : item.toolArgsSummary ? (
              <p>
                <span className="text-overline uppercase">
                  {t('conversation.args')}{' '}
                </span>
                {item.toolArgsSummary}
              </p>
            ) : null}
            {item.toolResultSummary ? (
              <p>
                <span className="text-overline uppercase">
                  {t('conversation.toolResult')}{' '}
                </span>
                {item.toolResultSummary}
              </p>
            ) : null}
            <InspectButton onClick={inspect} />
          </div>
        </details>
      </li>
    )
  }

  if (item.kind === 'system') {
    return (
      <li>
        <button
          type="button"
          data-testid="conversation-item"
          data-kind="system"
          onClick={inspect}
          className={cn(
            'flex min-h-7 w-full items-center gap-2 rounded-ctl px-2 py-0.5 text-left',
            'text-caption outline-none',
            // The engine's notice reads as the terminal's warning does (`session follow`).
            systemFailed(item)
              ? 'text-danger'
              : item.systemKind === 'notice'
                ? 'text-warning'
                : 'text-muted-foreground',
            'hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
            selected && 'bg-active text-text',
          )}
        >
          {/* A notice is the reason itself: it wraps instead of being cut on a phone. */}
          <span
            className={cn(
              'min-w-0',
              item.systemKind === 'notice' ? 'whitespace-normal' : 'truncate',
            )}
          >
            {systemText(t, item)}
          </span>
        </button>
      </li>
    )
  }

  if (item.kind === 'result') {
    const facts = [
      item.stopReason ? stopText(t, item.stopReason) : null,
      item.model,
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
    // THE END OF A TURN IS A DIVIDER, not a bare "Result" button: "Worked for 8s" when
    // the tool said how long, "Result" when it did not. It opens what the button showed
    // inline: how the turn stopped, the model, the tokens, the cost and the raw frame.
    // A failed turn keeps its duration: "Result · 8.0s", never a silent "Worked for".
    const label =
      item.durationMs === undefined
        ? t('conversation.result')
        : item.resultFailed
          ? `${t('conversation.result')} · ${formatDuration(item.durationMs)}`
          : t('conversation.workedFor', {
              duration: formatDuration(item.durationMs),
            })
    return (
      <li>
        <details
          data-testid="conversation-item"
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
        <button
          type="button"
          data-testid="conversation-item"
          data-kind="operator"
          onClick={inspect}
          className={cn(
            'w-full rounded-[12px] bg-muted px-4 py-3 text-left text-body-l text-text outline-none',
            'focus-visible:ring-2 focus-visible:ring-focus',
            selected && 'ring-1 ring-line-strong',
          )}
        >
          <span className="whitespace-pre-wrap break-words">
            {item.text || item.summary}
          </span>
        </button>
      </li>
    )

  if (item.kind === 'unknown')
    return (
      <li className="flex flex-col gap-0.5 py-1">
        <span className="text-overline uppercase text-text-3">
          {t('conversation.unknown')}
        </span>
        <button
          type="button"
          data-testid="conversation-item"
          data-kind="unknown"
          onClick={inspect}
          className={cn(
            'w-full rounded-md border border-line px-2.5 py-1.5 text-left text-body text-text outline-none',
            'focus-visible:ring-2 focus-visible:ring-focus',
            selected && 'bg-active',
          )}
        >
          <span className="whitespace-pre-wrap break-words">
            {item.text || item.summary}
          </span>
        </button>
      </li>
    )

  return (
    <li className="py-1">
      <button
        type="button"
        data-testid="conversation-item"
        data-kind={item.kind}
        onClick={inspect}
        className={cn(
          'w-full rounded-ctl text-left text-body-l text-text outline-none',
          'focus-visible:ring-2 focus-visible:ring-focus',
          selected && 'bg-active',
        )}
      >
        <span className="whitespace-pre-wrap break-words">
          {item.text || item.summary}
        </span>
      </button>
    </li>
  )
}

/** The person's message as the console sent it: there is no frame to inspect. A message
 * the engine refused says so, with the engine's reason, in the danger color; one held
 * for its launch's approval says it goes when the session runs. */
function SentTurnRow({ turn }: { turn: SentTurn }) {
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
      ) : null}
    </li>
  )
}

function InspectButton({ onClick }: { onClick?: () => void }) {
  const { t } = useTranslation('sessions')
  if (!onClick) return null
  return (
    <button
      type="button"
      onClick={onClick}
      className="text-caption text-text-2 underline underline-offset-[3px] hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
    >
      {t('conversation.inspect')}
    </button>
  )
}
