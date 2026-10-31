// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE WORK BETWEEN TWO REPLIES. The conversation folds the calls between two
// other rows into one work row that says what they did; open, it is one line per call, an
// edit with its diff, a subagent under its Agent row; while the last row's calls run it is
// one live line with their clock. session-conversation.tsx paints the rest of the thread.
import {
  Bot,
  Check,
  ChevronRight,
  Eye,
  Pencil,
  Terminal,
  Wrench,
} from 'lucide-react'
import { memo, type MouseEvent, type ReactNode } from 'react'
import type { TFunction } from 'i18next'
import { Trans, useTranslation } from 'react-i18next'
import { PathText } from '@/components/ui/path-text'
import { formatDuration } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { ConversationItem } from './conversation-frames'
import { MarkdownText } from './markdown-text'
import { samePlain } from './same-plain'
import { parseMarkdown, plainText } from './markdown'
import { ToolTarget } from './tool-target'
import { useNow } from './use-now'
import './i18n'

/** Each Agent call's own calls and replies, by its call id, and every item's place in
 * the thread: a call holds only what came after it, so no row can hold itself. */
interface Nested {
  byCall: ReadonlyMap<string, ConversationItem[]>
  place: ReadonlyMap<ConversationItem, number>
}

export type Part =
  | { kind: 'item'; item: ConversationItem }
  | { kind: 'card'; item: ConversationItem }
  | { kind: 'work'; items: ConversationItem[]; nested: Nested }

/**
 * The items as the thread paints them. The calls between two other rows (a reply,
 * the person's turn, the end of a turn, a notice) are one work row; a subagent's calls and
 * replies belong to the Agent call that started it, wherever they arrive. Nothing is
 * dropped: every call is still in the document, one click away.
 */
export function threadParts(
  items: readonly ConversationItem[],
  authoritativeTodos = false,
): Part[] {
  const calls = new Set<string>()
  const byCall = new Map<string, ConversationItem[]>()
  const place = new Map<ConversationItem, number>()
  const nested: Nested = { byCall, place }
  const parts: Part[] = []
  // A todo call sends the whole list each time: only the newest list is a card, the older
  // calls stay in the work rows.
  const lastTodos = items
    .map((i) => i.toolCard?.kind === 'todos' && !i.parentToolId)
    .lastIndexOf(true)
  for (const [index, item] of items.entries()) {
    place.set(item, index)
    const parent = item.parentToolId
    if (item.kind === 'tool' && item.toolId) calls.add(item.toolId)
    if (parent && parent !== item.toolId && calls.has(parent)) {
      const own = byCall.get(parent)
      if (own) own.push(item)
      else byCall.set(parent, [item])
      continue
    }
    const last = parts[parts.length - 1]
    if (item.kind !== 'tool') parts.push({ kind: 'item', item })
    else if (
      item.toolCard &&
      (item.toolCard.kind !== 'todos' ||
        (!authoritativeTodos && index === lastTodos))
    )
      parts.push({ kind: 'card', item })
    else if (last?.kind === 'work') last.items.push(item)
    else parts.push({ kind: 'work', items: [item], nested })
  }
  return parts
}

type Category = 'command' | 'edit' | 'agent' | 'read' | 'search' | 'other'

/** What a call does to the folder, by its tool: the words and the glyph of its row. */
function categoryOf(item: ConversationItem): Category {
  if (item.toolCommand) return 'command'
  switch ((item.toolName ?? '').toLowerCase()) {
    case 'bash':
    case 'command':
    case 'shell':
      return 'command'
    case 'edit':
    case 'write':
    case 'multiedit':
    case 'notebookedit':
      return 'edit'
    case 'agent':
    case 'task':
      return 'agent'
    case 'read':
      return 'read'
    case 'grep':
    case 'glob':
    case 'ls':
    case 'search':
      return 'search'
    default:
      return 'other'
  }
}

/** A call runs in the foreground: the tool says so, or it has neither an answer, a
 * failure nor an outcome yet. A call in the background is not waited on. */
export function isRunning(item: ConversationItem): boolean {
  if (item.toolFailed) return false
  if (item.toolStatus !== undefined) return item.toolStatus === 'running'
  return !item.toolBackground && item.toolResultSummary === undefined
}

/** The call answered, or the task it started completed: done, and only then counted. */
function isAnswered(item: ConversationItem): boolean {
  if (item.toolFailed) return false
  if (item.toolStatus !== undefined) return item.toolStatus === 'completed'
  return !item.toolBackground && item.toolResultSummary !== undefined
}

/** How long a call has run: the tool's own clock, or the time since it was called. */
function elapsedSeconds(item: ConversationItem, now: number): number {
  const since = item.at ? (now - Date.parse(item.at)) / 1000 : 0
  return Math.max(
    item.toolElapsedSeconds ?? 0,
    Number.isFinite(since) ? since : 0,
  )
}

const RANK: Category[] = ['command', 'edit', 'agent', 'read', 'search', 'other']

/**
 * What the calls did, in words (T3's activity rule): at most two kinds, commands and edits
 * before agents, reads and searches. A failed call is never counted as done; it is named on
 * its own. Files are counted by their path, and where one is missing the words say "files"
 * rather than guess a number.
 */
function workWords(
  t: TFunction,
  items: readonly ConversationItem[],
): { done: string; failed: string | null } {
  // A call counts once it answered; one that failed is named; one stopped or never
  // answered is neither done nor failed, and stays one click away.
  const done = items.filter(isAnswered)
  const failed = items.filter((item) => item.toolFailed).length
  const stopped = items.filter((item) => item.toolStatus === 'stopped').length
  const background = items.filter(
    (item) =>
      item.toolBackground && item.toolStatus === undefined && !item.toolFailed,
  ).length
  const files = (list: ConversationItem[]) =>
    list.some((item) => !item.toolArgsSummary)
      ? undefined
      : new Set(list.map((item) => item.toolArgsSummary)).size
  const named = RANK.flatMap((category) => {
    const list = done.filter((item) => categoryOf(item) === category)
    if (list.length === 0) return []
    const count = files(list)
    switch (category) {
      case 'command':
        return [t('conversation.work.ran', { count: list.length })]
      case 'edit':
        return [
          count === undefined
            ? t('conversation.work.editedSome')
            : t('conversation.work.edited', { count }),
        ]
      case 'agent':
        return [t('conversation.work.agents', { count: list.length })]
      case 'read':
        return [
          count === undefined
            ? t('conversation.work.readSome')
            : t('conversation.work.read', { count }),
        ]
      case 'search':
        return [t('conversation.work.searched', { count: list.length })]
      default:
        return [t('conversation.work.used', { count: list.length })]
    }
  }).slice(0, 2)
  const still = [
    background > 0
      ? t('conversation.work.background', { count: background })
      : '',
    stopped > 0 ? t('conversation.work.stopped', { count: stopped }) : '',
  ].filter(Boolean)
  // Calls that neither answered, failed, stopped nor run in the background (a session that
  // ended under them) are told in the broader words: no row is left without any.
  const words = [...named, ...still]
  if (words.length === 0 && failed === 0)
    words.push(t('conversation.work.used', { count: items.length }))
  return {
    done: words.join(t('conversation.work.join')),
    failed:
      failed > 0 ? t('conversation.work.failed', { count: failed }) : null,
  }
}

/** From the first call to the last answer, where the engine said when. */
function workDuration(items: readonly ConversationItem[]): number | undefined {
  const times = items
    .flatMap((item) => [item.at, item.toolEndAt])
    .flatMap((at) => (at ? [Date.parse(at)] : []))
    .filter(Number.isFinite)
  if (times.length < 2) return undefined
  return Math.max(...times) - Math.min(...times)
}

const capitalized = (words: string) =>
  words.charAt(0).toLocaleUpperCase() + words.slice(1)

/** Text shown as it is, in a Trans slot: React escapes a child, so nothing in it is markup. */
function PlainText({ text }: { text: string }) {
  return <>{text}</>
}

/** The live line of the calls that run: what they do, and for how long. A file the last
 * call reads or edits is named through PathText, whole (#3135). */
function liveWords(
  t: TFunction,
  running: readonly ConversationItem[],
  now: number,
): ReactNode {
  const lastCall = running[running.length - 1]!
  const category = categoryOf(lastCall)
  const target = lastCall.toolArgsSummary ?? ''
  const seconds = Math.floor(
    Math.max(...running.map((item) => elapsedSeconds(item, now))),
  )
  const duration = seconds >= 1 ? ` · ${formatDuration(seconds * 1000)}` : ''
  // Every category whose call names a path (read, edit, search) names it through PathText;
  // the words before it ("TODO · " of a Grep) stay text. Agent, command and other name none.
  const path = lastCall.toolPath
  if (
    path &&
    target.endsWith(path) &&
    (category === 'read' || category === 'edit' || category === 'search')
  )
    return (
      <span className="flex min-w-0 items-center whitespace-pre">
        <Trans
          t={t}
          i18nKey={`conversation.running.${category}Path`}
          components={{
            // The agent's own text (a Grep's pattern) is a child, never markup: Trans
            // parses only the catalog sentence.
            lead: (
              <PlainText text={target.slice(0, target.length - path.length)} />
            ),
            path: (
              <PathText value={path} copy={false} tone="muted" size="small" />
            ),
          }}
        />
        {duration}
      </span>
    )
  const what =
    category === 'command'
      ? t('conversation.running.command', {
          count: running.filter((item) => categoryOf(item) === 'command')
            .length,
        })
      : category === 'agent'
        ? t('conversation.running.agent', {
            name: target || lastCall.toolName,
          })
        : category === 'other' || !target
          ? t('conversation.running.tool', {
              tool: lastCall.toolName ?? t('conversation.tool'),
            })
          : t(`conversation.running.${category}`, { target })
  return `${what}${duration}`
}

/**
 * THE WORK BETWEEN TWO REPLIES IS ONE ROW (the regression, T3 "Worked for 22s ›"): "Worked for
 * 1m 12s · ran 3 commands, edited 2 files", closed. Open, it is one line per call, an edit
 * with its diff, a subagent under its Agent row. While its calls run it is the live line
 * instead: "Running 1 shell command · 1m 30s", with Esc to interrupt where the turn can be.
 */
function WorkRowView({
  items,
  nested,
  runRef,
  selectedId,
  onInspect,
  live,
  running,
  canInterrupt,
}: {
  items: ConversationItem[]
  nested: Nested
  runRef: string
  selectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  live: boolean
  running: readonly ConversationItem[]
  canInterrupt: boolean
}) {
  const { t } = useTranslation('sessions')
  const words = workWords(t, items)
  const duration = workDuration(items)
  const label =
    duration !== undefined && duration >= 1000
      ? [
          t('conversation.workedFor', { duration: formatDuration(duration) }),
          words.done,
        ]
          .filter(Boolean)
          .join(' · ')
      : capitalized(words.done)
  return (
    <li>
      <details
        data-testid="conversation-work"
        className="group/work rounded-ctl"
      >
        <summary
          className={cn(
            'flex min-h-8 cursor-pointer list-none items-center gap-2 rounded-ctl px-2 py-1',
            'text-caption text-text-3 outline-none hover:bg-hover hover:text-text-2',
            'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
            '[&::-webkit-details-marker]:hidden',
          )}
        >
          {running.length > 0 ? (
            <LiveLine running={running} canInterrupt={canInterrupt} />
          ) : (
            <span className="min-w-0 truncate tabular-nums">
              {label}
              {words.failed ? (
                <>
                  {label ? ' · ' : ''}
                  <span className="text-danger">{words.failed}</span>
                </>
              ) : null}
            </span>
          )}
          <ChevronRight
            aria-hidden
            className="size-3.5 shrink-0 transition-transform group-open/work:rotate-90 motion-reduce:transition-none"
          />
        </summary>
        <ol className="flex flex-col pb-1">
          {items.map((item) => (
            <CallRow
              key={item.id}
              item={item}
              nested={nested}
              runRef={runRef}
              selectedId={selectedId}
              onInspect={onInspect}
              live={live}
            />
          ))}
        </ol>
      </details>
    </li>
  )
}

/** The live line of the calls that run, with their clock: only this line ticks, the rest
 * of the thread does not paint again every second. The clock ticks inside the log, which a
 * screen reader reads out on every change, so the line itself is not announced. */
function LiveLine({
  running,
  canInterrupt,
}: {
  running: readonly ConversationItem[]
  canInterrupt: boolean
}) {
  const { t } = useTranslation('sessions')
  const now = useNow(true)
  return (
    <span
      data-testid="conversation-live"
      aria-live="off"
      className="flex min-w-0 flex-1 items-center gap-2 text-text-2"
    >
      <span className="min-w-0 truncate tabular-nums">
        {liveWords(t, running, now)}
      </span>
      {canInterrupt ? (
        <span className="ms-auto shrink-0 text-text-3">
          {t('conversation.escToInterrupt')}
        </span>
      ) : null}
    </span>
  )
}

/** One call of a work row: an Agent call with its subagent's work, or a tool call. */
function CallRow({
  item,
  nested,
  ...rest
}: {
  item: ConversationItem
  nested: Nested
  runRef: string
  selectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  live: boolean
}) {
  const at = nested.place.get(item) ?? -1
  const own = (
    item.toolId ? nested.byCall.get(item.toolId) : undefined
  )?.filter((child) => (nested.place.get(child) ?? -1) > at)
  if (categoryOf(item) === 'agent' || own?.length)
    return <AgentRow item={item} own={own ?? []} nested={nested} {...rest} />
  return (
    <ToolRow
      item={item}
      selected={rest.selectedId === item.id}
      onInspect={rest.onInspect}
      live={rest.live}
    />
  )
}

/**
 * A SUBAGENT IS ONE ROW UNDER ITS AGENT CALL: its name, and its one-line result
 * once it has one; folded, its own calls one line each, and its words.
 */
function AgentRow({
  item,
  own,
  nested,
  runRef,
  selectedId,
  onInspect,
  live,
}: {
  item: ConversationItem
  own: ConversationItem[]
  nested: Nested
  runRef: string
  selectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  live: boolean
}) {
  const { t } = useTranslation('sessions')
  const replies = own.filter(
    (child) => child.kind === 'assistant' && child.text,
  )
  const lastWords = replies[replies.length - 1]?.text
  const result =
    (lastWords
      ? plainText(parseMarkdown(lastWords))
          .split('\n')
          .find((line) => line.trim())
      : undefined) ?? (item.toolBackground ? undefined : item.toolResultSummary)
  const working =
    live &&
    !item.toolFailed &&
    item.toolStatus !== 'stopped' &&
    !isAnswered(item)
  return (
    <li>
      <details
        data-testid="conversation-agent"
        data-item-id={item.id}
        className={cn(
          'group/agent rounded-ctl',
          selectedId === item.id && 'bg-active',
        )}
      >
        <summary
          className={cn(
            'flex min-h-8 cursor-pointer list-none items-center gap-2 rounded-ctl px-2 py-1',
            'text-caption text-text outline-none hover:bg-hover',
            'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
            '[&::-webkit-details-marker]:hidden',
          )}
        >
          <Bot aria-hidden className="size-3.5 shrink-0 text-text-3" />
          <span className="shrink-0 font-medium">
            {item.toolArgsSummary || item.toolName || t('conversation.tool')}
          </span>
          <span className="min-w-0 flex-1 truncate text-text-2" title={result}>
            {result}
          </span>
          {item.toolFailed ? (
            <span className="shrink-0 text-danger">
              {t('conversation.toolFailed')}
            </span>
          ) : working ? (
            <span className="shrink-0 text-text-3">
              {t('conversation.agentWorking')}
            </span>
          ) : isAnswered(item) ? (
            <Check aria-hidden className="size-3.5 shrink-0 text-text-3" />
          ) : null}
        </summary>
        <ol className="ms-4 flex flex-col border-s border-line ps-2">
          {own.map((child) =>
            child.kind === 'tool' ? (
              <CallRow
                key={child.id}
                item={child}
                nested={nested}
                runRef={runRef}
                selectedId={selectedId}
                onInspect={onInspect}
                live={live}
              />
            ) : child.text ? (
              <li
                key={child.id}
                data-slot="agent-words"
                data-item-id={child.id}
                onClick={
                  onInspect ? openOnWords(() => onInspect(child)) : undefined
                }
                className="px-2 py-1 text-caption text-text-2 [overflow-wrap:anywhere]"
              >
                <MarkdownText text={child.text} />
                <KeyboardInspect
                  onInspect={onInspect ? () => onInspect(child) : undefined}
                />
              </li>
            ) : null,
          )}
          {item.toolFailed && item.toolResultSummary ? (
            <li className="px-2 py-1 text-caption text-danger [overflow-wrap:anywhere]">
              {item.toolResultSummary}
            </li>
          ) : null}
          {onInspect ? (
            <li className="px-2 py-1">
              <InspectButton onClick={() => onInspect(item)} />
            </li>
          ) : null}
        </ol>
      </details>
    </li>
  )
}

/** The glyph of a call: what it does to the folder, not which tool does it. */
function ToolGlyph({ item }: { item: ConversationItem }) {
  const cls = 'size-3.5 shrink-0 text-text-3'
  switch (categoryOf(item)) {
    case 'command':
      return <Terminal aria-hidden className={cls} />
    case 'read':
    case 'search':
      return <Eye aria-hidden className={cls} />
    case 'edit':
      return <Pencil aria-hidden className={cls} />
    case 'agent':
      return <Bot aria-hidden className={cls} />
    default:
      return <Wrench aria-hidden className={cls} />
  }
}

/** The first lines of an edit's diff, each marked added, removed or unchanged. */
function DiffExcerpt({ lines }: { lines: string[] }) {
  return (
    <div
      data-testid="conversation-diff"
      className="mx-2 mb-1 ms-8 overflow-hidden rounded-ctl border border-line font-mono text-caption"
    >
      {lines.map((line, i) => (
        <div
          key={i}
          className={cn(
            'flex gap-2 px-2',
            line[0] === '+' && 'bg-success-soft text-success',
            line[0] === '-' && 'bg-danger-soft text-danger',
            line[0] === ' ' && 'text-text-2',
          )}
        >
          <span className="w-2 shrink-0 select-none">
            {line[0] === ' ' ? '' : line[0]}
          </span>
          <span className="min-w-0 truncate whitespace-pre">
            {line.slice(1)}
          </span>
        </div>
      ))}
    </div>
  )
}

/**
 * A TOOL CALL IS A 13 px ROW: the glyph, the verb, the target in its short form (the regression:
 * never JSON), an edit's +N −M, and at the right how it stands: failed, in the background,
 * its clock while it runs, a check once it answered. An edit's excerpt sits under it.
 */
function ToolRow({
  item,
  selected,
  onInspect,
  live,
}: {
  item: ConversationItem
  selected: boolean
  onInspect?: (item: ConversationItem) => void
  live: boolean
}) {
  const { t } = useTranslation('sessions')
  const inspect = () => onInspect?.(item)
  const target = item.toolArgsSummary || item.toolCommand
  // A failed edit changed nothing: its diff is not shown as if it had.
  const diff = item.toolFailed ? undefined : item.toolDiff
  return (
    <li>
      <details
        data-testid="conversation-item"
        data-item-id={item.id}
        data-kind="tool"
        className={cn('group rounded-ctl', selected && 'bg-active')}
      >
        <summary
          className={cn(
            'flex min-h-8 cursor-pointer list-none items-center gap-2 rounded-ctl px-2 py-1',
            'text-caption text-text outline-none hover:bg-hover',
            'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
            '[&::-webkit-details-marker]:hidden',
          )}
        >
          <ToolGlyph item={item} />
          <span className="shrink-0 font-medium">
            {item.toolName ?? t('conversation.tool')}
          </span>
          {item.toolPath && !item.toolCommand ? (
            // The path whole through PathText; no copy button inside a <summary>.
            <span className="flex min-w-0 flex-1 items-center whitespace-pre font-mono text-text-2">
              <ToolTarget item={item} copy={false} />
            </span>
          ) : target ? (
            <span
              className="min-w-0 flex-1 truncate font-mono text-text-2"
              title={item.toolCommand ?? target}
            >
              {target}
            </span>
          ) : (
            <span className="flex-1" />
          )}
          {diff && diff.added > 0 ? (
            <span className="shrink-0 tabular-nums text-success">
              +{diff.added}
            </span>
          ) : null}
          {diff && diff.removed > 0 ? (
            <span className="shrink-0 tabular-nums text-danger">
              −{diff.removed}
            </span>
          ) : null}
          {item.toolFailed ? (
            <span className="shrink-0 text-danger">
              {t('conversation.toolFailed')}
            </span>
          ) : item.toolStatus === 'stopped' ? (
            <span className="shrink-0 text-text-3">
              {t('conversation.toolStopped')}
            </span>
          ) : item.toolBackground && item.toolStatus === undefined ? (
            live ? (
              <span className="shrink-0 text-text-3">
                {t('conversation.background')}
              </span>
            ) : null
          ) : isAnswered(item) ? (
            <Check aria-hidden className="size-3.5 shrink-0 text-text-3" />
          ) : item.toolElapsedSeconds !== undefined ? (
            <span className="shrink-0 tabular-nums text-text-3">
              {formatDuration(item.toolElapsedSeconds * 1000)}
            </span>
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
              <ToolTarget item={item} wrap />
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
      {diff && diff.lines.length > 0 ? (
        <DiffExcerpt lines={diff.lines} />
      ) : null}
    </li>
  )
}

/**
 * A MESSAGE IS ITS WORDS, NOT A BUTTON: words inside a button cannot be selected.
 * A click on them opens the inspector, but not on a link or a button, not as the second
 * click of a double click, and not while the person is selecting text to copy it;
 * "Inspect" is the keyboard's way in, shown when it has focus.
 */
export function openOnWords(open: () => void) {
  return (e: MouseEvent<HTMLElement>) => {
    if ((e.target as Element).closest('a, button')) return
    if (e.detail > 1) return
    const selection = window.getSelection()
    if (
      selection &&
      !selection.isCollapsed &&
      e.currentTarget.contains(selection.anchorNode)
    )
      return
    open()
  }
}

/** Inspect for the keyboard, shown while it has focus: a pointer clicks the words. A row
 * whose text is read as only its words (a system line, a message) has it beside, not in. */
export function KeyboardInspect({ onInspect }: { onInspect?: () => void }) {
  if (!onInspect) return null
  return (
    <div className="sr-only focus-within:not-sr-only focus-within:mt-1">
      <InspectButton onClick={onInspect} />
    </div>
  )
}

export function InspectButton({ onClick }: { onClick?: () => void }) {
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

/** What a work row paints: its calls and, under each Agent call, the subagent's calls and replies that came after it
 * (CallRow's own reading of `nested`), at every depth. */
function paints(items: readonly ConversationItem[], nested: Nested): unknown[] {
  return items.map((item) => {
    const at = nested.place.get(item) ?? -1
    const own = (
      item.toolId ? nested.byCall.get(item.toolId) : undefined
    )?.filter((child) => (nested.place.get(child) ?? -1) > at)
    return own?.length ? [item, paints(own, nested)] : item
  })
}

type WorkRowProps = Parameters<typeof WorkRowView>[0]

/** A long session gets a frame at a time: only the work row whose content changed paints again. */
export const WorkRow = memo(
  WorkRowView,
  (a: WorkRowProps, b: WorkRowProps) =>
    a.runRef === b.runRef &&
    a.selectedId === b.selectedId &&
    a.onInspect === b.onInspect &&
    a.live === b.live &&
    a.canInterrupt === b.canInterrupt &&
    samePlain(a.running, b.running) &&
    samePlain(paints(a.items, a.nested), paints(b.items, b.nested)),
)
