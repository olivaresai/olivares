// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE APP THE SESSION SERVES, beside its conversation: a port one of the session's own
// processes listens on, shown through the engine (modules/sessions/preview.go). The port
// is never exposed: the frame loads a short-lived same-origin URL whose token is its only
// credential, and the page runs sandboxed in an opaque origin, so it cannot read or use
// the console's session. `sandbox` here and the engine's CSP say the same thing.
//
// ANNOTATE: the console cannot read the page's elements, so the engine adds a picker
// script to the page (modules/sessions/preview_pick.go). Annotate asks it to pick; the
// element it reports, accepted only from this frame, opens a note in the same notes
// the Markdown viewer uses, and the notes go to a chosen live session as one turn.
import { useMutation, useQuery } from '@tanstack/react-query'
import { ExternalLink, MousePointerClick, RotateCw } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { PassageNotes } from '@/features/agentops/passage-notes'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import './i18n'

/** A dev server the agent starts shows up within a few seconds. */
const PORTS_REFRESH_MS = 5_000

const SANDBOX =
  'allow-scripts allow-forms allow-popups allow-modals allow-downloads'

const selectClass =
  'h-8 min-w-0 rounded-md border border-ctl-border bg-background px-2 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'

/** Ask the page's picker to start or stop. The page's origin is opaque, so no target origin can name it. */
function tellPage(frame: HTMLIFrameElement | null, on: boolean) {
  frame?.contentWindow?.postMessage({ type: 'olivares-annotate', on }, '*')
}

/**
 * The element a page reported, as one line to quote: its selector, text and page. The
 * page is the app's, so what it says is data to show and quote, never to act on.
 * `shown` is the preview the frame loaded: the page reports the engine's address, whose
 * token is the preview's credential, so only what follows `shown.url` leaves the console.
 */
function pickedElement(
  data: unknown,
  shown: { url: string; port: number } | undefined,
): string | null {
  const d = data as {
    type?: unknown
    selector?: unknown
    text?: unknown
    path?: unknown
  } | null
  if (d?.type !== 'olivares-element') return null
  const line = (v: unknown, max: number) =>
    (typeof v === 'string' ? v : '').replace(/\s+/g, ' ').trim().slice(0, max)
  const selector = line(d.selector, 200)
  const text = line(d.text, 80)
  const path = line(d.path, 200)
  const where =
    shown && path.startsWith(shown.url)
      ? ` at localhost:${shown.port}/${path.slice(shown.url.length)}`
      : ''
  // Single quotes: the notes list wraps the whole line in double ones.
  return selector ? selector + (text ? ` ‘${text}’` : '') + where : null
}

export function SessionPreview({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const [port, setPort] = useState<number | null>(null)
  const [path, setPath] = useState('/')
  const [src, setSrc] = useState<string | null>(null)
  const frame = useRef<HTMLIFrameElement>(null)
  const [annotating, setAnnotating] = useState(false)
  const [picked, setPicked] = useState<string | null>(null)
  // The frame's first load would cancel an earlier request, so Annotate waits for it.
  const [loaded, setLoaded] = useState<string | null>(null)
  // Only a running process listens; a stopped session has nothing to show.
  const live = run.state === 'running' || run.state === 'idle'
  const ports = useQuery({
    queryKey: agentOpsKeys.runPreviewPorts(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.previewPorts(run.run_ref, { signal }),
    refetchInterval: PORTS_REFRESH_MS,
    enabled: live,
  })
  const listed = ports.data?.ports ?? []
  const selected = port !== null && listed.includes(port) ? port : listed[0]
  const open = useMutation({
    mutationFn: (p: number) => agentOpsApi.openPreview(run.run_ref, p),
    onSuccess: (r) => setSrc(r.url + path.replace(/^\/+/, '')),
  })
  const show = () => {
    if (selected !== undefined) open.mutate(selected)
  }
  const shown = open.data
  useEffect(() => tellPage(frame.current, annotating), [annotating])
  useEffect(() => {
    if (!annotating) return
    let accepting = true
    const onMessage = (e: MessageEvent) => {
      // Opaque origins serialize as "null"; origin alone identifies no frame.
      // A pick is untrusted quoted text, never an instruction to send a turn.
      if (
        !accepting ||
        e.origin !== 'null' ||
        e.source !== frame.current?.contentWindow
      )
        return
      const element = pickedElement(e.data, shown)
      if (element === null) return
      accepting = false
      setPicked(element)
      setAnnotating(false)
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [annotating, shown])

  return (
    // AN EMPTY TAB IS ONE LINE: what the preview is (isolated, outside the session's
    // network limits) is the Open action's hover, once there is a port to open.
    <section data-testid="session-preview" className="flex flex-col gap-2">
      {!live ? (
        <p className="text-caption text-muted-foreground">
          {t('preview.notRunning')}
        </p>
      ) : ports.isError ? (
        <p className="text-caption text-danger">{t('preview.error')}</p>
      ) : listed.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {ports.isPending ? t('preview.loading') : t('preview.none')}
        </p>
      ) : !can('sessions:run:write') ? (
        <p className="text-caption text-muted-foreground">
          {t('preview.notAllowed')}
        </p>
      ) : (
        <>
          <form
            className="flex min-w-0 items-center gap-1"
            onSubmit={(e) => {
              e.preventDefault()
              show()
            }}
          >
            <span className="font-mono text-caption text-muted-foreground">
              localhost:
            </span>
            <select
              aria-label={t('preview.port')}
              className={selectClass}
              value={selected}
              onChange={(e) => {
                setPort(Number(e.target.value))
                setSrc(null)
                setAnnotating(false)
              }}
            >
              {listed.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
            <Input
              aria-label={t('preview.path')}
              className="h-8 min-w-0 flex-1 font-mono"
              value={path}
              onChange={(e) => setPath(e.target.value)}
            />
            <Button
              type="submit"
              variant="outline"
              size="sm"
              disabled={open.isPending}
              aria-label={src ? t('preview.reload') : undefined}
              title={t('preview.hint')}
            >
              {src ? <RotateCw className="size-3.5" /> : t('preview.open')}
            </Button>
            {src ? (
              <Button variant="ghost" size="sm" asChild>
                <a
                  href={src}
                  target="_blank"
                  rel="noopener noreferrer"
                  aria-label={t('preview.newTab')}
                >
                  <ExternalLink className="size-3.5" />
                </a>
              </Button>
            ) : null}
          </form>
          {open.isError ? (
            <p role="alert" className="text-caption text-danger">
              {open.error.message || t('preview.openError')}
            </p>
          ) : null}
          {src ? (
            // Its own row: the address row has no room for it in a narrow side panel.
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant={annotating ? 'secondary' : 'outline'}
                size="sm"
                aria-pressed={annotating}
                // One element at a time: a second pick would swap the quote under a note.
                disabled={picked !== null || loaded !== src}
                onClick={() => setAnnotating((on) => !on)}
              >
                <MousePointerClick aria-hidden className="size-3.5" />
                {t('preview.annotate')}
              </Button>
              {annotating ? (
                <p role="status" className="text-caption text-muted-foreground">
                  {t('preview.annotating')}
                </p>
              ) : null}
            </div>
          ) : null}
          {src ? (
            <iframe
              // Each open or reload is a new URL, so the frame loads again.
              key={src}
              ref={frame}
              data-testid="session-preview-frame"
              title={t('preview.frameTitle', { port: open.data?.port })}
              src={src}
              sandbox={SANDBOX}
              referrerPolicy="no-referrer"
              // A load ends the request; the next document must be asked again.
              onLoad={() => {
                setLoaded(src)
                setAnnotating(false)
              }}
              className="h-96 w-full rounded-md border border-border bg-background"
            />
          ) : null}
          <PassageNotes
            to={run}
            workspace={
              run.workspace_ref
                ? { workspace_ref: run.workspace_ref }
                : undefined
            }
            // Named: the notes may go to another session, for which "this" is itself.
            path={`the preview of session ${run.name || run.run_ref.slice(0, 8)}`}
            picked={picked}
            onPicked={() => setPicked(null)}
          />
        </>
      )}
    </section>
  )
}
