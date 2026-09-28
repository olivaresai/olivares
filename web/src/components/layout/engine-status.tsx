// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ENGINE'S STATE, IN THE SIDEBAR FOOTER (redesign §3.14): Engine ready, Checking the
// engine, Engine not reachable, or Offline since <time>. A dot and a word: the colour
// never says it alone. The answer is the engine's own server information, which every
// page already reads; the browser's own offline signal wins over it, because a request
// that cannot leave the machine says nothing about the engine.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useServerInfo } from '@/lib/hooks/use-server-info'

export type EngineState = 'checking' | 'ready' | 'unreachable' | 'offline'

/** The browser's offline signal, with the time it went offline. */
function useOfflineSince(): Date | null {
  const [since, setSince] = useState<Date | null>(() =>
    typeof navigator !== 'undefined' && navigator.onLine === false
      ? new Date()
      : null,
  )
  useEffect(() => {
    const offline = () => setSince(new Date())
    const online = () => setSince(null)
    window.addEventListener('offline', offline)
    window.addEventListener('online', online)
    return () => {
      window.removeEventListener('offline', offline)
      window.removeEventListener('online', online)
    }
  }, [])
  return since
}

const DOT: Record<EngineState, string> = {
  checking: 'bg-text-3',
  ready: 'bg-ok',
  unreachable: 'bg-bad',
  offline: 'bg-warn',
}

export function EngineStatus({ className }: { className?: string }) {
  const { t, i18n } = useTranslation('nav')
  const info = useServerInfo()
  const offlineSince = useOfflineSince()
  const state: EngineState = offlineSince
    ? 'offline'
    : info.isError
      ? 'unreachable'
      : info.data
        ? 'ready'
        : 'checking'
  const version = info.data?.version
  const word =
    state === 'offline'
      ? t('shell.engine.offline', {
          time: new Intl.DateTimeFormat(i18n.language, {
            timeStyle: 'short',
          }).format(offlineSince ?? new Date()),
        })
      : t(`shell.engine.${state}`)
  return (
    <p
      role="status"
      data-engine-state={state}
      className={cn(
        'flex min-w-0 flex-1 items-center gap-2 rounded-ctl px-2 py-1.5 text-caption text-text-2',
        className,
      )}
    >
      <span
        aria-hidden
        className={cn('inline-block size-2 shrink-0 rounded-full', DOT[state])}
      />
      <span
        className="min-w-0 truncate"
        title={version ? `${word} · ${version}` : word}
      >
        {word}
      </span>
    </p>
  )
}
