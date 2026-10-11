// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE APP THE SESSION SERVES, beside its conversation: a port one of the session's own
// processes listens on, shown through the engine (modules/sessions/preview.go). The port
// is never exposed: the frame loads a short-lived same-origin URL whose token is its only
// credential, and the page runs sandboxed in an opaque origin, so it cannot read or use
// the console's session. `sandbox` here and the engine's CSP say the same thing.
import { useMutation, useQuery } from '@tanstack/react-query'
import { ExternalLink, RotateCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import './i18n'

/** A dev server the agent starts shows up within a few seconds. */
const PORTS_REFRESH_MS = 5_000

const SANDBOX =
  'allow-scripts allow-forms allow-popups allow-modals allow-downloads'

const selectClass =
  'h-8 min-w-0 rounded-md border border-border bg-background px-2 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'

export function SessionPreview({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const [port, setPort] = useState<number | null>(null)
  const [path, setPath] = useState('/')
  const [src, setSrc] = useState<string | null>(null)
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

  return (
    <section data-testid="session-preview" className="flex flex-col gap-2">
      <p className="text-caption text-muted-foreground">{t('preview.hint')}</p>
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
            <iframe
              // Each open or reload is a new URL, so the frame loads again.
              key={src}
              data-testid="session-preview-frame"
              title={t('preview.frameTitle', { port: open.data?.port })}
              src={src}
              sandbox={SANDBOX}
              referrerPolicy="no-referrer"
              className="h-96 w-full rounded-md border border-border bg-background"
            />
          ) : null}
        </>
      )}
    </section>
  )
}
