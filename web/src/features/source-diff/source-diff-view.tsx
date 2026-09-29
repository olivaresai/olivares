// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState, type Ref } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { isTypingTarget, resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { useTenantStore } from '@/stores/tenant'
import {
  classifyDiffError,
  readContentDiff,
  SourceDiffError,
} from './source-diff-api'
import {
  DIFF_SEARCH_KEYS,
  DIFF_WINDOW,
  compareHref,
  fileTitle,
  lineCount,
  nextHunk,
  parseHunks,
  queryComplete,
  truncationBound,
  windowBounds,
  type DiffRow,
  type GitHostDiff,
  type GitHostDiffFile,
  type GitHostDiffQuery,
} from './source-diff-model'
import './i18n'

const KNOWN_STATUS = [
  'added',
  'modified',
  'removed',
  'renamed',
  'copied',
  'changed',
  'unchanged',
] as const

const GLYPH: Record<string, string> = {
  added: '+',
  removed: '−',
  renamed: '→',
  modified: '•',
}

function statusLabel(status: string, t: (key: string) => string): string {
  if ((KNOWN_STATUS as readonly string[]).includes(status)) {
    return t(`status.${status}`)
  }
  return status
}

function goToSources(): void {
  window.location.assign('/console?tab=connectors')
}

function readClock(): number {
  return Date.now()
}

function keptSummary(files: readonly GitHostDiffFile[]): {
  added: number
  removed: number
  partial: boolean
} | null {
  let added = 0
  let removed = 0
  let partial = false
  let seen = false
  for (const item of files) {
    const count = lineCount(item)
    if (!count) continue
    seen = true
    added += count.added
    removed += count.removed
    partial = partial || count.partial
  }
  if (!seen) return null
  return { added, removed, partial }
}

export function SourceDiffView({
  query,
  onChange,
  reader = readContentDiff,
}: {
  query: GitHostDiffQuery
  onChange: (next: GitHostDiffQuery) => void
  reader?: (query: GitHostDiffQuery) => Promise<GitHostDiff>
}) {
  const { t } = useTranslation(['source-diff', 'nav'])
  const activeTenant = useTenantStore((state) => state.activeTenant)
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [windowStart, setWindowStart] = useState(0)
  const optionNodes = useRef<Array<HTMLDivElement | null>>([])
  const focusIndex = useRef<number | null>(null)
  const baseRef = useRef<HTMLInputElement>(null)
  function focusBase() {
    baseRef.current?.focus()
  }
  const [seen, setSeen] = useState('')
  const identity = JSON.stringify([activeTenant, query])
  if (seen !== identity) {
    setSeen(identity)
    setSelectedIndex(0)
    setWindowStart(0)
  }

  const result = useQuery({
    queryKey: ['source-diff', activeTenant, query],
    enabled: queryComplete(query),
    retry: false,
    queryFn: async () => {
      try {
        return await reader(query)
      } catch (error) {
        if (error instanceof SourceDiffError) throw error
        throw classifyDiffError(error, null)
      }
    },
  })

  const failure = result.error instanceof SourceDiffError ? result.error : null
  const data = failure?.kind === 'forbidden' ? undefined : result.data
  const files = useMemo(() => data?.files ?? [], [data])
  const summary = data ? keptSummary(files) : null
  const waitingOnRead = queryComplete(query) && result.isPending
  const selected = files[Math.min(selectedIndex, Math.max(files.length - 1, 0))]

  function chooseFile(index: number) {
    focusIndex.current = index
    setSelectedIndex(index)
    setWindowStart(0)
  }

  useEffect(() => {
    const index = focusIndex.current
    if (index === null) return
    focusIndex.current = null
    optionNodes.current[index]?.focus()
  })

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (isTypingTarget(event.target)) return
      const command = resolveBinding(KEYBINDINGS, event, { sourceDiff: true })
      if (!command?.startsWith('sourceDiff.')) return
      event.preventDefault()
      if (command === 'sourceDiff.nextFile') {
        setSelectedIndex((index) => {
          const next = Math.min(files.length - 1, index + 1)
          focusIndex.current = next
          return next
        })
        setWindowStart(0)
      } else if (command === 'sourceDiff.previousFile') {
        setSelectedIndex((index) => {
          const next = Math.max(0, index - 1)
          focusIndex.current = next
          return next
        })
        setWindowStart(0)
      } else if (
        command === 'sourceDiff.nextHunk' ||
        command === 'sourceDiff.previousHunk'
      ) {
        const current =
          files[Math.min(selectedIndex, Math.max(files.length - 1, 0))]
        if (!current) return
        const rows = parseHunks(current.hunks)
        const direction = command === 'sourceDiff.nextHunk' ? 1 : -1
        setWindowStart((start) => nextHunk(rows, start, direction))
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [files, selectedIndex])

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex flex-wrap items-end gap-3 border-b border-border p-4">
        <PageHeader title={t('nav:items.sourceDiff')} className="w-full" />
        <div className="flex flex-col gap-1">
          <span className="text-sm text-muted-foreground">{t('host')}</span>
          <span className="font-mono text-sm">{query.host}</span>
        </div>
        <Field
          label={t('repository')}
          value={query.repository}
          onValue={(repository) => onChange({ ...query, repository })}
        />
        <Field
          label={t('base')}
          value={query.base}
          inputRef={baseRef}
          onValue={(base) => onChange({ ...query, base })}
        />
        <Field
          label={t('head')}
          value={query.head}
          onValue={(head) => onChange({ ...query, head })}
        />
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={!query.base.trim() || !query.head.trim()}
            aria-describedby={
              !query.base.trim() || !query.head.trim()
                ? 'swap-reason'
                : undefined
            }
            onClick={() =>
              onChange({ ...query, base: query.head, head: query.base })
            }
          >
            {t('swap')}
          </Button>
          <span className="text-sm text-muted-foreground">{t('requires')}</span>
          <span className="rounded-md border border-border px-2 py-1 text-sm">
            {t('permission')}
          </span>
        </div>
        {(!query.base.trim() || !query.head.trim()) && (
          <p id="swap-reason" className="w-full text-sm text-muted-foreground">
            {t('swapDisabled')}
          </p>
        )}
        {!queryComplete(query) && (
          <p className="w-full text-sm">{t('badRequest')}</p>
        )}
        {data?.truncated && (
          <p className="w-full text-sm" role="status">
            {truncationBound(data.files.length) === 'files'
              ? t('truncatedFiles')
              : t('truncatedBytes')}
          </p>
        )}
        {data && (
          <p className="flex w-full flex-wrap gap-3 text-sm">
            <span>{t('fileCount', { count: data.files.length })}</span>
            {summary && (
              <span>
                {t(
                  summary.partial || data.truncated
                    ? 'countPartial'
                    : 'countFull',
                  {
                    added: summary.added,
                    removed: summary.removed,
                  },
                )}
              </span>
            )}
          </p>
        )}
      </header>
      {waitingOnRead && (
        <p className="p-4" role="status" aria-busy="true">
          {t('loading')}
        </p>
      )}
      {failure && (
        <FailureNotice
          failure={failure}
          onRetry={() => void result.refetch()}
          onEdit={focusBase}
          queryComplete={queryComplete(query)}
        />
      )}
      {data && data.files.length === 0 && !failure && (
        <div className="flex flex-col items-start gap-3 p-6">
          <p>{t('emptyTitle')}</p>
          <Button type="button" variant="outline" onClick={focusBase}>
            {t('editRefs')}
          </Button>
        </div>
      )}
      {data && files.length > 0 && selected && (
        <div className="grid min-h-0 flex-1 grid-cols-1 min-[761px]:grid-cols-[260px_minmax(0,1fr)] min-[1440px]:grid-cols-[320px_minmax(0,1fr)]">
          <div className="min-h-0 min-w-0 border-b border-border min-[761px]:border-r min-[761px]:border-b-0">
            <h2 className="px-3 pt-3 text-sm font-semibold">
              {t('changedFiles')}
            </h2>
            <div
              role="listbox"
              aria-label={t('changedFiles')}
              className="max-h-[38vh] overflow-auto p-1 min-[761px]:max-h-none"
              onKeyDown={(event) => {
                if (event.altKey || event.metaKey || event.ctrlKey) return
                if (event.key === 'ArrowDown') {
                  event.preventDefault()
                  chooseFile(Math.min(files.length - 1, selectedIndex + 1))
                } else if (event.key === 'ArrowUp') {
                  event.preventDefault()
                  chooseFile(Math.max(0, selectedIndex - 1))
                } else if (event.key === 'Home') {
                  event.preventDefault()
                  chooseFile(0)
                } else if (event.key === 'End') {
                  event.preventDefault()
                  chooseFile(files.length - 1)
                }
              }}
            >
              {files.map((item, index) => (
                <FileOption
                  key={`${item.path}:${index}`}
                  file={item}
                  selected={index === Math.min(selectedIndex, files.length - 1)}
                  optionRef={(node) => {
                    optionNodes.current[index] = node
                  }}
                  onSelect={() => {
                    setSelectedIndex(index)
                    setWindowStart(0)
                  }}
                />
              ))}
            </div>
            <p className="px-3 pb-3 text-sm text-muted-foreground">
              {t('keysHint')}
            </p>
          </div>
          <FileDiff
            file={selected}
            windowStart={windowStart}
            onWindowStart={setWindowStart}
          />
        </div>
      )}
    </div>
  )
}

function Field({
  label,
  value,
  onValue,
  inputRef,
}: {
  label: string
  value: string
  onValue: (value: string) => void
  inputRef?: Ref<HTMLInputElement>
}) {
  return (
    <label className="flex min-w-0 flex-1 flex-col gap-1 basis-40">
      <span className="text-sm">{label}</span>
      <input
        ref={inputRef}
        className="h-8 rounded-md border border-ctl-border bg-canvas px-2 font-mono text-sm"
        value={value}
        onChange={(event) => onValue(event.target.value)}
      />
    </label>
  )
}

function FileOption({
  file,
  selected,
  onSelect,
  optionRef,
}: {
  file: GitHostDiffFile
  selected: boolean
  onSelect: () => void
  optionRef: (node: HTMLDivElement | null) => void
}) {
  const { t } = useTranslation('source-diff')
  const count = lineCount(file)
  const outline =
    'focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus'
  return (
    <div
      ref={optionRef}
      role="option"
      aria-selected={selected}
      tabIndex={selected ? 0 : -1}
      className={
        selected
          ? `grid cursor-pointer grid-cols-[1rem_minmax(0,1fr)] gap-x-2 rounded-md bg-muted px-2 py-1.5 shadow-[inset_3px_0_0_var(--color-accent)] ${outline}`
          : `grid cursor-pointer grid-cols-[1rem_minmax(0,1fr)] gap-x-2 rounded-md px-2 py-1.5 ${outline}`
      }
      onClick={onSelect}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') onSelect()
      }}
    >
      <span aria-hidden="true" className="font-mono text-muted-foreground">
        {GLYPH[file.status] ?? '•'}
      </span>
      <span className="truncate font-mono text-sm">{fileTitle(file)}</span>
      <span className="col-start-2 text-sm">{statusLabel(file.status, t)}</span>
      <span className="col-start-2 flex flex-wrap gap-2 text-sm">
        {count && (
          <span>
            {t(count.partial ? 'countPartial' : 'countFull', {
              added: count.added,
              removed: count.removed,
            })}
          </span>
        )}
        {file.binary && <span>{t('binaryTag')}</span>}
        {file.truncated && <span>{t('truncatedTag')}</span>}
      </span>
    </div>
  )
}

function FileDiff({
  file,
  windowStart,
  onWindowStart,
}: {
  file: GitHostDiffFile
  windowStart: number
  onWindowStart: (start: number) => void
}) {
  const { t } = useTranslation('source-diff')
  const title = fileTitle(file)
  const rows = parseHunks(file.hunks)
  const bounds = windowBounds(rows.length, windowStart)
  const visible = rows.slice(bounds.start, bounds.end)
  const removedWithoutPatch =
    !file.binary && file.hunks.length === 0 && file.status === 'removed'

  return (
    <div
      role="region"
      tabIndex={0}
      aria-label={t('diffLabel', { path: title })}
      className="min-h-0 min-w-0 overflow-auto p-3 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
      onKeyDown={(event) => {
        if (event.key === 'PageDown') {
          event.preventDefault()
          onWindowStart(windowStart + DIFF_WINDOW)
        } else if (event.key === 'PageUp') {
          event.preventDefault()
          onWindowStart(windowStart - DIFF_WINDOW)
        }
      }}
    >
      <div className="sticky top-0 z-10 flex items-center gap-2 bg-surface py-2">
        <span className="font-mono text-sm">{title}</span>
        <span className="text-sm">{statusLabel(file.status, t)}</span>
      </div>
      {file.binary && <p>{t('binary')}</p>}
      {removedWithoutPatch && (
        <div className="flex flex-col items-start gap-2">
          <p>{t('removed')}</p>
          <p>{t('removedNotBinary')}</p>
          <Button
            type="button"
            variant="outline"
            onClick={() => void navigator.clipboard?.writeText(file.path)}
          >
            {t('copyPath')}
          </Button>
        </div>
      )}
      {file.binary && (
        <Button
          type="button"
          variant="outline"
          onClick={() => void navigator.clipboard?.writeText(file.path)}
        >
          {t('copyPath')}
        </Button>
      )}
      {!file.binary && !removedWithoutPatch && rows.length > 0 && (
        <table className="w-max min-w-full border-collapse font-mono text-xs">
          <caption className="sr-only">
            {t('diffLabel', { path: title })}
          </caption>
          <thead>
            <tr>
              <th
                scope="col"
                className="px-2 text-right font-sans text-muted-foreground"
              >
                {t('colOld')}
              </th>
              <th
                scope="col"
                className="px-2 text-right font-sans text-muted-foreground"
              >
                {t('colNew')}
              </th>
              <th scope="col" className="px-2 font-sans text-muted-foreground">
                {t('colChange')}
              </th>
              <th
                scope="col"
                className="px-2 text-left font-sans text-muted-foreground"
              >
                {t('colText')}
              </th>
            </tr>
          </thead>
          <tbody>
            {visible.map((row, index) => (
              <DiffLine key={`${bounds.start + index}`} row={row} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function DiffLine({ row }: { row: DiffRow }) {
  const { t } = useTranslation('source-diff')
  if (row.kind === 'hunk') {
    return (
      <tr className="bg-surface">
        <th
          colSpan={4}
          scope="rowgroup"
          className="px-2 py-1 text-left font-normal text-muted-foreground"
        >
          {row.text}
        </th>
      </tr>
    )
  }
  const spoken =
    row.kind === 'added'
      ? t('addedLine', { line: row.newLine })
      : row.kind === 'removed'
        ? t('removedLine', { line: row.oldLine })
        : t('contextLine', { line: row.oldLine })
  const tone =
    row.kind === 'added'
      ? 'bg-diff-add'
      : row.kind === 'removed'
        ? 'bg-diff-del'
        : ''
  const markerTone =
    row.kind === 'added'
      ? 'text-success'
      : row.kind === 'removed'
        ? 'text-bad'
        : ''
  return (
    <tr className={tone}>
      <td className="px-2 text-right text-muted-foreground">
        {row.oldLine ?? ''}
      </td>
      <td className="px-2 text-right text-muted-foreground">
        {row.newLine ?? ''}
      </td>
      <td className={`px-2 text-center ${markerTone}`}>
        <span className="sr-only">{spoken} </span>
        {row.marker}
      </td>
      <td className="whitespace-pre px-2">{row.text}</td>
    </tr>
  )
}

export function FailureNotice({
  failure,
  onRetry,
  onEdit,
  queryComplete: complete = true,
}: {
  failure: SourceDiffError
  onRetry: () => void
  onEdit: () => void
  queryComplete?: boolean
}) {
  const { t } = useTranslation('source-diff')
  const [now, setNow] = useState(readClock)
  useEffect(() => {
    if (failure.kind !== 'rateLimited' || failure.retryAt === undefined) {
      return
    }
    const id = window.setInterval(() => setNow(readClock()), 1000)
    return () => window.clearInterval(id)
  }, [failure.kind, failure.retryAt])
  const waiting =
    failure.kind === 'rateLimited' &&
    failure.retryAt !== undefined &&
    failure.retryAt > now
  const seconds =
    failure.retryAt === undefined
      ? 0
      : Math.max(1, Math.ceil((failure.retryAt - now) / 1000))

  if (failure.kind === 'forbidden') {
    return (
      <div className="flex flex-col items-start gap-3 p-6">
        <p>{t('forbidden')}</p>
        <Button type="button" variant="outline" onClick={() => goToSources()}>
          {t('returnToSources')}
        </Button>
      </div>
    )
  }
  if (failure.kind === 'hostForbidden') {
    return (
      <div className="flex flex-col items-start gap-3 p-6">
        <p>{t('hostForbidden')}</p>
        <p>{t('hostForbiddenBody')}</p>
        <Button type="button" variant="outline" onClick={() => goToSources()}>
          {t('returnToSources')}
        </Button>
      </div>
    )
  }
  if (failure.kind === 'unavailable') {
    return (
      <div className="flex flex-col items-start gap-3 p-6">
        <p>{t('unavailable')}</p>
        <p>{t('unavailableBody')}</p>
        <Button type="button" variant="outline" onClick={() => goToSources()}>
          {t('returnToSources')}
        </Button>
      </div>
    )
  }
  if (failure.kind === 'unauthenticated') return null

  const message =
    failure.kind === 'badRequest'
      ? t(complete ? 'unknownSource' : 'badRequest')
      : failure.kind === 'unknownRef'
        ? t('unknownRef')
        : failure.kind === 'tooLarge'
          ? t('tooLarge')
          : failure.kind === 'rateLimited'
            ? t('rateLimited')
            : t('upstream')
  const action =
    failure.kind === 'unknownRef'
      ? t('editRefs')
      : failure.kind === 'upstream' || failure.kind === 'rateLimited'
        ? t('tryAgain')
        : t('editComparison')
  const retries = failure.kind === 'upstream' || failure.kind === 'rateLimited'
  const edits =
    failure.kind === 'badRequest' ||
    failure.kind === 'unknownRef' ||
    failure.kind === 'tooLarge'

  return (
    <div className="flex flex-col items-start gap-3 p-6">
      <p>{message}</p>
      {waiting && <p>{t('retryIn', { seconds })}</p>}
      <Button
        type="button"
        variant="outline"
        disabled={waiting}
        onClick={retries ? onRetry : edits ? onEdit : undefined}
      >
        {action}
      </Button>
    </div>
  )
}

export function SourceCompareControl({
  source,
  allowed,
}: {
  source: { name: string; kind?: string }
  allowed: boolean
}) {
  const { t } = useTranslation('source-diff')
  const href = compareHref(source)
  if (!href) return null
  if (!allowed) {
    const reasonId = `compare-why-${source.name}`
    return (
      <span className="inline-flex flex-col items-end gap-1">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled
          aria-describedby={reasonId}
        >
          {t('compare')}
        </Button>
        <p
          id={reasonId}
          className="max-w-64 text-left text-sm text-muted-foreground"
        >
          {t('forbidden')}
        </p>
      </span>
    )
  }
  return (
    <Button type="button" variant="ghost" size="sm" asChild>
      <a href={href}>{t('compare')}</a>
    </Button>
  )
}

export default function SourceDiffPage() {
  const [url, patch] = useUrlState(DIFF_SEARCH_KEYS, { history: 'replace' })
  const query: GitHostDiffQuery = {
    source: url.source ?? '',
    host: url.host ?? '',
    repository: url.repository ?? '',
    base: url.base ?? '',
    head: url.head ?? '',
  }
  return (
    <SourceDiffView
      query={query}
      onChange={(next) =>
        patch({
          source: next.source,
          host: next.host,
          repository: next.repository,
          base: next.base,
          head: next.head,
        })
      }
    />
  )
}
