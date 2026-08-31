// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Log Viewer — the superadmin-only live log tail. Merges a historical buffer
// (GET /v1/console/logs/buffer) with the SSE live stream (GET /v1/console/logs/stream).
// Buffer entries form the initial backfill; new SSE entries are appended. Entries
// are stored in a ref to avoid per-line re-renders; a periodic state update
// batches the visual refresh (~100ms via requestAnimationFrame). The stream is
// honest — the LiveDot shows the real connection status, never a fake "live".
//
// Filtering: level and module are sent as query params to the SSE stream AND
// applied client-side for consistency. Search is client-side only.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Pause, Play, ScrollText, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { currentLanguage } from '@/lib/i18n'
import { CaveatNotice } from '@/features/_intel'
import { LiveDot } from '@/features/shared'
import { useLiveStream } from '@/features/shared/sse'
import { logsApi, logsKeys } from './api'
import type { LogBufferParams } from './api'
import { LogFiltersBar } from './log-filters'
import { LogStream } from './log-stream'
import type { LogEntry, LogFilters, LogLevel } from './types'
import './i18n'

/** Maximum entries kept in memory to prevent unbounded growth. */
const MAX_ENTRIES = 5000

/** Initial buffer fetch limit. */
const BUFFER_LIMIT = 1000

/** Canonical order keeps query keys and transport CSVs stable for a Set. */
const LOG_LEVELS: LogLevel[] = ['DEBUG', 'INFO', 'WARN', 'ERROR']

/** Default filters: no level restriction, no module/search filter. */
function defaultFilters(): LogFilters {
  return { levels: new Set<LogLevel>(), module: '', search: '' }
}

export function LogsView() {
  const { t } = useTranslation('logs')
  // --- filter state -----------------------------------------------------------
  const [filters, setFilters] = useState<LogFilters>(defaultFilters)
  const levelsParam = useMemo(
    () =>
      LOG_LEVELS.filter((level) => filters.levels.has(level))
        .map((level) => level.toLowerCase())
        .join(',') || undefined,
    [filters.levels],
  )

  // --- pause / auto-scroll ----------------------------------------------------
  const [paused, setPaused] = useState(false)
  // Track whether the user has scrolled up (disables auto-scroll independently
  // of the pause toggle). Reset on "resume" or "scroll to bottom".
  const [userScrolledUp, setUserScrolledUp] = useState(false)
  const autoScroll = !paused && !userScrolledUp

  // --- entries accumulator (ref to avoid per-line re-renders) -----------------
  const entriesRef = useRef<LogEntry[]>([])
  // A render tick: bumped to trigger a batched visual refresh.
  const [renderTick, setRenderTick] = useState(0)
  const rafRef = useRef(0)

  /** Schedule a batched render via requestAnimationFrame. */
  const scheduleRender = useCallback(() => {
    if (rafRef.current) return // already scheduled
    rafRef.current = requestAnimationFrame(() => {
      rafRef.current = 0
      setRenderTick((t) => t + 1)
    })
  }, [])

  // Cleanup pending rAF on unmount.
  useEffect(() => () => cancelAnimationFrame(rafRef.current), [])

  // --- buffer (initial backfill) ----------------------------------------------
  const bufferParams = useMemo<LogBufferParams>(
    () => ({
      ...(levelsParam ? { levels: levelsParam } : {}),
      ...(filters.module ? { module: filters.module } : {}),
      limit: BUFFER_LIMIT,
    }),
    [filters.module, levelsParam],
  )
  const bufferQuery = useQuery({
    queryKey: logsKeys.buffer(bufferParams),
    queryFn: () => logsApi.buffer(bufferParams),
  })

  const seededRef = useRef(false)
  // Reset before the seed effect. If a previously visited filter is already in
  // the query cache, both effects run in one commit and the cached page must
  // still re-seed after the set content changes.
  useEffect(() => {
    seededRef.current = false
    entriesRef.current = []
    scheduleRender()
  }, [filters.module, levelsParam, scheduleRender])

  // Seed the entries from the buffer on first successful load.
  useEffect(() => {
    if (bufferQuery.data && !seededRef.current) {
      entriesRef.current = bufferQuery.data.items.slice(-MAX_ENTRIES)
      seededRef.current = true
      scheduleRender()
    }
  }, [bufferQuery.data, scheduleRender])

  // --- SSE live stream --------------------------------------------------------
  const streamQuery = useMemo<Record<string, string | undefined>>(() => {
    const q: Record<string, string | undefined> = {}
    if (levelsParam) q.levels = levelsParam
    if (filters.module) q.module = filters.module
    return q
  }, [filters.module, levelsParam])

  const onSnapshot = useCallback(
    (entry: LogEntry) => {
      if (!entry?.timestamp) return
      const arr = entriesRef.current
      arr.push(entry)
      // Trim from the front if over the cap.
      if (arr.length > MAX_ENTRIES) {
        entriesRef.current = arr.slice(arr.length - MAX_ENTRIES)
      }
      scheduleRender()
    },
    [scheduleRender],
  )

  const { status: streamStatus } = useLiveStream<LogEntry>({
    path: '/v1/console/logs/stream',
    events: ['log'],
    query: streamQuery,
    onSnapshot,
  })
  const debugNotCaptured =
    filters.levels.has('DEBUG') &&
    bufferQuery.data !== undefined &&
    bufferQuery.data.capture_level.toLowerCase() !== 'debug'

  // --- filtered view (applies client-side level + search filters) -------------
  const displayed = useMemo(() => {
    let items = entriesRef.current
    // Level filter (client-side, when multiple levels selected or for consistency).
    if (filters.levels.size > 0) {
      items = items.filter((e) => filters.levels.has(e.level))
    }
    // Module filter (belt-and-suspenders: also sent as query param).
    if (filters.module) {
      const mod = filters.module.toLowerCase()
      items = items.filter(
        (e) => e.module && e.module.toLowerCase().includes(mod),
      )
    }
    // Search in message (client-side only).
    if (filters.search) {
      const needle = filters.search.toLowerCase()
      items = items.filter((e) => e.message.toLowerCase().includes(needle))
    }
    return items
    // renderTick is in the deps to pick up new entries from the rAF batch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters, renderTick])

  // --- scroll tracking --------------------------------------------------------
  const scrollContainerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = scrollContainerRef.current
    if (!el) return
    const onScroll = () => {
      // If the user is within 40px of the bottom, consider them "at bottom".
      const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
      setUserScrolledUp(!atBottom)
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    return () => el.removeEventListener('scroll', onScroll)
  }, [])

  // --- clear entries ----------------------------------------------------------
  const clearEntries = useCallback(() => {
    entriesRef.current = []
    scheduleRender()
  }, [scheduleRender])

  return (
    <div className="flex flex-col gap-3 p-4">
      {/* Header */}
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <ScrollText className="size-5 text-muted-foreground" />
          <h1 className="text-lg font-semibold text-foreground">
            {t('title')}
          </h1>
          <LiveDot status={streamStatus} />
        </div>
        <div className="flex items-center gap-1.5">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => {
              setPaused((p) => !p)
              if (paused) setUserScrolledUp(false)
            }}
            aria-label={
              paused ? t('controls.resumeAria') : t('controls.pauseAria')
            }
          >
            {paused ? (
              <Play className="size-3.5" />
            ) : (
              <Pause className="size-3.5" />
            )}
            <span className="ml-1 text-xs">
              {paused ? t('controls.resume') : t('controls.pause')}
            </span>
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={clearEntries}
            aria-label={t('controls.clearAria')}
          >
            <Trash2 className="size-3.5" />
            <span className="ml-1 text-xs">{t('controls.clear')}</span>
          </Button>
          <span className="text-xs tabular-nums text-muted-foreground">
            {t('controls.entryCount', {
              value: displayed.length.toLocaleString(currentLanguage()),
            })}
          </span>
        </div>
      </div>

      {/* Filter bar */}
      <LogFiltersBar filters={filters} onChange={setFilters} />
      {debugNotCaptured && (
        <CaveatNotice tone="info">{t('capture.debugUnavailable')}</CaveatNotice>
      )}

      {/* Log stream */}
      <div ref={scrollContainerRef}>
        <LogStream entries={displayed} autoScroll={autoScroll} />
      </div>
    </div>
  )
}

export default LogsView
