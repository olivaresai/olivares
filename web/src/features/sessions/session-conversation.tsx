// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NARRATIVE AS A CONVERSATION. Attach frames are mapped before they are
// painted: the default view is operator, assistant, tool, system, result. The
// raw line is one click away (inspect), never the row itself.
import { ChevronDown, Wrench } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LiveDot } from '@/features/shared'
import { useRunAttach } from '@/features/agentops/attach'
import type { AttachFrame, RunDTO } from '@/features/agentops/types'
import { formatDuration, formatInt } from '@/lib/format'
import { cn } from '@/lib/utils'
import {
  conversationCwd,
  mapConversationFrames,
  type ConversationItem,
} from './conversation-frames'
import './i18n'

const MAX_FRAMES = 5000

export function SessionConversation({
  run,
  selectedId,
  onInspect,
  workspaceRef,
}: {
  run: RunDTO
  selectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  /** Absent workspace is a value: the warning names the driver's cwd rather than hiding it. */
  workspaceRef?: string | null
}) {
  const { t } = useTranslation('sessions')
  const [frames, setFrames] = useState<AttachFrame[]>([])
  const [dropped, setDropped] = useState(0)
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
    onLag,
  })

  const items = useMemo(
    () => mapConversationFrames(frames.map((f) => f.line)),
    [frames],
  )
  const cwd = conversationCwd(items)
  const showCwdWarning = !workspaceRef && !!cwd

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
      className="flex min-h-0 flex-col gap-2"
      data-testid="session-conversation"
    >
      <div className="flex items-center gap-2 text-caption text-muted-foreground">
        <LiveDot status={status} />
        {ended ? <span>{t('conversation.ended')}</span> : null}
      </div>
      {dropped > 0 ? (
        <p className="text-caption text-warning">
          {t('conversation.lag', { count: dropped })}
        </p>
      ) : null}
      {showCwdWarning ? (
        <p
          data-testid="conversation-cwd-warning"
          className="rounded-md border border-warning-line bg-warning-soft px-2.5 py-1.5 text-caption text-warning"
        >
          {t('conversation.cwdWarning', { cwd })}
        </p>
      ) : null}
      <div
        ref={scrollRef}
        onScroll={onScroll}
        tabIndex={0}
        role="log"
        aria-label={t('conversation.log')}
        className="min-h-0 flex-1 overflow-auto focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background"
      >
        {items.length === 0 ? (
          <p className="px-1 py-2 text-caption text-muted-foreground">
            {status === 'open'
              ? t('conversation.waiting')
              : t('conversation.empty')}
          </p>
        ) : (
          <ol className="flex flex-col">
            {items.map((item) => (
              <ConversationRow
                key={item.id}
                item={item}
                selected={selectedId === item.id}
                onInspect={onInspect}
              />
            ))}
          </ol>
        )}
      </div>
    </div>
  )
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
    return (
      <li>
        <details
          data-testid="conversation-item"
          data-kind="tool"
          className={cn(
            'group border-l-2 border-transparent',
            'hover:bg-muted',
            selected && 'border-l-accent bg-accent-soft',
          )}
        >
          <summary
            className={cn(
              'flex min-h-9 cursor-pointer list-none items-center gap-2 px-2 py-1',
              'text-caption text-foreground outline-none',
              'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset',
              '[&::-webkit-details-marker]:hidden',
            )}
          >
            <ChevronDown
              className="size-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-0 -rotate-90"
              aria-hidden
            />
            <Wrench
              className="size-3.5 shrink-0 text-muted-foreground"
              aria-hidden
            />
            <span className="min-w-0 flex-1 truncate font-medium">
              {item.toolName ?? t('conversation.tool')}
            </span>
            {item.toolArgsSummary ? (
              <span className="min-w-0 max-w-[50%] truncate text-muted-foreground">
                {item.toolArgsSummary}
              </span>
            ) : null}
          </summary>
          <div className="space-y-1 px-8 pb-2 text-caption text-muted-foreground">
            {item.toolArgsSummary ? (
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
            'flex w-full min-h-7 items-center gap-2 border-l-2 border-transparent px-2 py-0.5 text-left',
            'text-caption text-muted-foreground outline-none',
            'hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset',
            selected && 'border-l-accent bg-accent-soft text-foreground',
          )}
        >
          <span className="min-w-0 truncate">{item.summary}</span>
        </button>
      </li>
    )
  }

  if (item.kind === 'result') {
    const facts = [
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
      item.durationMs !== undefined ? formatDuration(item.durationMs) : null,
    ].filter(Boolean)
    return (
      <li>
        <button
          type="button"
          data-testid="conversation-item"
          data-kind="result"
          onClick={inspect}
          className={cn(
            'flex w-full flex-wrap items-center gap-x-2 gap-y-0.5 border-l-2 border-transparent px-2 py-1.5 text-left',
            'text-caption text-muted-foreground outline-none',
            'hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset',
            selected && 'border-l-accent bg-accent-soft text-foreground',
          )}
        >
          <span className="font-medium text-foreground">
            {t('conversation.result')}
          </span>
          {facts.map((fact) => (
            <span key={String(fact)}>· {fact}</span>
          ))}
        </button>
      </li>
    )
  }

  const align = item.kind === 'operator' ? 'items-end' : 'items-start'
  const bubble =
    item.kind === 'operator'
      ? 'bg-accent-soft text-foreground'
      : item.kind === 'unknown'
        ? 'border border-border bg-surface text-foreground'
        : 'bg-muted text-foreground'

  return (
    <li className={cn('flex flex-col gap-0.5 px-2 py-1', align)}>
      <span className="text-overline uppercase text-muted-foreground">
        {t(`conversation.${item.kind}`)}
      </span>
      <button
        type="button"
        data-testid="conversation-item"
        data-kind={item.kind}
        onClick={inspect}
        className={cn(
          'max-w-[42rem] rounded-md px-2.5 py-1.5 text-left text-body leading-snug outline-none',
          'border-l-2 border-transparent',
          'focus-visible:ring-2 focus-visible:ring-ring',
          'hover:brightness-[1.03]',
          bubble,
          selected && 'border-l-accent ring-1 ring-accent-line',
        )}
      >
        <span className="whitespace-pre-wrap break-words">
          {item.text || item.summary}
        </span>
      </button>
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
      className="text-caption text-accent-text underline-offset-2 hover:underline focus-visible:ring-2 focus-visible:ring-ring"
    >
      {t('conversation.inspect')}
    </button>
  )
}
