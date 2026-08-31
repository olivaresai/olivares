// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect, useRef, useState } from 'react'
import { subscribeStream, type StreamStatus } from '@/features/shared'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { runStreamPath } from './api'
import type { Output, RunStreamSummary } from './types'

/**
 * useRunStream — watches one sandbox run over SSE (GET /runs/{id}/stream). Modelled on
 * `useRunAttach` (features/agentops/attach.ts), and DIFFERENT from it in the three
 * places the wire is different. What the wire actually does is documented in api.ts;
 * what this hook does about it:
 *
 *  - IT NEVER INFERS AN ENDING FROM THE SOCKET. `complete` is set ONLY by the engine's
 *    `event: done`. A body that ends, or a transport error, before that sets
 *    `interrupted` — the third answer (canon §1.5: clean / broken / I COULD NOT LOOK).
 *    A caller cannot read "the run finished" out of `status === 'closed'`, because that
 *    is exactly the confusion this surface exists to avoid.
 *  - RESUME IS BY ROW IDENTITY, NOT BY CURSOR, because the engine offers no cursor
 *    (stream.go:44-122 reads neither a query param nor `Last-Event-ID`). It replays the
 *    WHOLE set from the first output on every connect, so a reconnect loses nothing;
 *    this hook drops the ones it already holds by `id` and appends only the tail. It
 *    deliberately does NOT dedupe on `step_key`: duplicate step keys are legal
 *    (scenarios.go:259-263) and that would silently drop real evidence.
 *  - RECONNECTION IS THE OPERATOR'S, not a background loop. Reasons, in order: opening
 *    the stream writes an audit row (stream.go:134-148), so an automatic retry would
 *    fill the ledger with opens nobody asked for; the endpoint is a finite replay, so a
 *    retry buys nothing a click does not; and an invisible auto-recovery would hide the
 *    very fact the view must show — that the operator stopped seeing the run.
 *
 * Appending in arrival order is the engine's order (`sort` by step key,
 * runs.go:426-441), and the SET is immutable across reconnects because a run's outputs
 * are written in the SAME transaction as the run row (runs.go:225-232) ⇒ a replay can
 * only re-send what we already hold. Two limits of that, measured rather than assumed:
 * the engine sorts with `sort.Slice`, which is NOT stable, so two rows sharing a step
 * key have no guaranteed relative order between one replay and the next (the contrast
 * caught this comment claiming more than the engine promises); and a future async runner
 * that appended outputs later would need an explicit sort here.
 */
export interface UseRunStreamOptions {
  /** The run to watch; null keeps the stream closed. */
  runId: string | null
  /** Gate the subscription (default true) — e.g. while a dialog is closed. */
  enabled?: boolean
}

export interface UseRunStreamResult {
  /** Every distinct `event: output` received, in the order the engine sent them. */
  outputs: Output[]
  /** The `event: summary` aggregate, once the engine has sent it. */
  summary: RunStreamSummary | null
  /** The live transport state (for an honest indicator — never a fake "live"). */
  status: StreamStatus
  /** True ONLY after `event: done`: the engine replayed everything and closed. Mutually
   *  exclusive with `interrupted` — the two are terminal states, not two flags. */
  complete: boolean
  /** True when the connection ended WITHOUT `done` — "I lost the view". Never means
   *  the run ended, and is cleared when a reconnect opens. */
  interrupted: boolean
  /** Frames that arrived and could NOT be read (malformed JSON, or a payload with no
   *  step key). Non-zero means the replay is INCOMPLETE even if it ended cleanly: a
   *  `complete` with unreadable frames is not the whole record, and the caller must not
   *  present it as one. */
  unreadable: number
  /** Re-open the stream, keeping what was already received. */
  reconnect: () => void
}

export function useRunStream({
  runId,
  enabled = true,
}: UseRunStreamOptions): UseRunStreamResult {
  const token = useSessionStore((s) => s.token)
  const tenant = useTenantStore((s) => s.activeTenant)
  const [outputs, setOutputs] = useState<Output[]>([])
  const [summary, setSummary] = useState<RunStreamSummary | null>(null)
  const [status, setStatus] = useState<StreamStatus>('closed')
  const [complete, setComplete] = useState(false)
  const [interrupted, setInterrupted] = useState(false)
  const [unreadable, setUnreadable] = useState(0)
  // Bumped by reconnect() to re-run the subscription effect deliberately.
  const [epoch, setEpoch] = useState(0)
  // Row identities already delivered — a ref, because the frame handler runs inside
  // the stream and must not close over a stale render's state.
  const seenRef = useRef<Set<string>>(new Set())

  const active = enabled && !!runId && !!token

  // A DIFFERENT run (or a tenant/token switch) is a different transcript, so it starts
  // empty. A reconnect is not: it keeps what it has, which is what makes the resume
  // loss-free. Hence this reset keys on identity only, never on `epoch`.
  useEffect(() => {
    seenRef.current = new Set()
    setOutputs([])
    setSummary(null)
    setComplete(false)
    setInterrupted(false)
    setUnreadable(0)
  }, [runId, tenant, token])

  useEffect(() => {
    if (!active || !runId) return
    const controller = new AbortController()
    let cancelled = false
    let doneReceived = false

    const handle = (msg: { event: string; data: string }) => {
      if (cancelled) return // a frame queued before the abort is not this view's data
      switch (msg.event) {
        case 'output': {
          let parsed: unknown
          try {
            parsed = JSON.parse(msg.data)
          } catch {
            setUnreadable((n) => n + 1) // COUNTED, never swallowed
            return
          }
          const o = parsed as Output
          // A frame this client cannot render is evidence it did NOT receive, and a
          // silent `return` here would let a later `done` present the replay as whole.
          // So an unreadable frame is counted and surfaced (the caller renders it), the
          // way the agentops attach surfaces the server's `lag` sentinel.
          if (!o || typeof o !== 'object' || typeof o.step_key !== 'string') {
            setUnreadable((n) => n + 1)
            return
          }
          // Identity is the row id (types.ts). If a payload ever arrived without one,
          // fall back to the raw frame: a replayed row is byte-identical, so the dedupe
          // still holds, and an output is never DROPPED for lacking an id — showing a
          // duplicate is visible, losing evidence is not.
          const key = typeof o.id === 'string' && o.id !== '' ? o.id : msg.data
          if (seenRef.current.has(key)) return
          seenRef.current.add(key)
          setOutputs((prev) => [...prev, o])
          break
        }
        case 'summary': {
          try {
            setSummary(JSON.parse(msg.data) as RunStreamSummary)
          } catch {
            /* ignore a malformed aggregate; the outputs stand on their own */
          }
          break
        }
        case 'done': {
          doneReceived = true
          setComplete(true)
          break
        }
      }
    }

    const run = async () => {
      setStatus('connecting')
      try {
        await subscribeStream({
          path: runStreamPath(runId),
          token,
          tenant,
          signal: controller.signal,
          onOpen: () => {
            if (cancelled) return
            setStatus('open')
            setInterrupted(false)
          },
          onMessage: handle,
        })
        if (cancelled) return
        setStatus('closed')
        // The body ended. `done` ⇒ the engine finished the replay. No `done` ⇒ the
        // connection dropped mid-replay, which is NOT the run ending.
        if (!doneReceived) setInterrupted(true)
      } catch (err) {
        if (cancelled || (err as Error).name === 'AbortError') return
        // A transport error AFTER `done` is not a lost view: the engine already sent
        // everything and closed the replay, so nothing is missing. Guarding on
        // doneReceived is what keeps the two terminal states MUTUALLY EXCLUSIVE — the
        // contrast reproduced "Replay complete" and "Connection lost" rendered together,
        // with the lost-hint claiming the connection died before a `done` the client had
        // already seen. Two independent booleans were not enough.
        if (doneReceived) {
          setStatus('closed')
          return
        }
        setStatus('error')
        setInterrupted(true)
      }
    }
    void run()

    return () => {
      cancelled = true
      controller.abort()
    }
  }, [active, runId, token, tenant, epoch])

  const reconnect = useCallback(() => {
    setEpoch((e) => e + 1)
  }, [])

  return {
    outputs,
    summary,
    status: active ? status : 'closed',
    complete,
    interrupted,
    unreadable,
    reconnect,
  }
}
