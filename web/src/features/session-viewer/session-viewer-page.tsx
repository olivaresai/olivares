// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SessionViewerPage — top-level page for the unified session recording viewer
//. Reads the session ID from the URL path, fetches the unified data via
// useInfiniteQuery (frames and timeline are independently cursor-paginated),
// and composes the viewer layout: header at top, sidebar (tools + files +
// decisions) on left, unified timeline in center, detail panel below.
//
// Registered in the feature registry at /session-viewer/$id (Task 11).
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { useRouterState } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { CaveatNotice } from '@/features/_intel'
import { recordingKeys } from '@/features/recordings/api'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import type { KeyedFrame, KeyedTimelineEntry, VerifyResult } from './types'
import { viewerApi, viewerKeys } from './api'
import { DecisionsPanel } from './decisions-panel'
import { EventDetailPanel } from './event-detail-panel'
import { FilesPanel } from './files-panel'
import { LedgerPanel } from './ledger-panel'
import { ToolsPanel } from './tools-panel'
import { UnifiedTimeline } from './unified-timeline'
import { ViewerHeader } from './viewer-header'
import './i18n'

const FRAME_PAGE = 50
const TIMELINE_PAGE = 100
const UNIFIED_BASE_PARAMS = {
  limit: FRAME_PAGE,
  timeline_limit: TIMELINE_PAGE,
} as const

interface ViewerPageParam {
  frame_cursor?: string
  timeline_cursor?: string
}

type SealOutcome = { status: 'sealed' } | { status: 'already-sealed' }

type ActionNotice = {
  tone: 'neutral' | 'warning'
  message: string
}

/**
 * Extract the session ID from the current URL pathname. The route is registered
 * as /session-viewer/$id, so the last segment is the session identifier.
 */
function useSessionId(): string {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const segments = pathname.split('/')
  return segments[segments.length - 1] ?? ''
}

export function SessionViewerPage() {
  const { t } = useTranslation(['session-viewer', 'common'])
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const sessionId = useSessionId()

  // UI state.
  const [selectedEventId, setSelectedEventId] = useState<string | null>(null)
  const [toolFilter, setToolFilter] = useState<string | null>(null)
  const [confirmSeal, setConfirmSeal] = useState(false)
  const [freshVerify, setFreshVerify] = useState<VerifyResult | null>(null)
  const [sealNotice, setSealNotice] = useState<ActionNotice | null>(null)
  const [summaryNotice, setSummaryNotice] = useState<ActionNotice | null>(null)
  const stateScope = `${activeTenant ?? ''}:${sessionId}`
  const [previousStateScope, setPreviousStateScope] = useState(stateScope)
  if (previousStateScope !== stateScope) {
    setPreviousStateScope(stateScope)
    setSelectedEventId(null)
    setToolFilter(null)
    setConfirmSeal(false)
    setFreshVerify(null)
    setSealNotice(null)
    setSummaryNotice(null)
  }

  // Unified query — frames and timeline are independently cursor-paginated.
  const unifiedQuery = useInfiniteQuery({
    queryKey: viewerKeys.unified(activeTenant, sessionId, {
      ...UNIFIED_BASE_PARAMS,
    }),
    queryFn: ({ pageParam }) => {
      const params = (pageParam ?? {}) as ViewerPageParam
      return viewerApi.unified(sessionId, {
        limit: FRAME_PAGE,
        timeline_limit: TIMELINE_PAGE,
        ...params,
      })
    },
    enabled: !!sessionId,
    initialPageParam: undefined as ViewerPageParam | undefined,
    getNextPageParam: (last, _pages, lastPageParam, allPageParams) => {
      const next: ViewerPageParam = {}
      const input = (lastPageParam ?? {}) as ViewerPageParam
      const previousInputs = (
        allPageParams.slice(0, -1) as Array<ViewerPageParam | undefined>
      ).map((params) => params ?? {})
      const frameInputRepeated = previousInputs.some(
        (params) =>
          cursorInputKey(params.frame_cursor) ===
          cursorInputKey(input.frame_cursor),
      )
      const timelineInputRepeated = previousInputs.some(
        (params) =>
          cursorInputKey(params.timeline_cursor) ===
          cursorInputKey(input.timeline_cursor),
      )
      if (
        !frameInputRepeated &&
        last.frames.has_more &&
        last.frames.cursor &&
        last.frames.cursor !== input.frame_cursor
      ) {
        next.frame_cursor = last.frames.cursor
      }
      if (
        !timelineInputRepeated &&
        last.timeline.has_more &&
        last.timeline.cursor &&
        last.timeline.cursor !== input.timeline_cursor
      ) {
        next.timeline_cursor = last.timeline.cursor
      }
      return next.frame_cursor || next.timeline_cursor ? next : undefined
    },
  })

  // Flatten paginated data.
  const session = unifiedQuery.data?.pages[0]?.session ?? null
  const live = unifiedQuery.data?.pages[0]?.live ?? null
  // A later cursor page can fail after the first page succeeded. One unavailable
  // page makes the activity lane incomplete, even while evidence stays usable.
  const timelineAvailable =
    unifiedQuery.data?.pages.every((page) => page.timeline.available) ?? false
  const verify = unifiedQuery.data?.pages[0]?.verify ?? null
  const ledger = unifiedQuery.data?.pages[0]?.ledger ?? []
  const ledgerTruncated = unifiedQuery.data?.pages[0]?.ledger_truncated ?? false

  // Each lane advances independently. When the next request omits one cursor,
  // the endpoint legitimately returns that lane's first page again. Accept each
  // cursor input once per lane so those fallback pages never append duplicates.
  const frames = useMemo<KeyedFrame[]>(
    () =>
      accumulateLane(
        unifiedQuery.data?.pages ?? [],
        unifiedQuery.data?.pageParams ?? [],
        'frame_cursor',
        (page) => page.frames.items,
        (frame) => frame.hash || `frame-${frame.idx}`,
      ).map(({ key, value: frame }) => ({ key, frame })),
    [unifiedQuery.data],
  )
  const timeline = useMemo<KeyedTimelineEntry[]>(
    () =>
      accumulateLane(
        unifiedQuery.data?.pages ?? [],
        unifiedQuery.data?.pageParams ?? [],
        'timeline_cursor',
        (page) => page.timeline.items,
        (entry, sourceCursor, sourceIndex) =>
          `${sourceCursor}-${sourceIndex}-${entry.at}`,
      ).map(({ key, value: entry }) => ({ key, entry })),
    [unifiedQuery.data],
  )
  const timelineEntries = useMemo(
    () => timeline.map(({ entry }) => entry),
    [timeline],
  )

  // Selected event/frame for the detail panel.
  const selectedTimelineEntry = useMemo(() => {
    if (!selectedEventId?.startsWith('activity:')) return null
    const key = selectedEventId.slice('activity:'.length)
    return timeline.find((item) => item.key === key)?.entry ?? null
  }, [selectedEventId, timeline])

  const selectedFrame = useMemo(() => {
    if (!selectedEventId?.startsWith('evidence:')) return null
    const key = selectedEventId.slice('evidence:'.length)
    return frames.find((item) => item.key === key)?.frame ?? null
  }, [selectedEventId, frames])

  // Export mutations.
  const exportJSON = useMutation({
    mutationFn: () => viewerApi.exportJSON(sessionId),
    onSuccess: (data) => {
      const blob = new Blob([JSON.stringify(data, null, 2)], {
        type: 'application/json',
      })
      downloadBlob(blob, `session-${sessionId}.json`)
      toast.success(t('export.json'))
    },
    onError: () => toast.error(t('export.json')),
  })

  const exportSummary = useMutation({
    mutationFn: () => viewerApi.exportSummary(sessionId),
    onSuccess: (text) => {
      const blob = new Blob([text], { type: 'text/plain' })
      downloadBlob(blob, `session-${sessionId}-summary.txt`)
      toast.success(t('export.summary'))
    },
    onError: () => toast.error(t('export.summary')),
  })

  // Verify is a point-in-time read, separate from the passive inline verdict.
  const verifyMutation = useMutation({
    mutationFn: () => viewerApi.verify(sessionId),
    onSuccess: (result) => setFreshVerify(result),
    onError: () => toast.error(t('verify.failed')),
  })

  // A stale active page can race another operator sealing the session. Normalize
  // that 409 into an honest result so the confirmed action closes calmly and the
  // header shows "already sealed" inline.
  const sealMutation = usePrivilegedMutation<void, SealOutcome>({
    mutationFn: async () => {
      try {
        await viewerApi.seal(sessionId)
        return { status: 'sealed' }
      } catch (error) {
        if (error instanceof ApiError && error.status === 409) {
          return { status: 'already-sealed' }
        }
        throw error
      }
    },
    invalidateKeys: [
      viewerKeys.unified(activeTenant, sessionId, UNIFIED_BASE_PARAMS),
      recordingKeys.all(activeTenant),
    ],
    successMessage: (outcome) =>
      t(outcome.status === 'sealed' ? 'seal.done' : 'seal.alreadySealed'),
    onDone: (outcome) => {
      setConfirmSeal(false)
      setSealNotice({
        tone: outcome.status === 'sealed' ? 'neutral' : 'warning',
        message: t(
          outcome.status === 'sealed' ? 'seal.done' : 'seal.alreadySealed',
        ),
      })
    },
  })

  // Summarization has three intentional refusal states. Keep them inline so an
  // operator can distinguish an unwired dependency, tenant egress policy, and
  // the sealed-only integrity rule without relying on a transient toast.
  const summarizeMutation = useMutation({
    mutationFn: () => viewerApi.summarize(sessionId),
    onMutate: () => setSummaryNotice(null),
    onSuccess: async () => {
      setSummaryNotice({ tone: 'neutral', message: t('summarize.done') })
      toast.success(t('summarize.done'))
      await queryClient.invalidateQueries({
        queryKey: viewerKeys.unified(
          activeTenant,
          sessionId,
          UNIFIED_BASE_PARAMS,
        ),
      })
    },
    onError: (error) => {
      let message = t('summarize.failed')
      const tone: ActionNotice['tone'] = 'warning'
      if (error instanceof ApiError) {
        if (error.status === 501) message = t('summarize.unavailable')
        // ⛔ UN STEP-UP NO ES «RESUMEN DESHABILITADO». Este despacho por status trataba los dos
        // 403 igual: el de rol —donde «deshabilitado» es una aproximación aceptable— y el de
        // ceremonia, que tiene remedio y aquí se anunciaba como una función apagada. Se
        // distingue por el CÓDIGO, que es lo único que los separa (lib/api/errors.ts:71-79).
        else if (error.isStepUpRequired)
          // Se reusa la cadena común, ya traducida en los siete idiomas, en vez de crear una
          // clave nueva y su deuda de traducción.
          message = t('common:privileged.stepUp.title')
        // ⛔ Y EL 403 DE ROL TAMPOCO ES «DESHABILITADO». El motor tiene DOS causas distintas
        // para ese status y las separa por código: el wrapper deniega por `permSessionAdmin`
        // antes del handler y sale con `forbidden` (core/api/middleware.go:267-280,
        // errors.go:148-149); el handler deniega porque `ai_summaries` está apagado y usa el
        // sobre de módulo SIN código, que el cliente conserva como `internal`
        // (modules/recording/handlers.go:975-985, lib/api/errors.ts:108-126).
        //
        // Mirar sólo `isForbidden` —que es exclusivamente el status— daba a una revocación de
        // permiso entre la carga y el clic una explicación de política de egress. Lo levantó el
        // contraste; la información para distinguirlas ya estaba, sólo había que leerla.
        else if (error.isForbidden && error.code === 'forbidden')
          message = t('common:privileged.notAuthorized')
        else if (error.isForbidden) message = t('summarize.disabled')
        else if (error.status === 409) message = t('summarize.active')
      }
      setSummaryNotice({ tone, message })
      if (error instanceof ApiError && error.status === 501) toast.info(message)
      else if (error instanceof ApiError && error.isForbidden)
        toast.warning(message)
      else if (error instanceof ApiError && error.status === 409)
        toast.warning(message)
      else toast.error(message)
    },
  })

  // No session ID in URL.
  if (!sessionId) {
    return <EmptyState title={t('notFound')} description="" />
  }

  // Loading.
  if (unifiedQuery.isLoading) {
    return (
      <div className="flex flex-col gap-4 p-6">
        <Skeleton className="h-20 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    )
  }

  // Error states.
  if (unifiedQuery.error) {
    if (
      unifiedQuery.error instanceof ApiError &&
      unifiedQuery.error.isStepUpRequired
    ) {
      // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que la
      // sesión entera se sustituía por «no tienes autorización» — falso, y sin salida.
      return (
        <StepUpRequiredState
          action="generic"
          onElevated={() => void unifiedQuery.refetch()}
        />
      )
    }
    if (
      unifiedQuery.error instanceof ApiError &&
      unifiedQuery.error.isForbidden
    ) {
      return <ForbiddenState />
    }
    return <ErrorState retry={() => void unifiedQuery.refetch()} />
  }

  if (!session) {
    return <EmptyState title={t('notFound')} description="" />
  }

  return (
    <div className="flex flex-col gap-6 p-6">
      {/* Header with session metadata + actions */}
      <ViewerHeader
        session={session}
        live={live}
        verify={verify}
        onExportJSON={() => exportJSON.mutate()}
        onExportSummary={() => exportSummary.mutate()}
        onVerify={() => verifyMutation.mutate()}
        onSeal={() => setConfirmSeal(true)}
        onSummarize={() => summarizeMutation.mutate()}
        exporting={exportJSON.isPending || exportSummary.isPending}
        verifying={verifyMutation.isPending}
        sealing={sealMutation.isPending}
        summarizing={summarizeMutation.isPending}
        freshVerify={freshVerify}
      />

      {(sealNotice || summaryNotice) && (
        <div className="flex flex-col gap-2" role="status" aria-live="polite">
          {sealNotice && (
            <CaveatNotice tone={sealNotice.tone}>
              {sealNotice.message}
            </CaveatNotice>
          )}
          {summaryNotice && (
            <CaveatNotice tone={summaryNotice.tone}>
              {summaryNotice.message}
            </CaveatNotice>
          )}
        </div>
      )}

      <Separator />

      {/* Main body: sidebar left + timeline center */}
      <div className="flex gap-6">
        {/* Sidebar */}
        <aside className="hidden w-64 shrink-0 flex-col gap-4 lg:flex">
          <ToolsPanel
            timeline={timelineEntries}
            activeFilter={toolFilter}
            onFilterChange={setToolFilter}
          />
          <Separator />
          <FilesPanel timeline={timelineEntries} />
          <Separator />
          <DecisionsPanel timeline={timelineEntries} />
        </aside>

        {/* Timeline + detail */}
        <div className="min-w-0 flex-1">
          <UnifiedTimeline
            timeline={timeline}
            timelineAvailable={timelineAvailable}
            frames={frames}
            selectedEventId={selectedEventId}
            onSelectTimeline={(key) =>
              setSelectedEventId((selected) =>
                selected === `activity:${key}` ? null : `activity:${key}`,
              )
            }
            onSelectFrame={(key) =>
              setSelectedEventId((selected) =>
                selected === `evidence:${key}` ? null : `evidence:${key}`,
              )
            }
            toolFilter={toolFilter}
          />

          {/* Load more */}
          {unifiedQuery.hasNextPage && (
            <div className="mt-4 flex justify-center">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void unifiedQuery.fetchNextPage()}
                disabled={unifiedQuery.isFetchingNextPage}
              >
                {t('timeline.loadMore')}
              </Button>
            </div>
          )}

          {/* Detail panel — shown when an event or frame is selected */}
          {selectedEventId && (
            <div className="mt-4">
              <EventDetailPanel
                timelineEntry={selectedTimelineEntry}
                frame={selectedFrame}
                onClose={() => setSelectedEventId(null)}
              />
            </div>
          )}
        </div>
      </div>

      <LedgerPanel events={ledger} truncated={ledgerTruncated} />

      <ConfirmDialog
        open={confirmSeal}
        onOpenChange={setConfirmSeal}
        title={t('seal.title')}
        description={t('seal.body')}
        tone="danger"
        confirmLabel={t('seal.confirm')}
        pending={sealMutation.isPending}
        onConfirm={() => sealMutation.mutate(undefined)}
      />
    </div>
  )
}

function accumulateLane<T>(
  pages: Array<import('./types').UnifiedResponse>,
  pageParams: unknown[],
  cursorField: keyof ViewerPageParam,
  itemsOf: (page: import('./types').UnifiedResponse) => T[],
  keyOf: (item: T, sourceCursor: string, sourceIndex: number) => string,
): Array<{ key: string; value: T }> {
  const seenInputs = new Set<string>()
  const keyed: Array<{ key: string; value: T }> = []

  pages.forEach((page, pageIndex) => {
    const params = (pageParams[pageIndex] ?? {}) as ViewerPageParam
    const sourceCursor = cursorInputKey(params[cursorField])
    if (seenInputs.has(sourceCursor)) return
    seenInputs.add(sourceCursor)
    itemsOf(page).forEach((item, sourceIndex) => {
      keyed.push({
        key: keyOf(item, sourceCursor, sourceIndex),
        value: item,
      })
    })
  })

  return keyed
}

function cursorInputKey(cursor: string | undefined): string {
  return cursor === undefined ? 'initial' : `cursor:${cursor}`
}

/** Trigger a browser file download for a Blob. */
function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
