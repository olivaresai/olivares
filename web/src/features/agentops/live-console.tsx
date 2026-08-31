// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { AlertTriangle, Radio, Send, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/toaster'
import { LiveDot } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { useRunAttach } from './attach'
import { agentOpsApi } from './api'
import type { AttachFrame, RunDTO } from './types'
import './i18n'

const MAX_FRAMES = 5000

/**
 * LiveConsole — attaches to one operated session's bridged I/O and renders it as a
 * scrollable, loss-free transcript with an honest live indicator. A `lag` sentinel
 * surfaces a visible "N frames dropped" banner (never a silent gap). Input is offered
 * only while the session is running over the governed stream-json transport; a
 * remote-control session shows the honest "I/O not bridged" notice instead.
 */
export function LiveConsole({ run }: { run: RunDTO }) {
  const { t } = useTranslation('agentops')
  const isRemote = run.transport === 'remote-control'
  const isLive = run.state === 'running' || run.state === 'idle'

  const [frames, setFrames] = useState<AttachFrame[]>([])
  const [dropped, setDropped] = useState(0)
  const [autoscroll, setAutoscroll] = useState(true)
  const scrollRef = useRef<HTMLDivElement | null>(null)

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

  // Attach only for the governed (bridged) transport; remote-control is not bridged.
  const { status, ended } = useRunAttach({
    runRef: run.run_ref,
    enabled: !isRemote,
    onFrame,
    onLag,
  })

  // Auto-scroll to the tail as frames arrive, unless the operator scrolled up.
  useEffect(() => {
    if (!autoscroll) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [frames, autoscroll])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
    setAutoscroll(atBottom)
  }, [])

  const [line, setLine] = useState('')
  const inputMutation = useMutation({
    mutationFn: (l: string) => agentOpsApi.input(run.run_ref, l),
    onSuccess: () => setLine(''),
    onError: (err) => {
      if (err instanceof ApiError) toast.error(err.message)
      else toast.error(t('live.inputNotAllowed'))
    },
  })

  const canSend = run.state === 'running' && !isRemote
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const l = line.trim()
    if (!l || inputMutation.isPending) return
    inputMutation.mutate(l)
  }

  if (isRemote) {
    return <Notice icon={Radio}>{t('live.remoteControl')}</Notice>
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <LiveDot status={status} />
          {ended && <span>{t('live.ended')}</span>}
          {!isLive && !ended && (
            <span>{t('live.notLive', { state: t(`state.${run.state}`) })}</span>
          )}
        </div>
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <Switch checked={autoscroll} onCheckedChange={setAutoscroll} />
            {t('live.autoscroll')}
          </label>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setFrames([])
              setDropped(0)
            }}
            disabled={frames.length === 0}
          >
            <Trash2 className="size-3.5" />
            {t('live.clear')}
          </Button>
        </div>
      </div>

      {dropped > 0 && (
        <div className="flex items-center gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-1.5 text-xs text-warning">
          <AlertTriangle className="size-3.5 shrink-0" />
          {t('live.lag', { count: dropped })}
        </div>
      )}

      <div
        ref={scrollRef}
        onScroll={onScroll}
        tabIndex={0}
        role="log"
        aria-label={t('detail.live')}
        className="h-80 overflow-auto rounded-md border border-border bg-surface p-2 font-mono text-xs leading-relaxed focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background"
      >
        {frames.length === 0 ? (
          <p className="p-2 text-muted-foreground">
            {status === 'open' ? t('live.waiting') : t('live.noOutput')}
          </p>
        ) : (
          frames.map((f, i) => (
            <div
              key={`${f.seq}-${i}`}
              className={cn(
                'whitespace-pre-wrap break-all',
                f.stream === 'stderr' ? 'text-danger' : 'text-foreground',
              )}
            >
              {f.line}
            </div>
          ))
        )}
      </div>

      <form onSubmit={onSubmit} className="flex items-center gap-2">
        <Input
          value={line}
          onChange={(e) => setLine(e.target.value)}
          placeholder={t('live.inputPlaceholder')}
          aria-label={t('live.inputAria')}
          disabled={!canSend || inputMutation.isPending}
          mono
        />
        <Button
          type="submit"
          variant="primary"
          size="sm"
          disabled={!canSend || !line.trim() || inputMutation.isPending}
        >
          <Send className="size-3.5" />
          {t('live.send')}
        </Button>
      </form>
      {!canSend && !isRemote && (
        <p className="text-xs text-muted-foreground">
          {t('live.inputNotAllowed')}
        </p>
      )}
    </div>
  )
}

function Notice({
  icon: Icon,
  children,
}: {
  icon: typeof Radio
  children: React.ReactNode
}) {
  return (
    <div className="flex items-start gap-2 rounded-md border border-border bg-muted px-3 py-2.5 text-sm text-muted-foreground">
      <Icon className="mt-0.5 size-4 shrink-0" />
      <span>{children}</span>
    </div>
  )
}
