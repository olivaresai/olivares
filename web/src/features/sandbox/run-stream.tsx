// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, RotateCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { AsyncSection, CaveatNotice } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { sandboxApi, sandboxKeys } from './api'
import { OutputsList } from './components'
import { useRunStream } from './stream'
import type { Run } from './types'

/**
 * RunStreamPanel — watching ONE sandbox run: its step outputs as the engine emits
 * them, the aggregate it sends at the end, and an honest account of the connection.
 *
 * The three things this panel refuses to do, each one a measured trap:
 *
 *  1. It never renders a dropped connection as a finished run. A drop shows an explicit
 *     "you stopped seeing this run" alert with a reconnect action; the run's own status
 *     keeps coming from the engine's row, never from the socket.
 *  2. It never claims to be watching an execution in flight. A sandbox run completes
 *     synchronously inside its launch handler (modules/sandbox/runs.go:185), so what
 *     arrives is committed evidence replayed progressively — said in the copy, not
 *     implied by a "live" badge.
 *  3. It does not leave the operator with nothing when SSE cannot get through. The
 *     stored outputs are also readable as plain JSON, so a stream that never delivered
 *     falls back to that read — LABELLED as a snapshot, so "I have the evidence" and "I
 *     am watching" never look the same. It is a fallback, not a parallel fetch: the
 *     query is disabled while the stream works.
 *
 * `LiveDot` is deliberately NOT used here at all, and the contrast is what proved the
 * point: it renders the shared `live.*` copy, whose `open` state is the literal word
 * "Live" / "En vivo" / "ライブ" (features/shared/i18n/*.json), and whose `error` state is
 * "Reconnecting…" — which this hook does not do. Either one printed next to a note that
 * says "not an execution in flight" is a contradiction on the same screen. The status
 * copy below lives in the sandbox namespace and says what this surface actually is.
 */
export function RunStreamPanel({ run }: { run: Run }) {
  const { t } = useTranslation('sandbox')
  const { activeTenant } = useAuth()
  const { outputs, summary, status, complete, interrupted, unreadable, reconnect } =
    useRunStream({ runId: run.id })

  const fallbackQ = useQuery({
    queryKey: sandboxKeys.outputs(activeTenant, run.id),
    queryFn: () => sandboxApi.outputs(run.id),
    enabled: interrupted && outputs.length === 0,
  })

  // ONE terminal statement, never two. `complete` and `interrupted` are now mutually
  // exclusive in the hook, and a clean end that lost frames is reported as PARTIAL
  // rather than complete — an unreadable frame is evidence this client did not receive,
  // so it must beat "complete" instead of hiding behind it.
  const statusLabel = interrupted
    ? t('stream.disconnected')
    : complete
      ? unreadable > 0
        ? t('stream.completePartial')
        : t('stream.complete')
      : status === 'open'
        ? t('stream.receiving')
        : t('stream.connecting')

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">{statusLabel}</span>
      </div>

      <CaveatNotice>{t('stream.replayNote')}</CaveatNotice>

      {unreadable > 0 ? (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-3 py-2.5 text-xs text-warning"
        >
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          {/* `n`, not `count`: i18next reserves `count` for plural resolution, which
              would send this key looking for `_one`/`_other` variants in seven
              languages. The number is informative, not grammatical. */}
          <span>{t('stream.unreadable', { n: unreadable })}</span>
        </div>
      ) : null}

      {interrupted ? (
        <div
          role="alert"
          className="flex flex-col gap-2 rounded-md border border-warning-line bg-warning-soft px-3 py-2.5 text-xs text-warning"
        >
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
            <div className="flex flex-col gap-1">
              <span className="font-medium">{t('stream.lost')}</span>
              <span>{t('stream.lostHint')}</span>
            </div>
          </div>
          <div>
            <Button type="button" variant="outline" size="sm" onClick={reconnect}>
              <RotateCw className="size-3.5" aria-hidden />
              {t('stream.reconnect')}
            </Button>
          </div>
        </div>
      ) : null}

      {outputs.length > 0 ? (
        <OutputsList outputs={outputs} />
      ) : complete ? (
        // "No outputs recorded" is a claim about the RUN. It is only true if nothing was
        // lost on the way: with unreadable frames, what happened is that this client
        // could not read them, which is a different sentence entirely.
        <EmptyState
          title={
            unreadable > 0 ? t('stream.emptyUnreadable') : t('outputs.empty')
          }
        />
      ) : interrupted ? (
        // Nothing arrived over the stream: read the stored outputs instead, and say
        // plainly that this is a snapshot rather than the live view.
        <AsyncSection query={fallbackQ} skeletonHeight={160}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState title={t('outputs.empty')} />
            ) : (
              <div className="flex flex-col gap-3">
                <CaveatNotice>{t('stream.snapshot')}</CaveatNotice>
                <OutputsList outputs={list.items} />
              </div>
            )
          }
        </AsyncSection>
      ) : status === 'connecting' || status === 'open' ? (
        // Only while a connection is actually being made or held. A stream that is not
        // running at all (no session, so `active` is false and the status is `closed`)
        // must not sit there saying it is waiting for output that nothing is fetching.
        <p className="px-1 text-xs text-muted-foreground" role="status">
          {t('stream.waiting')}
        </p>
      ) : null}

      {summary ? (
        <p className="px-1 text-xs text-muted-foreground">
          {t('stream.summary', {
            ok: summary.steps_ok,
            error: summary.steps_error,
            total: summary.steps_total,
          })}
        </p>
      ) : null}
    </div>
  )
}
