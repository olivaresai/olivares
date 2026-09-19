// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { AlertTriangle, CirclePause, Radio, Send, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/toaster'
import { LiveDot } from '@/features/shared'
import { isUnknownVerdict, workErrorCode } from '@/features/work/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useRunAttach } from './attach'
import { agentOpsApi } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import { runInputMode } from './provider-contract'
import { sessionTurnBody } from './session-turn'
import type { AttachFrame, RunDTO } from './types'
import { isWorkBound, workLeaseFenceFor } from './work-fence'
import {
  mapConversationFrames,
  type ConversationItem,
} from '@/features/sessions/conversation-frames'
import './i18n'
import '@/features/sessions/i18n'

const MAX_FRAMES = 5000

function useOpaqueAttachEpoch(runRef: string) {
  // Opaque remount key: the bearer is never written into a React key, the DOM
  // or a log. credentialGeneration is the non-secret "the credential moved"
  // counter (stores/session.ts).
  const credentialGeneration = useSessionStore((s) => s.credentialGeneration)
  const tenant = useTenantStore((s) => s.activeTenant)
  const identity = `${credentialGeneration}:${tenant ?? ''}:${runRef}`
  const [stamp, setStamp] = useState({ identity, epoch: 0 })
  if (stamp.identity !== identity) {
    setStamp({ identity, epoch: stamp.epoch + 1 })
  }
  return stamp.epoch
}

/**
 * LiveConsole — attaches to one operated session's bridged I/O and renders it as a
 * scrollable, loss-free transcript with an honest live indicator. A `lag` sentinel
 * surfaces a visible "N frames dropped" banner (never a silent gap). Input is offered
 * only while the session is running over the governed stream-json transport; a
 * remote-control session shows the honest "I/O not bridged" notice instead.
 *
 * ⛔ WHICH INPUT CONTRACT IS A FACT OF THE RUN, NOT OF THE TEXT. `runInputMode` reads
 * the driver the SERVER persisted at launch: the historical Claude path (and a legacy
 * run with no profile) takes a raw NDJSON line, and a run driven by an owned provider
 * protocol takes a TURN. Nothing here inspects what the operator typed, and nothing
 * here turns a typed line into a protocol frame — the engine refuses both mistakes
 * with a 400, and this only stops the console making one. The label and the
 * placeholder follow the same fact, so the box never asks for NDJSON on a session that
 * does not accept it.
 */
export function LiveConsole({ run }: { run: RunDTO }) {
  const epoch = useOpaqueAttachEpoch(run.run_ref)
  return <LiveConsoleSession key={epoch} run={run} />
}

function LiveConsoleSession({ run }: { run: RunDTO }) {
  const { t } = useTranslation('agentops')
  const { can } = useAuth()
  const boundary = useAuthBoundary()
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
  const { status, ended, ioUnavailable, retry } = useRunAttach({
    runRef: run.run_ref,
    enabled: !isRemote,
    sessionKey: `${run.state}:${run.transport}`,
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
  const [wire, setWire] = useState('')
  const inputMode = runInputMode(run)
  const items = mapConversationFrames(frames.map((f) => f.line))
  const workBound = isWorkBound(run)
  const workFenceValid =
    !workBound ||
    (Number.isSafeInteger(run.work_lease_fence) &&
      (run.work_lease_fence ?? 0) > 0)
  const interruptSupported = inputMode === 'text' && !isRemote
  const canInterrupt =
    can('sessions:run:write') &&
    interruptSupported &&
    run.state === 'running' &&
    workFenceValid
  // Recheck the current permission and target at dispatch. An intent captured
  // before a tenant, credential, run or fence change must not address its successor.
  const interruptIntent = `${boundary.epoch}:${run.run_ref}:${run.work_lease_fence ?? '-'}`
  const isAuthorized = () => can('sessions:run:write')
  const currentInterrupt = useRef({
    isAuthorized,
    allowed: canInterrupt,
    intent: interruptIntent,
  })
  useEffect(() => {
    currentInterrupt.current = {
      isAuthorized,
      allowed: canInterrupt,
      intent: interruptIntent,
    }
  })
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const interruptMutation = useMutation({
    mutationFn: (intent: string) => {
      const current = currentInterrupt.current
      if (
        !mounted.current ||
        !current.allowed ||
        !current.isAuthorized() ||
        current.intent !== intent
      ) {
        throw new AuthorityLostError()
      }
      return agentOpsApi.interrupt(
        run.run_ref,
        workBound ? run.work_lease_fence : undefined,
      )
    },
    onSuccess: (_, intent) => {
      if (mounted.current && currentInterrupt.current.intent === intent)
        toast.success(t('live.interrupted'))
    },
    onError: (err, intent) => {
      if (
        !mounted.current ||
        currentInterrupt.current.intent !== intent ||
        err instanceof AuthorityLostError
      )
        return
      if (isUnknownVerdict(err)) {
        toast.warning(t('live.interruptUnknown'))
        return
      }
      const code = workErrorCode(err)
      if (code === 'stale_fence' || code === 'dispatch_conflict') {
        toast.warning(t('live.interruptWorkConflict'))
        return
      }
      toast.error(
        err instanceof ApiError ? err.message : t('live.interruptFailed'),
      )
    },
  })
  const inputMutation = useMutation({
    mutationFn: (payload: { value: string; asWire: boolean }) => {
      const body = sessionTurnBody(run, payload.value, payload.asWire)
      // The SAME fence the interrupt above presents. A work-bound run has one
      // control plane, and a turn is a control on it: sent unfenced it is refused
      // with 409 before the child sees a byte.
      const fence = workLeaseFenceFor(run)
      return 'text' in body
        ? agentOpsApi.inputText(run.run_ref, body.text, fence)
        : agentOpsApi.input(run.run_ref, body.line, fence)
    },
    // Cleared ONLY on an accepted response: a refused turn (400, 409, 503 UNKNOWN)
    // leaves the operator's text in the box, because losing it would make a retry a
    // retype and an UNKNOWN indistinguishable from a send.
    onSuccess: (_ok, payload) => {
      if (payload.asWire) setWire('')
      else setLine('')
    },
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
    inputMutation.mutate({ value: l, asWire: false })
  }
  const onSubmitWire = (e: FormEvent) => {
    e.preventDefault()
    const l = wire.trim()
    if (!l || inputMutation.isPending) return
    inputMutation.mutate({ value: l, asWire: true })
  }

  if (isRemote) {
    return <Notice icon={Radio}>{t('live.remoteControl')}</Notice>
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-caption text-muted-foreground">
          <LiveDot status={status} />
          {ended && <span>{t('live.ended')}</span>}
          {ioUnavailable === 'not_live_on_node' && (
            <span>{t('live.ioUnavailableNotLiveOnNode')}</span>
          )}
          {ioUnavailable === 'remote_control' && (
            <span>{t('live.ioUnavailableRemoteControl')}</span>
          )}
          {!isLive && !ended && (
            <span>{t('live.notLive', { state: t(`state.${run.state}`) })}</span>
          )}
          {ioUnavailable && (
            <Button variant="secondary" size="sm" onClick={() => retry()}>
              {t('live.retryAttach')}
            </Button>
          )}
        </div>
        <div className="flex items-center gap-3">
          {can('sessions:run:write') && interruptSupported && (
            <Button
              variant="secondary"
              size="sm"
              title={t('live.interruptHint')}
              onClick={() => interruptMutation.mutate(interruptIntent)}
              disabled={!canInterrupt || interruptMutation.isPending}
            >
              <CirclePause className="size-3.5" />
              {t('live.interrupt')}
            </Button>
          )}
          <label className="flex items-center gap-1.5 text-caption text-muted-foreground">
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

      {interruptSupported && can('sessions:run:write') && !workFenceValid && (
        <p className="text-caption text-muted-foreground">
          {t('live.interruptFenceUnavailable')}
        </p>
      )}

      {dropped > 0 && (
        <div className="flex items-center gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-1.5 text-caption text-warning">
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
        className="h-80 overflow-auto rounded-md border border-border bg-surface p-2 text-body leading-snug focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background"
      >
        {items.length === 0 ? (
          <p className="p-2 text-muted-foreground">
            {status === 'open' ? t('live.waiting') : t('live.noOutput')}
          </p>
        ) : (
          items.map((item) => (
            <LiveConversationLine key={item.id} item={item} />
          ))
        )}
      </div>

      <form onSubmit={onSubmit} className="flex items-center gap-2">
        <Input
          value={line}
          onChange={(e) => setLine(e.target.value)}
          placeholder={t('live.textPlaceholder')}
          aria-label={t('live.textAria')}
          disabled={!canSend || inputMutation.isPending}
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
      <details className="text-caption text-muted-foreground">
        <summary className="cursor-pointer outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
          {t('live.advanced')}
        </summary>
        <p className="mt-1">{t('live.advancedHint')}</p>
        <form
          onSubmit={onSubmitWire}
          className="mt-1.5 flex items-center gap-2"
        >
          <Input
            value={wire}
            onChange={(e) => setWire(e.target.value)}
            placeholder={t('live.inputPlaceholder')}
            aria-label={t('live.inputAria')}
            disabled={!canSend || inputMutation.isPending}
            mono
          />
          <Button
            type="submit"
            variant="secondary"
            size="sm"
            disabled={!canSend || !wire.trim() || inputMutation.isPending}
          >
            {t('live.send')}
          </Button>
        </form>
      </details>
      {!canSend && !isRemote && (
        <p className="text-caption text-muted-foreground">
          {t('live.inputNotAllowed')}
        </p>
      )}
    </div>
  )
}

function LiveConversationLine({ item }: { item: ConversationItem }) {
  const { t } = useTranslation('sessions')
  if (item.kind === 'tool') {
    return (
      <div
        data-testid="conversation-item"
        data-kind="tool"
        className="flex min-h-9 items-center gap-2 py-1 text-caption"
      >
        <span className="font-medium">
          {item.toolName ?? t('conversation.tool')}
        </span>
        {item.toolArgsSummary ? (
          <span className="truncate text-muted-foreground">
            {item.toolArgsSummary}
          </span>
        ) : null}
      </div>
    )
  }
  if (item.kind === 'system' || item.kind === 'result') {
    return (
      <div
        data-testid="conversation-item"
        data-kind={item.kind}
        className="py-0.5 text-caption text-muted-foreground"
      >
        {item.summary}
      </div>
    )
  }
  return (
    <div
      data-testid="conversation-item"
      data-kind={item.kind}
      className={cn(
        'whitespace-pre-wrap break-words py-1',
        item.kind === 'unknown' && 'font-mono text-caption',
      )}
    >
      {item.text || item.summary}
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
    <div className="flex items-start gap-2 rounded-md border border-border bg-muted px-3 py-2.5 text-body text-muted-foreground">
      <Icon className="mt-0.5 size-4 shrink-0" />
      <span>{children}</span>
    </div>
  )
}
