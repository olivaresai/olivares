// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MESSAGE NAVIGATOR (#2129): find and reach any point of a session. A rail of marks
// beside the conversation, one per message the person sent; a hover or focus shows its time
// and first line and a click scrolls to it. Beside the rail, a panel: every message in a
// list, a search over the whole session, and a date and time to go to. The index, the
// search and the time anchor are the engine's (GET /runs/{ref}/messages, read from the
// tool's own transcript, in pages), so a session of thousands of messages is never loaded
// whole and the CLI shows the same (`olivares session messages`).
import { useInfiniteQuery } from '@tanstack/react-query'
import { MessageSquareText, X } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuth } from '@/lib/auth/context'
import { formatDateTime, formatInt, formatShortDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { instantOf } from './message-rows'
import type { MessageNavigator } from './use-message-navigator'
import './i18n'

const RESULT_PAGE = 50
const SEARCH_DELAY_MS = 250

const clock = (at: string | undefined) => formatShortDateTime(at)

/** The rail: one mark per message sent, beside the conversation. */
export function MessageRail({ nav }: { nav: MessageNavigator }) {
  const { t } = useTranslation('sessions')
  const [shown, setShown] = useState<number | null>(null)
  if (!nav.available || nav.marks.length === 0) return null
  return (
    <nav
      aria-label={t('messages.rail')}
      data-testid="message-rail"
      className="absolute inset-y-2 right-4 z-10 flex w-6 flex-col overflow-y-auto"
    >
      <ul className="m-0 flex min-h-full list-none flex-col justify-between p-0">
        {nav.marks.map((m) => (
          <li key={m.id} className="relative">
            <button
              type="button"
              data-testid="message-mark"
              aria-label={t('messages.mark', {
                n: formatInt(m.id),
                time: clock(m.at),
                line: m.first_line,
              })}
              onClick={() => void nav.jump(m)}
              onMouseEnter={() => setShown(m.id)}
              onMouseLeave={() => setShown(null)}
              onFocus={() => setShown(m.id)}
              onBlur={() => setShown(null)}
              className="group flex h-6 w-6 items-center justify-end rounded-ctl pr-1 outline-none focus-visible:ring-2 focus-visible:ring-focus"
            >
              <span
                aria-hidden
                className="h-0.5 w-3 rounded-full bg-text-3 group-hover:bg-text group-focus-visible:bg-text"
              />
            </button>
            {shown === m.id ? (
              <span
                aria-hidden
                data-testid="message-preview"
                className="pointer-events-none absolute right-7 top-1/2 z-20 flex w-64 -translate-y-1/2 flex-col rounded-ctl border border-line bg-canvas px-2 py-1 text-caption text-text"
              >
                <span className="tabular-nums text-text-3">{clock(m.at)}</span>
                <span className="truncate">{m.first_line}</span>
              </span>
            ) : null}
          </li>
        ))}
      </ul>
    </nav>
  )
}

/** The toggle above the conversation, the panel it opens, and a message too far back for
 * the conversation this view holds. */
export function MessageBar({ nav }: { nav: MessageNavigator }) {
  const { t } = useTranslation('sessions')
  const panelId = useId()
  if (!nav.available)
    return nav.problem ? (
      <p
        role="status"
        data-testid="message-problem"
        className="px-4 text-right text-caption text-text-3"
      >
        {t(`messages.${nav.problem}`)}
      </p>
    ) : null
  return (
    <div data-testid="message-bar" className="flex flex-col gap-2">
      <div className="flex items-center justify-end px-2">
        <Button
          type="button"
          size="sm"
          variant="ghost"
          aria-expanded={nav.open}
          aria-controls={panelId}
          onClick={() => nav.setOpen(!nav.open)}
        >
          <MessageSquareText aria-hidden />
          {t('messages.title')}
          <span className="tabular-nums text-text-3">
            {formatInt(nav.total)}
          </span>
        </Button>
      </div>
      <p role="status" className="sr-only">
        {nav.notice}
      </p>
      {nav.jumpFailed ? (
        <p role="alert" className="px-4 text-caption text-danger">
          {t('messages.error')}
        </p>
      ) : null}
      {nav.problem ? (
        <p
          role="status"
          data-testid="message-problem"
          className="px-4 text-caption text-text-3"
        >
          {t(`messages.${nav.problem}`)}
        </p>
      ) : null}
      {nav.far ? (
        <section
          aria-label={t('messages.farTitle')}
          data-testid="message-far"
          className="mx-2 flex flex-col gap-1 rounded-ctl border border-line px-3 py-2"
        >
          <div className="flex items-center justify-between gap-2">
            <p className="text-caption text-text-3">
              {t('messages.farBody', {
                n: formatInt(nav.far.id),
                time: clock(nav.far.at),
              })}
            </p>
            <Button
              type="button"
              size="icon-sm"
              variant="ghost"
              aria-label={t('messages.close')}
              onClick={nav.closeFar}
            >
              <X aria-hidden />
            </Button>
          </div>
          <p className="max-h-48 overflow-auto whitespace-pre-wrap break-words text-body text-text">
            {nav.far.text ?? nav.far.first_line}
          </p>
        </section>
      ) : null}
      {nav.open ? <MessagePanel nav={nav} id={panelId} /> : null}
    </div>
  )
}

function MessagePanel({ nav, id }: { nav: MessageNavigator; id: string }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const [text, setText] = useState('')
  const [query, setQuery] = useState('')
  const [answers, setAnswers] = useState(false)
  const [when, setWhen] = useState('')
  const [missing, setMissing] = useState(false)
  const [timeFailed, setTimeFailed] = useState(false)
  const searchId = useId()
  const timeId = useId()
  const answersId = useId()

  useEffect(() => {
    const timer = window.setTimeout(
      () => setQuery(text.trim()),
      SEARCH_DELAY_MS,
    )
    return () => window.clearTimeout(timer)
  }, [text])

  const list = useInfiniteQuery({
    queryKey: [
      ...agentOpsKeys.runMessages(activeTenant, nav.runRef),
      'list',
      query,
      answers,
    ],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      agentOpsApi.messages(
        nav.runRef,
        {
          q: query || undefined,
          role: answers ? 'all' : undefined,
          order: 'desc',
          limit: RESULT_PAGE,
          cursor: pageParam,
        },
        { signal },
      ),
    getNextPageParam: (last) => last.next,
    // A search keeps the rows it had while the next ones come, of this session only.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[3] === nav.runRef ? previous : undefined,
    refetchOnWindowFocus: false,
    retry: false,
  })
  // Pages come newest first, each oldest first inside: the list reads newest first.
  const rows = useMemo(
    () => list.data?.pages.flatMap((page) => [...page.items].reverse()) ?? [],
    [list.data],
  )
  const shown = list.data?.pages[0]?.total ?? 0

  const goToTime = async () => {
    const at = instantOf(when)
    if (!at) return
    setMissing(false)
    setTimeFailed(false)
    try {
      const reply = await agentOpsApi.messages(nav.runRef, {
        at,
        limit: 1,
        role: answers ? 'all' : undefined,
      })
      const first = reply.items[0]
      if (first) await nav.jump(first)
      else setMissing(true)
    } catch {
      setTimeFailed(true)
    }
  }

  return (
    <section
      id={id}
      aria-label={t('messages.title')}
      data-testid="message-panel"
      className="mx-2 flex max-h-72 flex-col gap-2 overflow-hidden rounded-ctl border border-line p-3"
    >
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex min-w-48 flex-1 flex-col gap-1">
          <label htmlFor={searchId} className="text-caption text-text-3">
            {t('messages.search')}
          </label>
          <Input
            id={searchId}
            type="search"
            value={text}
            placeholder={t('messages.searchHint')}
            onChange={(e) => setText(e.target.value)}
          />
        </div>
        <div className="flex items-end gap-2">
          <div className="flex flex-col gap-1">
            <label htmlFor={timeId} className="text-caption text-text-3">
              {t('messages.goToTime')}
            </label>
            <Input
              id={timeId}
              type="datetime-local"
              value={when}
              onChange={(e) => {
                setWhen(e.target.value)
                setMissing(false)
              }}
            />
          </div>
          <Button
            type="button"
            variant="secondary"
            disabled={instantOf(when) === undefined}
            onClick={() => void goToTime()}
          >
            {t('messages.go')}
          </Button>
        </div>
        <label
          htmlFor={answersId}
          className="flex h-8 items-center gap-2 text-caption text-text-2"
        >
          <input
            id={answersId}
            type="checkbox"
            checked={answers}
            onChange={(e) => setAnswers(e.target.checked)}
          />
          {t('messages.includeAnswers')}
        </label>
      </div>
      {missing ? (
        <p role="status" className="text-caption text-text-2">
          {t('messages.timeNone')}
        </p>
      ) : null}
      {timeFailed || list.isError ? (
        <p role="alert" className="text-caption text-danger">
          {t('messages.error')}
        </p>
      ) : null}
      {list.data && list.data.pages[0]?.transcript === 'pending' ? (
        <p className="text-caption text-text-2">{t('messages.pending')}</p>
      ) : null}
      {list.data && list.data.pages[0]?.transcript === 'missing' ? (
        <p className="text-caption text-text-2">{t('messages.missing')}</p>
      ) : null}
      {list.data && (list.data.pages[0]?.unreadable ?? 0) > 0 ? (
        <p className="text-caption text-warning">
          {t('messages.unreadable', {
            n: formatInt(list.data.pages[0]!.unreadable ?? 0),
          })}
        </p>
      ) : null}
      {list.isPending ? (
        <p role="status" className="text-caption text-text-3">
          {t('messages.loading')}
        </p>
      ) : list.isError ? null : rows.length === 0 ? (
        <p className="text-caption text-text-2">{t('messages.none')}</p>
      ) : (
        <>
          <p className="text-caption tabular-nums text-text-3">
            {t('messages.count', {
              shown: formatInt(rows.length),
              total: formatInt(shown),
            })}
          </p>
          <ul
            data-testid="message-list"
            className="m-0 flex min-h-0 flex-1 list-none flex-col overflow-auto p-0"
          >
            {rows.map((m) => (
              <li key={m.id}>
                <button
                  type="button"
                  data-testid="message-result"
                  onClick={() => void nav.jump(m)}
                  className={cn(
                    'flex min-h-8 w-full items-baseline gap-3 rounded-ctl px-2 py-1 text-left',
                    'text-caption outline-none hover:bg-hover',
                    'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
                  )}
                >
                  <span className="w-28 shrink-0 tabular-nums text-text-3">
                    {formatDateTime(m.at)}
                  </span>
                  {m.role === 'assistant' ? (
                    <span className="shrink-0 text-text-3">
                      {t('messages.answer')}
                    </span>
                  ) : null}
                  <span className="min-w-0 flex-1 truncate text-text">
                    {m.first_line}
                  </span>
                </button>
              </li>
            ))}
          </ul>
          {list.hasNextPage ? (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={list.isFetchingNextPage}
              onClick={() => void list.fetchNextPage()}
            >
              {t('messages.older')}
            </Button>
          ) : null}
        </>
      )}
    </section>
  )
}
